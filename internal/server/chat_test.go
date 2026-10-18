package server

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"netdiag/internal/bubbles/bubblestest"
	"netdiag/internal/bubbles/protocol"
	"netdiag/internal/chat"
	"netdiag/internal/mdns"
	"netdiag/internal/model"
	"netdiag/internal/scan"
	"netdiag/internal/store"
)

// startChat is a server with a data folder and a team chat manager that
// finds the fake bubbles server f by mDNS and never reads the real
// bubbles tray config.
func startChat(t *testing.T, f *bubblestest.Server) (string, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := url.Parse(f.API.URL)
	o, _ := url.Parse(f.Onboard.URL)
	port, _ := strconv.Atoi(a.Port())
	browse := func(context.Context, string, []string, time.Duration) ([]mdns.Record, error) {
		return []mdns.Record{{IP: a.Hostname(), Info: model.MDNSInfo{Details: []model.MDNSService{
			{Type: "_bubbles._tcp", Instance: "Office", Port: port, TXT: map[string]string{"onboard": o.Port()}}}}}}, nil
	}
	m := chat.NewManager(chat.Options{Files: st, Browse: browse, PairPoll: 5 * time.Millisecond,
		TrayConfig: func() (string, error) { return filepath.Join(t.TempDir(), "none.json"), nil },
		Hostname:   func() (string, error) { return "FRONTDESK-PC", nil }})
	reg := scan.NewRegistry(scan.New(scan.Deps{Pinger: fakePinger{}, ARP: fakeARP{}, Ports: fakePorts{}, Web: fakeWeb{},
		SNMP: fakeSNMP{}, DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{}}))
	s := New(Config{Store: st, Chat: m, SessionToken: testToken, Registry: reg})
	if err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	exited := make(chan struct{})
	go func() { _ = s.Serve(ctx); close(exited) }()
	t.Cleanup(func() { cancel(); <-exited })
	return s.URL(), st
}

func decode[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return v
}

