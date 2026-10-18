package chat

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"netdiag/internal/bubbles/protocol"
	"netdiag/internal/model"
	"netdiag/internal/problems"
)

// The alert router (v0.6 4.9 "Router rules"): one per scan, on its event
// bus. Fault notices become posts, OK notices resolves, both through the
// outbox. Alerts go to chat only from a scan that watches the network
// (live mode, v0.6-D10); a resolve goes from any scan, so a card closes
// however its problem was found fixed.

// Watched is the part of *scan.Scan the router uses.
type Watched interface {
	Subscribe() (<-chan model.Event, func())
	Network() string
	Request() model.ScanRequest
	Reconciled() <-chan struct{}
	Done() <-chan struct{}
}

// maxBurst is how many current problems go to chat at once when netdiag
// starts watching a network or starts sending.
const maxBurst = 10

// maxAlerts is how many alerts Manager.alerts keeps.
const maxAlerts = MaxOutbox

// After a live scan's first pass, its burst waits until no notice has come
// for burstQuiet, at most burstWait: the notices the pass held back are on
// the bus before Reconciled closes, but may not have reached the router
// yet, and a problem with a notice is posted from it.
const (
	burstQuiet = 100 * time.Millisecond
	burstWait  = 2 * time.Second
)

// Attach puts the router on sc's bus until the scan ends. Once the scan's
// first pass is reconciled (the ledger then knows which problems are still
// open), the items a previous run left in the outbox may go, and a live
// scan sends its network's current problems that have no card in chat yet:
// the ledger's problems from before the pass bring no notice of their own.
func (m *Manager) Attach(sc Watched) {
	ch, unsubscribe := sc.Subscribe()
	live := sc.Request().Live
	m.scansMu.Lock()
	m.scans = append(m.scans, sc)
	m.scansMu.Unlock()
	go func() {
		defer unsubscribe()
		defer m.detach(sc)
		hosts := map[string]model.Host{}
		// burst is the keys the first pass sent from the ledger: the notice
		// of a problem the pass found may still come after, and then only
		// gives that post its words.
		var burst map[string]bool
		var quiet *time.Timer
		var burstDue <-chan time.Time
		var until time.Time
		defer func() {
			if quiet != nil {
				quiet.Stop()
			}
		}()
		reconciled := sc.Reconciled()
		for {
			select {
			case <-reconciled:
				reconciled = nil
				m.out.release(sc.Network(), live)
				if live {
					quiet, until = time.NewTimer(burstQuiet), time.Now().Add(burstWait)
					burstDue = quiet.C
				}
			case <-burstDue:
				burstDue = nil
				m.sayHello()
				burst = m.burst(sc.Network(), hosts)
			case ev, ok := <-ch:
				if !ok {
					return
				}
				switch ev.Type {
				case model.EventHost:
					if h, ok := ev.Data.(model.Host); ok {
						hosts[h.IP] = h
					}
				case model.EventNotice:
					n, ok := ev.Data.(model.NoticeEvent)
					if !ok || n.Replay {
						continue
					}
					if burstDue != nil && time.Now().Before(until) {
						quiet.Reset(burstQuiet)
					}
					var h *model.Host
					if x, ok := hosts[n.IP]; ok && n.IP != "" {
						h = &x
					}
					if burst[n.Key] {
						delete(burst, n.Key)
						if m.burstNotice(sc.Network(), n.Notice, h) {
							continue
						}
					}
					m.route(sc.Network(), live, n.Notice, h)
				}
			}
		}
	}()
}

// detach forgets an ended scan.
func (m *Manager) detach(sc Watched) {
	m.scansMu.Lock()
	defer m.scansMu.Unlock()
	m.scans = slices.DeleteFunc(m.scans, func(x Watched) bool { return x == sc })
}

// route sends one notice of a scan on network. Replayed notices never
// reach it (the bus replays the last 20 to every new subscriber), nor do
// the storm guard's own (local only, v0.6-D16) and the test alert's.
func (m *Manager) route(network string, live bool, n model.Notice, h *model.Host) {
	switch n.Trigger {
	case model.TriggerNetdiagLink, model.TriggerChatTest:
		return
	}
	if n.OK {
		m.forget(network, n.Key)
		m.routeOK(network, n)
		return
	}
	var in protocol.AlertInput
	if n.Trigger != model.TriggerNewDevice {
		in = alertFor(n, h)
		m.remember(network, n.Key, in)
	}
	m.mu.Lock()
	ok := m.conn != nil && m.enabled && live && sends(n.Severity, m.minSeverity)
	m.mu.Unlock()
	if !ok {
		return
	}
	if n.Trigger == model.TriggerNewDevice {
		// Nothing will ever close a new device's card, so it is a note,
		// not an alert.
		m.out.Message(network, "New device on the local network: "+nameAt(n.Name, n.IP)+".")
		return
	}
	update := m.opt.Ledger != nil && m.opt.Ledger.Chat(network, n.Key) == problems.ChatPosted
	m.out.Post(network, n.Key, in, update)
}

