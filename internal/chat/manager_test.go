package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"netdiag/internal/bubbles"
	"netdiag/internal/bubbles/bubblestest"
	"netdiag/internal/bubbles/protocol"
	"netdiag/internal/mdns"
	"netdiag/internal/model"
	"netdiag/internal/store"
)

// noTray is a bubbles tray config that does not exist.
func noTray() (string, error) {
	return filepath.Join(os.TempDir(), "netdiag-no-such-tray", "config.json"), nil
}

func noBrowse(context.Context, string, []string, time.Duration) ([]mdns.Record, error) {
	return nil, nil
}

// newManager is a manager on st that never browses the network or reads
// the real bubbles tray config, and polls pairings quickly.
func newManager(t *testing.T, st DataFiles, edit ...func(*Options)) *Manager {
	t.Helper()
	opt := Options{Files: st, Browse: noBrowse, TrayConfig: noTray, PairPoll: 5 * time.Millisecond,
		Hostname: func() (string, error) { return "FRONTDESK-PC", nil }}
	for _, e := range edit {
		e(&opt)
	}
	m := NewManager(opt)
	t.Cleanup(m.Close)
	return m
}

// diagLog keeps what a manager writes to the diagnostics log.
type diagLog struct {
	mu    sync.Mutex
	lines []string
}

// into sends the manager's diagnostics to l.
func (l *diagLog) into(o *Options) {
	o.Logf = func(format string, args ...any) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.lines = append(l.lines, fmt.Sprintf(format, args...))
	}
}

