package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"netdiag/internal/bubbles"
	"netdiag/internal/bubbles/protocol"
	"netdiag/internal/mdns"
	"netdiag/internal/problems"
	"netdiag/internal/store"
)

// AppName is the app name netdiag pairs under. bubbles names the channel
// that "Also create a channel for this app" makes after it: #netdiag-alerts.
const AppName = "netdiag"

// Scopes are what netdiag asks for at the first pairing (v0.6-D2):
// alerts, the channel list, messages, and files for v0.7's Send to chat,
// so that feature never needs a second pairing (which bubbles would make a
// second app, netdiag-2). Not messages:read.
var Scopes = []string{protocol.ScopeAlertsWrite, protocol.ScopeChannelsRead, protocol.ScopeMessagesWrite, protocol.ScopeFilesWrite}

// TestKey is the dedupe key of the test alert (roadmap 8.2: info, chat
// only).
const TestKey = "netdiag:test"

// States of the team chat connection (GET /api/bubbles).
const (
	StateOff             = "off"              // not set up
	StateConnected       = "connected"        // set up, and the last call worked
	StateNoChannel       = "no_channel"       // paired, but no channel chosen yet
	StateUnreachable     = "unreachable"      // the last call could not reach the server
	StateNeedsReconnect  = "needs_reconnect"  // the server no longer accepts the token, or it cannot be opened here
	StateIdentityChanged = "identity_changed" // the server's CA is not the pinned one
)

// Reasons (Status.Reason) tell two causes of the same state apart, so the
// page says the right thing.
const (
	ReasonRefused    = "refused"     // needs_reconnect: the server answered 401 to the token
	ReasonUnreadable = "unreadable"  // needs_reconnect: the saved token cannot be opened on this computer
	ReasonNotAllowed = "not_allowed" // no_channel: the channel refused netdiag's post (404 or 403)
	ReasonArchived   = "archived"    // no_channel: the channel is archived
)

// Pairing request states (GET /api/bubbles/pair/status). Approved means
// netdiag has its token (saved the moment it arrived) and has asked which
// channels its bot is in; until then the request reads pending. Claimed
// means the server says the token went out already, but not to netdiag.
const (
	PairNone     = "none"
	PairPending  = "pending"
	PairApproved = "approved"
	PairDenied   = "denied"
	PairExpired  = "expired"
	PairClaimed  = "claimed"
	PairError    = "error"
)

// callTimeout bounds each call to the chat server.
const callTimeout = 15 * time.Second

// Options configures a Manager. Zero values get production defaults.
type Options struct {
	// Files is the data folder (bubbles.json and chat-outbox.json); nil
	// leaves netdiag unable to connect (no data store).
	Files DataFiles
	// Browse runs the mDNS browse (default mdns.Browse).
	Browse BrowseFunc
	// TrayConfig names the bubbles tray app's config.json (default
	// TrayConfigPath).
	TrayConfig func() (string, error)
	// Hostname names this computer in the pairing request (default
	// os.Hostname).
	Hostname func() (string, error)
	// Now is the clock (default time.Now).
	Now func() time.Time
	// PairPoll is how often a pairing request is polled while it waits
	// (default 1 s, bubbles' own); tests make it short.
	PairPoll time.Duration
	// Logf receives diagnostics, never a token (default: none).
	Logf func(format string, args ...any)
	// Ledger is the open-problem ledger (*problems.Ledger): the team chat
	// state of each problem, so a card is resolved only when it was posted
	// (v0.6 4.9). nil: the outbox alone tells.
	Ledger Ledger
	// PollEvery is how often the chat server is asked whether it answers
	// (default 60 s); MoveAfter is how long it may not answer before
	// netdiag looks for it at another address (default 2 min).
	PollEvery, MoveAfter time.Duration
	// RetryMin and RetryMax bound the wait before an item is sent again
	// (default 2 s and 60 s).
	RetryMin, RetryMax time.Duration
	// SaveDelay is the shortest time between two saves of chat-outbox.json
	// (default 1 s).
	SaveDelay time.Duration
}

// Ledger is the part of *problems.Ledger the router and the outbox use.
type Ledger interface {
	Chat(network, key string) string
	MarkChat(network, key, state string)
	IsOpen(network, key string) bool
	Snapshot() ([]problems.Entry, []problems.Fixed)
	OnDrop(fn func([]problems.Entry))
}

