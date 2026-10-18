package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"netdiag/internal/instance"
	"netdiag/internal/model"
	"netdiag/internal/problems"
	"netdiag/internal/store"
)

// fakeWindow serves GET /api/identity on 127.0.0.1 as the netdiag window
// holding token does (instance id id); proofToken signs the nonce, so a
// netdiag that does not hold token answers with a proof that fails.
func fakeWindow(t *testing.T, id, proofToken string) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/identity" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(instance.Identity{App: instance.App, Version: "test", InstanceID: id,
			Proof: instance.Proof(proofToken, r.URL.Query().Get("nonce"))})
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	return port
}

// Status outside the window's netdiag (stdio) finds the window through
// instance.json and its proof, says whether Agent access is on, and never
// holds the session token.
func TestStatusFindsWindow(t *testing.T) {
	const token = "tok-very-secret-session-token-0123456789"
	s, st := newSvc(t, true, nil)
	ctx := context.Background()
	if got := s.Status(ctx); got.Window.Open || got.Note != NoteNoWindow || got.Watching != nil || got.Problems.Source != problems.SourceNone {
		t.Fatalf("no window: %+v", got)
	}

	port := fakeWindow(t, "i-1", token)
	if err := instance.Save(st, instance.Info{Port: port, PID: 1, InstanceID: "i-1", Token: token, Version: "test"}); err != nil {
		t.Fatal(err)
	}
	got := s.Status(ctx)
	if !got.Window.Open || got.Window.Port != port || got.Window.ThisProcess || got.Window.AgentAccess || got.Note != NoteAgentOff {
		t.Fatalf("window, agent access off: %+v", got)
	}
	set, err := st.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	set.MCP.Enabled = true
	if _, err := s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	got = s.Status(ctx)
	if !got.Window.Open || !got.Window.AgentAccess || got.Note != NoteOtherProcess {
		t.Fatalf("window, agent access on: %+v", got)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), token) || strings.Contains(string(b), "tok-") || strings.Contains(string(b), "bub_") {
		t.Fatalf("status holds a token: %s", b)
	}

	// Another netdiag on the remembered port (it cannot prove the token)
	// is not the window.
	other := fakeWindow(t, "i-1", "another-token")
	if err := instance.Save(st, instance.Info{Port: other, InstanceID: "i-1", Token: token}); err != nil {
		t.Fatal(err)
	}
	if got := s.Status(ctx); got.Window.Open || got.Note != NoteNoWindow {
		t.Fatalf("foreign netdiag counted as the window: %+v", got)
	}
}

// Status of the window's netdiag: the window is this process, and a
// live-mode scan is watching, with its interval and when it started.
func TestStatusWatching(t *testing.T) {
	s, _ := newSvc(t, true, nil)
	s.SetWindow(func() int { return 8787 })
	got := s.Status(context.Background())
	if !got.Window.Open || !got.Window.ThisProcess || got.Window.Port != 8787 || got.Running != nil || got.Watching != nil ||
		got.Note != NoteNotChecked {
		t.Fatalf("nothing yet: %+v", got)
	}
	sc, err := s.Start(model.ScanRequest{CIDR: "10.0.0.0/30", Live: true, LiveIntervalSec: 300}.WithOrigin(model.OriginUI))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Cancel(); <-sc.Done() })
	deadline := time.Now().Add(5 * time.Second)
	for sc.State() != model.StateLive {
		if time.Now().After(deadline) {
			t.Fatalf("scan is %s", sc.State())
		}
		time.Sleep(10 * time.Millisecond)
	}
	got = s.Status(context.Background())
	w := got.Watching
	if w == nil || w.ScanID != sc.ID || w.CIDR != "10.0.0.0/30" || w.Since == "" || w.IntervalSec != 300 || w.Held {
		t.Fatalf("watching: %+v", w)
	}
	if got.Running == nil || got.Running.ScanID != sc.ID || got.Running.State != model.StateLive || got.Note != "" {
		t.Fatalf("running: %+v", got)
	}
	if got.Problems.Source != problems.SourceLive {
		t.Fatalf("problems: %+v", got.Problems)
	}
}

// Without a ledger of its own (stdio), Problems reads the window's
// open-problems.json for the start of each problem and the fixed list, and
// leaves the file as it is.
func TestProblemsReadsLedgerFile(t *testing.T) {
	s, st := newSvc(t, true, nil)
	if err := st.SaveScan(savedDNSFail()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	l, err := problems.OpenLedger(st)
	if err != nil {
		t.Fatal(err)
	}
	l.Apply(testNet, []model.Notice{{Trigger: model.TriggerHealthFailed, Key: "healthFailed||dns", Severity: "warn",
		Title: "Health check failed", At: "2026-10-17T09:50:00Z"}})
	l.Apply(testNet, []model.Notice{{Trigger: model.TriggerHealthFailed, Key: "healthFailed||gateway", Severity: "critical",
		Title: "Health check failed", At: now.Add(-time.Hour).Format(time.RFC3339)}})
	l.Apply(testNet, []model.Notice{{Trigger: model.TriggerHealthFailed, Key: "healthFailed||gateway", OK: true,
		Title: "Health check OK again", At: now.Add(-time.Minute).Format(time.RFC3339)}})
	l.MarkChat(testNet, "healthFailed||dns", problems.ChatPosted)
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
	before, err := st.ReadFile(store.FileOpenProblems)
	if err != nil {
		t.Fatal(err)
	}

	v := s.Problems()
	if v.Source != problems.SourceSaved || len(v.Open) != 1 || v.Open[0].Since != "2026-10-17T09:50:00Z" || v.Open[0].Chat != problems.ChatPosted {
		t.Fatalf("open: %+v", v.Open)
	}
	if len(v.Fixed) != 1 || v.Fixed[0].Key != "healthFailed||gateway" || v.Fixed[0].DownForSec <= 0 {
		t.Fatalf("fixed: %+v", v.Fixed)
	}
	after, _ := st.ReadFile(store.FileOpenProblems)
	if string(before) != string(after) {
		t.Fatal("Problems wrote open-problems.json")
	}

	// An unreadable file only leaves out the starts and the fixed list,
	// and stays for the window's ledger to set aside.
	if err := st.WriteFile(store.FileOpenProblems, []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	if v := s.Problems(); len(v.Open) != 1 || v.Open[0].Since != "" || len(v.Fixed) != 0 {
		t.Fatalf("unreadable: %+v", v)
	}
	if b, _ := st.ReadFile(store.FileOpenProblems); string(b) != "{not json" {
		t.Fatalf("file changed: %q", b)
	}
}
