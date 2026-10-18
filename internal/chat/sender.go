package chat

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"netdiag/internal/bubbles"
	"netdiag/internal/bubbles/protocol"
	"netdiag/internal/mdns"
)

// The outbox's sender and the status polling (v0.6 4.9 and 4.10
// Recovery). The sender goes through the outbox in order while the
// connection can take posts; the poller asks the server every minute
// whether it answers, without the token (bubbles v0.2.0 refuses GET /status
// to an app token, v0.6-D39), and after 2 minutes without an answer looks
// for it on the local network at another address.

// sendGate is the connection the sender uses now, or why it waits.
type sendGate struct {
	gen     int
	client  *bubbles.Client
	channel Channel
}

// gate reports whether items may go now: connected, with a channel that
// takes posts, and neither a token the server refused nor a server whose
// identity changed (those wait for the person: Reconnect).
func (m *Manager) gate() (sendGate, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil || m.conn.Channel.ID == "" || m.chanRefused != "" ||
		m.trouble == StateNeedsReconnect || m.trouble == StateIdentityChanged {
		return sendGate{}, false
	}
	c, err := bubbles.New(m.conn.ClientConfig())
	if err != nil {
		return sendGate{}, false
	}
	return sendGate{gen: m.gen, client: c, channel: m.conn.Channel}, true
}

// sender delivers the outbox until the manager closes.
func (m *Manager) sender() {
	defer m.loops.Done()
	idle := time.NewTimer(time.Hour)
	defer idle.Stop()
	for m.ctx.Err() == nil {
		wait := time.Hour
		if g, ok := m.gate(); ok {
			it, w := m.out.next()
			if it != nil {
				outcome, wait := m.deliver(g, it)
				m.out.done(it, outcome, wait)
				continue
			}
			if w > 0 {
				wait = w
			}
		}
		idle.Reset(wait)
		select {
		case <-m.ctx.Done():
			return
		case <-m.out.wake:
		case <-idle.C:
		}
	}
}

