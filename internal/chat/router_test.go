package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"netdiag/internal/bubbles/bubblestest"
	"netdiag/internal/bubbles/protocol"
	"netdiag/internal/mdns"
	"netdiag/internal/model"
	"netdiag/internal/problems"
	"netdiag/internal/store"
)

// fakeScan is a scan's bus for the router: the test publishes its events.
type fakeScan struct {
	network    string
	live       bool
	ch         chan model.Event
	reconciled chan struct{}
	done       chan struct{}
	once       sync.Once
}

func newFakeScan(network string, live bool) *fakeScan {
	return &fakeScan{network: network, live: live, ch: make(chan model.Event, 256),
		reconciled: make(chan struct{}), done: make(chan struct{})}
}

func (f *fakeScan) Subscribe() (<-chan model.Event, func()) { return f.ch, func() {} }
func (f *fakeScan) Network() string                         { return f.network }
func (f *fakeScan) Request() model.ScanRequest              { return model.ScanRequest{Live: f.live} }
func (f *fakeScan) Reconciled() <-chan struct{}             { return f.reconciled }
func (f *fakeScan) Done() <-chan struct{}                   { return f.done }
func (f *fakeScan) reconcile()                              { f.once.Do(func() { close(f.reconciled) }) }

// notice records n in the ledger first, as the scan's addNotices does, then
// publishes it.
func (f *fakeScan) notice(l *problems.Ledger, n model.Notice) {
	if l != nil {
		l.Apply(f.network, []model.Notice{n})
	}
	f.ch <- model.Event{Type: model.EventNotice, Data: model.NoticeEvent{Notice: n}}
}

func (f *fakeScan) host(h model.Host) { f.ch <- model.Event{Type: model.EventHost, Data: h} }

const testNet = "n-0123456789ab"

var t0 = time.Date(2026, 10, 17, 10, 30, 0, 0, time.UTC)

const zebraKey = "printerAlert|00:07:4D:11:22:33|hrPrinterDetectedErrorState.doorOpen|1a2b3c4d"

// coverOpen is the ZD621's cover alert as the engine reports it.
func coverOpen(at time.Time) model.Notice {
	return model.Notice{ID: "n-1", Trigger: model.TriggerPrinterAlert, Severity: "critical", IP: "192.168.68.76",
		MAC: "00:07:4D:11:22:33", Name: "ZD621", Title: "Printer alert", Message: "ZD621 (192.168.68.76): Cover open",
		At: at.Format(time.RFC3339), Key: zebraKey, Code: "hrPrinterDetectedErrorState.doorOpen", Detail: "Cover open",
		Advice: &model.Advice{Meaning: "The printer says a door or cover is open.", Steps: []string{"Close the printer's cover or door."}}}
}

// coverClosed is its OK.
func coverClosed(at, opened time.Time) model.Notice {
	return model.Notice{ID: "n-2", Trigger: model.TriggerPrinterAlert, Severity: "info", IP: "192.168.68.76",
		MAC: "00:07:4D:11:22:33", Name: "ZD621", Title: "Printer alert cleared", Message: "ZD621 (192.168.68.76): Cover open is gone.",
		At: at.Format(time.RFC3339), OpenedAt: opened.Format(time.RFC3339), Key: zebraKey, OK: true}
}

