package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/chat"
	"netdiag/internal/model"
	"netdiag/internal/problems"
	"netdiag/internal/service"
	"netdiag/internal/store"
)

// get_status over HTTP speaks for the window: this process, with the scan
// watching the network and its scanId; it holds no token.
func TestGetStatusHTTPWatching(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	e.svc.SetWindow(func() int { return 8787 })
	var st StatusOut
	e.ok(t, "get_status", nil, &st)
	if st.Version != "test" || st.Transport != "http" || !st.Window.Open || !st.Window.ThisProcess || st.Window.Port != 8787 ||
		st.Watching != nil || st.Running != nil || st.Problems.Source != problems.SourceNone || st.Note != service.NoteNotChecked {
		t.Fatalf("nothing yet: %+v", st)
	}
	live := startUILive(t, e)
	st = StatusOut{}
	res := e.ok(t, "get_status", nil, &st)
	if st.Watching == nil || st.Watching.ScanID != live.ID || st.Running == nil || st.Running.State != model.StateLive ||
		st.Problems.Source != problems.SourceLive || st.Note != "" {
		t.Fatalf("watching: %+v", st)
	}
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), "bub_") || strings.Contains(strings.ToLower(string(b)), "token") {
		t.Fatalf("get_status holds a token: %s", b)
	}
}

// get_status over stdio with no window says so; watching is null.
func TestGetStatusStdioNoWindow(t *testing.T) {
	e := setup(t, "stdio", true, fakePinger{})
	var st StatusOut
	res := e.ok(t, "get_status", nil, &st)
	if st.Transport != "stdio" || st.Window.Open || st.Watching != nil || st.Note != service.NoteNoWindow || st.Chat.State != service.ChatUnknown {
		t.Fatalf("stdio: %+v", st)
	}
	var raw map[string]any
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if w, ok := raw["watching"]; !ok || w != nil {
		t.Fatalf("watching should be null: %s", b)
	}
	// Without a data folder it still answers.
	e2 := setup(t, "stdio", false, fakePinger{})
	e2.ok(t, "get_status", nil, &st)
	if st.Window.Open || st.Problems.Source != problems.SourceNone {
		t.Fatalf("no store: %+v", st)
	}
}

// get_status.chat over the window's MCP address is the window's team chat:
// state, server, channel and app, whether Send alerts is on and what still
// waits; never the chat token or its start (tokenPrefix).
func TestGetStatusChat(t *testing.T) {
	const token = "bub_0123456789abcdefSECRET"
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := chat.NewConnection(chat.Server{Name: "Front office chat", BaseURL: "https://127.0.0.1:9443", OnboardingURL: "http://127.0.0.1:9080",
		CAFingerprint: "sha256:" + strings.Repeat("ab", 32)}, "netdiag", []string{"alerts:write"}, token, time.Now())
	c.Channel = chat.Channel{ID: "C1", Name: "netdiag-alerts"}
	if err := chat.SaveConnection(st, c); err != nil {
		t.Fatal(err)
	}
	m := chat.NewManager(chat.Options{Files: st, PollEvery: time.Hour})
	t.Cleanup(m.Close)
	svc := service.New(service.Config{Store: st, Chat: m})
	svc.SetWindow(func() int { return 8787 })
	ctx := context.Background()
	ct, sst := mcp.NewInMemoryTransports()
	ss, err := New(svc, Options{Transport: "http", Version: "test"}).Connect(ctx, sst, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	e := &env{cs: cs, svc: svc, st: st}
	var out StatusOut
	res := e.ok(t, "get_status", nil, &out)
	ch := out.Chat
	if ch.State != "connected" || ch.Server == nil || ch.Server.Host != "127.0.0.1" || ch.Channel != "netdiag-alerts" || ch.AppName != "netdiag" ||
		ch.SendAlerts || ch.MinSeverity != model.ChatSeverityWarning || ch.Waiting != 0 {
		t.Fatalf("chat: %+v", ch)
	}
	b, _ := json.Marshal(res)
	if strings.Contains(string(b), "bub_") || strings.Contains(strings.ToLower(string(b)), "token") {
		t.Fatalf("get_status holds the chat token: %s", b)
	}
	var raw struct {
		Chat map[string]any `json:"chat"`
	}
	b, _ = json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	// The plan's fields (v0.6 4.14) under their names.
	for _, k := range []string{"state", "server", "channel", "appName", "waiting", "sendAlerts"} {
		if _, ok := raw.Chat[k]; !ok {
			t.Errorf("chat has no %s: %s", k, b)
		}
	}
}

// problemsFixture is the fixture network for the problems tests: the
// shared golden hosts and conflicts (testdata/problems/hosts.json, the
// cases the Problems right now card is tested with) and a health report of
// network testNetwork. failGateway makes the router check fail.
func problemsFixture(t *testing.T, failGateway bool) model.ScanResult {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "problems", "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Conflicts []model.Conflict `json:"conflicts"`
		Hosts     []model.Host     `json:"hosts"`
	}
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	gw := model.HealthCheck{ID: "gateway", Label: "Gateway", Status: model.HealthOK, Detail: "10.0.1.1 replied in 2 ms"}
	if failGateway {
		gw = model.HealthCheck{ID: "gateway", Label: "Gateway", Status: model.HealthFail, Detail: "10.0.1.1 did not reply to ping",
			Data: map[string]any{"reason": model.ReasonNoReply}}
	}
	return model.ScanResult{ID: "fixture1", State: model.StateDone, Request: model.ScanRequest{CIDR: "10.0.0.0/22"}, Gateway: "10.0.1.1",
		StartedAt: now.Add(-2 * time.Minute).Format(time.RFC3339), FinishedAt: now.Add(-time.Minute).Format(time.RFC3339),
		Hosts: g.Hosts, Conflicts: g.Conflicts,
		Health: &model.HealthReport{CheckedAt: now.Add(-90 * time.Second).Format(time.RFC3339), Network: testNetwork,
			Checks: []model.HealthCheck{gw, {ID: "dns", Label: "DNS", Status: model.HealthOK}, {ID: "internet", Label: "Internet", Status: model.HealthOK}}}}
}