// deliver sends one item and says what became of it, with the wait before
// a retry.
func (m *Manager) deliver(g sendGate, it *Item) (int, time.Duration) {
	ctx, cancel := context.WithTimeout(m.ctx, callTimeout)
	defer cancel()
	var err error
	switch it.Op {
	case OpPost:
		in := *it.Alert
		in.Channel = g.channel.ID
		_, err = g.client.PostAlertResult(ctx, in)
	case OpResolve:
		_, err = g.client.ResolveAlertKey(ctx, it.Key, it.Text)
	case OpMessage:
		_, err = g.client.PostMessage(ctx, g.channel.ID, protocol.NewMessage{Body: it.Text, Nonce: "netdiag-" + it.ID})
	default:
		return sentGone, 0
	}
	if m.ctx.Err() != nil {
		return sentKeep, 0 // netdiag is closing: it stays for the next run
	}
	m.note(g.gen, err)
	if err == nil {
		if it.Op != OpResolve {
			m.channelWorks(g.gen, g.channel.ID)
		}
		return sentOK, 0
	}
	var ae *bubbles.APIError
	switch {
	case errors.Is(err, bubbles.ErrServerIdentityChanged), errors.Is(err, bubbles.ErrUnauthorized):
		return sentKeep, 0 // note stopped the sending
	case errors.Is(err, bubbles.ErrRateLimited):
		w := m.backoff(it.Attempts)
		if errors.As(err, &ae) && ae.RetryAfter > w {
			w = ae.RetryAfter
		}
		return sentRetry, w
	case errors.Is(err, bubbles.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		return sentRetry, m.backoff(it.Attempts)
	case !errors.As(err, &ae):
		return sentRetry, m.backoff(it.Attempts)
	}
	switch ae.Status {
	case http.StatusNotFound, http.StatusForbidden:
		if it.Op == OpResolve {
			// A key never posted (or posted by another app, or in a channel
			// netdiag left): there is no card of netdiag's to close.
			return sentGone, 0
		}
		m.refuseChannel(g.gen, g.channel.ID, ReasonNotAllowed,
			"netdiag isn't allowed to post in #"+g.channel.Name+". Ask your chat admin to add netdiag to that channel.")
		return sentKeep, 0
	case http.StatusConflict:
		return m.conflict(ctx, g, it)
	case http.StatusBadRequest:
		_, msg := explain(err)
		m.addNote("Team chat refused an alert: " + strings.TrimPrefix(msg, "The chat server refused it: "))
		return sentDrop, 0
	}
	_, msg := explain(err)
	m.addNote("Team chat refused an alert: " + strings.TrimPrefix(msg, "The chat server refused it: "))
	return sentDrop, 0
}

// conflict handles bubbles' 409, which means one of two things (v0.6-D24):
// the channel is archived (it stays listed, archived), or the card of this
// key is still open in another channel (after Change channel). An archived
// channel takes nothing: the state is no_channel and the items wait for
// Change channel. Otherwise the old card is resolved by key with "Moved to
// #<channel>." and the alert posted once more.
func (m *Manager) conflict(ctx context.Context, g sendGate, it *Item) (int, time.Duration) {
	chans, err := m.channels(ctx, g.client, g.gen)
	if err != nil {
		return sentRetry, m.backoff(it.Attempts)
	}
	archivedMsg := "The channel #" + g.channel.Name + " is archived. Choose another channel."
	if !slices.ContainsFunc(chans, func(c Channel) bool { return c.ID == g.channel.ID }) {
		m.refuseChannel(g.gen, g.channel.ID, ReasonArchived, archivedMsg)
		return sentKeep, 0
	}
	m.mu.Lock()
	old := m.oldChannel
	m.mu.Unlock()
	from := "an archived channel"
	if old != "" {
		from = "the archived channel #" + old
	}
	switch {
	case it.Op == OpResolve:
		// The card is in a channel archived since: bubbles keeps it
		// read-only, so it cannot be closed.
		m.addNote("Team chat could not close an alert in " + strings.TrimPrefix(from, "the ") + ".")
		return sentGone, 0
	case it.Op != OpPost || it.Moved:
		// A post that met the same 409 after moving once.
		m.addNote("Team chat could not move an alert out of " + from + ".")
		return sentGone, 0
	}
	_, err = g.client.ResolveAlertKey(ctx, it.Key, "Moved to #"+g.channel.Name+".")
	var ae *bubbles.APIError
	switch {
	case errors.As(err, &ae) && ae.Status == http.StatusConflict:
		// bubbles keeps an archived channel read-only: that card stays.
		m.addNote("Team chat could not move an alert out of " + from + ".")
		return sentGone, 0
	case err == nil, errors.As(err, &ae) && ae.Status == http.StatusNotFound:
		it.Moved = true
		return sentRetry, 0
	}
	m.note(g.gen, err)
	return sentRetry, m.backoff(it.Attempts)
}

// backoff is the wait before attempt n+1: from RetryMin doubling up to
// RetryMax, with a fifth either way at random so many netdiags do not come
// back at once.
func (m *Manager) backoff(attempts int) time.Duration {
	d := m.opt.RetryMin
	for i := 1; i < attempts && d < m.opt.RetryMax; i++ {
		d *= 2
	}
	d = min(d, m.opt.RetryMax)
	j := time.Duration(rand.Int64N(int64(d)/5 + 1))
	if rand.IntN(2) == 0 {
		return d - j
	}
	return d + j
}

// poller asks the chat server whether it answers every PollEvery, and
// soon after MoveAfter without an answer looks for it elsewhere.
func (m *Manager) poller() {
	defer m.loops.Done()
	t := time.NewTimer(m.opt.PollEvery)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
		case <-m.pollNow:
		}
		m.poll(m.ctx, false)
		t.Reset(m.nextPoll())
	}
}

// nextPoll is the wait before the next poll: PollEvery, or less when the
// server will have been away for MoveAfter before then.
func (m *Manager) nextPoll() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.opt.PollEvery
	if m.trouble == StateUnreachable && !m.since.IsZero() {
		if until := m.since.Add(m.opt.MoveAfter).Sub(m.opt.Now()); until > 0 && until < d {
			d = until
		}
	}
	return max(d, 10*time.Millisecond)
}

// poll asks the server for its status without the token. An answer clears
// unreachable (never needs_reconnect: only a call the token passes says
// the token works, v0.6-D39); a CA that is not the pinned one is
// identity_changed. When the server has not answered for MoveAfter (or
// at once, for Try now), netdiag looks for it on the local network.
func (m *Manager) poll(ctx context.Context, now bool) {
	m.mu.Lock()
	c, gen := m.conn, m.gen
	stopped := c == nil || m.trouble == StateIdentityChanged
	m.mu.Unlock()
	if stopped {
		return
	}
	err := m.reach(ctx, gen, c.Server.BaseURL, c.Server.CAFingerprint)
	if err == nil || ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	trouble, since := m.trouble, m.since
	m.mu.Unlock()
	if trouble == StateUnreachable && (now || m.opt.Now().Sub(since) >= m.opt.MoveAfter) {
		m.follow(ctx, gen, *c)
	}
}

// probe asks the server at baseURL for its status (GET /status, without
// the token, the CA pinned). Any answer from it counts.
func probe(ctx context.Context, baseURL, fp string) error {
	cl, err := bubbles.New(bubbles.Config{BaseURL: baseURL, CAFingerprint: fp})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err = cl.Status(ctx)
	var ae *bubbles.APIError
	if errors.As(err, &ae) && !errors.Is(err, bubbles.ErrUnavailable) {
		return nil
	}
	return err
}