func (l *diagLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// fakeAddress is the fake server's address as the person would type it,
// and its onboarding port.
func fakeAddress(t *testing.T, f *bubblestest.Server) (string, int) {
	t.Helper()
	a, _ := url.Parse(f.API.URL)
	o, _ := url.Parse(f.Onboard.URL)
	p, _ := strconv.Atoi(o.Port())
	return a.Host, p
}

// waitPair waits for the pairing request to leave pending.
func waitPair(t *testing.T, m *Manager) PairStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ps := m.PairStatus(); ps.State != PairPending {
			return ps
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pairing still %+v", m.PairStatus())
	return PairStatus{}
}

// waitApproved waits until the approved pairing has learnt its channels.
func waitApproved(t *testing.T, m *Manager) PairStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ps := m.PairStatus()
		if ps.State == PairApproved && len(ps.Channels) > 0 {
			return ps
		}
		if ps.State != PairPending && ps.State != PairApproved {
			t.Fatalf("pairing ended %+v", ps)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pairing still %+v", m.PairStatus())
	return PairStatus{}
}

// pairWith walks the wizard against the fake: inspect, compare (the test
// takes the fingerprint as matching), ask the admin, approve.
func pairWith(t *testing.T, m *Manager, f *bubblestest.Server) PairStatus {
	t.Helper()
	ctx := context.Background()
	addr, onboard := fakeAddress(t, f)
	in, err := m.Inspect(ctx, addr, onboard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartPairing(ctx, in.Address, in.Fingerprint, false); err != nil {
		t.Fatal(err)
	}
	f.SetPairingState(protocol.PairingApproved)
	return waitApproved(t, m)
}

// The wizard end to end against the bubbles v0.2.0 fake: the fingerprint
// shown is the server's CA, in 16 groups of four; the pairing asks for the
// D2 scopes as "netdiag on <PC>"; the token is saved sealed the moment it
// arrives; the one channel is picked by itself; and the test alert reaches
// it and is resolved at once (4.10 steps 3-7).
func TestPairingAgainstV020(t *testing.T) {
	f := bubblestest.New(t)
	st, dir := newStore(t)
	m := newManager(t, st)
	ctx := context.Background()
	if s := m.Status(); s.State != StateOff || s.Server != nil {
		t.Fatalf("before pairing: %+v", s)
	}

	addr, onboard := fakeAddress(t, f)
	in, err := m.Inspect(ctx, addr, onboard)
	if err != nil {
		t.Fatal(err)
	}
	if in.Fingerprint != f.CAFingerprint || len(in.Groups) != 16 || in.ServerName != "Office" || in.Address != addr {
		t.Fatalf("inspection %+v, want %s", in, f.CAFingerprint)
	}
	if strings.Join(in.Groups, "") != strings.ToUpper(strings.TrimPrefix(f.CAFingerprint, "sha256:")) {
		t.Fatalf("groups %v", in.Groups)
	}
	if in.ThisComputer != "127.0.0.1" {
		t.Errorf("this computer %q", in.ThisComputer)
	}

	start, err := m.StartPairing(ctx, in.Address, in.Fingerprint, false)
	if err != nil {
		t.Fatal(err)
	}
	if start.Code != "123 456" || start.AdminURL != f.API.URL+"/admin/pairing" || start.ExpiresAt == "" {
		t.Fatalf("start %+v", start)
	}
	if ps := m.PairStatus(); ps.State != PairPending || ps.Code != "123 456" {
		t.Fatalf("pair status %+v", ps)
	}
	f.SetPairingState(protocol.PairingApproved)
	ps := waitApproved(t, m)
	if ps.Channel == nil || ps.Channel.ID != "C1" || len(ps.Channels) != 1 || ps.Channels[0].Name != "netdiag-alerts" {
		t.Fatalf("approved %+v", ps)
	}
	if !slices.Equal(f.Scopes(), Scopes) {
		t.Fatalf("asked for %v, want %v", f.Scopes(), Scopes)
	}

	// Saved at once, sealed, with the pin, server and channel.
	c, err := LoadConnection(st)
	if err != nil {
		t.Fatal(err)
	}
	if c.Token() != f.Token() || c.Server.CAFingerprint != f.CAFingerprint || c.Server.Name != "Office" ||
		c.Server.BaseURL != f.API.URL || c.Channel.ID != "C1" || c.AppName != "netdiag" {
		t.Fatalf("saved %v (token ok %v)", c, c.Token() == f.Token())
	}
	b, _ := os.ReadFile(filepath.Join(dir, store.FileBubbles))
	if strings.Contains(string(b), f.Token()) && readFile(t, dir).Token.Protected != "none" {
		t.Fatal("bubbles.json holds the token in clear")
	}

	st2 := m.Status()
	if st2.State != StateConnected || st2.Channel == nil || st2.Channel.Name != "netdiag-alerts" ||
		st2.Server == nil || st2.Server.Name != "Office" || st2.Server.Host != "127.0.0.1" || st2.TokenPrefix != f.Token()[:8] {
		t.Fatalf("status %+v", st2)
	}
	for _, v := range []any{st2, m.PairStatus()} {
		if j, _ := json.Marshal(v); strings.Contains(string(j), f.Token()) {
			t.Fatalf("%s shows the token", j)
		}
	}

	res := m.SendTest(ctx)
	if !res.OK || res.Transition != protocol.AlertTransitionCreated || res.Channel != "netdiag-alerts" {
		t.Fatalf("test %+v", res)
	}
	posted := f.Posted()
	if len(posted) != 1 || posted[0].DedupeKey != TestKey || posted[0].Severity != protocol.SeverityInfo ||
		posted[0].Channel != "C1" || posted[0].RenotifyAfterS == nil || *posted[0].RenotifyAfterS != 0 ||
		!strings.Contains(posted[0].Text, "FRONTDESK-PC") {
		t.Fatalf("posted %+v", posted)
	}
	if card, ok := f.Alert(TestKey); !ok || card.Alert.State != protocol.AlertResolved {
		t.Fatalf("test card %+v (found %v), want resolved", card, ok)
	}
}

// The pairing request names this computer and only the inspected server
// with the confirmed fingerprint can be paired with.
func TestPairNeedsTheInspectedFingerprint(t *testing.T) {
	f := bubblestest.New(t)
	st, _ := newStore(t)
	m := newManager(t, st)
	ctx := context.Background()
	addr, onboard := fakeAddress(t, f)
	var refusedErr *RefusedError
	if _, err := m.StartPairing(ctx, addr, f.CAFingerprint, false); !errors.As(err, &refusedErr) {
		t.Fatalf("pairing without inspecting: %v", err)
	}
	in, err := m.Inspect(ctx, addr, onboard)
	if err != nil {
		t.Fatal(err)
	}
	other := "sha256:" + strings.Repeat("ab", 32)
	if _, err := m.StartPairing(ctx, in.Address, other, false); !errors.As(err, &refusedErr) {
		t.Fatalf("pairing with another fingerprint: %v", err)
	}
	if _, err := m.StartPairing(ctx, in.Address, strings.Join(in.Groups, " "), false); err != nil {
		t.Fatalf("the grouped form of the same fingerprint: %v", err)
	}
	if m.PairStatus().State != PairPending {
		t.Fatalf("state %+v", m.PairStatus())
	}
}

// Denied and expired requests say so; Cancel stops waiting locally.
func TestPairingDeniedExpiredCancelled(t *testing.T) {
	for _, c := range []struct{ state, want, msg string }{
		{protocol.PairingDenied, PairDenied, "Your chat admin said no."},
		{protocol.PairingExpired, PairExpired, "The request ran out of time after 10 minutes."},
	} {
		f := bubblestest.New(t)
		st, dir := newStore(t)
		m := newManager(t, st)
		addr, onboard := fakeAddress(t, f)
		in, err := m.Inspect(context.Background(), addr, onboard)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.StartPairing(context.Background(), in.Address, in.Fingerprint, false); err != nil {
			t.Fatal(err)
		}
		f.SetPairingState(c.state)
		ps := waitPair(t, m)
		if ps.State != c.want || ps.Error != c.msg {
			t.Errorf("%s: %+v", c.state, ps)
		}
		if _, err := os.Stat(filepath.Join(dir, store.FileBubbles)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: bubbles.json written", c.state)
		}
		if s := m.Status(); s.State != StateOff {
			t.Errorf("%s: status %+v", c.state, s)
		}
	}

	f := bubblestest.New(t)
	st, _ := newStore(t)
	m := newManager(t, st)
	addr, onboard := fakeAddress(t, f)
	in, _ := m.Inspect(context.Background(), addr, onboard)
	if _, err := m.StartPairing(context.Background(), in.Address, in.Fingerprint, false); err != nil {
		t.Fatal(err)
	}
	m.CancelPairing()
	if ps := m.PairStatus(); ps.State != PairNone {
		t.Fatalf("after cancel %+v", ps)
	}
	polls := f.Polls()
	time.Sleep(50 * time.Millisecond)
	if f.Polls() > polls+1 {
		t.Fatalf("still polling after cancel: %d -> %d", polls, f.Polls())
	}
}

// Pairing targets are local only (4.10): private, link-local, shared and
// loopback IPv4 addresses and .local names; the default ports are 28225
// and 28226, and a typed HTTPS port puts onboarding on the next one.
func TestParseTarget(t *testing.T) {
	for _, c := range []struct {
		in             string
		onboard        int
		host           string
		port, onboardP int
	}{
		{"192.168.1.20", 0, "192.168.1.20", 28225, 28226},
		{" 10.1.2.3:28225 ", 0, "10.1.2.3", 28225, 28226},
		{"172.16.0.9:30000", 0, "172.16.0.9", 30000, 30001},
		{"172.31.255.1:30000", 31000, "172.31.255.1", 30000, 31000},
		{"https://192.168.1.20:28225/", 0, "192.168.1.20", 28225, 28226},
		{"bubbles://192.168.1.20", 0, "192.168.1.20", 28225, 28226},
		{"169.254.10.10", 0, "169.254.10.10", 28225, 28226},
		{"100.64.0.1", 0, "100.64.0.1", 28225, 28226},
		{"127.0.0.1:40000", 0, "127.0.0.1", 40000, 40001},
		{"Office.Local", 0, "office.local", 28225, 28226},
		{"chat-1.office.local:28225", 0, "chat-1.office.local", 28225, 28226},
	} {
		got, err := ParseTarget(c.in, c.onboard)
		if err != nil || got.Host != c.host || got.Port != c.port || got.OnboardPort != c.onboardP {
			t.Errorf("%q: %+v %v", c.in, got, err)
		}
	}
	for _, bad := range []string{"", "8.8.8.8", "203.0.113.5:28225", "chat.example.com", "100.128.0.1", "[fe80::1]:28225",
		"::1", "http://192.168.1.20", "192.168.1.20:0", "192.168.1.20:99999", "-x.local", ".local", "a_b.local", "ftp://10.0.0.1"} {
		if got, err := ParseTarget(bad, 0); err == nil {
			t.Errorf("%q accepted: %+v", bad, got)
		}
	}
	tg, _ := ParseTarget("192.168.1.20", 0)
	if tg.BaseURL() != "https://192.168.1.20:28225" || tg.OnboardingURL() != "http://192.168.1.20:28226" ||
		tg.AdminPairingURL() != "https://192.168.1.20:28225/admin/pairing" {
		t.Fatalf("urls %s %s %s", tg.BaseURL(), tg.OnboardingURL(), tg.AdminPairingURL())
	}
}

// Inspect refuses a server on the internet before it sends anything, and
// says plainly when nothing answers.
func TestInspectRefusals(t *testing.T) {
	st, _ := newStore(t)
	m := newManager(t, st)
	var r *RefusedError
	if _, err := m.Inspect(context.Background(), "203.0.113.5", 0); !errors.As(err, &r) || !strings.Contains(r.Msg, "local network") {
		t.Fatalf("public address: %v", err)
	}
	// A loopback port nothing listens on.
	if _, err := m.Inspect(context.Background(), "127.0.0.1:1", 2); !errors.As(err, &r) || !strings.Contains(r.Msg, "can't reach") {
		t.Fatalf("nothing there: %v", err)
	}
	none := NewManager(Options{})
	defer none.Close()
	if _, err := none.Inspect(context.Background(), "192.168.1.20", 0); !errors.Is(err, errNoStore) {
		t.Fatalf("no store: %v", err)
	}
}

// With the bot in several channels, the person picks one (step 6); until
// then the state is no_channel. Check again with one channel picks it.
func TestChooseChannel(t *testing.T) {
	f := bubblestest.New(t)
	f.AddChannel("C2", "front-desk")
	st, _ := newStore(t)
	m := newManager(t, st)
	ps := pairWith(t, m, f)
	if ps.Channel != nil || len(ps.Channels) != 2 || ps.Channels[0].Name != "front-desk" {
		t.Fatalf("approved %+v", ps)
	}
	if s := m.Status(); s.State != StateNoChannel {
		t.Fatalf("status %+v", s)
	}
	if r := m.SendTest(context.Background()); r.OK || r.Hint != "no_channel" {
		t.Fatalf("test without a channel %+v", r)
	}
	ctx := context.Background()
	var r *RefusedError
	if _, err := m.SetChannel(ctx, "C9"); !errors.As(err, &r) {
		t.Fatalf("a channel the bot is not in: %v", err)
	}
	ch, err := m.SetChannel(ctx, "C2")
	if err != nil || ch.Channel == nil || ch.Channel.Name != "front-desk" || ch.State != StateConnected {
		t.Fatalf("set %+v %v", ch, err)
	}
	if c, _ := LoadConnection(st); c.Channel != (Channel{ID: "C2", Name: "front-desk"}) {
		t.Fatalf("saved channel %+v", c.Channel)
	}

	// An archived channel is not offered; Check again with one left picks it.
	f.ArchiveChannel("C2")
	ch, err = m.SetChannel(ctx, "")
	if err != nil || len(ch.Channels) != 1 || ch.Channel == nil || ch.Channel.ID != "C1" {
		t.Fatalf("check again %+v %v", ch, err)
	}
}

// A failed test alert says why in words, and puts the connection in the
// state the status tool shows (4.10 step 7, 4.9 Errors): a channel that
// refuses the post (404, or archived) is no_channel with the channel kept,
// until a post gets through or a channel is chosen; no answer is
// unreachable until the server answers again; a 401 is needs_reconnect.
func TestSendTestFailures(t *testing.T) {
	f := bubblestest.New(t)
	st, _ := newStore(t)
	m := newManager(t, st)
	pairWith(t, m, f)
	ctx := context.Background()

	f.RemoveChannel("C1")
	r := m.SendTest(ctx)
	if r.OK || r.Hint != "not_allowed" || r.Error != "netdiag isn't allowed to post in #netdiag-alerts. Ask your chat admin to add netdiag to that channel." {
		t.Fatalf("not in the channel: %+v", r)
	}
	if s := m.Status(); s.State != StateNoChannel || s.Reason != ReasonNotAllowed || s.Channel == nil || s.Channel.ID != "C1" || s.LastError != r.Error {
		t.Fatalf("after a 404 the channel takes no posts: %+v", s)
	}
	f.AddChannel("C1", "netdiag-alerts")
	if r := m.SendTest(ctx); !r.OK {
		t.Fatalf("back in the channel: %+v", r)
	}
	if s := m.Status(); s.State != StateConnected || s.Reason != "" || s.LastError != "" {
		t.Fatalf("a post that gets through clears no_channel: %+v", s)
	}

	f.ArchiveChannel("C1")
	if r := m.SendTest(ctx); r.OK || r.Hint != "no_channel" || !strings.Contains(r.Error, "archived") {
		t.Fatalf("archived: %+v", r)
	}
	if s := m.Status(); s.State != StateNoChannel || s.Reason != ReasonArchived || s.Channel == nil || s.Channel.ID != "C1" {
		t.Fatalf("status when archived %+v", s)
	}
	f.AddChannel("C2", "front-desk")
	if ch, err := m.SetChannel(ctx, "C2"); err != nil || ch.State != StateConnected {
		t.Fatalf("choosing a channel clears no_channel: %+v %v", ch, err)
	}

	f.FailNext(429, protocol.CodeRateLimited, "slow down", time.Second)
	if r := m.SendTest(ctx); r.OK || r.Hint != "busy" {
		t.Fatalf("busy: %+v", r)
	}
	if s := m.Status(); s.State != StateConnected {
		t.Fatalf("busy is not a connection problem: %+v", s)
	}

	f.FailNext(503, protocol.CodeUnavailable, "resetting", 0)
	now := time.Now()
	if r := m.SendTest(ctx); r.OK || r.Hint != "unreachable" || r.Error != "netdiag can't reach the chat server." {
		t.Fatalf("5xx: %+v", r)
	}
	s := m.Status()
	since, _ := time.Parse(time.RFC3339, s.Since)
	if s.State != StateUnreachable || since.Before(now.Add(-2*time.Second)) {
		t.Fatalf("status when unreachable %+v", s)
	}
	// A request the page gave up on tells nothing about the server.
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if r := m.SendTest(gone); r.OK {
		t.Fatalf("cancelled test %+v", r)
	}
	if s := m.Status(); s.State != StateUnreachable {
		t.Fatalf("a cancelled request cleared unreachable: %+v", s)
	}
	if r := m.SendTest(ctx); !r.OK {
		t.Fatalf("back: %+v", r)
	}
	if s := m.Status(); s.State != StateConnected || s.Since != "" {
		t.Fatalf("an answer clears unreachable: %+v", s)
	}

	f.Revoke()
	r = m.SendTest(ctx)
	if r.OK || r.Hint != "needs_reconnect" || r.Error != "The chat server no longer accepts netdiag. Click Reconnect." {
		t.Fatalf("revoked: %+v", r)
	}
	if s := m.Status(); s.State != StateNeedsReconnect || s.Reason != ReasonRefused || s.LastError == "" {
		t.Fatalf("status after 401 %+v", s)
	}
	// Only a call the token passes clears it: not another answer (a GET
	// /status without the token would get 200, one with it 403), not a
	// server that stopped answering, not a cancelled call.
	_, gen, _, _ := m.current()
	for _, err := range []error{&bubbles.APIError{Status: 403, Code: protocol.CodeForbidden}, &bubbles.APIError{Status: 404, Code: protocol.CodeNotFound},
		&bubbles.APIError{Status: 429, Code: protocol.CodeRateLimited}, fmt.Errorf("%w: down", bubbles.ErrUnavailable), context.Canceled, context.DeadlineExceeded} {
		m.note(gen, err)
		if s := m.Status(); s.State != StateNeedsReconnect {
			t.Fatalf("after a 401, %v gave %+v", err, s)
		}
	}
	m.note(gen, nil)
	if s := m.Status(); s.State != StateConnected {
		t.Fatalf("a call the token passes: %+v", s)
	}

	f.API.Close()
	r = m.SendTest(ctx)
	if r.OK || r.Hint != "unreachable" || r.Error != "netdiag can't reach the chat server." {
		t.Fatalf("down: %+v", r)
	}
	if s := m.Status(); s.State != StateUnreachable {
		t.Fatalf("status when down %+v", s)
	}
}

// A test card left open (an earlier test whose resolve did not get
// through) and then Change channel: the next test resolves the old card by
// key and posts once more, instead of failing for good with bubbles' 409
// "already open in another channel".
func TestSendTestAfterAnOpenTestCard(t *testing.T) {
	f := bubblestest.New(t)
	f.AddChannel("C2", "front-desk")
	st, _ := newStore(t)
	m := newManager(t, st)
	pairWith(t, m, f)
	ctx := context.Background()
	if _, err := m.SetChannel(ctx, "C1"); err != nil {
		t.Fatal(err)
	}
	_, _, c, _ := m.current()
	if _, err := c.PostAlertResult(ctx, protocol.AlertInput{Channel: "C1", Severity: protocol.SeverityInfo, Title: "Test alert from netdiag", DedupeKey: TestKey}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetChannel(ctx, "C2"); err != nil {
		t.Fatal(err)
	}
	r := m.SendTest(ctx)
	if !r.OK || r.Channel != "front-desk" || r.Transition != protocol.AlertTransitionCreated {
		t.Fatalf("test after the channel change %+v", r)
	}
	card, _ := f.Alert(TestKey)
	if card.ChannelID != "C2" || card.Alert.State != protocol.AlertResolved || len(f.Resolved()) != 2 {
		t.Fatalf("card %+v, resolved %+v", card, f.Resolved())
	}
	if s := m.Status(); s.State != StateConnected {
		t.Fatalf("status %+v", s)
	}
}

// A server whose CA is not the pinned one is "identity changed", and stays
// so: it is never trusted again by itself (4.9), not when the server stops
// answering and not when the pinned server answers again. No test alert
// goes out; only a new connection (Reconnect) clears it.
func TestIdentityChanged(t *testing.T) {
	a, b := bubblestest.New(t), bubblestest.New(t)
	st, _ := newStore(t)
	m := newManager(t, st)
	pairWith(t, m, a)
	c, _ := LoadConnection(st)
	// The same address now presents another server's certificate.
	c.Server.BaseURL = b.API.URL
	if err := SaveConnection(st, c); err != nil {
		t.Fatal(err)
	}
	m2 := newManager(t, st)
	ctx := context.Background()
	if _, err := m2.SetChannel(ctx, ""); err == nil {
		t.Fatal("listed the channels of a server with another CA")
	}
	if s := m2.Status(); s.State != StateIdentityChanged {
		t.Fatalf("status %+v", s)
	}
	r := m2.SendTest(ctx)
	if r.OK || r.Hint != "identity_changed" || !strings.HasPrefix(r.Error, "Warning: the chat server's identity changed.") {
		t.Fatalf("test %+v", r)
	}
	b.API.Close()
	_, gen, _, _ := m2.current()
	m2.note(gen, fmt.Errorf("%w: down", bubbles.ErrUnavailable))
	if s := m2.Status(); s.State != StateIdentityChanged {
		t.Fatalf("unreachable cleared it: %+v", s)
	}
	// The pinned server answers again at that address (an impostor that
	// comes and goes): still identity changed.
	m2.mu.Lock()
	moved := *m2.conn
	moved.Server.BaseURL = a.API.URL
	m2.conn = &moved
	m2.mu.Unlock()
	if ch, err := m2.SetChannel(ctx, ""); err != nil || ch.State != StateIdentityChanged {
		t.Fatalf("a pinned call that works: %+v %v", ch, err)
	}
	for _, err := range []error{nil, &bubbles.APIError{Status: 404, Code: protocol.CodeNotFound}, context.Canceled} {
		m2.note(gen, err)
		if s := m2.Status(); s.State != StateIdentityChanged {
			t.Fatalf("%v cleared it: %+v", err, s)
		}
	}
	if r := m2.SendTest(ctx); r.OK || r.Hint != "identity_changed" {
		t.Fatalf("test %+v", r)
	}
	if len(b.Posted()) != 0 || len(a.Posted()) != 0 {
		t.Fatal("posted while the identity is in doubt")
	}
	// Reconnect replaces the connection, and with it the state.
	if _, err := m2.StartPairing(ctx, "", "", true); err != nil {
		t.Fatal(err)
	}
	a.SetPairingState(protocol.PairingApproved)
	waitApproved(t, m2)
	if s := m2.Status(); s.State != StateConnected {
		t.Fatalf("after Reconnect %+v", s)
	}
}

// A connection string carries the pin: no fingerprint step, and the
// channel is picked when there is one. A string for a server on the
// internet is refused before anything is sent.
func TestConnectString(t *testing.T) {
	f := bubblestest.New(t)
	st, _ := newStore(t)
	pairWith(t, newManager(t, st), f) // issues a token
	u, _ := url.Parse(f.API.URL)
	cs := "bubbles://" + u.Host + "/?token=" + url.QueryEscape(f.Token()) + "&fp=" + f.CAFingerprint

	st2, _ := newStore(t)
	m := newManager(t, st2)
	ch, err := m.ConnectString(context.Background(), cs)
	if err != nil {
		t.Fatal(err)
	}
	if ch.State != StateConnected || ch.Channel == nil || ch.Channel.ID != "C1" || ch.Error != "" {
		t.Fatalf("connected %+v", ch)
	}
	// The server's name comes from GET /status asked without the token,
	// which bubbles v0.2.0 refuses to an app token (403).
	c, err := LoadConnection(st2)
	if err != nil || c.Token() != f.Token() || c.Server.Name != "Office" || !slices.Equal(c.Scopes, Scopes) {
		t.Fatalf("saved %v %v", c, err)
	}
	if s := m.Status(); s.Server == nil || s.Server.Name != "Office" {
		t.Fatalf("status %+v", s)
	}
	if !m.SendTest(context.Background()).OK {
		t.Fatal("test alert")
	}
	var r *RefusedError
	for _, bad := range []string{"not a string", "bubbles://8.8.8.8/?token=bub_x&fp=" + f.CAFingerprint,
		"bubbles://" + u.Host + "/?token=bub_wrong&fp=" + f.CAFingerprint} {
		if _, err := newManager(t, st2).ConnectString(context.Background(), bad); !errors.As(err, &r) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// The pairing request reads approved only once netdiag knows the
// channels, so the wizard never takes an approval with the channel still
// unknown for "netdiag isn't in any channel yet" (the token is saved, and
// the connection shows, the moment it arrives). Channels that cannot be
// listed after the approval come with the reason.
func TestPairApprovedOnlyWithItsChannels(t *testing.T) {
	f := bubblestest.New(t)
	st, _ := newStore(t)
	m := newManager(t, st)
	ctx := context.Background()
	addr, onboard := fakeAddress(t, f)
	in, err := m.Inspect(ctx, addr, onboard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartPairing(ctx, in.Address, in.Fingerprint, false); err != nil {
		t.Fatal(err)
	}
	release := f.Hold("GET /api/v1/channels")
	f.SetPairingState(protocol.PairingApproved)
	deadline := time.Now().Add(5 * time.Second)
	for f.Held() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the channels were never asked for: %+v", m.PairStatus())
		}
		time.Sleep(time.Millisecond)
	}
	if ps := m.PairStatus(); ps.State != PairPending || len(ps.Channels) != 0 {
		t.Fatalf("while the channels are on their way: %+v", ps)
	}
	if s := m.Status(); s.State != StateNoChannel {
		t.Fatalf("the token is kept at once: %+v", s)
	}
	if _, err := LoadConnection(st); err != nil {
		t.Fatalf("saved at once: %v", err)
	}
	release()
	for {
		ps := m.PairStatus()
		if ps.State == PairApproved {
			if ps.Channel == nil || ps.Channel.ID != "C1" || len(ps.Channels) != 1 || ps.Error != "" {
				t.Fatalf("approved %+v", ps)
			}
			break
		}
		if ps.State != PairPending || time.Now().After(deadline) {
			t.Fatalf("pairing %+v", ps)
		}
		time.Sleep(time.Millisecond)
	}

	// The server stops answering right after the approval: approved, with
	// why the channels are unknown.
	f2 := bubblestest.New(t)
	st2, _ := newStore(t)
	m2 := newManager(t, st2)
	addr, onboard = fakeAddress(t, f2)
	if in, err = m2.Inspect(ctx, addr, onboard); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.StartPairing(ctx, in.Address, in.Fingerprint, false); err != nil {
		t.Fatal(err)
	}
	f2.FailNext(503, protocol.CodeUnavailable, "resetting", 0) // GET /apps/self
	f2.FailNext(503, protocol.CodeUnavailable, "resetting", 0) // GET /channels
	f2.SetPairingState(protocol.PairingApproved)
	ps := waitPair(t, m2)
	if ps.State != PairApproved || len(ps.Channels) != 0 || ps.Error != "netdiag can't reach the chat server." {
		t.Fatalf("approved without channels %+v", ps)
	}
}

// A pairing request still on its way to the chat server when the person
// cancels (the wizard's Cancel or Esc) or disconnects is dropped when it
// arrives: netdiag never waits on it, so a later approval by the admin
// never connects netdiag.
func TestPairingCancelledOnItsWay(t *testing.T) {
	for _, stop := range []string{"cancel", "disconnect"} {
		f := bubblestest.New(t)
		st, dir := newStore(t)
		m := newManager(t, st)
		ctx := context.Background()
		addr, onboard := fakeAddress(t, f)
		in, err := m.Inspect(ctx, addr, onboard)
		if err != nil {
			t.Fatal(err)
		}
		release := f.Hold("POST /api/v1/pairing/requests")
		done := make(chan error, 1)
		go func() {
			_, err := m.StartPairing(ctx, in.Address, in.Fingerprint, false)
			done <- err
		}()
		deadline := time.Now().Add(5 * time.Second)
		for f.Held() == 0 {
			if time.Now().After(deadline) {
				t.Fatal("the pairing request never reached the server")
			}
			time.Sleep(time.Millisecond)
		}
		if stop == "cancel" {
			m.CancelPairing()
		} else if err := m.Disconnect(); err != nil {
			t.Fatal(err)
		}
		release()
		var r *RefusedError
		if err := <-done; !errors.As(err, &r) {
			t.Fatalf("%s: the request on its way gave %v", stop, err)
		}
		if ps := m.PairStatus(); ps.State != PairNone {
			t.Fatalf("%s: %+v", stop, ps)
		}
		f.SetPairingState(protocol.PairingApproved)
		time.Sleep(50 * time.Millisecond)
		if f.Polls() != 0 || f.Token() != "" {
			t.Fatalf("%s: still waited on it (%d polls)", stop, f.Polls())
		}
		if _, err := os.Stat(filepath.Join(dir, store.FileBubbles)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s: bubbles.json written", stop)
		}
		if s := m.Status(); s.State != StateOff {
			t.Fatalf("%s: status %+v", stop, s)
		}
	}
}

// flakyFiles is a data folder whose writes fail while fail is set: a full
// disk, or a virus scanner holding the file while netdiag replaces it.
type flakyFiles struct {
	*store.Store
	fail atomic.Bool
}

func (f *flakyFiles) WriteFile(name string, data []byte) error {
	if f.fail.Load() {
		return errors.New("the disk is full")
	}
	return f.Store.WriteFile(name, data)
}

// A token that arrives when bubbles.json cannot be written is kept in
// memory (bubbles hands it out once) and the wizard and the status say so,
// and every status read tries the save again. A connection string that
// cannot be saved does not connect, and a channel that cannot be saved is
// refused in words.
func TestSaveFailures(t *testing.T) {
	f := bubblestest.New(t)
	s, _ := newStore(t)
	st := &flakyFiles{Store: s}
	var diag diagLog
	m := newManager(t, st, diag.into)
	ctx := context.Background()
	addr, onboard := fakeAddress(t, f)
	in, err := m.Inspect(ctx, addr, onboard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartPairing(ctx, in.Address, in.Fingerprint, false); err != nil {
		t.Fatal(err)
	}
	st.fail.Store(true)
	f.SetPairingState(protocol.PairingApproved)
	ps := waitApproved(t, m)
	// Plain words and what to check; the OS error and the path only in the
	// diagnostics log.
	want := "netdiag couldn't save the team chat connection in its data folder. It works until netdiag closes, and netdiag keeps trying to save it. Check that the data folder isn't read-only or full."
	if ps.Error != want {
		t.Fatalf("approved but not saved: %+v", ps)
	}
	if s := m.Status(); s.State != StateConnected || s.Unsaved != want {
		t.Fatalf("status %+v", s)
	}
	if !strings.Contains(diag.String(), "the disk is full") {
		t.Fatalf("the reason is not in the diagnostics log: %s", diag.String())
	}
	if _, err := LoadConnection(st); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("load %v", err)
	}
	if r := m.SendTest(ctx); !r.OK {
		t.Fatalf("the connection works from memory: %+v", r)
	}
	f.AddChannel("C2", "front-desk")
	var r *RefusedError
	if _, err := m.SetChannel(ctx, "C2"); !errors.As(err, &r) ||
		r.Msg != "netdiag couldn't save the channel in its data folder. It is used until netdiag closes. Check that the data folder isn't read-only or full, then try again." {
		t.Fatalf("channel not saved: %v", err)
	}
	st.fail.Store(false)
	if s := m.Status(); s.Unsaved != "" || s.Channel == nil || s.Channel.ID != "C2" {
		t.Fatalf("status after the disk has room %+v", s)
	}
	if c, err := LoadConnection(st); err != nil || c.Token() != f.Token() || c.Channel.ID != "C2" {
		t.Fatalf("saved on the next status read: %v %v", c, err)
	}
	if ps := m.PairStatus(); ps.Error != "" {
		t.Fatalf("pair status %+v", ps)
	}

	// A connection string is saved before it replaces anything.
	u, _ := url.Parse(f.API.URL)
	cs := "bubbles://" + u.Host + "/?token=" + url.QueryEscape(f.Token()) + "&fp=" + f.CAFingerprint
	s3, _ := newStore(t)
	st3 := &flakyFiles{Store: s3}
	st3.fail.Store(true)
	m3 := newManager(t, st3)
	if _, err := m3.ConnectString(ctx, cs); !errors.As(err, &r) || !strings.HasPrefix(r.Msg, "netdiag couldn't save the team chat connection in its data folder, so it did not connect.") ||
		strings.Contains(r.Msg, "disk is full") {
		t.Fatalf("connection string not saved: %v", err)
	}
	if s := m3.Status(); s.State != StateOff {
		t.Fatalf("connected without saving: %+v", s)
	}
}

// Disconnect forgets bubbles.json, its set-aside copies and the outbox.
func TestDisconnect(t *testing.T) {
	f := bubblestest.New(t)
	st, dir := newStore(t)
	m := newManager(t, st)
	pairWith(t, m, f)
	if err := st.WriteFile(store.FileChatOutbox, []byte(`{"schemaVersion":1,"items":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, store.FileBubbles+".bad-20261017T104000Z"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Disconnect(); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	for _, p := range left {
		if n := filepath.Base(p); strings.HasPrefix(n, "bubbles") || strings.HasPrefix(n, "chat-outbox") {
			t.Errorf("%s is still there", n)
		}
	}
	if s := m.Status(); s.State != StateOff || s.Server != nil {
		t.Fatalf("status %+v", s)
	}
	if r := m.SendTest(context.Background()); r.OK {
		t.Fatal("test after disconnect")
	}
	if err := m.Disconnect(); err != nil {
		t.Fatalf("second disconnect: %v", err)
	}
}

// A bubbles.json whose token cannot be opened here (copied from another
// Windows user) is "needs reconnecting" with the server shown, and
// Reconnect pairs again with the saved server and pin, skipping the find
// and fingerprint steps; the channel is kept when the new bot is in it.
func TestNeedsReconnectAndReconnect(t *testing.T) {
	f := bubblestest.New(t)
	st, dir := newStore(t)
	o, _ := url.Parse(f.Onboard.URL)
	srv := Server{Name: "Office", BaseURL: f.API.URL, OnboardingURL: "http://127.0.0.1:" + o.Port(), CAFingerprint: f.CAFingerprint}
	c := NewConnection(srv, "netdiag", Scopes, "bub_old", time.Now())
	c.Channel = Channel{ID: "C1", Name: "netdiag-alerts"}
	if err := SaveConnection(st, c); err != nil {
		t.Fatal(err)
	}
	fl := readFile(t, dir)
	fl.Token.Protected, fl.Token.Data = "other", "AAAA"
	writeFile(t, dir, fl)

	m := newManager(t, st)
	s := m.Status()
	if s.State != StateNeedsReconnect || s.Reason != ReasonUnreadable || s.Server == nil || s.Server.Name != "Office" || s.Channel == nil || s.LastError == "" {
		t.Fatalf("status %+v", s)
	}
	f.AddChannel("C2", "other")
	if _, err := m.StartPairing(context.Background(), "", "", true); err != nil {
		t.Fatal(err)
	}
	f.SetPairingState(protocol.PairingApproved)
	ps := waitApproved(t, m)
	if ps.Channel == nil || ps.Channel.ID != "C1" {
		t.Fatalf("reconnected %+v", ps)
	}
	if s := m.Status(); s.State != StateConnected {
		t.Fatalf("status %+v", s)
	}
	if c, err := LoadConnection(st); err != nil || c.Token() != f.Token() {
		t.Fatalf("saved %v %v", c, err)
	}
}

// Discovery lists the _bubbles._tcp servers mDNS found (name, address, the
// SRV port, the TXT onboard port and fp prefix) and the server the bubbles
// tray app uses; Inspect then tells whether the fingerprint starts with
// the announced prefix, without the page ever showing that as a check.
func TestDiscover(t *testing.T) {
	f := bubblestest.New(t)
	addr, onboard := fakeAddress(t, f)
	host, portText, _ := strings.Cut(addr, ":")
	port, _ := strconv.Atoi(portText)
	fp16 := strings.TrimPrefix(f.CAFingerprint, "sha256:")[:16]
	tray := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(tray, []byte(fmt.Sprintf(`{"server_url":%q,"onboarding_url":%q,"ca_fingerprint":%q,"device_id":"tray-device-7"}`,
		f.API.URL, f.Onboard.URL, f.CAFingerprint)), 0o600); err != nil {
		t.Fatal(err)
	}
	var asked []string
	browse := func(_ context.Context, iface string, services []string, window time.Duration) ([]mdns.Record, error) {
		asked = append(asked, iface+" "+strings.Join(services, ",")+" "+window.String())
		return []mdns.Record{
			{IP: host, Info: model.MDNSInfo{Details: []model.MDNSService{
				{Type: "_bubbles._tcp", Instance: "Office", Port: port, TXT: map[string]string{"fp": fp16, "onboard": strconv.Itoa(onboard), "v": "1"}},
				{Type: "_http._tcp", Instance: "web", Port: 80},
			}}},
			{IP: "8.8.8.8", Info: model.MDNSInfo{Details: []model.MDNSService{{Type: "_bubbles._tcp", Instance: "Far", Port: 28225}}}},
		}, nil
	}
	st, _ := newStore(t)
	m := newManager(t, st, func(o *Options) {
		o.Browse = browse
		o.TrayConfig = func() (string, error) { return tray, nil }
	})
	d, err := m.Discover(context.Background(), "eth0")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(asked, []string{"eth0 _bubbles._tcp 3s"}) {
		t.Fatalf("browsed %v", asked)
	}
	want := Found{Name: "Office", Address: host, Port: port, OnboardPort: onboard, FPPrefix: fp16}
	if len(d.Servers) != 1 || d.Servers[0] != want {
		t.Fatalf("servers %+v", d.Servers)
	}
	if d.TrayServer == nil || d.TrayServer.Name != "Office" || d.TrayServer.Port != port || d.TrayServer.OnboardPort != onboard {
		t.Fatalf("tray %+v", d.TrayServer)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "tray-device-7") {
		t.Fatalf("discovery shows the tray's device: %s", b)
	}
	in, err := m.Inspect(context.Background(), addr, onboard)
	if err != nil || in.FPPrefixMatches == nil || !*in.FPPrefixMatches {
		t.Fatalf("inspect %+v %v", in, err)
	}

	// No tray config, nothing found: an empty list, not null.
	m2 := newManager(t, st)
	d, err = m2.Discover(context.Background(), "")
	if err != nil || d.Servers == nil || len(d.Servers) != 0 || d.TrayServer != nil {
		t.Fatalf("empty %+v %v", d, err)
	}
	if b, _ := json.Marshal(d); string(b) != `{"servers":[],"trayServer":null}` {
		t.Fatalf("json %s", b)
	}
}

// A server whose certificate is not the one its own onboarding port
// vouches for is refused in words for the person at the desk: no
// "certificate" and no "fingerprint".
func TestInspectIdentityMismatch(t *testing.T) {
	a, b := bubblestest.New(t), bubblestest.New(t)
	st, _ := newStore(t)
	m := newManager(t, st)
	_, onboardA := fakeAddress(t, a)
	addrB, _ := fakeAddress(t, b)
	_, err := m.Inspect(context.Background(), addrB, onboardA)
	var r *RefusedError
	want := "Something is wrong with the chat server at 127.0.0.1: it doesn't prove it is the server it says it is. Stop here and tell your chat admin."
	if !errors.As(err, &r) || r.Msg != want {
		t.Fatalf("inspect: %v, want %q", err, want)
	}
}

// What the chat server or the network said never reaches the page as it
// is: a refusal and a transport or TLS error are plain words with a next
// step, and the raw text goes to the diagnostics log only.
func TestExplainPlainWords(t *testing.T) {
	var diag diagLog
	s, _ := newStore(t)
	m := newManager(t, s, diag.into)
	for _, tc := range []struct {
		err             error
		hint, want, raw string
	}{
		{&bubbles.APIError{Status: 400, Code: "invalid", Message: "dedupe_key: must be at most 200 characters"}, "refused",
			"The chat server turned down netdiag's request. Ask your chat admin to check bubbles' log.", "400 dedupe_key: must be at most 200 characters"},
		{errors.New(`Post "https://192.168.2.20:28225/v1/alerts": tls: failed to verify certificate`), "refused",
			"netdiag couldn't talk to the chat server. Try again in a moment, or ask your chat admin whether it is running.", "tls: failed to verify certificate"},
	} {
		hint, msg := m.explain(tc.err)
		if hint != tc.hint || msg != tc.want {
			t.Errorf("explain(%v) = %q, %q", tc.err, hint, msg)
		}
		if !strings.Contains(diag.String(), tc.raw) {
			t.Errorf("%q is not in the diagnostics log: %s", tc.raw, diag.String())
		}
	}
	r := m.testFailure(&bubbles.APIError{Status: 400, Message: "title: title is required"}, "netdiag-alerts")
	if r.Hint != "refused" || r.Error != "The chat server turned down the test alert. Ask your chat admin to check bubbles' log." {
		t.Errorf("test failure %+v", r)
	}
}
