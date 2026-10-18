package scan

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"netdiag/internal/bubbles/bubblestest"
	"netdiag/internal/bubbles/protocol"
	"netdiag/internal/chat"
	"netdiag/internal/mdns"
	"netdiag/internal/model"
	"netdiag/internal/store"
)

// pairedChat is a team chat manager paired with the fake bubbles server f,
// on st, sending alerts at the default level.
func pairedChat(t *testing.T, f *bubblestest.Server, st *store.Store, l chat.Ledger) *chat.Manager {
	t.Helper()
	m := chat.NewManager(chat.Options{Files: st, Ledger: l, PairPoll: 5 * time.Millisecond,
		RetryMin: 5 * time.Millisecond, RetryMax: 20 * time.Millisecond,
		Browse:     func(context.Context, string, []string, time.Duration) ([]mdns.Record, error) { return nil, nil },
		TrayConfig: func() (string, error) { return t.TempDir() + "/none.json", nil },
		Hostname:   func() (string, error) { return "FRONTDESK-PC", nil }})
	t.Cleanup(m.Close)
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
	waitUntil(t, "the pairing", func() bool { ps := m.PairStatus(); return ps.State == chat.PairApproved && ps.Channel != nil })
	m.ApplySettings(model.SettingsChat{Enabled: true, MinSeverity: model.ChatSeverityWarning})
	return m
}

func waitUntil(t *testing.T, what string, cond func() bool) {
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

// End to end with a live scan, the real ledger and the chat router against
// the bubbles v0.2.0 fake (M11 acceptance, the ZD621's cover with a fake
// SNMP printer): the cover open in the first pass posts exactly one
// critical alert once the pass is reconciled; the cover closed (a live
// poll) resolves that same card with "Fixed at…".
func TestPrinterAlertReachesTeamChatAndCloses(t *testing.T) {
	f := bubblestest.New(t)
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := openLedger(t, st)
	m := pairedChat(t, f, st, l)
	d := fakeDeps()
	d.SNMP = &scriptSNMP{ip: "10.0.0.10", faulty: 1, faultyPolls: 1 << 30}
	d.PrinterPoll = 300 * time.Millisecond
	d.LiveInterval = 15 * time.Millisecond
	d.Ledger = l
	s, err := New(d).Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/26", Live: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Cancel(); s.Wait() }()
	m.Attach(s)
	select {
	case <-s.Reconciled():
	case <-time.After(5 * time.Second):
		t.Fatal("never reconciled")
	}
	waitUntil(t, "the alert", func() bool { return len(f.Posted()) == 1 })
	p := f.Posted()[0]
	if p.Severity != protocol.SeverityCritical || !strings.HasPrefix(p.Title, "Printer alert: ") || !strings.Contains(p.Title, "10.0.0.10") {
		t.Fatalf("posted %+v", p)
	}
	card, _ := f.Alert(p.DedupeKey)
	waitUntil(t, "the resolve", func() bool { return len(f.Resolved()) == 1 })
	r := f.Resolved()[0]
	after, _ := f.Alert(p.DedupeKey)
	if r.DedupeKey != p.DedupeKey || !strings.HasPrefix(r.Text, "Fixed at ") || after.ID != card.ID || after.Alert.State != protocol.AlertResolved {
		t.Fatalf("resolve %+v card %+v", r, after)
	}
	time.Sleep(100 * time.Millisecond)
	if len(f.Posted()) != 1 {
		t.Fatalf("posted %d times", len(f.Posted()))
	}
}

// Reconciled is closed once, after the ledger heard the first pass's
// notices, also for a one-shot scan. A first pass that is cancelled checked
// nothing: Reconciled stays open and only Done closes.
func TestReconciledAfterLedger(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		d := zebraDeps(coverOpen)
		d.Ledger = &recLedger{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s, err := New(d).Start(ctx, model.ScanRequest{CIDR: "10.0.0.0/24", Live: true})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-s.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("never done")
		}
		select {
		case <-s.Reconciled():
			t.Fatal("a cancelled first pass closed Reconciled")
		default:
		}
		if res := s.Wait(); res.State != model.StateCancelled {
			t.Fatalf("state %q", res.State)
		}
	})
	rl := &recLedger{}
	d := zebraDeps(coverOpen)
	d.Ledger = rl
	s, err := New(d).Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Reconciled():
	case <-time.After(5 * time.Second):
		t.Fatal("never reconciled")
	}
	rl.mu.Lock()
	n := len(rl.applied)
	rl.mu.Unlock()
	if n == 0 {
		t.Fatalf("reconciled before the ledger heard the pass: %d applied", n)
	}
	s.Wait()
}