// Manager owns the team chat connection: bubbles.json and
// chat-outbox.json (their one writer), the pairing wizard's requests, the
// connection's state, the alert router on each scan, the outbox and its
// sender, and the status polling. It is safe for concurrent use. The token
// never leaves it except to the chat server.
//
// Locks: m.mu may be held while the outbox's or the ledger's lock is taken,
// never the other way round.
type Manager struct {
	opt    Options
	ctx    context.Context
	cancel context.CancelFunc
	out    *outbox
	loops  sync.WaitGroup // the sender and the status poller

	amu     sync.Mutex // guards notes
	notes   []Note
	noteSeq int
	run     string // names this run in Status, since note ids start again with it
	pollNow chan struct{}
	scansMu sync.Mutex
	scans   []Watched // the scans the router is attached to, newest last
	// alerts is the last alert built from each open problem's notice (by
	// network and key), so a burst or a moved card says what the notice
	// said; guarded by scansMu.
	alerts map[string]protocol.AlertInput

	discoverMu sync.Mutex // one discovery at a time
	burstMu    sync.Mutex // one burst at a time

	mu      sync.Mutex
	conn    *Connection // nil when not connected
	needs   *Connection // bubbles.json's server, pin and channel when its token cannot be opened here
	gen     int         // counts connection changes, so a late answer never touches a newer connection
	trouble string      // "", StateUnreachable, StateNeedsReconnect or StateIdentityChanged: what the calls found (note)
	since   time.Time   // when trouble began
	lastErr string      // trouble in words for the page
	// chanRefused is why the saved channel refused the last post
	// (ReasonNotAllowed or ReasonArchived; "" when it did not): the state
	// is no_channel until a post gets through or a channel is chosen.
	chanRefused, chanMsg string
	unsaved              string     // why conn could not be saved in bubbles.json ("" when it is saved)
	found                []Found    // the servers the last discovery found
	cand                 *candidate // the server the person last inspected
	pair                 *pairSession
	// pairEpoch counts pairing starts, cancels and disconnects, so a
	// pairing request still on its way when the person cancelled is never
	// waited on.
	pairEpoch int
	// settings.chat as last saved (ApplySettings); known is false until the
	// first ApplySettings.
	enabled, known bool
	minSeverity    string
	// helloDue says sending starts: after a new connection, or Send alerts
	// to team chat switched on, the "is connected" message goes once there
	// is a channel, with the current problems of a network a live scan
	// watches (the next live scan's first pass sends them otherwise).
	helloDue bool
	// summed is the problems (by network and key) a burst only counted in
	// its "…and N more problems" since sending started: a later burst
	// leaves them out, so they never trickle in after it.
	summed map[string]bool
	// oldChannel is the channel before the last Change channel, for the
	// words when a card cannot be moved out of it.
	oldChannel string
	// sendCl is the sender's client, for connection generation sendGen at
	// sendURL (sendClientLocked).
	sendCl  *bubbles.Client
	sendGen int
	sendURL string
}

// candidate is a server whose CA netdiag fetched and showed the person.
type candidate struct {
	t        Target
	fp, name string
}

// pairSession is one pairing request waiting for the admin.
type pairSession struct {
	c        candidate
	code     string
	expires  time.Time
	state    string
	err      string
	channels []Channel
	cancel   context.CancelFunc
}