// fault is a warning about the device at ip under its own key.
func fault(i int, severity string) model.Notice {
	ip := fmt.Sprintf("192.168.1.%d", 10+i)
	return model.Notice{ID: fmt.Sprintf("n-f%d", i), Trigger: model.TriggerPrinterOffline, Severity: severity, IP: ip,
		Title: "Printer offline", Message: "Printer " + ip + " stopped responding.", At: t0.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
		Key: "printerOffline|ip:" + ip + "|"}
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

// fastRetry makes the sender retry quickly.
func fastRetry(o *Options) {
	o.RetryMin, o.RetryMax, o.SaveDelay = 5*time.Millisecond, 20*time.Millisecond, 5*time.Millisecond
}

// connected is a manager paired with f, its channel chosen and Send alerts
// to team chat on, after its "is connected" message went out.
func connected(t *testing.T, f *bubblestest.Server, st DataFiles, l Ledger, edit ...func(*Options)) *Manager {
	t.Helper()
	m := newManager(t, st, append([]func(*Options){fastRetry, func(o *Options) { o.Ledger = l }}, edit...)...)
	pairWith(t, m, f)
	before := len(f.Messages())
	m.ApplySettings(model.SettingsChat{Enabled: true, MinSeverity: model.ChatSeverityWarning})
	waitFor(t, "the connected message", func() bool { return len(f.Messages()) > before })
	return m
}

// ledgerOn opens the ledger on st with a clock at t0. On the real one, the
// fixes the tests make at t0 are pruned once they are more than
// problems.FixedFor old, and their chat state goes with them.
func ledgerOn(t *testing.T, st *store.Store) *problems.Ledger {
	t.Helper()
	l, err := problems.OpenLedger(st)
	if err != nil {
		t.Fatal(err)
	}
	l.SetClock(func() time.Time { return t0 })
	return l
}

// The M11 acceptance with a fake ZD621: opening the cover posts one alert;
// a replay of it posts nothing more; closing it resolves the same card with
// "Fixed at…" (v0.6 4.9).
func TestFaultOpensOneCardAndFixedResolvesIt(t *testing.T) {
	f := bubblestest.New(t)
	st, _ := newStore(t)
	l := ledgerOn(t, st)
	m := connected(t, f, st, l)
	sc := newFakeScan(testNet, true)
	m.Attach(sc)
	sc.reconcile()
	sc.host(model.Host{IP: "192.168.68.76", MAC: "00:07:4D:11:22:33", Vendor: "Zebra Technologies",
		Printer: &model.PrinterInfo{Model: "ZD621-300dpi"}, Web: []model.WebService{{URL: "http://192.168.68.76/"}}})
	open := coverOpen(t0)
	sc.notice(l, open)
	sc.ch <- model.Event{Type: model.EventNotice, Data: model.NoticeEvent{Notice: open, Replay: true}}
	waitFor(t, "the post", func() bool { return l.Chat(testNet, zebraKey) == problems.ChatPosted })
	card, ok := f.Alert(zebraKey)
	if !ok || card.Alert.State != protocol.AlertOpen || card.ChannelID != "C1" || card.Alert.Severity != protocol.SeverityCritical {
		t.Fatalf("card %+v", card)
	}
	p := f.Posted()
	if len(p) != 1 {
		t.Fatalf("posted %d times", len(p))
	}
	in := p[0]
	if in.Title != "Printer alert: ZD621 (192.168.68.76)" || in.DedupeKey != zebraKey || in.RenotifyAfterS == nil || *in.RenotifyAfterS != 0 ||
		in.OccurredAt == nil || *in.OccurredAt != t0.Format(time.RFC3339) || in.Link == nil || *in.Link != "http://192.168.68.76/" {
		t.Fatalf("alert %+v", in)
	}
	if in.Text != "ZD621 (192.168.68.76): Cover open\n\nWhat to try:\n1. Close the printer's cover or door." {
		t.Fatalf("text %q", in.Text)
	}
	fields := map[string]string{}
	for _, fl := range in.Fields {
		fields[fl.Name] = fl.Value
	}
	if fields["Device"] != "ZD621" || fields["Address"] != "192.168.68.76" || fields["MAC"] != "00:07:4D:11:22:33" ||
		fields["Maker"] != "Zebra Technologies" || fields["Model"] != "ZD621-300dpi" || fields["Since"] == "" {
		t.Fatalf("fields %v", fields)
	}

	sc.notice(l, coverClosed(t0.Add(12*time.Minute), t0))
	waitFor(t, "the resolve", func() bool { return len(f.Resolved()) == 1 })
	r := f.Resolved()[0]
	want := "Fixed at " + t0.Add(12*time.Minute).Local().Format("15:04") + " (was down 12 min). ZD621 (192.168.68.76): Cover open is gone."
	if r.DedupeKey != zebraKey || r.Text != want {
		t.Fatalf("resolve %+v, want %q", r, want)
	}
	after, _ := f.Alert(zebraKey)
	if after.ID != card.ID || after.Alert.State != protocol.AlertResolved || len(f.Posted()) != 1 {
		t.Fatalf("not the same card closed: %+v posted %d", after, len(f.Posted()))
	}
	waitFor(t, "the ledger", func() bool { return l.Chat(testNet, zebraKey) == "" })
	if st := m.Status(); st.Waiting != 0 || st.LastSentAt == "" || st.State != StateConnected {
		t.Fatalf("status %+v", st)
	}
}

// D17: the chat server is down while a fault opens and clears: when it is
// back, nothing about it is posted (the post and its resolve are dropped
// together), and later alerts go as usual.
func TestChatDownWhileFaultOpensAndClears(t *testing.T) {
	f := bubblestest.New(t)
	st, _ := newStore(t)
	l := ledgerOn(t, st)
	m := connected(t, f, st, l)
	sc := newFakeScan(testNet, true)
	m.Attach(sc)
	sc.reconcile()
	f.Stop()
	sc.notice(l, coverOpen(t0))
	waitFor(t, "unreachable with one waiting", func() bool {
		s := m.Status()
		return s.State == StateUnreachable && s.Waiting == 1 && s.Since != ""
	})
	sc.notice(l, coverClosed(t0.Add(time.Minute), t0))
	waitFor(t, "nothing waiting", func() bool { return m.Status().Waiting == 0 })
	f.Start()
	m.TryNow(context.Background())
	sc.notice(l, fault(1, "warn"))
	waitFor(t, "the later alert", func() bool { return len(f.Posted()) == 1 })
	if p := f.Posted(); p[0].DedupeKey != fault(1, "warn").Key || len(f.Resolved()) != 0 {
		t.Fatalf("posted %+v resolved %+v", p, f.Resolved())
	}
	if _, ok := f.Alert(zebraKey); ok {
		t.Fatal("the dropped alert reached chat")
	}
	if c := l.Chat(testNet, zebraKey); c != "" {
		t.Fatalf("ledger chat state %q", c)
	}
	if s := m.Status(); s.State != StateConnected || s.Dropped != 0 {
		t.Fatalf("status %+v", s)
	}
}

// D17 with the post on its way (bubbles slow, or its computer off so each
// call takes its whole time): the problem is fixed while the post is being
// sent and the post does not get through. When the server is back, nothing
// about it is posted or resolved. When the post does get through, the same
// card is closed.
func TestFixedWhilePostIsOnItsWay(t *testing.T) {
	t.Run("the post fails", func(t *testing.T) {
		f := bubblestest.New(t)
		st, _ := newStore(t)
		l := ledgerOn(t, st)
		m := connected(t, f, st, l)
		sc := newFakeScan(testNet, true)
		m.Attach(sc)
		sc.reconcile()
		release := f.Hold("POST /api/v1/alerts")
		defer release()
		sc.notice(l, coverOpen(t0))
		waitFor(t, "the post on its way", func() bool { return f.Held() == 1 })
		sc.notice(l, coverClosed(t0.Add(time.Minute), t0))
		waitFor(t, "the resolve queued", func() bool { return m.Status().Waiting == 2 })
		f.FailNext(503, protocol.CodeUnavailable, "starting", 0)
		release()
		waitFor(t, "nothing waiting", func() bool { return m.Status().Waiting == 0 })
		m.TryNow(context.Background())
		time.Sleep(100 * time.Millisecond)
		if _, ok := f.Alert(zebraKey); ok || len(f.Posted()) != 0 || len(f.Resolved()) != 0 {
			t.Fatalf("posted %+v resolved %+v", f.Posted(), f.Resolved())
		}
		if c := l.Chat(testNet, zebraKey); c != "" {
			t.Fatalf("ledger chat state %q", c)
		}
		sc.notice(l, fault(1, "warn"))
		waitFor(t, "the next alert", func() bool { return len(f.Posted()) == 1 })
	})
	t.Run("the post gets through", func(t *testing.T) {
		f := bubblestest.New(t)
		st, _ := newStore(t)
		l := ledgerOn(t, st)
		m := connected(t, f, st, l)
		sc := newFakeScan(testNet, true)
		m.Attach(sc)
		sc.reconcile()
		release := f.Hold("POST /api/v1/alerts")
		defer release()
		sc.notice(l, coverOpen(t0))
		waitFor(t, "the post on its way", func() bool { return f.Held() == 1 })
		sc.notice(l, coverClosed(t0.Add(time.Minute), t0))
		waitFor(t, "the resolve queued", func() bool { return m.Status().Waiting == 2 })
		release()
		waitFor(t, "the resolve", func() bool { return len(f.Resolved()) == 1 })
		if c, _ := f.Alert(zebraKey); c.Alert.State != protocol.AlertResolved || len(f.Posted()) != 1 {
			t.Fatalf("card %+v posted %d", c, len(f.Posted()))
		}
		waitFor(t, "the ledger", func() bool { return l.Chat(testNet, zebraKey) == "" })
	})
}

// Problems the ledger already holds reach team chat once netdiag watches
// their network (4.9 "First connect", D10), even when sending started
// before: the burst waits for a live scan's first pass. A one-shot scan
// posts nothing, but its problem goes as soon as Live mode starts, with
// the words its notice had; one the live scan's first pass also reports
// in a notice of its own is posted once.
func TestBurstWhenWatchingStarts(t *testing.T) {
	t.Run("a one-shot scan, then Live mode", func(t *testing.T) {
		f := bubblestest.New(t)
		st, _ := newStore(t)
		l := ledgerOn(t, st)
		m := connected(t, f, st, l)
		msgs := len(f.Messages())
		one := newFakeScan(testNet, false)
		m.Attach(one)
		one.reconcile()
		one.host(model.Host{IP: "192.168.68.76", MAC: "00:07:4D:11:22:33", Vendor: "Zebra Technologies",
			Printer: &model.PrinterInfo{Model: "ZD621-300dpi"}})
		one.notice(l, coverOpen(t0))
		time.Sleep(100 * time.Millisecond)
		if len(f.Posted()) != 0 {
			t.Fatalf("a one-shot scan posted %+v", f.Posted())
		}
		close(one.done)

		live := newFakeScan(testNet, true)
		m.Attach(live)
		live.reconcile() // the cover is still open: seeded, so no notice
		waitFor(t, "the post", func() bool { return len(f.Posted()) == 1 })
		p := f.Posted()[0]
		if p.DedupeKey != zebraKey || p.Text != "ZD621 (192.168.68.76): Cover open\n\nWhat to try:\n1. Close the printer's cover or door." ||
			p.OccurredAt == nil || *p.OccurredAt != t0.Format(time.RFC3339) {
			t.Fatalf("posted %+v", p)
		}
		fields := map[string]string{}
		for _, fl := range p.Fields {
			fields[fl.Name] = fl.Value
		}
		if fields["Maker"] != "Zebra Technologies" || fields["Model"] != "ZD621-300dpi" {
			t.Fatalf("fields %v", fields)
		}
		waitFor(t, "posted", func() bool { return l.Chat(testNet, zebraKey) == problems.ChatPosted })
		time.Sleep(50 * time.Millisecond)
		if len(f.Posted()) != 1 || len(f.Messages()) != msgs {
			t.Fatalf("posted %d, messages %q", len(f.Posted()), f.Messages()[msgs:])
		}
	})
	t.Run("sending starts before Live mode", func(t *testing.T) {
		f := bubblestest.New(t)
		st, _ := newStore(t)
		l := ledgerOn(t, st)
		web := model.Notice{Trigger: model.TriggerHealthFailed, Severity: "warn", Title: "Health check failed",
			Message: "Web server check failed.", Key: "healthFailed||webserver", At: t0.Format(time.RFC3339)}
		l.Apply(testNet, []model.Notice{web})
		m := connected(t, f, st, l) // the wizard's Done: no scan yet
		if msgs := f.Messages(); len(msgs) != 1 || len(f.Posted()) != 0 {
			t.Fatalf("messages %q posted %d", msgs, len(f.Posted()))
		}
		live := newFakeScan(testNet, true)
		m.Attach(live)
		found := coverOpen(t0.Add(time.Minute)) // found by this pass
		l.Apply(testNet, []model.Notice{found})
		live.reconcile()
		waitFor(t, "the two alerts", func() bool { return len(f.Posted()) == 2 })
		live.ch <- model.Event{Type: model.EventNotice, Data: model.NoticeEvent{Notice: found}} // its notice comes after
		time.Sleep(100 * time.Millisecond)
		var keys []string
		for _, p := range f.Posted() {
			keys = append(keys, p.DedupeKey)
			if p.DedupeKey == web.Key && (p.Title != "Health check failed: Web server" || !strings.Contains(p.Text, "What to try:")) {
				t.Fatalf("health alert %+v", p)
			}
		}
		if len(keys) != 2 || keys[0] != zebraKey || keys[1] != web.Key {
			t.Fatalf("posted %v", keys)
		}
		if len(f.Messages()) != 1 {
			t.Fatalf("a second connected message: %q", f.Messages())
		}
	})
}

// A new connection (Reconnect after the token was refused) keeps the posts
// that still wait as pending: they make new cards, so an OK that comes
// before one could go drops both (D17), and the card never opens for a
// fixed problem.
func TestReconnectKeepsWaitingPostPending(t *testing.T) {
	f := bubblestest.New(t)
	st, _ := newStore(t)
	l := ledgerOn(t, st)
	m := connected(t, f, st, l)
	sc := newFakeScan(testNet, true)
	m.Attach(sc)
	sc.reconcile()
	f.Revoke()
	sc.notice(l, coverOpen(t0))
	waitFor(t, "needs reconnecting", func() bool { return m.Status().State == StateNeedsReconnect })
	f.RemoveChannel("C1") // the new app is in no channel yet: the post cannot go at once
	if _, err := m.StartPairing(context.Background(), "", "", true); err != nil {
		t.Fatal(err)
	}
	f.SetPairingState(protocol.PairingApproved)
	waitFor(t, "the approval", func() bool { return m.PairStatus().State == PairApproved })
	if s := m.Status(); s.State != StateNoChannel || s.Waiting != 1 {
		t.Fatalf("status %+v", s)
	}
	if c := l.Chat(testNet, zebraKey); c != problems.ChatPending {
		t.Fatalf("after the new connection: %q", c)
	}
	sc.notice(l, coverClosed(t0.Add(time.Minute), t0))
	waitFor(t, "nothing waiting", func() bool { return m.Status().Waiting == 0 })
	f.AddChannel("C1", "netdiag-alerts")
	if _, err := m.SetChannel(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the connected message", func() bool { return len(f.Messages()) == 2 })
	time.Sleep(100 * time.Millisecond)
	if len(f.Posted()) != 0 {
		t.Fatalf("posted a fixed problem: %+v", f.Posted())
	}
	if c := l.Chat(testNet, zebraKey); c != "" {
		t.Fatalf("ledger chat state %q", c)
	}
}

// Message nonces never repeat across runs: bubbles keeps them for good and
// answers one it has seen with its old message, so a later run's note
// would vanish (v0.2.0 internal/chat/messages.go replayNonce).
func TestMessageNoncesAcrossRestart(t *testing.T) {
	f := bubblestest.New(t)
	st, _ := newStore(t)
	m1 := connected(t, f, st, nil) // its connected message
	m1.Close()
	if n := len(f.Messages()); n != 1 {
		t.Fatalf("messages %q", f.Messages())
	}
	m2 := newManager(t, st, fastRetry)
	m2.ApplySettings(model.SettingsChat{Enabled: true, MinSeverity: model.ChatSeverityInfo})
	sc := newFakeScan(testNet, true)
	m2.Attach(sc)
	sc.reconcile()
	sc.notice(nil, model.Notice{Trigger: model.TriggerNewDevice, Severity: "info", IP: "192.168.1.77", Name: "phone",
		Title: "New device", Key: "newDevice|AA:BB:CC:DD:EE:FF|192.168.1.77"})
	waitFor(t, "the note", func() bool { return len(f.Messages()) == 2 })
	if got := f.Messages()[1]; got != "C1 New device on the local network: phone (192.168.1.77)." {
		t.Fatalf("message %q", got)
	}
}

// Quit while an alert waits: chat-outbox.json holds it; the next run holds
// it until its first pass is reconciled, then posts it once with the
// original occurred_at. A problem fixed while netdiag was closed is not
// posted at all.
func TestQuitWhilePendingPostsOnceAfterRestart(t *testing.T) {
	f := bubblestest.New(t)
	st, dir := newStore(t)
	l1 := ledgerOn(t, st)
	m1 := connected(t, f, st, l1)
	sc1 := newFakeScan(testNet, true)
	m1.Attach(sc1)
	sc1.reconcile()
	f.Stop()
	gone := fault(2, "warn")
	sc1.notice(l1, coverOpen(t0))
	sc1.notice(l1, gone)
	waitFor(t, "two waiting", func() bool { return m1.Status().Waiting == 2 })
	start := time.Now()
	if err := m1.Flush(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Flush waited %v for a server that does not answer", d)
	}
	if err := l1.Flush(); err != nil {
		t.Fatal(err)
	}
	m1.Close()

	b, err := os.ReadFile(filepath.Join(dir, store.FileChatOutbox))
	if err != nil {
		t.Fatal(err)
	}
	var file outboxFile
	if err := json.Unmarshal(b, &file); err != nil || file.SchemaVersion != 1 || len(file.Items) != 2 {
		t.Fatalf("outbox file %s %v", b, err)
	}
	if it := file.Items[0]; it.Op != OpPost || it.Key != zebraKey || it.Network != testNet || *it.Alert.OccurredAt != t0.Format(time.RFC3339) {
		t.Fatalf("item %+v", it)
	}

	// The next run: the printer device's problem is fixed while netdiag
	// was closed, the cover is still open.
	f.Start()
	l2 := ledgerOn(t, st)
	m2 := newManager(t, st, fastRetry, func(o *Options) { o.Ledger = l2 })
	m2.ApplySettings(model.SettingsChat{Enabled: true, MinSeverity: model.ChatSeverityWarning})
	if s := m2.Status(); s.Waiting != 2 {
		t.Fatalf("restart waiting %+v", s)
	}
	// Neither a one-shot scan nor a live scan of another network lets the
	// held posts go: the ledger has not checked them again (and a cancelled
	// first pass never closes Reconciled, scan.TestReconciledAfterLedger).
	one := newFakeScan(testNet, false)
	m2.Attach(one)
	one.reconcile()
	away := newFakeScan("n-elsewhere", true)
	m2.Attach(away)
	away.reconcile()
	sc2 := newFakeScan(testNet, true)
	m2.Attach(sc2)
	time.Sleep(100 * time.Millisecond)
	if n := len(f.Posted()); n != 0 {
		t.Fatalf("posted %d before the first pass was reconciled", n)
	}
	if s := m2.Status(); s.Waiting != 2 || !m2.out.holding() {
		t.Fatalf("released by another scan: %+v", s)
	}
	ok := gone
	ok.OK, ok.Severity, ok.OpenedAt, ok.At = true, "info", gone.At, t0.Add(time.Hour).Format(time.RFC3339)
	l2.Apply(testNet, []model.Notice{ok}) // the reconcile's OK
	sc2.reconcile()
	waitFor(t, "the post", func() bool { return len(f.Posted()) == 1 })
	time.Sleep(100 * time.Millisecond)
	p := f.Posted()
	if len(p) != 1 || p[0].DedupeKey != zebraKey || *p[0].OccurredAt != t0.Format(time.RFC3339) {
		t.Fatalf("posted %+v", p)
	}
	waitFor(t, "the outbox file gone", func() bool {
		_, err := os.Stat(filepath.Join(dir, store.FileChatOutbox))
		return os.IsNotExist(err)
	})
	if c := l2.Chat(testNet, zebraKey); c != problems.ChatPosted {
		t.Fatalf("ledger %q", c)
	}
}

// The chat server's address changes (v0.6 4.9 "Following address
// changes"): once it has not answered for MoveAfter, netdiag finds it by
// its mDNS announcement whose fp starts like the pinned fingerprint, moves
// there without pairing again, and the waiting alert goes. A server that
// announces another fingerprint, or the same prefix with another CA, is
// never used.
func TestFollowsServerAddress(t *testing.T) {
	f := bubblestest.New(t)
	impostor := bubblestest.New(t)
	st, dir := newStore(t)
	l := ledgerOn(t, st)
	fp16 := strings.TrimPrefix(f.CAFingerprint, "sha256:")[:16]
	var mu sync.Mutex
	var recs []mdns.Record
	browses := 0
	browse := func(context.Context, string, []string, time.Duration) ([]mdns.Record, error) {
		mu.Lock()
		defer mu.Unlock()
		browses++
		return recs, nil
	}
	announce := func(port int, fp string) mdns.Record {
		return mdns.Record{IP: "127.0.0.1", Info: model.MDNSInfo{Details: []model.MDNSService{
			{Type: ServiceType, Instance: "Office", Port: port, TXT: map[string]string{"fp": fp}}}}}
	}
	m := connected(t, f, st, l, func(o *Options) {
		o.Browse, o.PollEvery, o.MoveAfter = browse, 20*time.Millisecond, 150*time.Millisecond
	})
	sc := newFakeScan(testNet, true)
	m.Attach(sc)
	sc.reconcile()

	oldURL := f.API.URL
	moved := f.Move()
	_, imPortText, _ := strings.Cut(strings.TrimPrefix(impostor.API.URL, "https://"), ":")
	var imPort, newPort int
	fmt.Sscan(imPortText, &imPort)
	_, newPortText, _ := strings.Cut(strings.TrimPrefix(moved.URL, "https://"), ":")
	fmt.Sscan(newPortText, &newPort)
	mu.Lock()
	recs = []mdns.Record{announce(newPort, "ffffffffffffffff"), announce(imPort, fp16)}
	mu.Unlock()
	moveAt := time.Now()
	sc.notice(l, coverOpen(t0))
	waitFor(t, "unreachable", func() bool { return m.Status().State == StateUnreachable })
	waitFor(t, "two browses", func() bool { mu.Lock(); defer mu.Unlock(); return browses >= 2 })
	if n := len(f.Posted()); n != 0 {
		t.Fatalf("posted %d to a server that only another fingerprint or CA announced", n)
	}
	if s := m.Status(); s.State != StateUnreachable {
		t.Fatalf("an impostor's announcement changed the state: %+v", s)
	}

	mu.Lock()
	recs = append(recs, announce(newPort, fp16))
	mu.Unlock()
	waitFor(t, "the post at the new address", func() bool { return len(f.Posted()) == 1 })
	if d := time.Since(moveAt); d > 3*time.Second {
		t.Fatalf("took %v", d)
	}
	// The fake records the post before netdiag reads its answer, and the
	// sender may post at the new address before the move's Activity note is
	// added: wait for the status to show all of it.
	moveNoted := func(s Status) bool {
		for _, n := range s.Activity {
			if n.Text == "Team chat server moved to 127.0.0.1." {
				return true
			}
		}
		return false
	}
	waitFor(t, "connected at the new address, nothing waiting, the move noted", func() bool {
		s := m.Status()
		return s.State == StateConnected && s.Waiting == 0 && moveNoted(s)
	})
	if s := m.Status(); s.Server == nil || s.Server.Port != newPort {
		t.Fatalf("status %+v", s)
	}
	saved := readFile(t, dir)
	if saved.Server.BaseURL != moved.URL || saved.Server.BaseURL == oldURL || saved.Server.CAFingerprint != f.CAFingerprint {
		t.Fatalf("bubbles.json server %+v", saved.Server)
	}
}

// The poll waits less than PollEvery when the server will have been away
// for MoveAfter before then, so with the defaults (60 s, 2 min) netdiag
// looks for a moved server 2 minutes after it stopped answering, and a
// waiting alert goes within 3 minutes.
func TestNextPollBeforeMove(t *testing.T) {
	now := t0
	m := &Manager{opt: Options{PollEvery: time.Minute, MoveAfter: 2 * time.Minute, Now: func() time.Time { return now }}}
	if d := m.nextPoll(); d != time.Minute {
		t.Fatalf("connected: %v", d)
	}
	m.trouble, m.since = StateUnreachable, now.Add(-90*time.Second)
	if d := m.nextPoll(); d != 30*time.Second {
		t.Fatalf("unreachable 90 s: %v", d)
	}
	m.since = now.Add(-5 * time.Minute)
	if d := m.nextPoll(); d != time.Minute {
		t.Fatalf("unreachable 5 min: %v", d)
	}
}

// The outbox's answers to the chat server's errors (4.9 "Errors").
func TestOutboxErrors(t *testing.T) {
	t.Run("401 needs reconnecting and keeps the item", func(t *testing.T) {
		f := bubblestest.New(t)
		st, _ := newStore(t)
		l := ledgerOn(t, st)
		m := connected(t, f, st, l)
		sc := newFakeScan(testNet, true)
		m.Attach(sc)
		sc.reconcile()
		f.Revoke()
		sc.notice(l, coverOpen(t0))
		waitFor(t, "needs reconnecting", func() bool { return m.Status().State == StateNeedsReconnect })
		time.Sleep(50 * time.Millisecond)
		if s := m.Status(); s.Waiting != 1 || s.Reason != ReasonRefused {
			t.Fatalf("status %+v", s)
		}
		m.TryNow(context.Background()) // the token still fails
		if s := m.Status(); s.State != StateNeedsReconnect {
			t.Fatalf("after Try now %+v", s)
		}
		m.poll(context.Background(), false) // a poll without the token never clears it
		if s := m.Status(); s.State != StateNeedsReconnect {
			t.Fatalf("after a poll %+v", s)
		}
	})
	t.Run("identity changed stops", func(t *testing.T) {
		f := bubblestest.New(t)
		other := bubblestest.New(t)
		st, _ := newStore(t)
		l := ledgerOn(t, st)
		m := connected(t, f, st, l)
		_, gen, _, _ := m.current()
		_ = m.updateConn(gen, func(c *Connection) { c.Server.BaseURL = other.API.URL })
		sc := newFakeScan(testNet, true)
		m.Attach(sc)
		sc.reconcile()
		sc.notice(l, coverOpen(t0))
		waitFor(t, "identity changed", func() bool { return m.Status().State == StateIdentityChanged })
		time.Sleep(50 * time.Millisecond)
		if s := m.Status(); s.Waiting != 1 || len(other.Posted()) != 0 {
			t.Fatalf("status %+v", s)
		}
	})
	t.Run("404 is no channel, items kept until the channel works", func(t *testing.T) {
		f := bubblestest.New(t)
		st, _ := newStore(t)
		l := ledgerOn(t, st)
		m := connected(t, f, st, l)
		sc := newFakeScan(testNet, true)
		m.Attach(sc)
		sc.reconcile()
		f.RemoveChannel("C1")
		sc.notice(l, coverOpen(t0))
		waitFor(t, "no channel", func() bool { s := m.Status(); return s.State == StateNoChannel && s.Reason == ReasonNotAllowed })
		if s := m.Status(); s.Waiting != 1 {
			t.Fatalf("status %+v", s)
		}
		f.AddChannel("C1", "netdiag-alerts")
		if _, err := m.SetChannel(context.Background(), ""); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the post", func() bool { return len(f.Posted()) == 1 })
	})
	t.Run("400 drops the item with an Activity line", func(t *testing.T) {
		f := bubblestest.New(t)
		st, _ := newStore(t)
		l := ledgerOn(t, st)
		m := connected(t, f, st, l)
		bad := coverOpen(t0)
		bad.Title, bad.Name, bad.IP = " ", "", "" // bubbles: title is required
		in := alertFor(bad, nil, "")
		in.Title = " "
		m.out.Post(testNet, bad.Key, in, false)
		waitFor(t, "dropped", func() bool { return m.Status().Dropped == 1 })
		s := m.Status()
		if s.Waiting != 0 || len(s.Activity) == 0 || s.Activity[len(s.Activity)-1].Text != "Team chat refused an alert: title: title is required" {
			t.Fatalf("status %+v", s)
		}
		sc := newFakeScan(testNet, true)
		m.Attach(sc)
		sc.reconcile()
		sc.notice(l, fault(1, "warn"))
		waitFor(t, "the next post", func() bool { return len(f.Posted()) == 1 })
	})
	t.Run("429 waits for Retry-After, 503 retries", func(t *testing.T) {
		f := bubblestest.New(t)
		st, _ := newStore(t)
		l := ledgerOn(t, st)
		m := connected(t, f, st, l)
		sc := newFakeScan(testNet, true)
		m.Attach(sc)
		sc.reconcile()
		f.FailNext(503, protocol.CodeUnavailable, "starting", 0)
		f.FailNext(429, protocol.CodeRateLimited, "slow down", time.Second)
		start := time.Now()
		sc.notice(l, coverOpen(t0))
		waitFor(t, "the post", func() bool { return len(f.Posted()) == 1 })
		if d := time.Since(start); d < 900*time.Millisecond {
			t.Fatalf("posted after %v, before Retry-After", d)
		}
		if s := m.Status(); s.State != StateConnected || s.Dropped != 0 {
			t.Fatalf("status %+v", s)
		}
	})
}

// v0.6-D24 against the fake: an archived channel is no_channel (archived)
// with the items kept; Change channel moves the open cards ("Moved to
// #ops.") and posts them in the new channel; a card still open in another
// channel is resolved by key there and the alert posted once more; and a
// card in an archived channel cannot be moved, which the Activity log says.
func TestArchivedChannelConflictAgainstV020(t *testing.T) {
	t.Run("archived", func(t *testing.T) {
		f := bubblestest.New(t)
		st, _ := newStore(t)
		l := ledgerOn(t, st)
		m := connected(t, f, st, l)
		sc := newFakeScan(testNet, true)
		m.Attach(sc)
		sc.reconcile()
		f.ArchiveChannel("C1")
		sc.notice(l, coverOpen(t0))
		waitFor(t, "archived", func() bool { s := m.Status(); return s.State == StateNoChannel && s.Reason == ReasonArchived })
		if s := m.Status(); s.Waiting != 1 {
			t.Fatalf("status %+v", s)
		}
	})
	t.Run("change channel moves the cards", func(t *testing.T) {
		f := bubblestest.New(t)
		st, _ := newStore(t)
		l := ledgerOn(t, st)
		m := connected(t, f, st, l)
		sc := newFakeScan(testNet, true)
		m.Attach(sc)
		sc.reconcile()
		sc.notice(l, coverOpen(t0))
		waitFor(t, "posted", func() bool { return l.Chat(testNet, zebraKey) == problems.ChatPosted })
		f.AddChannel("C2", "ops")
		if _, err := m.SetChannel(context.Background(), "C2"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "moved", func() bool {
			c, _ := f.Alert(zebraKey)
			return c.ChannelID == "C2" && c.Alert.State == protocol.AlertOpen
		})
		if r := f.Resolved(); len(r) != 1 || r[0].Text != "Moved to #ops." {
			t.Fatalf("resolved %+v", r)
		}
		waitFor(t, "posted again", func() bool { return l.Chat(testNet, zebraKey) == problems.ChatPosted })
		// The moved card says what the first one said.
		if p := f.Posted(); len(p) != 2 || p[1].Title != p[0].Title || p[1].Text != p[0].Text || !strings.Contains(p[1].Text, "What to try:") ||
			len(p[1].Fields) != len(p[0].Fields) || p[1].Channel != "C2" {
			t.Fatalf("moved card %+v, first %+v", p[1], p[0])
		}
	})
	t.Run("open in another channel", func(t *testing.T) {
		f := bubblestest.New(t)
		st, _ := newStore(t)
		l := ledgerOn(t, st)
		m := connected(t, f, st, l)
		sc := newFakeScan(testNet, true)
		m.Attach(sc)
		sc.reconcile()
		sc.notice(l, coverOpen(t0))
		waitFor(t, "posted", func() bool { return l.Chat(testNet, zebraKey) == problems.ChatPosted })
		f.AddChannel("C2", "ops")
		_, gen, _, _ := m.current()
		_ = m.setChannel(gen, Channel{ID: "C2", Name: "ops"}) // no move
		m.out.Post(testNet, zebraKey, alertFor(coverOpen(t0), nil, ""), true)
		waitFor(t, "moved", func() bool { c, _ := f.Alert(zebraKey); return c.ChannelID == "C2" })
		if r := f.Resolved(); len(r) != 1 || r[0].Text != "Moved to #ops." {
			t.Fatalf("resolved %+v", r)
		}
	})
	t.Run("out of an archived channel", func(t *testing.T) {
		f := bubblestest.New(t)
		st, _ := newStore(t)
		l := ledgerOn(t, st)
		m := connected(t, f, st, l)
		sc := newFakeScan(testNet, true)
		m.Attach(sc)
		sc.reconcile()
		sc.notice(l, coverOpen(t0))
		waitFor(t, "posted", func() bool { return l.Chat(testNet, zebraKey) == problems.ChatPosted })
		f.AddChannel("C2", "ops")
		f.ArchiveChannel("C1")
		m.mu.Lock()
		m.oldChannel = "netdiag-alerts"
		m.mu.Unlock()
		_, gen, _, _ := m.current()
		_ = m.setChannel(gen, Channel{ID: "C2", Name: "ops"})
		m.out.Post(testNet, zebraKey, alertFor(coverOpen(t0), nil, ""), true)
		want := "Team chat could not move an alert out of the archived channel #netdiag-alerts."
		waitFor(t, "the Activity line", func() bool {
			s := m.Status()
			return len(s.Activity) > 0 && s.Activity[len(s.Activity)-1].Text == want
		})
		if s := m.Status(); s.Waiting != 0 {
			t.Fatalf("status %+v", s)
		}
	})
}

// The router's filters: replays, the storm guard's notices and the test
// key never reach chat; the Send level holds back what is below it; a scan
// that is not watching posts nothing but still closes cards; new devices
// are notes; Send switched off resolves the cards with "netdiag stopped
// sending alerts here."
func TestRouterFilters(t *testing.T) {
	f := bubblestest.New(t)
	st, _ := newStore(t)
	l := ledgerOn(t, st)
	m := connected(t, f, st, l)
	sc := newFakeScan(testNet, true)
	m.Attach(sc)
	sc.reconcile()
	link := model.Notice{Trigger: model.TriggerNetdiagLink, Severity: "warn", Title: "netdiag lost the network", Key: "netdiagLink||" + testNet}
	sc.notice(l, link)
	sc.notice(l, model.Notice{Trigger: model.TriggerChatTest, Severity: "info", Title: "t", Key: TestKey})
	sc.ch <- model.Event{Type: model.EventNotice, Data: model.NoticeEvent{Notice: fault(9, "warn"), Replay: true}}
	sc.notice(l, fault(1, "info")) // below the default level
	nd := model.Notice{Trigger: model.TriggerNewDevice, Severity: "info", IP: "192.168.1.77", Name: "phone", Title: "New device", Key: "newDevice|AA:BB:CC:DD:EE:FF|192.168.1.77"}
	sc.notice(l, nd)
	sc.notice(l, fault(2, "warn"))
	waitFor(t, "the warning", func() bool { return len(f.Posted()) == 1 })
	if p := f.Posted(); p[0].DedupeKey != fault(2, "warn").Key || p[0].Severity != protocol.SeverityWarning {
		t.Fatalf("posted %+v", p)
	}

	// Everything, including notes: the new device is a message.
	m.ApplySettings(model.SettingsChat{Enabled: true, MinSeverity: model.ChatSeverityInfo})
	msgs := len(f.Messages())
	sc.notice(l, nd)
	waitFor(t, "the note", func() bool { return len(f.Messages()) == msgs+1 })
	if got := f.Messages()[msgs]; got != "C1 New device on the local network: phone (192.168.1.77)." {
		t.Fatalf("message %q", got)
	}

	// Only urgent problems.
	m.ApplySettings(model.SettingsChat{Enabled: true, MinSeverity: model.ChatSeverityCritical})
	sc.notice(l, fault(3, "warn"))
	sc.notice(l, fault(4, "critical"))
	waitFor(t, "the critical", func() bool { return len(f.Posted()) == 2 })

	// A one-shot scan: no posts, but its OK closes a card.
	one := newFakeScan(testNet, false)
	m.Attach(one)
	one.reconcile()
	one.notice(l, fault(5, "critical"))
	ok := fault(4, "critical")
	ok.OK, ok.Severity, ok.Title, ok.OpenedAt = true, "info", "Printer back online", ok.At
	one.notice(l, ok)
	waitFor(t, "the resolve", func() bool { return len(f.Resolved()) == 1 })
	if len(f.Posted()) != 2 {
		t.Fatalf("a scan that is not watching posted: %+v", f.Posted())
	}

	// Send off: the open card closes with the note; nothing more is posted.
	m.ApplySettings(model.SettingsChat{Enabled: false, MinSeverity: model.ChatSeverityWarning})
	waitFor(t, "the stop resolve", func() bool { return len(f.Resolved()) == 2 })
	if r := f.Resolved()[1]; r.DedupeKey != fault(2, "warn").Key || r.Text != "netdiag stopped sending alerts here." {
		t.Fatalf("resolve %+v", r)
	}
	sc.notice(l, fault(6, "critical"))
	time.Sleep(100 * time.Millisecond)
	if len(f.Posted()) != 2 {
		t.Fatalf("posted with Send off: %+v", f.Posted())
	}
}

// First connect (4.9): one message, then at most 10 alerts for the current
// problems at or above the Send level, the most severe first, then how
// many more there are.
func TestFirstConnectBurst(t *testing.T) {
	f := bubblestest.New(t)
	st, _ := newStore(t)
	l := ledgerOn(t, st)
	var ns []model.Notice
	for i := range 12 {
		ns = append(ns, fault(i, "warn"))
	}
	ns = append(ns, fault(20, "critical"), fault(21, "info"))
	l.Apply(testNet, ns)
	l.Apply("n-other", []model.Notice{fault(30, "critical")})
	m := newManager(t, st, fastRetry, func(o *Options) { o.Ledger = l })
	m.ApplySettings(model.SettingsChat{Enabled: false, MinSeverity: model.ChatSeverityWarning})
	sc := newFakeScan(testNet, true)
	m.Attach(sc)
	sc.reconcile()
	pairWith(t, m, f)
	time.Sleep(50 * time.Millisecond)
	if len(f.Messages()) != 0 || len(f.Posted()) != 0 {
		t.Fatal("sent before Send alerts was on")
	}
	m.ApplySettings(model.SettingsChat{Enabled: true, MinSeverity: model.ChatSeverityWarning}) // the wizard's Done
	waitFor(t, "the burst", func() bool { return len(f.Messages()) == 2 && len(f.Posted()) == 10 })
	msgs := f.Messages()
	if msgs[0] != "C1 netdiag on FRONTDESK-PC is connected. It posts alerts here while it is watching the local network." ||
		msgs[1] != "C1 …and 3 more problems. Open netdiag on FRONTDESK-PC to see them all." {
		t.Fatalf("messages %q", msgs)
	}
	p := f.Posted()
	if p[0].DedupeKey != fault(20, "critical").Key || p[0].Severity != protocol.SeverityCritical || p[1].DedupeKey != fault(0, "warn").Key {
		t.Fatalf("order %+v", p[:2])
	}
	for _, in := range p {
		if in.DedupeKey == fault(21, "info").Key || in.DedupeKey == fault(30, "critical").Key {
			t.Fatalf("posted %s", in.DedupeKey)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if len(f.Messages()) != 2 || len(f.Posted()) != 10 {
		t.Fatal("a second burst")
	}
	// The next live scan's first pass leaves out the problems the burst
	// only counted: they do not trickle in after "…and 3 more".
	again := newFakeScan(testNet, true)
	m.Attach(again)
	again.reconcile()
	time.Sleep(burstQuiet + 150*time.Millisecond)
	if len(f.Messages()) != 2 || len(f.Posted()) != 10 {
		t.Fatalf("the next pass posted %d, messages %q", len(f.Posted()), f.Messages())
	}
}

// The ledger drops a problem of a network netdiag has not been on for 7
// days: its card is resolved with words that say so.
func TestSevenDayDropResolves(t *testing.T) {
	f := bubblestest.New(t)
	st, _ := newStore(t)
	l := ledgerOn(t, st)
	now := time.Now()
	var cmu sync.Mutex
	l.SetClock(func() time.Time { cmu.Lock(); defer cmu.Unlock(); return now })
	m := connected(t, f, st, l)
	sc := newFakeScan(testNet, true)
	m.Attach(sc)
	sc.reconcile()
	sc.notice(l, coverOpen(t0))
	waitFor(t, "posted", func() bool { return l.Chat(testNet, zebraKey) == problems.ChatPosted })
	cmu.Lock()
	now = now.Add(8 * 24 * time.Hour)
	cmu.Unlock()
	l.Apply("n-elsewhere", nil)
	waitFor(t, "the resolve", func() bool { return len(f.Resolved()) == 1 })
	if r := f.Resolved()[0]; r.DedupeKey != zebraKey || r.Text != "netdiag has not been on this network for 7 days." {
		t.Fatalf("resolve %+v", r)
	}
}

// Flush on Quit waits at most its timeout for a server that does not
// answer in time, and the item stays in the file for the next run.
func TestFlushTimeout(t *testing.T) {
	f := bubblestest.New(t)
	st, dir := newStore(t)
	l := ledgerOn(t, st)
	m := connected(t, f, st, l)
	release := f.Hold("POST /api/v1/alerts")
	defer release()
	m.out.Post(testNet, zebraKey, alertFor(coverOpen(t0), nil, ""), false)
	waitFor(t, "the held call", func() bool { return f.Held() == 1 })
	start := time.Now()
	if err := m.Flush(200 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > 2*time.Second {
		t.Fatalf("Flush took %v", d)
	}
	b, err := os.ReadFile(filepath.Join(dir, store.FileChatOutbox))
	if err != nil || !strings.Contains(string(b), zebraKey) {
		t.Fatalf("outbox file %s %v", b, err)
	}
}

// Try now asks the server at once and sends what waits, without waiting
// for the next retry.
func TestTryNow(t *testing.T) {
	f := bubblestest.New(t)
	st, _ := newStore(t)
	l := ledgerOn(t, st)
	m := connected(t, f, st, l, func(o *Options) { o.RetryMin, o.RetryMax = time.Hour, time.Hour })
	f.Stop()
	m.out.Post(testNet, zebraKey, alertFor(coverOpen(t0), nil, ""), false)
	waitFor(t, "unreachable", func() bool { return m.Status().State == StateUnreachable })
	f.Start()
	if s := m.TryNow(context.Background()); s.State != StateConnected {
		t.Fatalf("Try now %+v", s)
	}
	waitFor(t, "the post", func() bool { return len(f.Posted()) == 1 })
}

// Polling asks GET /status without the token: a server that answers again
// clears unreachable.
func TestPollClearsUnreachable(t *testing.T) {
	f := bubblestest.New(t)
	st, _ := newStore(t)
	m := connected(t, f, st, nil, func(o *Options) { o.PollEvery = 10 * time.Millisecond })
	f.Stop()
	waitFor(t, "unreachable", func() bool { return m.Status().State == StateUnreachable })
	f.Start()
	waitFor(t, "connected", func() bool { return m.Status().State == StateConnected })
}

// Disconnect forgets what waits and the chat state of every problem.
func TestDisconnectForgetsOutbox(t *testing.T) {
	f := bubblestest.New(t)
	st, dir := newStore(t)
	l := ledgerOn(t, st)
	m := connected(t, f, st, l)
	f.Stop()
	sc := newFakeScan(testNet, true)
	m.Attach(sc)
	sc.reconcile()
	sc.notice(l, coverOpen(t0))
	waitFor(t, "waiting", func() bool { return m.Status().Waiting == 1 })
	if err := m.out.save(); err != nil {
		t.Fatal(err)
	}
	if err := m.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if s := m.Status(); s.Waiting != 0 || l.Chat(testNet, zebraKey) != "" {
		t.Fatalf("status %+v chat %q", s, l.Chat(testNet, zebraKey))
	}
	if _, err := os.Stat(filepath.Join(dir, store.FileChatOutbox)); !os.IsNotExist(err) {
		t.Fatalf("outbox file still there: %v", err)
	}
}
