package chat

import (
	"context"
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

// Pairing request states (GET /api/bubbles/pair/status). Approved means
// netdiag has its token and saved it; claimed means the server says the
// token went out already, but not to netdiag.
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
}

// Manager owns the team chat connection: bubbles.json (its one writer),
// the pairing wizard's requests and the connection's state. It is safe for
// concurrent use. The token never leaves it except to the chat server.
// The alert router, outbox and status polling join it in v0.6 M11.
type Manager struct {
	opt    Options
	ctx    context.Context
	cancel context.CancelFunc

	discoverMu sync.Mutex // one discovery at a time

	mu      sync.Mutex
	conn    *Connection // nil when not connected
	needs   *Connection // bubbles.json's server, pin and channel when its token cannot be opened here
	gen     int         // counts connection changes, so a late answer never touches a newer connection
	trouble string      // "", StateUnreachable, StateNeedsReconnect or StateIdentityChanged: what the last call found
	since   time.Time   // when trouble began
	lastErr string      // the last call's error, in words for the page
	found   []Found     // the servers the last discovery found
	cand    *candidate  // the server the person last inspected
	pair    *pairSession
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
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{opt: opt, ctx: ctx, cancel: cancel}
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
	}
	return m
}

// Close stops a pairing request that is still waiting.
func (m *Manager) Close() { m.cancel() }

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
// only the start of it that bubbles shows in Admin > Apps. Waiting,
// Dropped and LastSentAt are the outbox's (v0.6 M11).
type Status struct {
	State       string      `json:"state"`
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
}

