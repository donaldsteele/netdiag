package server

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"netdiag/internal/bubbles/bubblestest"
	"netdiag/internal/bubbles/protocol"
	"netdiag/internal/chat"
	"netdiag/internal/model"
	"netdiag/internal/problems"
	"netdiag/internal/scan"
	"netdiag/internal/store"
)

// watchScan is a live scan's bus for the team chat router: the test
// publishes its notices.
type watchScan struct {
	ch         chan model.Event
	reconciled chan struct{}
	done       chan struct{}
	once       sync.Once
}

func newWatchScan() *watchScan {
	w := &watchScan{ch: make(chan model.Event, 16), reconciled: make(chan struct{}), done: make(chan struct{})}
	close(w.reconciled)
	return w
}

func (w *watchScan) Subscribe() (<-chan model.Event, func()) { return w.ch, func() {} }
func (w *watchScan) Network() string                         { return quitNet }
func (w *watchScan) Request() model.ScanRequest              { return model.ScanRequest{Live: true} }
func (w *watchScan) Reconciled() <-chan struct{}             { return w.reconciled }
func (w *watchScan) Done() <-chan struct{}                   { return w.done }
func (w *watchScan) Adopted() []string                       { return nil }

const (
	quitNet = "n-0123456789ab"
	quitKey = "printerOffline|ip:192.168.1.50|"
)

// quitFault is a printer that stopped answering, recorded in the ledger
// first and then published, as a scan does.
func (w *watchScan) quitFault(l *problems.Ledger) {
	n := model.Notice{Trigger: model.TriggerPrinterOffline, Severity: "warn", IP: "192.168.1.50", Title: "Printer offline",
		Message: "Printer 192.168.1.50 stopped responding.", At: time.Now().UTC().Format(time.RFC3339), Key: quitKey}
	l.Apply(quitNet, []model.Notice{n})
	w.ch <- model.Event{Type: model.EventNotice, Data: model.NoticeEvent{Notice: n}}
}

// quitServer is a served server with a team chat manager paired with f,
// sending alerts, and its router on a live scan. It returns what Quit
// needs and a channel that gets Serve's answer.
func quitServer(t *testing.T, f *bubblestest.Server) (*Server, *store.Store, *problems.Ledger, *watchScan, chan error) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l, err := problems.OpenLedger(st)
	if err != nil {
		t.Fatal(err)
	}
	set := store.DefaultSettings()
	set.Chat = model.SettingsChat{Enabled: true, MinSeverity: model.ChatSeverityWarning} // New applies the saved settings
	if _, err := st.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	m := chat.NewManager(chat.Options{Files: st, Ledger: l, PairPoll: 5 * time.Millisecond,
		TrayConfig: func() (string, error) { return filepath.Join(t.TempDir(), "none.json"), nil },
		Hostname:   func() (string, error) { return "FRONTDESK-PC", nil }, Logf: t.Logf})
	pairChat(t, m, f)
	m.ApplySettings(model.SettingsChat{Enabled: true, MinSeverity: model.ChatSeverityWarning})
	waitFor(t, "the connected message", func() bool { return len(f.Messages()) == 1 })
	sc := newWatchScan()
	m.Attach(sc)
	s := New(Config{Registry: scan.NewRegistry(scan.New(scan.Deps{Env: fakeEnv{}})), Store: st, Health: fakeHealth{},
		Gateway: func() (string, error) { return "10.0.0.1", nil }, Ledger: l, Chat: m, SessionToken: testToken})
	if err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- s.Serve(context.Background()) }()
	return s, st, l, sc, served
}

// pairChat pairs m with f by its address, as the wizard does once the
// person compared the fingerprint, and waits until the chat admin's
// approval brought the channel.
func pairChat(t *testing.T, m *chat.Manager, f *bubblestest.Server) {
	t.Helper()
	a, _ := url.Parse(f.API.URL)
	o, _ := url.Parse(f.Onboard.URL)
	onboard, _ := strconv.Atoi(o.Port())
	ctx := context.Background()
	in, err := m.Inspect(ctx, a.Host, onboard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartPairing(ctx, in.Address, in.Fingerprint, false); err != nil {
		t.Fatal(err)
	}
	f.SetPairingState(protocol.PairingApproved)
	waitFor(t, "the pairing", func() bool { ps := m.PairStatus(); return ps.State == chat.PairApproved && ps.Channel != nil })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// quit asks the server to quit and waits for Serve to return.
func quit(t *testing.T, s *Server, served chan error) {
	t.Helper()
	if r, _ := doj(t, "POST", s.URL()+"/api/quit", "{}", "application/json"); r.StatusCode != 204 {
		t.Fatalf("quit: %d", r.StatusCode)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("server did not exit")
	}
}

// ledgerChat is the team chat state open-problems.json gives quitKey.
func ledgerChat(t *testing.T, st *store.Store) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(st.Dir(), store.FileOpenProblems))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Open []problems.Entry `json:"open"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for _, e := range f.Open {
		if e.Key == quitKey {
			return e.Chat
		}
	}
	t.Fatalf("%s has no %s: %s", store.FileOpenProblems, quitKey, b)
	return ""
}

// Quit saves what team chat still has to send in the order that keeps it
// (v0.6 7.1 "Close flushes chat and ledger"): the outbox gets its last 3 s
// first, then the ledger is saved with the chat state the outbox left,
// and only then do the chat's sender and polling stop.
func TestQuitFlushesTeamChat(t *testing.T) {
	t.Run("the chat server is away", func(t *testing.T) {
		f := bubblestest.New(t)
		s, st, l, sc, served := quitServer(t, f)
		f.Stop()
		sc.quitFault(l)
		waitFor(t, "the waiting alert", func() bool {
			c := s.chat.Status()
			return c.State == chat.StateUnreachable && c.Waiting == 1
		})
		start := time.Now()
		quit(t, s, served)
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("Quit waited %v for a server that does not answer", d)
		}
		b, err := os.ReadFile(filepath.Join(st.Dir(), store.FileChatOutbox))
		if err != nil || !strings.Contains(string(b), quitKey) || !strings.Contains(string(b), `"op": "post"`) {
			t.Fatalf("outbox after quit: %s (%v)", b, err)
		}
		if c := ledgerChat(t, st); c != problems.ChatPending {
			t.Fatalf("ledger chat state %q", c)
		}
	})
	t.Run("the chat server is slow", func(t *testing.T) {
		f := bubblestest.New(t)
		s, st, l, sc, served := quitServer(t, f)
		release := f.Hold("POST /api/v1/alerts")
		defer release()
		sc.quitFault(l)
		waitFor(t, "the post on its way", func() bool { return f.Held() == 1 })
		go func() {
			time.Sleep(300 * time.Millisecond)
			release()
		}()
		quit(t, s, served)
		if len(f.Posted()) != 1 {
			t.Fatalf("posted %d during Quit", len(f.Posted()))
		}
		if _, err := os.Stat(filepath.Join(st.Dir(), store.FileChatOutbox)); !os.IsNotExist(err) {
			t.Fatalf("outbox file after the post went: %v", err)
		}
		if c := ledgerChat(t, st); c != problems.ChatPosted {
			t.Fatalf("ledger chat state %q", c)
		}
	})
}