// NewManager loads bubbles.json (when there is one) and returns the
// manager. Close it when netdiag quits.
func NewManager(opt Options) *Manager {
	if opt.Browse == nil {
		opt.Browse = mdns.Browse
	}
	if opt.TrayConfig == nil {
		opt.TrayConfig = TrayConfigPath
	}
	if opt.Hostname == nil {
		opt.Hostname = os.Hostname
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.PollEvery <= 0 {
		opt.PollEvery = 60 * time.Second
	}
	if opt.MoveAfter <= 0 {
		opt.MoveAfter = 2 * time.Minute
	}
	if opt.RetryMin <= 0 {
		opt.RetryMin = 2 * time.Second
	}
	if opt.RetryMax < opt.RetryMin {
		opt.RetryMax = max(60*time.Second, opt.RetryMin)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{opt: opt, ctx: ctx, cancel: cancel, pollNow: make(chan struct{}, 1), run: newRun(),
		alerts: map[string]protocol.AlertInput{}}
	m.out = newOutbox(opt.Files, opt.Now, m.markChat)
	if opt.Ledger != nil {
		m.out.isOpen = opt.Ledger.IsOpen
	}
	if opt.SaveDelay > 0 {
		m.out.saveDelay = opt.SaveDelay
	}
	if opt.Files != nil {
		c, err := LoadConnection(opt.Files)
		switch {
		case err == nil:
			m.conn = &c
		case errors.Is(err, ErrNeedsReconnect):
			m.needs = &c
			m.lastErr = "netdiag can't open its saved team chat key on this computer, so it has to be connected again."
			opt.Logf("team chat: %v", err)
		case err != ErrNotConnected: // not just a missing file: say what happened to it
			opt.Logf("team chat: %v", err)
		}
		if m.conn != nil || m.needs != nil {
			if err := m.out.load(); err != nil {
				opt.Logf("team chat: %v", err)
			}
		}
	}
	if opt.Ledger != nil {
		opt.Ledger.OnDrop(m.droppedProblems)
	}
	m.loops.Add(2)
	go m.sender()
	go m.poller()
	return m
}

// Close stops the sender, the status polling and a pairing request that
// is still waiting, and saves what the outbox still holds. Flush first to
// give what waits a last chance to go.
func (m *Manager) Close() {
	m.cancel()
	m.loops.Wait()
	m.mu.Lock()
	m.dropSendClientLocked()
	m.mu.Unlock()
	if err := m.out.save(); err != nil {
		m.opt.Logf("team chat: save %s: %v", store.FileChatOutbox, err)
	}
}

// newRun is a random name for this run of netdiag (Status.Run).
func newRun() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// markChat records a problem's team chat state in the ledger.
func (m *Manager) markChat(network, key, state string) {
	if m.opt.Ledger != nil && key != "" {
		m.opt.Ledger.MarkChat(network, key, state)
	}
}

// RefusedError is a request the manager turns down, in words for the page.
type RefusedError struct{ Msg string }

func (e *RefusedError) Error() string { return e.Msg }

func refused(msg string) error { return &RefusedError{Msg: msg} }

// errNoStore is the answer when netdiag has no data folder to keep the
// connection in.
var errNoStore = refused("netdiag can't keep a team chat connection because its data folder is unavailable.")

// ServerView is the chat server as the page shows it.
type ServerView struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	OnboardPort int    `json:"onboardPort"`
}

// Status is the answer of GET /api/bubbles. It never holds the token,
// only the start of it that bubbles shows in Admin > Apps. Reason tells
// why for needs_reconnect and no_channel (the Reason* values). Unsaved
// says, in words, that the connection works but could not be saved, so it
// would be lost when netdiag closes. Waiting, Dropped and LastSentAt are
// the outbox's: what still waits to go (also what a previous run left),
// what it gave up on, and when it last delivered something. Activity is
// the last things the outbox and the polling had to say, for the page's
// Activity log; their ids count up within Run, which names this run of
// netdiag, so a page open across a restart knows the ids started again.
type Status struct {
	State       string      `json:"state"`
	Reason      string      `json:"reason,omitempty"`
	Since       string      `json:"since,omitempty"`
	Server      *ServerView `json:"server,omitempty"`
	Channel     *Channel    `json:"channel,omitempty"`
	AppName     string      `json:"appName,omitempty"`
	TokenPrefix string      `json:"tokenPrefix,omitempty"`
	Scopes      []string    `json:"scopes,omitempty"`
	Waiting     int         `json:"waiting"`
	Dropped     int         `json:"dropped"`
	LastSentAt  string      `json:"lastSentAt,omitempty"`
	LastError   string      `json:"lastError,omitempty"`
	Unsaved     string      `json:"unsaved,omitempty"`
	Activity    []Note      `json:"activity"`
	Run         string      `json:"run"`
}

// Note is one line for the Activity log.
type Note struct {
	ID   int    `json:"id"`
	At   string `json:"at"`
	Text string `json:"text"`
}

// maxNotes is how many notes the status keeps.
const maxNotes = 20

// note adds a line for the Activity log (and the diagnostics).
func (m *Manager) addNote(text string) {
	m.opt.Logf("team chat: %s", text)
	m.amu.Lock()
	defer m.amu.Unlock()
	m.noteSeq++
	m.notes = append(m.notes, Note{ID: m.noteSeq, At: m.opt.Now().UTC().Format(time.RFC3339), Text: text})
	if len(m.notes) > maxNotes {
		m.notes = slices.Clone(m.notes[len(m.notes)-maxNotes:])
	}
}

// Status reports the connection's state. A connection that could not be
// saved is saved again first, so the page's status reads (every 30 s)
// keep trying.
func (m *Manager) Status() Status {
	st := m.status()
	waiting, dropped, last := m.out.stats()
	st.Waiting, st.Dropped = waiting, dropped
	if !last.IsZero() {
		st.LastSentAt = last.UTC().Format(time.RFC3339)
	}
	m.amu.Lock()
	st.Activity, st.Run = slices.Clone(m.notes), m.run
	m.amu.Unlock()
	if st.Activity == nil {
		st.Activity = []Note{}
	}
	return st
}

func (m *Manager) status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{State: StateOff}
	c := m.conn
	switch {
	case c != nil:
		st.State = StateConnected
		switch {
		case m.trouble != "":
			st.State, st.LastError = m.trouble, m.lastErr
			if m.trouble == StateNeedsReconnect {
				st.Reason = ReasonRefused
			}
			if !m.since.IsZero() {
				st.Since = m.since.UTC().Format(time.RFC3339)
			}
		case c.Channel.ID == "":
			st.State = StateNoChannel
		case m.chanRefused != "":
			st.State, st.Reason, st.LastError = StateNoChannel, m.chanRefused, m.chanMsg
		}
		st.TokenPrefix = c.TokenPrefix()
		if m.unsaved != "" && m.saveLocked() != nil {
			st.Unsaved = unsavedMsg(m.unsaved)
		}
	case m.needs != nil:
		c = m.needs
		st.State, st.Reason, st.LastError = StateNeedsReconnect, ReasonUnreadable, m.lastErr
	default:
		return st
	}
	if t, err := targetOf(c.Server.BaseURL, c.Server.OnboardingURL); err == nil {
		st.Server = &ServerView{Name: c.Server.Name, Host: t.Host, Port: t.Port, OnboardPort: t.OnboardPort}
	}
	if c.Channel.ID != "" {
		ch := c.Channel
		st.Channel = &ch
	}
	st.AppName, st.Scopes = c.AppName, slices.Clone(c.Scopes)
	return st
}

// Inspection is the answer of POST /api/bubbles/inspect: the server's CA
// fingerprint for the person to compare with the one the chat admin reads
// out in bubbles (Admin > Overview).
type Inspection struct {
	Address      string   `json:"address"`
	Fingerprint  string   `json:"fingerprint"`
	Groups       []string `json:"groups"`
	ServerName   string   `json:"serverName"`
	ThisComputer string   `json:"thisComputer,omitempty"`
	// FPPrefixMatches is whether the fingerprint starts with the one the
	// server's mDNS announcement gave, when it gave one. The page never
	// shows it as a check: only the person's comparison counts.
	FPPrefixMatches *bool `json:"fpPrefixMatches,omitempty"`
}