// burstNotice takes the notice of a problem the first pass sent from the
// ledger (burst): the post, while it waits, gets the notice's words; one on
// its way or delivered stays as it is, so the card opens once. It reports
// whether the notice is dealt with; one that came after the problem's post
// was dropped is routed as usual.
func (m *Manager) burstNotice(network string, n model.Notice, h *model.Host) bool {
	if n.OK || n.Trigger == model.TriggerNewDevice {
		return false
	}
	in := alertFor(n, h)
	m.remember(network, n.Key, in)
	if m.out.refresh(network, n.Key, in) {
		return true
	}
	return m.opt.Ledger != nil && m.opt.Ledger.Chat(network, n.Key) == problems.ChatPosted
}

// routeOK resolves the card of a fixed problem, whatever its severity and
// whatever Send is set to now, when it was posted or is about to be: the
// ledger keeps that state on the fixed problem too. A problem never sent
// to chat is fixed without a word there.
func (m *Manager) routeOK(network string, n model.Notice) {
	if m.opt.Ledger != nil && m.opt.Ledger.Chat(network, n.Key) == "" {
		return
	}
	m.out.Resolve(network, n.Key, fixedText(n, m.opt.Now()))
}

// droppedProblems resolves the cards of the problems the ledger dropped
// because netdiag has not been on their network for 7 days.
func (m *Manager) droppedProblems(es []problems.Entry) {
	for _, e := range es {
		m.forget(e.Network, e.Key)
		if e.Chat != "" {
			m.out.Resolve(e.Network, e.Key, "netdiag has not been on this network for 7 days.")
		}
	}
}

// remember keeps in, the alert built from the notice of the problem key on
// network, for a burst or a moved card (alertOf).
func (m *Manager) remember(network, key string, in protocol.AlertInput) {
	k := network + "\x00" + key
	m.scansMu.Lock()
	defer m.scansMu.Unlock()
	if _, ok := m.alerts[k]; !ok && len(m.alerts) >= maxAlerts {
		for old := range m.alerts {
			delete(m.alerts, old)
			break
		}
	}
	m.alerts[k] = in
}

// forget drops what the router keeps of a problem that is fixed or
// dropped: its alert, and that a burst only counted it.
func (m *Manager) forget(network, key string) {
	m.scansMu.Lock()
	delete(m.alerts, network+"\x00"+key)
	m.scansMu.Unlock()
	m.mu.Lock()
	delete(m.summed, network+"\x00"+key)
	m.mu.Unlock()
}

// alertOf is the alert for the open problem e: the one built from its
// notice when netdiag saw one, else one built from the ledger's entry (h
// as alertForEntry takes it).
func (m *Manager) alertOf(e problems.Entry, h *model.Host) protocol.AlertInput {
	m.scansMu.Lock()
	in, ok := m.alerts[e.Network+"\x00"+e.Key]
	m.scansMu.Unlock()
	if ok {
		return in
	}
	return alertForEntry(e, h)
}

// ApplySettings takes the saved settings.chat (at start and after every
// save). Switching Send alerts to team chat off resolves the cards netdiag
// posted, with "netdiag stopped sending alerts here.", and drops what
// waits to be posted; switching it on starts sending again (maybeBurst).
func (m *Manager) ApplySettings(c model.SettingsChat) {
	m.mu.Lock()
	was, first := m.enabled, !m.known
	m.enabled, m.minSeverity, m.known = c.Enabled, c.MinSeverity, true
	switch {
	case first:
	case was && !c.Enabled:
		m.helloDue = false
		m.out.stopPosts()
		if m.opt.Ledger != nil {
			open, _ := m.opt.Ledger.Snapshot()
			for _, e := range open {
				if e.Chat == problems.ChatPosted {
					m.out.Resolve(e.Network, e.Key, "netdiag stopped sending alerts here.")
				}
			}
		}
	case !was && c.Enabled:
		m.helloDue, m.summed = true, nil
	}
	m.mu.Unlock()
	m.maybeBurst()
}

// watched is the network of the scan watching it now, once its first pass
// is reconciled.
func (m *Manager) watched() (string, bool) {
	m.scansMu.Lock()
	defer m.scansMu.Unlock()
	for i := len(m.scans) - 1; i >= 0; i-- {
		sc := m.scans[i]
		if !sc.Request().Live {
			continue
		}
		select {
		case <-sc.Done():
			continue
		case <-sc.Reconciled():
			return sc.Network(), true
		default:
		}
	}
	return "", false
}

// who is how the messages name this netdiag: "netdiag on FRONTDESK-PC".
func (m *Manager) who() string {
	host, _ := m.opt.Hostname()
	if host = strings.TrimSpace(host); host != "" {
		return "netdiag on " + host
	}
	return "netdiag"
}

// sending reports whether alerts can go to chat now (Send alerts on, a
// connection, a channel chosen) and the Send level.
func (m *Manager) sending() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.minSeverity, m.enabled && m.conn != nil && m.conn.Channel.ID != ""
}

