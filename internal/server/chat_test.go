package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	return startChatFiles(t, f, nil, nil)
}

// startChatFiles is startChat with the manager's data folder seen through
// files (nil: the store itself), to make its saves or removals fail, and
// the diagnostics log going to logf (nil: none).
func startChatFiles(t *testing.T, f *bubblestest.Server, files func(*store.Store) chat.DataFiles, logf func(string, ...any)) (string, *store.Store) {
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
	var df chat.DataFiles = st
	if files != nil {
		df = files(st)
	}
	m := chat.NewManager(chat.Options{Files: df, Browse: browse, PairPoll: 5 * time.Millisecond,
		TrayConfig: func() (string, error) { return filepath.Join(t.TempDir(), "none.json"), nil },
		Hostname:   func() (string, error) { return "FRONTDESK-PC", nil }})
	reg := scan.NewRegistry(scan.New(scan.Deps{Pinger: fakePinger{}, ARP: fakeARP{}, Ports: fakePorts{}, Web: fakeWeb{},
		SNMP: fakeSNMP{}, DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{}}))
	s := New(Config{Store: st, Chat: m, SessionToken: testToken, Registry: reg, Logf: logf})
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
	// The status says so too, for every page open (v0.6-D52).
	if _, b := do(t, "GET", u+"/api/bubbles", "", ""); !strings.Contains(b, `"sendAlerts":true`) {
		t.Fatalf("status after Send alerts was saved on: %s", b)
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

// noRemove is a data folder whose files cannot be removed (read-only):
// the error names the file, as the OS's does.
type noRemove struct{ *store.Store }

func (n noRemove) RemoveFile(name string) error {
	return &fs.PathError{Op: "remove", Path: filepath.Join(n.Dir(), name), Err: errors.New("Access is denied.")}
}

// logLines keeps what the server writes to the diagnostics log.
type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logLines) has(parts ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.ContainsFunc(l.lines, func(line string) bool {
		for _, p := range parts {
			if !strings.Contains(line, p) {
				return false
			}
		}
		return true
	})
}

// A Disconnect that cannot remove bubbles.json answers 500 with plain
// words and a next step, which Settings shows as they are; the OS error
// and the file's path go only to the diagnostics log (v0.6-D52).
func TestChatDisconnectFails(t *testing.T) {
	f := bubblestest.New(t)
	var log logLines
	u, _ := startChatFiles(t, f, func(st *store.Store) chat.DataFiles { return noRemove{st} }, log.logf)
	r, b := do(t, "DELETE", u+"/api/bubbles", "", "")
	if r.StatusCode != 500 || decode[map[string]string](t, b)["error"] != chatNoForget {
		t.Fatalf("disconnect %d %s", r.StatusCode, b)
	}
	for _, raw := range []string{store.FileBubbles, "Access is denied", `\`, "/"} {
		if strings.Contains(chatNoForget, raw) || strings.Contains(b, raw) {
			t.Errorf("the answer has %q: %s", raw, b)
		}
	}
	if !log.has("disconnect", store.FileBubbles, "Access is denied") {
		t.Fatalf("diagnostics %q", log.lines)
	}
}

// chatErr: words for the page (a RefusedError, also wrapped) are a 400
// with those words; any other manager error is a 500 with plain words, and
// what went wrong goes only to the diagnostics log.
func TestChatErrAnswers(t *testing.T) {
	var log logLines
	s := &Server{cfg: Config{Logf: log.logf}}
	w := httptest.NewRecorder()
	s.chatErr(w, fmt.Errorf("inspect: %w", &chat.RefusedError{Msg: "Use the address of a chat server on the local network."}))
	if w.Code != 400 || decode[map[string]string](t, w.Body.String())["error"] != "Use the address of a chat server on the local network." {
		t.Fatalf("refused %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	raw := &fs.PathError{Op: "open", Path: filepath.Join(t.TempDir(), "bubbles.json.tmp"), Err: errors.New("Access is denied.")}
	s.chatErr(w, fmt.Errorf("save: %w", raw))
	if w.Code != 500 || decode[map[string]string](t, w.Body.String())["error"] != chatInternal || strings.Contains(w.Body.String(), "bubbles.json") {
		t.Fatalf("internal %d %s", w.Code, w.Body)
	}
	if !log.has("team chat", "bubbles.json.tmp", "Access is denied") {
		t.Fatalf("diagnostics %q", log.lines)
	}
}