// The wizard's routes end to end against the bubbles v0.2.0 fake: find,
// inspect, pair (only once the fingerprint was confirmed), the pairing
// state, the test alert and Disconnect. The two reads need the session
// token like the actions (v0.6-D14), and no answer shows the token.
func TestChatRoutes(t *testing.T) {
	f := bubblestest.New(t)
	u, st := startChat(t, f)

	for _, p := range []string{"/api/bubbles", "/api/bubbles/pair/status"} {
		r, b := raw(t, "GET", u+p, "", "")
		sessionRefused(t, "GET "+p, r, b)
	}
	r, b := do(t, "GET", u+"/api/bubbles", "", "")
	if r.StatusCode != 200 || decode[chat.Status](t, b).State != chat.StateOff || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d %s", r.StatusCode, b)
	}

	r, b = do(t, "POST", u+"/api/bubbles/discover", `{"interface":"eth0"}`, "")
	d := decode[chat.Discovery](t, b)
	if r.StatusCode != 200 || len(d.Servers) != 1 || d.Servers[0].Name != "Office" {
		t.Fatalf("discover %d %s", r.StatusCode, b)
	}
	srv := d.Servers[0]
	body, _ := json.Marshal(map[string]any{"address": srv.Address + ":" + strconv.Itoa(srv.Port), "onboardPort": srv.OnboardPort})
	r, b = do(t, "POST", u+"/api/bubbles/inspect", string(body), "")
	in := decode[chat.Inspection](t, b)
	if r.StatusCode != 200 || in.Fingerprint != f.CAFingerprint || len(in.Groups) != 16 {
		t.Fatalf("inspect %d %s", r.StatusCode, b)
	}

	pair := func(confirmed bool) (int, string) {
		body, _ := json.Marshal(map[string]any{"address": in.Address, "fingerprint": in.Fingerprint, "confirmed": confirmed})
		r, b := do(t, "POST", u+"/api/bubbles/pair", string(body), "")
		return r.StatusCode, b
	}
	if code, b := pair(false); code != 400 || !strings.Contains(b, "fingerprint") {
		t.Fatalf("pair without confirming: %d %s", code, b)
	}
	code, b := pair(true)
	ps := decode[chat.PairStart](t, b)
	if code != 200 || ps.Code != "123 456" || !strings.HasSuffix(ps.AdminURL, "/admin/pairing") {
		t.Fatalf("pair %d %s", code, b)
	}
	f.SetPairingState(protocol.PairingApproved)
	var status chat.PairStatus
	deadline := time.Now().Add(5 * time.Second)
	// Approved comes with the channel: the page never sees one without it.
	for time.Now().Before(deadline) {
		_, b = do(t, "GET", u+"/api/bubbles/pair/status", "", "")
		if status = decode[chat.PairStatus](t, b); status.State != chat.PairPending {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status.State != chat.PairApproved || status.Channel == nil || status.Channel.Name != "netdiag-alerts" || status.Error != "" {
		t.Fatalf("pair status %+v", status)
	}

	r, b = do(t, "POST", u+"/api/bubbles/test", `{}`, "")
	if tr := decode[chat.TestResult](t, b); r.StatusCode != 200 || !tr.OK {
		t.Fatalf("test %d %s", r.StatusCode, b)
	}
	r, b = do(t, "GET", u+"/api/bubbles", "", "")
	cs := decode[chat.Status](t, b)
	if cs.State != chat.StateConnected || cs.Channel == nil || cs.Server == nil || cs.Server.Name != "Office" || strings.Contains(b, f.Token()) {
		t.Fatalf("status %s", b)
	}
	// Try now answers the status.
	r, b = do(t, "POST", u+"/api/bubbles/retry", `{}`, "")
	if r.StatusCode != 200 || decode[chat.Status](t, b).State != chat.StateConnected || strings.Contains(b, f.Token()) {
		t.Fatalf("retry %d %s", r.StatusCode, b)
	}
	// Saving Settings with Send alerts on reaches the manager: the first
	// connect says so in the channel.
	r, b = do(t, "GET", u+"/api/settings", "", "")
	set := decode[map[string]any](t, b)
	set["chat"] = map[string]any{"enabled": true, "minSeverity": "warning"}
	delete(set, "mcpUrl")
	delete(set, "repaired")
	sb, _ := json.Marshal(set)
	if r, b := do(t, "PUT", u+"/api/settings", string(sb), ""); r.StatusCode != 200 {
		t.Fatalf("save %d %s", r.StatusCode, b)
	}
	deadline = time.Now().Add(5 * time.Second)
	for len(f.Messages()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m := f.Messages(); len(m) != 1 || !strings.Contains(m[0], "netdiag on FRONTDESK-PC is connected.") {
		t.Fatalf("messages %q", m)
	}

	r, b = do(t, "POST", u+"/api/bubbles/channel", `{"channelId":"C1"}`, "")
	if r.StatusCode != 200 || decode[chat.ChannelChoice](t, b).Channel.ID != "C1" {
		t.Fatalf("channel %d %s", r.StatusCode, b)
	}
	if r, b := do(t, "POST", u+"/api/bubbles/channel", `{"channelId":"nope"}`, ""); r.StatusCode != 400 {
		t.Fatalf("unknown channel %d %s", r.StatusCode, b)
	}

	if r, b := do(t, "DELETE", u+"/api/bubbles/pair", "", ""); r.StatusCode != 204 {
		t.Fatalf("cancel %d %s", r.StatusCode, b)
	}
	if r, b := do(t, "DELETE", u+"/api/bubbles", "", ""); r.StatusCode != 204 {
		t.Fatalf("disconnect %d %s", r.StatusCode, b)
	}
	if _, err := os.Stat(filepath.Join(st.Dir(), store.FileBubbles)); !os.IsNotExist(err) {
		t.Fatalf("bubbles.json after disconnect: %v", err)
	}
	if _, b := do(t, "GET", u+"/api/bubbles", "", ""); decode[chat.Status](t, b).State != chat.StateOff {
		t.Fatalf("after disconnect %s", b)
	}
}

// Words for the page come back as 400 with the message; a connection
// string that is not one is refused the same way.
func TestChatRoutesRefuse(t *testing.T) {
	f := bubblestest.New(t)
	u, _ := startChat(t, f)
	for _, c := range []struct{ path, body, want string }{
		{"/api/bubbles/inspect", `{"address":"203.0.113.5"}`, "local network"},
		{"/api/bubbles/connect", `{"connectionString":"hello"}`, "bubbles://"},
		{"/api/bubbles/pair", `{"address":"192.168.1.20","fingerprint":"x","confirmed":true}`, "fingerprint"},
		{"/api/bubbles/channel", `{}`, "isn't connected"},
		// The wizard shows the error as it is: plain words, not the decoder's.
		{"/api/bubbles/inspect", `{"address":`, `"netdiag couldn't read what this page sent. Reload the page and try again."`},
		{"/api/bubbles/channel", `{"channelId":`, `"netdiag couldn't read what this page sent. Reload the page and try again."`},
	} {
		r, b := do(t, "POST", u+c.path, c.body, "")
		if r.StatusCode != 400 || !strings.Contains(b, c.want) || strings.Contains(b, "JSON") || strings.Contains(b, "EOF") {
			t.Errorf("%s %s: %d %s", c.path, c.body, r.StatusCode, b)
		}
	}
	r, b := do(t, "POST", u+"/api/bubbles/test", "", "")
	if tr := decode[chat.TestResult](t, b); r.StatusCode != 200 || tr.OK || tr.Error == "" {
		t.Fatalf("test while not connected %d %s", r.StatusCode, b)
	}
}

// Without a data folder there is nowhere to keep the connection: the
// status says off and the actions answer 503.
func TestChatRoutesWithoutStore(t *testing.T) {
	_, u, _ := start(t, nil)
	if r, b := do(t, "GET", u+"/api/bubbles", "", ""); r.StatusCode != 200 || decode[chat.Status](t, b).State != chat.StateOff {
		t.Fatalf("status %d %s", r.StatusCode, b)
	}
	for _, p := range []string{"/api/bubbles/discover", "/api/bubbles/inspect", "/api/bubbles/test"} {
		if r, b := do(t, "POST", u+p, `{}`, ""); r.StatusCode != 503 || !strings.Contains(b, chat.NoStoreMsg) {
			t.Errorf("%s: %d %s", p, r.StatusCode, b)
		}
	}
}