// Inspect fetches the CA of the chat server at address from its onboarding
// port (onboard, or the default) and returns its fingerprint. The download
// is not trusted by itself: the person compares the fingerprint first.
func (m *Manager) Inspect(ctx context.Context, address string, onboard int) (Inspection, error) {
	if m.opt.Files == nil {
		return Inspection{}, errNoStore
	}
	t, err := ParseTarget(address, onboard)
	if err != nil {
		return Inspection{}, refused(err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, fp, err := bubbles.FetchCA(ctx, t.OnboardingURL())
	if err != nil {
		m.opt.Logf("team chat: fetch CA from %s: %v", t.OnboardingURL(), err)
		return Inspection{}, refused("netdiag can't reach a chat server at " + t.Host + ". Check the address, and that the chat server is running.")
	}
	c, err := bubbles.New(bubbles.Config{BaseURL: t.BaseURL(), CAFingerprint: fp})
	if err != nil {
		return Inspection{}, err
	}
	st, err := c.Status(ctx)
	switch {
	case errors.Is(err, bubbles.ErrServerIdentityChanged):
		return Inspection{}, refused("The chat server at " + t.Host + " uses a certificate that does not belong to the fingerprint it hands out. Stop here and tell your chat admin.")
	case err != nil:
		m.opt.Logf("team chat: status of %s: %v", t.BaseURL(), err)
		return Inspection{}, refused("netdiag can't reach the chat server at " + t.Address() + ". Check the address, and that the chat server is running.")
	}
	in := Inspection{Address: t.Address(), Fingerprint: fp, Groups: fingerprintGroups(fp),
		ServerName: strings.TrimSpace(st.InstanceName), ThisComputer: localIPFor(t)}
	m.mu.Lock()
	for _, f := range m.found {
		if f.Address == t.Host && f.Port == t.Port && f.FPPrefix != "" {
			ok := strings.HasPrefix(fp[len("sha256:"):], f.FPPrefix)
			in.FPPrefixMatches = &ok
		}
	}
	m.cand = &candidate{t: t, fp: fp, name: in.ServerName}
	m.mu.Unlock()
	return in, nil
}

// localIPFor is this computer's address on the way to t (what the admin
// sees as the request's source), or "". Connecting a UDP socket sends
// nothing.
func localIPFor(t Target) string {
	c, err := net.Dial("udp4", t.Address())
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

// PairStart is the answer of POST /api/bubbles/pair: the code the admin
// compares, when it lapses, and the server's own Admin > Pairing page.
type PairStart struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	AdminURL  string `json:"adminUrl"`
}

// StartPairing asks the chat server for a token for netdiag: the server at
// address must be the one the person inspected, and fingerprint the one
// they confirmed. With reconnect, the saved connection's server and pin
// are used instead and the find and fingerprint steps are skipped (4.10
// Recovery). The request then waits for the chat admin; the token is saved
// the moment it arrives, since bubbles hands it out once. A new request
// replaces one that is still waiting or still on its way, and one that is
// cancelled (CancelPairing, Disconnect) while on its way is never waited on.
func (m *Manager) StartPairing(ctx context.Context, address, fingerprint string, reconnect bool) (PairStart, error) {
	if m.opt.Files == nil {
		return PairStart{}, errNoStore
	}
	var c candidate
	m.mu.Lock()
	if reconnect {
		saved := m.conn
		if saved == nil {
			saved = m.needs
		}
		if saved == nil {
			m.mu.Unlock()
			return PairStart{}, refused("netdiag has no team chat server to reconnect to. Click Connect to team chat.")
		}
		t, err := targetOf(saved.Server.BaseURL, saved.Server.OnboardingURL)
		if err != nil {
			m.mu.Unlock()
			return PairStart{}, refused(err.Error())
		}
		c = candidate{t: t, fp: saved.Server.CAFingerprint, name: saved.Server.Name}
	} else {
		t, terr := ParseTarget(address, 0)
		fp, ferr := bubbles.NormalizeFingerprint(fingerprint)
		if m.cand == nil || terr != nil || ferr != nil || m.cand.t.Address() != t.Address() || m.cand.fp != fp {
			m.mu.Unlock()
			return PairStart{}, refused("Check the chat server's fingerprint again before you connect.")
		}
		c = *m.cand
	}
	m.stopPairLocked()
	epoch := m.pairEpoch
	m.mu.Unlock()

	host, _ := m.opt.Hostname()
	desc := "netdiag"
	if host = strings.TrimSpace(host); host != "" {
		desc = "netdiag on " + host
	}
	rctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	p, err := bubbles.RequestPairing(rctx, c.t.BaseURL(), c.fp,
		protocol.PairingRequest{AppName: AppName, Description: desc, RequestedScopes: Scopes})
	if err != nil {
		_, msg := explain(err)
		return PairStart{}, refused(msg)
	}
	p.SetPollInterval(m.opt.PairPoll)
	wctx, wcancel := context.WithCancel(m.ctx)
	s := &pairSession{c: c, code: p.Code(), expires: p.ExpiresAt(), state: PairPending, cancel: wcancel}
	m.mu.Lock()
	if m.pairEpoch != epoch {
		// Cancelled, disconnected or replaced while the request was on its
		// way: never wait for this one. bubbles lets it lapse.
		m.mu.Unlock()
		wcancel()
		return PairStart{}, refused("The request to connect was cancelled.")
	}
	m.pair = s
	m.mu.Unlock()
	go m.wait(wctx, s, p)
	out := PairStart{Code: formatCode(s.code), AdminURL: c.t.AdminPairingURL()}
	if !s.expires.IsZero() {
		out.ExpiresAt = s.expires.UTC().Format(time.RFC3339)
	}
	return out, nil
}

// formatCode shows a six-digit code as two groups: "123 456".
func formatCode(code string) string {
	if len(code) == 6 {
		return code[:3] + " " + code[3:]
	}
	return code
}

// wait polls the pairing request until the admin decides, then saves the
// token at once and learns the app's name, scopes and channels. The
// request reads approved only once the channels are learnt, so the wizard
// never sees an approval without the channel the admin ticked.
func (m *Manager) wait(ctx context.Context, s *pairSession, p *bubbles.Pairing) {
	cfg, err := p.Wait(ctx)
	if err != nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.pair != s || errors.Is(err, context.Canceled) {
			return
		}
		switch {
		case errors.Is(err, bubbles.ErrPairingDenied):
			s.state, s.err = PairDenied, "Your chat admin said no."
		case errors.Is(err, bubbles.ErrPairingExpired):
			s.state, s.err = PairExpired, "The request ran out of time after 10 minutes."
		case errors.Is(err, bubbles.ErrPairingClaimed):
			s.state, s.err = PairClaimed, "The chat server says this request was already used. Try again."
		default:
			_, msg := explain(err)
			s.state, s.err = PairError, msg
		}
		return
	}
	srv := Server{Name: s.c.name, BaseURL: s.c.t.BaseURL(), OnboardingURL: s.c.t.OnboardingURL(), CAFingerprint: s.c.fp}
	conn := NewConnection(srv, AppName, slices.Clone(Scopes), cfg.Token, m.opt.Now())
	m.mu.Lock()
	if m.pair != s {
		m.mu.Unlock()
		return
	}
	prev := m.conn
	if prev == nil {
		prev = m.needs
	}
	// Kept in memory even when the save fails (m.unsaved, tried again by
	// Status): the token cannot be fetched a second time.
	m.swapLocked(conn)
	_ = m.saveLocked()
	gen := m.gen
	m.mu.Unlock()
	keep := ""
	if prev != nil {
		keep = prev.Channel.ID
	}
	chans, lerr := m.learn(ctx, gen, keep)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pair != s {
		return
	}
	s.state, s.channels = PairApproved, chans
	if lerr != nil {
		_, s.err = explain(lerr)
	}
}

// swapLocked makes c the connection, in memory only; m.mu must be held.
// The trouble the old connection had goes with it, and so do the cards it
// posted: a new connection is a new app in bubbles, which cannot close
// them, so the current problems go to chat again once sending starts. The
// posts still waiting make new cards.
func (m *Manager) swapLocked(c Connection) {
	m.gen++
	m.conn, m.needs = &c, nil
	m.trouble, m.since, m.lastErr = "", time.Time{}, ""
	m.chanRefused, m.chanMsg, m.unsaved = "", "", ""
	m.dropSendClientLocked()
	m.forgetCards()
	m.helloDue, m.summed = true, nil
	m.out.kick()
}

// forgetCards records that no card of netdiag's is in chat any more: a
// problem whose post still waits in the outbox is pending (that post makes
// a new card), every other one has no chat state.
func (m *Manager) forgetCards() {
	if m.opt.Ledger == nil {
		return
	}
	open, _ := m.opt.Ledger.Snapshot()
	m.out.newApp(open)
}

// saveLocked writes the connection to bubbles.json; m.mu must be held and
// m.conn set. A failure is kept in m.unsaved until a save works.
func (m *Manager) saveLocked() error {
	err := SaveConnection(m.opt.Files, *m.conn)
	m.unsaved = ""
	if err != nil {
		m.unsaved = err.Error()
		m.opt.Logf("team chat: save %s: %v", store.FileBubbles, err)
	}
	return err
}

// unsavedMsg is a failed save in words for the page.
func unsavedMsg(reason string) string {
	return "netdiag could not save the team chat connection on this computer (" + reason +
		"). It works until netdiag closes, and netdiag keeps trying to save it."
}

// learn asks the server which app and scopes the new token has and which
// channels its bot is in, and saves what it learnt. When the bot is in
// exactly one channel (or in keep, the channel of the connection this one
// replaces), alerts go there. It returns the channels.
func (m *Manager) learn(ctx context.Context, gen int, keep string) ([]Channel, error) {
	c, ok := m.clientFor(gen)
	if !ok {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	self, err := c.Self(ctx)
	m.note(gen, err)
	if err == nil {
		_ = m.updateConn(gen, func(conn *Connection) {
			if n := strings.TrimSpace(self.App.Name); n != "" {
				conn.AppName = n
			}
			if len(self.Token.Scopes) > 0 {
				conn.Scopes = slices.Clone(self.Token.Scopes)
			}
		})
	}
	chans, err := m.channels(ctx, c, gen)
	if err != nil {
		return nil, err
	}
	pick := Channel{}
	if i := slices.IndexFunc(chans, func(ch Channel) bool { return ch.ID == keep }); i >= 0 {
		pick = chans[i]
	} else if len(chans) == 1 {
		pick = chans[0]
	}
	if pick.ID != "" {
		_ = m.setChannel(gen, pick)
		m.maybeBurst()
	}
	return chans, nil
}

// clientFor builds a client for the connection of generation gen.
func (m *Manager) clientFor(gen int) (*bubbles.Client, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil || m.gen != gen {
		return nil, false
	}
	c, err := bubbles.New(m.conn.ClientConfig())
	return c, err == nil
}

// current is the connection, its generation and a client for it.
func (m *Manager) current() (Connection, int, *bubbles.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil {
		return Connection{}, 0, nil, refused("netdiag isn't connected to team chat. Click Connect to team chat.")
	}
	c, err := bubbles.New(m.conn.ClientConfig())
	if err != nil {
		return Connection{}, 0, nil, err
	}
	return *m.conn, m.gen, c, nil
}

// updateConn changes the connection of generation gen and saves it. The
// change holds in memory even when the save fails (m.unsaved).
func (m *Manager) updateConn(gen int, f func(*Connection)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil || m.gen != gen {
		return nil
	}
	c := *m.conn
	f(&c)
	m.conn = &c
	return m.saveLocked()
}

// setChannel makes ch the channel of generation gen: it works for posts
// again until one is refused.
func (m *Manager) setChannel(gen int, ch Channel) error {
	return m.updateConn(gen, func(c *Connection) {
		c.Channel = ch
		m.chanRefused, m.chanMsg = "", "" // updateConn holds m.mu
		m.out.kick()
	})
}

// refuseChannel records that the channel with id refused a post (reason
// ReasonNotAllowed or ReasonArchived): the state is no_channel, with the
// channel kept so Change channel can show it.
func (m *Manager) refuseChannel(gen int, id, reason, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn != nil && m.gen == gen && m.conn.Channel.ID == id {
		m.chanRefused, m.chanMsg = reason, msg
	}
}

// channelWorks records that a post to the channel with id got through.
func (m *Manager) channelWorks(gen int, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn != nil && m.gen == gen && m.conn.Channel.ID == id && m.chanRefused != "" {
		m.chanRefused, m.chanMsg = "", ""
		m.out.kick()
	}
}

// PairStatus is the answer of GET /api/bubbles/pair/status.
type PairStatus struct {
	State     string    `json:"state"`
	Code      string    `json:"code,omitempty"`
	ExpiresAt string    `json:"expiresAt,omitempty"`
	Channel   *Channel  `json:"channel,omitempty"`
	Channels  []Channel `json:"channels"`
	Error     string    `json:"error,omitempty"`
}

// PairStatus reports the pairing request the wizard is waiting on. An
// approved request carries an error when the channels could not be
// listed, or when the connection works but could not be saved.
func (m *Manager) PairStatus() PairStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.pair
	if s == nil {
		return PairStatus{State: PairNone, Channels: []Channel{}}
	}
	out := PairStatus{State: s.state, Code: formatCode(s.code), Channels: slices.Clone(s.channels), Error: s.err}
	if out.Channels == nil {
		out.Channels = []Channel{}
	}
	if !s.expires.IsZero() {
		out.ExpiresAt = s.expires.UTC().Format(time.RFC3339)
	}
	if s.state == PairApproved && m.conn != nil {
		if m.conn.Channel.ID != "" {
			ch := m.conn.Channel
			out.Channel = &ch
		}
		if m.unsaved != "" {
			out.Error = unsavedMsg(m.unsaved)
		}
	}
	return out
}

// CancelPairing stops waiting for the admin, also for a request still on
// its way. bubbles has no way to take a request back, so it lapses there
// after its 10 minutes.
func (m *Manager) CancelPairing() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopPairLocked()
}