// reach probes the connection's server and records what it found.
func (m *Manager) reach(ctx context.Context, gen int, baseURL, fp string) error {
	err := probe(ctx, baseURL, fp)
	if errors.Is(err, context.Canceled) {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gen != gen || m.conn == nil || m.trouble == StateIdentityChanged {
		return err
	}
	switch {
	case err == nil:
		if m.trouble == StateUnreachable {
			m.troubleLocked("", "")
		}
		return nil
	case errors.Is(err, bubbles.ErrServerIdentityChanged):
		m.troubleLocked(StateIdentityChanged, identityMsg)
	case m.trouble == StateNeedsReconnect:
	default:
		m.troubleLocked(StateUnreachable, unreachableMsg)
	}
	return err
}

// follow looks for the chat server at another address (v0.6 4.9
// "Following address changes"): a bubbles server on the local network
// whose announcement (mDNS TXT fp) starts like the pinned fingerprint.
// The announcement is only a hint: the server must answer there with the
// pinned CA before netdiag moves. The token and pin stay.
func (m *Manager) follow(ctx context.Context, gen int, c Connection) {
	fp, err := bubbles.NormalizeFingerprint(c.Server.CAFingerprint)
	if err != nil {
		return
	}
	prefix := fp[len("sha256:") : len("sha256:")+16]
	cur, err := targetOf(c.Server.BaseURL, c.Server.OnboardingURL)
	if err != nil {
		return
	}
	recs, _ := m.opt.Browse(ctx, "", []string{ServiceType}, discoverWindow)
	for _, f := range foundIn(recs) {
		if f.FPPrefix != prefix || (f.Address == cur.Host && f.Port == cur.Port) {
			continue
		}
		t, err := ParseTarget(netJoin(f.Address, f.Port), f.OnboardPort)
		if err != nil {
			continue
		}
		// Only a server with the pinned CA counts; another one that copies
		// the announcement changes nothing.
		if probe(ctx, t.BaseURL(), fp) != nil {
			continue
		}
		moved := false
		_ = m.updateConn(gen, func(conn *Connection) {
			conn.Server.BaseURL, conn.Server.OnboardingURL = t.BaseURL(), t.OnboardingURL()
			moved = true
		})
		if moved {
			m.addNote("Team chat server moved to " + t.Host + ".")
			_ = m.reach(ctx, gen, t.BaseURL(), fp) // it answers there: no longer unreachable
			m.out.resetWaits()
		}
		return
	}
}

// foundIn lists the chat servers in mDNS records.
func foundIn(recs []mdns.Record) []Found {
	var out []Found
	for _, r := range recs {
		for _, d := range r.Info.Details {
			if d.Type != ServiceType || d.Port == 0 {
				continue
			}
			onboard, _ := strconv.Atoi(d.TXT["onboard"])
			t, err := ParseTarget(netJoin(r.IP, d.Port), onboard)
			if err != nil {
				continue
			}
			f := Found{Name: d.Instance, Address: t.Host, Port: t.Port, OnboardPort: t.OnboardPort, FPPrefix: strings.ToLower(d.TXT["fp"])}
			if f.Name == "" {
				f.Name = t.Host
			}
			out = append(out, f)
		}
	}
	return out
}

// TryNow is the page's Try now: it asks the server at once (with the token
// when the server refused it, so a token that works again clears
// needs_reconnect), looks for it at another address when it does not
// answer, and lets everything in the outbox go now.
func (m *Manager) TryNow(ctx context.Context) Status {
	m.mu.Lock()
	c, gen, trouble := m.conn, m.gen, m.trouble
	m.mu.Unlock()
	if c != nil {
		switch trouble {
		case StateIdentityChanged:
		case StateNeedsReconnect:
			if cl, err := bubbles.New(c.ClientConfig()); err == nil {
				sctx, cancel := context.WithTimeout(ctx, callTimeout)
				_, err := cl.Self(sctx)
				cancel()
				m.note(gen, err)
			}
		default:
			m.poll(ctx, true)
		}
	}
	m.out.resetWaits()
	return m.Status()
}

// Flush gives what waits in the outbox until timeout to go (Quit), then
// saves the rest in chat-outbox.json for the next run. It does not wait
// for a server that did not answer the last call.
func (m *Manager) Flush(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	m.out.kick()
	for time.Now().Before(deadline) && m.ctx.Err() == nil {
		m.mu.Lock()
		away := m.trouble == StateUnreachable
		m.mu.Unlock()
		if _, ok := m.gate(); !ok || away || !m.out.sendable(m.opt.Now().Add(time.Until(deadline))) {
			break
		}
		select {
		case <-m.ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
	return m.out.save()
}