const testNetwork = "n-0123456789ab"

// fineFixture is a healthy network: two devices, every check fine.
func fineFixture() model.ScanResult {
	now := time.Now().UTC()
	return model.ScanResult{ID: "fine1", State: model.StateDone, Request: model.ScanRequest{CIDR: "10.0.0.0/24"}, Gateway: "10.0.0.1",
		StartedAt: now.Add(-2 * time.Minute).Format(time.RFC3339), FinishedAt: now.Add(-time.Minute).Format(time.RFC3339),
		Hosts: []model.Host{{IP: "10.0.0.1", Alive: true, Online: true, MAC: "02:00:5E:00:00:01"}, {IP: "10.0.0.20", Alive: true, Online: true}},
		Health: &model.HealthReport{CheckedAt: now.Add(-time.Minute).Format(time.RFC3339), Network: testNetwork,
			Checks: []model.HealthCheck{{ID: "gateway", Label: "Gateway", Status: model.HealthOK}, {ID: "dns", Label: "DNS", Status: model.HealthOK}}}}
}

// list_problems over stdio reads the newest saved scan (source saved) and
// the window's open-problems.json: the open problems with advice, their
// start, chat state and re-check, and the fixed list; includeFixed and
// limit trim it.
func TestListProblemsSaved(t *testing.T) {
	e := setup(t, "stdio", true, fakePinger{})
	var out ProblemsOut
	e.ok(t, "list_problems", nil, &out)
	if out.Source != problems.SourceNone || out.EverythingFine || len(out.Open) != 0 {
		t.Fatalf("nothing yet: %+v", out)
	}
	if err := e.st.SaveScan(problemsFixture(t, true)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	l, err := problems.OpenLedger(e.st)
	if err != nil {
		t.Fatal(err)
	}
	l.Apply(testNetwork, []model.Notice{{Trigger: model.TriggerHealthFailed, Key: "healthFailed||gateway", Severity: "critical",
		Title: "Health check failed", At: "2026-10-17T09:50:00Z"}})
	l.Apply(testNetwork, []model.Notice{{Trigger: model.TriggerHealthFailed, Key: "healthFailed||dns", Severity: "warn",
		Title: "Health check failed", At: now.Add(-time.Hour).Format(time.RFC3339)}})
	l.Apply(testNetwork, []model.Notice{{Trigger: model.TriggerHealthFailed, Key: "healthFailed||dns", OK: true,
		Title: "Health check OK again", At: now.Add(-48 * time.Minute).Format(time.RFC3339)}})
	l.MarkChat(testNetwork, "healthFailed||gateway", problems.ChatPosted)
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}

	e.ok(t, "list_problems", nil, &out)
	if out.Source != problems.SourceSaved || out.EverythingFine || out.CheckedAt == "" || len(out.Open) < 3 || out.Truncated {
		t.Fatalf("saved: %+v", out)
	}
	gw := out.Open[0]
	if gw.Key != "healthFailed||gateway" || gw.Severity != problems.SevCritical || gw.Check != "gateway" ||
		gw.Since != "2026-10-17T09:50:00Z" || !gw.Chat.Posted || gw.Recheck.Tool != "run_health_check" || gw.LastCheckedAt != out.CheckedAt {
		t.Fatalf("gateway item: %+v", gw)
	}
	if gw.Advice == nil || gw.Advice.Meaning != "This computer can't reach your router." || len(gw.Advice.Steps) == 0 {
		t.Fatalf("gateway advice: %+v", gw.Advice)
	}
	for _, p := range out.Open[1:] {
		if p.Device != nil && p.Check == "" && (p.Recheck.Tool != "probe_host" || p.Recheck.Args["ip"] != p.Device.IP) {
			t.Errorf("device item recheck: %+v", p)
		}
		if p.Advice == nil || p.Advice.Meaning == "" {
			t.Errorf("item without advice: %+v", p)
		}
		if p.Chat.Posted {
			t.Errorf("only the gateway was posted: %+v", p)
		}
	}
	if len(out.Fixed) != 1 || out.Fixed[0].Key != "healthFailed||dns" || out.Fixed[0].DownForSec != 12*60 {
		t.Fatalf("fixed: %+v", out.Fixed)
	}

	e.ok(t, "list_problems", map[string]any{"includeFixed": false, "limit": 1}, &out)
	if len(out.Open) != 1 || len(out.Fixed) != 0 || !out.Truncated {
		t.Fatalf("trimmed: %+v", out)
	}
}