// sayHello posts, once sending starts and can (helloDue, and sending), the
// message saying netdiag is connected, and reports whether it did.
func (m *Manager) sayHello() bool {
	m.mu.Lock()
	if !m.helloDue || !m.enabled || m.conn == nil || m.conn.Channel.ID == "" {
		m.mu.Unlock()
		return false
	}
	m.helloDue = false
	m.mu.Unlock()
	network, _ := m.watched()
	m.out.Message(network, m.who()+" is connected. It posts alerts here while it is watching the local network.")
	return true
}

// maybeBurst starts sending when that is due and possible (4.9 "First
// connect"): the message saying netdiag is connected, then the current
// problems of the network a live scan watches now. With no live scan, the
// next one sends them when its first pass ends (Attach): netdiag posts
// alerts only while it watches (v0.6-D10).
func (m *Manager) maybeBurst() {
	if !m.sayHello() {
		return
	}
	if network, ok := m.watched(); ok {
		m.burst(network, nil)
	}
}

// SetProblems gives the burst the Problems right now card's view (the
// service sets it): a burst then posts only the problems the card shows
// as alerted (v0.6 4.9 "First connect").
func (m *Manager) SetProblems(fn Problems) {
	m.mu.Lock()
	m.problems = fn
	m.mu.Unlock()
}

// shown is the keys of network's problems the card shows as alerted
// (SetProblems), and whether it speaks for network; nil and true without
// a card: then every open problem of the ledger counts.
func (m *Manager) shown(network string) (map[string]bool, bool) {
	m.mu.Lock()
	fn := m.problems
	m.mu.Unlock()
	if fn == nil {
		return nil, true
	}
	ps, ok := fn(network)
	if !ok {
		return nil, false
	}
	keys := make(map[string]bool, len(ps))
	for _, p := range ps {
		keys[p.Key] = true
	}
	return keys, true
}

// burst posts network's current problems at or above the Send level that
// have no card in chat and none on its way: at most 10, the most severe
// first, then a message saying how many more there are. The current
// problems are the card's alerted ones (View.Alerted: the ledger's open
// problems the watching scan shows), never a ledger entry the first pass
// adopted without checking it (its device outside the address range). A
// problem marked pending whose post is no longer in the outbox (it lapsed,
// or the outbox file was lost) counts as having none; one an earlier burst
// only counted (summed) is left out until sending starts again. hosts are
// the scan's, when the router has them. It returns the keys it posted.
func (m *Manager) burst(network string, hosts map[string]model.Host) map[string]bool {
	min, ok := m.sending()
	if !ok || m.opt.Ledger == nil {
		return nil
	}
	m.burstMu.Lock() // two bursts at once would post a problem twice
	defer m.burstMu.Unlock()
	shown, ok := m.shown(network)
	if !ok {
		return nil
	}
	m.mu.Lock()
	summed := maps.Clone(m.summed)
	m.mu.Unlock()
	open, _ := m.opt.Ledger.Snapshot()
	var cur []problems.Entry
	for _, e := range open {
		if e.Network != network || !sends(e.Severity, min) || summed[e.Network+"\x00"+e.Key] {
			continue
		}
		if shown != nil && !shown[e.Key] {
			continue
		}
		if e.Chat == "" || (e.Chat == problems.ChatPending && !m.out.hasPost(e.Network, e.Key)) {
			cur = append(cur, e)
		}
	}
	slices.SortStableFunc(cur, func(a, b problems.Entry) int {
		return cmp.Or(severityRank(b.Severity)-severityRank(a.Severity), strings.Compare(a.OpenedAt, b.OpenedAt))
	})
	keys := map[string]bool{}
	for i, e := range cur {
		if i == maxBurst {
			more := len(cur) - maxBurst
			m.out.Message(network, fmt.Sprintf("…and %d more %s. Open %s to see them all.", more, plural(more, "problem", "problems"), m.who()))
			m.mu.Lock()
			if m.summed == nil {
				m.summed = map[string]bool{}
			}
			for _, x := range cur[maxBurst:] {
				m.summed[x.Network+"\x00"+x.Key] = true
			}
			m.mu.Unlock()
			break
		}
		var h *model.Host
		if x, ok := hosts[e.IP]; ok && e.IP != "" {
			h = &x
		}
		m.out.Post(e.Network, e.Key, m.alertOf(e, h), false)
		keys[e.Key] = true
	}
	return keys
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// moveCards moves the open cards to the new channel after Change channel:
// each is resolved with "Moved to #new." and posted again there, saying
// what it said.
func (m *Manager) moveCards(old, ch Channel) {
	m.mu.Lock()
	m.oldChannel = old.Name
	m.mu.Unlock()
	if m.opt.Ledger == nil {
		return
	}
	open, _ := m.opt.Ledger.Snapshot()
	for _, e := range open {
		if e.Chat != problems.ChatPosted {
			continue
		}
		m.out.Resolve(e.Network, e.Key, "Moved to #"+ch.Name+".")
		m.out.Post(e.Network, e.Key, m.alertOf(e, nil), false)
	}
}