// Status reports the connection's state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{State: StateOff, LastError: m.lastErr}
	c := m.conn
	switch {
	case c != nil:
		st.State = StateConnected
		if c.Channel.ID == "" {
			st.State = StateNoChannel
		}
		if m.trouble != "" {
			st.State = m.trouble
			if !m.since.IsZero() {
				st.Since = m.since.UTC().Format(time.RFC3339)
			}
		}
		st.TokenPrefix = c.TokenPrefix()
	case m.needs != nil:
		c = m.needs
		st.State = StateNeedsReconnect
	default:
		st.LastError = ""
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
// replaces one that is still waiting.
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
	if m.pair != nil {
		m.pair.cancel()
		m.pair = nil
	}
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
	if m.pair != nil {
		m.pair.cancel()
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
// token at once and learns the app's name, scopes and channels.
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
	err = m.replaceLocked(conn)
	s.state = PairApproved
	if err != nil {
		s.err = "netdiag could not save the team chat connection: " + err.Error()
	}
	gen := m.gen
	m.mu.Unlock()
	keep := ""
	if prev != nil {
		keep = prev.Channel.ID
	}
	chans, _ := m.learn(ctx, gen, keep)
	m.mu.Lock()
	if m.pair == s {
		s.channels = chans
	}
	m.mu.Unlock()
}

// replaceLocked makes c the connection and saves it; m.mu must be held.
func (m *Manager) replaceLocked(c Connection) error {
	m.gen++
	m.conn, m.needs = &c, nil
	m.trouble, m.since, m.lastErr = "", time.Time{}, ""
	return SaveConnection(m.opt.Files, c)
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
	if err == nil {
		m.updateConn(gen, func(conn *Connection) {
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
		m.updateConn(gen, func(conn *Connection) { conn.Channel = pick })
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

// updateConn changes the connection of generation gen and saves it.
func (m *Manager) updateConn(gen int, f func(*Connection)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil || m.gen != gen {
		return
	}
	c := *m.conn
	f(&c)
	m.conn = &c
	if err := SaveConnection(m.opt.Files, c); err != nil {
		m.opt.Logf("team chat: save %s: %v", store.FileBubbles, err)
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

// PairStatus reports the pairing request the wizard is waiting on.
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
	if s.state == PairApproved && m.conn != nil && m.conn.Channel.ID != "" {
		ch := m.conn.Channel
		out.Channel = &ch
	}
	return out
}

// CancelPairing stops waiting for the admin. bubbles has no way to take a
// request back, so it lapses there after its 10 minutes.
func (m *Manager) CancelPairing() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pair != nil {
		m.pair.cancel()
		m.pair = nil
	}
}

// ChannelChoice is the connection's channel and the channels netdiag's bot
// is in (POST /api/bubbles/channel and /connect).
type ChannelChoice struct {
	State    string    `json:"state"`
	Channel  *Channel  `json:"channel,omitempty"`
	Channels []Channel `json:"channels"`
}

func (m *Manager) choice(chans []Channel) ChannelChoice {
	st := m.Status()
	if chans == nil {
		chans = []Channel{}
	}
	return ChannelChoice{State: st.State, Channel: st.Channel, Channels: chans}
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
	name := ""
	if st, err := c.Status(ctx); err == nil {
		name = strings.TrimSpace(st.InstanceName)
	}
	appName := strings.TrimSpace(self.App.Name)
	if appName == "" {
		appName = AppName
	}
	srv := Server{Name: name, BaseURL: t.BaseURL(), OnboardingURL: t.OnboardingURL(), CAFingerprint: cfg.CAFingerprint}
	conn := NewConnection(srv, appName, slices.Clone(self.Token.Scopes), cfg.Token, m.opt.Now())
	m.mu.Lock()
	keep := ""
	if m.conn != nil {
		keep = m.conn.Channel.ID
	}
	if m.pair != nil {
		m.pair.cancel()
		m.pair = nil
	}
	err = m.replaceLocked(conn)
	gen := m.gen
	m.mu.Unlock()
	if err != nil {
		return ChannelChoice{}, fmt.Errorf("netdiag could not save the team chat connection: %w", err)
	}
	chans, err := m.learn(ctx, gen, keep)
	if err != nil {
		return m.choice(nil), nil
	}
	return m.choice(chans), nil
}

// SetChannel sends alerts to the channel with id from now on. With id ""
// it only lists the channels netdiag's bot is in again (wizard step 6,
// Check again), and picks the channel when there is exactly one.
func (m *Manager) SetChannel(ctx context.Context, id string) (ChannelChoice, error) {
	_, gen, c, err := m.current()
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
	if id == "" {
		if len(chans) == 1 {
			m.updateConn(gen, func(conn *Connection) { conn.Channel = chans[0] })
		}
		return m.choice(chans), nil
	}
	i := slices.IndexFunc(chans, func(ch Channel) bool { return ch.ID == id })
	if i < 0 {
		return m.choice(chans), refused("netdiag isn't in that channel. Choose another one, or ask your chat admin to add netdiag to it.")
	}
	m.updateConn(gen, func(conn *Connection) { conn.Channel = chans[i] })
	return m.choice(chans), nil
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
// membership or scope shows at once.
func (m *Manager) SendTest(ctx context.Context) TestResult {
	conn, gen, c, err := m.current()
	if err != nil {
		return TestResult{Error: err.Error(), Hint: "needs_reconnect"}
	}
	ch := conn.Channel
	if ch.ID == "" {
		return TestResult{Error: "Choose a channel for alerts first.", Hint: "no_channel"}
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	host, _ := m.opt.Hostname()
	text := "netdiag can post its alerts here. This test closes again at once."
	if host = strings.TrimSpace(host); host != "" {
		text = "netdiag on " + host + " can post its alerts here. This test closes again at once."
	}
	zero := 0
	at := m.opt.Now().UTC().Format(time.RFC3339)
	posted, err := c.PostAlertResult(ctx, protocol.AlertInput{Channel: ch.ID, Severity: protocol.SeverityInfo,
		Title: "Test alert from netdiag", Text: text, DedupeKey: TestKey, RenotifyAfterS: &zero, OccurredAt: &at})
	m.note(gen, err)
	if err != nil {
		return testFailure(err, ch.Name)
	}
	_, err = c.ResolveAlertKey(ctx, TestKey, "Test finished.")
	m.note(gen, err)
	if err != nil {
		r := testFailure(err, ch.Name)
		r.Error = "The test alert reached #" + ch.Name + " but netdiag could not close it: " + r.Error
		return r
	}
	return TestResult{OK: true, Transition: posted.Transition, Channel: ch.Name}
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

// note records what a call to the server found: trouble reaching it, a
// token it no longer accepts, or a CA that is not the pinned one. A call
// that gets an answer clears the trouble.
func (m *Manager) note(gen int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gen != gen || m.conn == nil {
		return
	}
	state, msg := "", ""
	switch {
	case err == nil:
	case errors.Is(err, bubbles.ErrServerIdentityChanged):
		state, msg = StateIdentityChanged, identityMsg
	case errors.Is(err, bubbles.ErrUnauthorized):
		state, msg = StateNeedsReconnect, unauthorizedMsg
	case errors.Is(err, bubbles.ErrUnavailable), errors.Is(err, context.DeadlineExceeded):
		state, msg = StateUnreachable, unreachableMsg
	default:
		// The server answered: it is reachable and accepts netdiag.
		m.trouble, m.since = "", time.Time{}
		return
	}
	if m.trouble == StateIdentityChanged && state != StateIdentityChanged && state != "" {
		return // never trusted again by itself
	}
	if state != m.trouble {
		m.since = m.opt.Now()
	}
	if state == "" {
		m.since = time.Time{}
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
	if m.pair != nil {
		m.pair.cancel()
		m.pair = nil
	}
	m.gen++
	m.conn, m.needs, m.cand = nil, nil, nil
	m.trouble, m.since, m.lastErr = "", time.Time{}, ""
	if m.opt.Files == nil {
		return nil
	}
	if err := RemoveConnection(m.opt.Files); err != nil {
		return err
	}
	return m.opt.Files.RemoveFile(store.FileChatOutbox)
}