// stopPairLocked stops waiting for the admin, and makes StartPairing drop
// a request that is still on its way; m.mu must be held.
func (m *Manager) stopPairLocked() {
	m.pairEpoch++
	if m.pair != nil {
		m.pair.cancel()
		m.pair = nil
	}
}

// ChannelChoice is the connection's channel and the channels netdiag's bot
// is in (POST /api/bubbles/channel and /connect). Error says what went
// wrong after connecting: the channels could not be listed, or the
// connection works but could not be saved.
type ChannelChoice struct {
	State    string    `json:"state"`
	Channel  *Channel  `json:"channel,omitempty"`
	Channels []Channel `json:"channels"`
	Error    string    `json:"error,omitempty"`
}

func (m *Manager) choice(chans []Channel, err error) ChannelChoice {
	st := m.status()
	if chans == nil {
		chans = []Channel{}
	}
	out := ChannelChoice{State: st.State, Channel: st.Channel, Channels: chans, Error: st.Unsaved}
	if err != nil {
		_, out.Error = explain(err)
	}
	return out
}

// ConnectString connects with a bubbles connection string
// (bubbles://host:port/?token=…&fp=…) that the chat admin made in Admin >
// Apps. The string carries the pin, so there is no fingerprint step. The
// connection is saved at once, and when the bot is in exactly one channel
// alerts go there.
func (m *Manager) ConnectString(ctx context.Context, s string) (ChannelChoice, error) {
	if m.opt.Files == nil {
		return ChannelChoice{}, errNoStore
	}
	cfg, err := bubbles.ParseConnectionString(s)
	if err != nil {
		return ChannelChoice{}, refused("This is not a team chat connection string. It starts with bubbles://")
	}
	t, err := targetOf(cfg.BaseURL, "")
	if err != nil {
		return ChannelChoice{}, refused(err.Error())
	}
	c, err := bubbles.New(cfg)
	if err != nil {
		return ChannelChoice{}, refused("This connection string can't be used.")
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	self, err := c.Self(ctx)
	switch {
	case errors.Is(err, bubbles.ErrUnauthorized):
		return ChannelChoice{}, refused("The chat server does not accept this connection string. Ask your chat admin for a new one.")
	case err != nil:
		_, msg := explain(err)
		return ChannelChoice{}, refused(msg)
	}
	// The name comes from GET /status asked without the token: bubbles
	// v0.2.0 answers 403 to an app token there.
	name := serverName(ctx, t, cfg.CAFingerprint)
	appName := strings.TrimSpace(self.App.Name)
	if appName == "" {
		appName = AppName
	}
	srv := Server{Name: name, BaseURL: t.BaseURL(), OnboardingURL: t.OnboardingURL(), CAFingerprint: cfg.CAFingerprint}
	conn := NewConnection(srv, appName, slices.Clone(self.Token.Scopes), cfg.Token, m.opt.Now())
	m.mu.Lock()
	// Saved before it replaces anything: when the save fails, the
	// connection netdiag had stays, and the person can paste the string
	// again.
	if err := SaveConnection(m.opt.Files, conn); err != nil {
		m.mu.Unlock()
		m.opt.Logf("team chat: save %s: %v", store.FileBubbles, err)
		return ChannelChoice{}, fmt.Errorf("netdiag could not save the team chat connection on this computer, so it did not connect: %w", err)
	}
	keep := ""
	if m.conn != nil {
		keep = m.conn.Channel.ID
	}
	m.stopPairLocked()
	m.swapLocked(conn)
	gen := m.gen
	m.mu.Unlock()
	chans, err := m.learn(ctx, gen, keep)
	return m.choice(chans, err), nil
}

// SetChannel sends alerts to the channel with id from now on. With id ""
// it only lists the channels netdiag's bot is in again (wizard step 6,
// Check again, and Change channel), and picks the channel when there is
// exactly one; the current channel, when the bot is in it, counts as
// chosen again. A chosen channel takes posts again (it clears no_channel).
// A choice that could not be saved holds until netdiag closes, and is
// refused in words so the person knows.
func (m *Manager) SetChannel(ctx context.Context, id string) (ChannelChoice, error) {
	conn, gen, c, err := m.current()
	if err != nil {
		return ChannelChoice{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	chans, err := m.channels(ctx, c, gen)
	if err != nil {
		_, msg := explain(err)
		return ChannelChoice{}, refused(msg)
	}
	index := func(id string) int { return slices.IndexFunc(chans, func(ch Channel) bool { return ch.ID == id }) }
	i := -1
	switch {
	case id != "":
		if i = index(id); i < 0 {
			return m.choice(chans, nil), refused("netdiag isn't in that channel. Choose another one, or ask your chat admin to add netdiag to it.")
		}
	case len(chans) == 1:
		i = 0
	case conn.Channel.ID != "":
		i = index(conn.Channel.ID)
	}
	if i >= 0 {
		if err := m.setChannel(gen, chans[i]); err != nil {
			return m.choice(chans, nil), refused("netdiag could not save the channel on this computer (" + err.Error() + "). Try again.")
		}
		if old := conn.Channel; old.ID != "" && old.ID != chans[i].ID {
			m.moveCards(old, chans[i])
		}
		m.maybeBurst()
	}
	return m.choice(chans, nil), nil
}

// channels lists the channels the bot can post in: public and private
// ones that are not archived.
func (m *Manager) channels(ctx context.Context, c *bubbles.Client, gen int) ([]Channel, error) {
	list, err := c.ListChannels(ctx)
	m.note(gen, err)
	if err != nil {
		return nil, err
	}
	out := []Channel{}
	for _, ch := range list {
		if ch.ArchivedAt != nil || ch.Name == nil || (ch.Kind != protocol.ChannelPublic && ch.Kind != protocol.ChannelPrivate) {
			continue
		}
		out = append(out, Channel{ID: ch.ID, Name: *ch.Name})
	}
	slices.SortFunc(out, func(a, b Channel) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// TestResult is the answer of POST /api/bubbles/test. Hint names what went
// wrong for the page: not_allowed, unreachable, needs_reconnect,
// identity_changed, no_channel, busy or refused.
type TestResult struct {
	OK         bool   `json:"ok"`
	Transition string `json:"transition,omitempty"`
	Channel    string `json:"channel,omitempty"`
	Error      string `json:"error,omitempty"`
	Hint       string `json:"hint,omitempty"`
}

// SendTest posts a test alert to the channel and resolves it again at
// once, both directly (never through the outbox), so a missing channel
// membership or scope shows at once. Each call has its own time, and the
// resolve goes on when the page that asked is closed, so the test card is
// not left open. A test card still open in another channel (an earlier
// test that could not close, then Change channel) is resolved there and
// the test posted once more (4.9's 409 rule). A post the channel refuses
// (404, 403, or archived) makes the state no_channel.
func (m *Manager) SendTest(ctx context.Context) TestResult {
	conn, gen, c, err := m.current()
	if err != nil {
		return TestResult{Error: err.Error(), Hint: "needs_reconnect"}
	}
	m.mu.Lock()
	changed := m.trouble == StateIdentityChanged && m.gen == gen
	m.mu.Unlock()
	if changed {
		// netdiag stopped sending: only Reconnect trusts a server again.
		return TestResult{Error: identityMsg, Hint: "identity_changed"}
	}
	ch := conn.Channel
	if ch.ID == "" {
		return TestResult{Error: "Choose a channel for alerts first.", Hint: "no_channel"}
	}
	host, _ := m.opt.Hostname()
	text := "netdiag can post its alerts here. This test closes again at once."
	if host = strings.TrimSpace(host); host != "" {
		text = "netdiag on " + host + " can post its alerts here. This test closes again at once."
	}
	zero := 0
	at := m.opt.Now().UTC().Format(time.RFC3339)
	in := protocol.AlertInput{Channel: ch.ID, Severity: protocol.SeverityInfo,
		Title: "Test alert from netdiag", Text: text, DedupeKey: TestKey, RenotifyAfterS: &zero, OccurredAt: &at}
	post := func() (*protocol.PostedAlert, error) {
		pctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		p, err := c.PostAlertResult(pctx, in)
		m.note(gen, err)
		return p, err
	}
	resolve := func() error {
		rctx, cancel := context.WithTimeout(m.ctx, callTimeout)
		defer cancel()
		_, err := c.ResolveAlertKey(rctx, TestKey, "Test finished.")
		m.note(gen, err)
		return err
	}
	posted, err := post()
	if openElsewhere(err) && resolve() == nil {
		posted, err = post()
	}
	if err != nil {
		r := testFailure(err, ch.Name)
		switch r.Hint {
		case "not_allowed":
			m.refuseChannel(gen, ch.ID, ReasonNotAllowed, r.Error)
		case "no_channel":
			m.refuseChannel(gen, ch.ID, ReasonArchived, r.Error)
		}
		return r
	}
	m.channelWorks(gen, ch.ID)
	if err := resolve(); err != nil {
		r := testFailure(err, ch.Name)
		r.Error = "The test alert reached #" + ch.Name + " but netdiag could not close it: " + r.Error
		return r
	}
	return TestResult{OK: true, Transition: posted.Transition, Channel: ch.Name}
}

// openElsewhere is bubbles' 409 for a dedupe key whose alert is still open
// in another channel. The same 409 "conflict" for an archived channel says
// "archived" (v0.6-D24).
func openElsewhere(err error) bool {
	var ae *bubbles.APIError
	return errors.As(err, &ae) && ae.Status == http.StatusConflict && !strings.Contains(ae.Message, "archived")
}

// testFailure puts a failed test post into words.
func testFailure(err error, channel string) TestResult {
	var ae *bubbles.APIError
	if errors.As(err, &ae) {
		switch {
		case ae.Status == http.StatusNotFound || ae.Status == http.StatusForbidden:
			return TestResult{Error: "netdiag isn't allowed to post in #" + channel + ". Ask your chat admin to add netdiag to that channel.", Hint: "not_allowed"}
		case ae.Status == http.StatusConflict && strings.Contains(ae.Message, "archived"):
			return TestResult{Error: "The channel #" + channel + " is archived. Choose another channel.", Hint: "no_channel"}
		}
	}
	hint, msg := explain(err)
	return TestResult{Error: msg, Hint: hint}
}

// note records what a call made with the token found (v0.6-D39): a CA
// that is not the pinned one (identity_changed), a token the server no
// longer accepts (needs_reconnect), or no answer (unreachable). Only a new
// connection (Reconnect, a connection string, pairing) clears
// identity_changed, since it is never trusted again by itself; only a call
// the token passes clears needs_reconnect; and any answer from the server
// clears unreachable. A call that was cancelled (the page closed, or the
// wizard cancelled) tells nothing about the server and changes nothing.
func (m *Manager) note(gen int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gen != gen || m.conn == nil || m.trouble == StateIdentityChanged {
		return
	}
	var ae *bubbles.APIError
	switch {
	case err == nil:
		m.troubleLocked("", "")
	case errors.Is(err, bubbles.ErrServerIdentityChanged):
		m.troubleLocked(StateIdentityChanged, identityMsg)
	case errors.Is(err, bubbles.ErrUnauthorized):
		m.troubleLocked(StateNeedsReconnect, unauthorizedMsg)
	case m.trouble == StateNeedsReconnect:
		// Stays until a call the token passes.
	case errors.Is(err, bubbles.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		m.troubleLocked(StateUnreachable, unreachableMsg)
	case errors.As(err, &ae):
		// The server answered (400, 403, 404, 409, 429): it is reachable.
		m.troubleLocked("", "")
	}
}

// troubleLocked sets the trouble the calls found ("" for none); m.mu must
// be held.
func (m *Manager) troubleLocked(state, msg string) {
	if state != m.trouble {
		m.since = m.opt.Now()
	}
	if state == "" {
		m.since = time.Time{}
		if m.trouble != "" {
			m.out.kick() // the outbox may go on
		}
	}
	m.trouble, m.lastErr = state, msg
}

const (
	identityMsg     = "Warning: the chat server's identity changed. netdiag stopped sending alerts. Ask your chat admin before you reconnect."
	unauthorizedMsg = "The chat server no longer accepts netdiag. Click Reconnect."
	unreachableMsg  = "netdiag can't reach the chat server."
)

// explain puts an error from the chat server into a hint and words.
func explain(err error) (hint, msg string) {
	var ae *bubbles.APIError
	switch {
	case errors.Is(err, bubbles.ErrServerIdentityChanged):
		return "identity_changed", identityMsg
	case errors.Is(err, bubbles.ErrUnauthorized):
		return "needs_reconnect", unauthorizedMsg
	case errors.Is(err, bubbles.ErrRateLimited):
		return "busy", "The chat server is busy. Try again in a minute."
	case errors.Is(err, bubbles.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		return "unreachable", unreachableMsg
	case errors.As(err, &ae):
		reason := strings.TrimSpace(ae.Message)
		if fe := ae.FieldErrors(); len(fe) > 0 {
			k := slices.Sorted(maps.Keys(fe))[0]
			reason = k + ": " + fe[k]
		}
		return "refused", "The chat server refused it: " + reason
	}
	return "refused", "Something went wrong talking to the chat server: " + err.Error()
}

// Disconnect forgets the connection: bubbles.json, its set-aside copies
// and the outbox. Apps cannot revoke their own token in bubbles, so the
// page asks the person to have the chat admin remove netdiag in Admin >
// Apps too.
func (m *Manager) Disconnect() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopPairLocked()
	m.gen++
	m.conn, m.needs, m.cand = nil, nil, nil
	m.trouble, m.since, m.lastErr = "", time.Time{}, ""
	m.chanRefused, m.chanMsg, m.unsaved = "", "", ""
	m.helloDue = false
	m.dropSendClientLocked()
	m.out.clearAll()
	m.forgetCards()
	if m.opt.Files == nil {
		return nil
	}
	if err := RemoveConnection(m.opt.Files); err != nil {
		return err
	}
	return m.opt.Files.RemoveFile(store.FileChatOutbox)
}
