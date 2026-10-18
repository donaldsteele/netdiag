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
	"testing"
	"time"

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
// state the status tool shows (4.10 step 7).
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
	if s := m.Status(); s.State != StateConnected {
		t.Fatalf("a 404 is not a connection problem: %+v", s)
	}
	f.AddChannel("C1", "netdiag-alerts")
	f.ArchiveChannel("C1")
	if r := m.SendTest(ctx); r.OK || r.Hint != "no_channel" || !strings.Contains(r.Error, "archived") {
		t.Fatalf("archived: %+v", r)
	}

	f.FailNext(429, protocol.CodeRateLimited, "slow down", time.Second)
	if r := m.SendTest(ctx); r.OK || r.Hint != "busy" {
		t.Fatalf("busy: %+v", r)
	}

	f.Revoke()
	r = m.SendTest(ctx)
	if r.OK || r.Hint != "needs_reconnect" || r.Error != "The chat server no longer accepts netdiag. Click Reconnect." {
		t.Fatalf("revoked: %+v", r)
	}
	if s := m.Status(); s.State != StateNeedsReconnect || s.LastError == "" {
		t.Fatalf("status after 401 %+v", s)
	}

	f.API.Close()
	now := time.Now()
	r = m.SendTest(ctx)
	if r.OK || r.Hint != "unreachable" || r.Error != "netdiag can't reach the chat server." {
		t.Fatalf("down: %+v", r)
	}
	s := m.Status()
	since, _ := time.Parse(time.RFC3339, s.Since)
	if s.State != StateUnreachable || since.Before(now.Add(-2*time.Second)) {
		t.Fatalf("status when down %+v", s)
	}
}

// A server whose CA is not the pinned one is "identity changed", and stays
// so: it is never trusted again by itself.
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
	r := m2.SendTest(context.Background())
	if r.OK || r.Hint != "identity_changed" || !strings.HasPrefix(r.Error, "Warning: the chat server's identity changed.") {
		t.Fatalf("test %+v", r)
	}
	b.API.Close()
	m2.SendTest(context.Background())
	if s := m2.Status(); s.State != StateIdentityChanged {
		t.Fatalf("status %+v", s)
	}
	if len(b.Posted()) != 0 {
		t.Fatal("posted to a server with another CA")
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
	if ch.State != StateConnected || ch.Channel == nil || ch.Channel.ID != "C1" {
		t.Fatalf("connected %+v", ch)
	}
	c, err := LoadConnection(st2)
	if err != nil || c.Token() != f.Token() || c.Server.Name != "Office" || !slices.Equal(c.Scopes, Scopes) {
		t.Fatalf("saved %v %v", c, err)
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
	if s.State != StateNeedsReconnect || s.Server == nil || s.Server.Name != "Office" || s.Channel == nil || s.LastError == "" {
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
	if err := os.WriteFile(tray, []byte(fmt.Sprintf(`{"server_url":%q,"onboarding_url":%q,"ca_fingerprint":%q,"device_id":"d1"}`,
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
	if strings.Contains(string(b), "d1") {
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
