package service

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"netdiag/internal/model"
	"netdiag/internal/scan"
)

func types(evs []AgentEvent) []string {
	out := []string{}
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func TestRingOverflowAndDropped(t *testing.T) {
	l := newEventLog(false)
	for i := 0; i < EventRingCap+50; i++ {
		l.ingest(model.Event{Type: model.EventPresence, Data: model.Presence{IP: "10.0.0.1", Online: i%2 == 0}})
	}
	evs, next, dropped := l.Since(0, 500)
	if !dropped {
		t.Fatal("expected dropped")
	}
	if len(evs) != 500 || evs[0].Seq != 51 {
		t.Fatalf("got %d first=%d", len(evs), evs[0].Seq)
	}
	if next != 550 {
		t.Fatalf("next=%d", next)
	}
	_, _, dropped = l.Since(50, 10)
	if dropped {
		t.Fatal("since=oldest-1 must not be dropped")
	}
	_, _, dropped = l.Since(49, 10)
	if !dropped {
		t.Fatal("since=49 should be dropped")
	}
}

func TestSincePagination(t *testing.T) {
	l := newEventLog(false)
	for i := 0; i < 25; i++ {
		l.ingest(model.Event{Type: model.EventNotice, Data: model.NoticeEvent{Notice: model.Notice{Title: "t"}}})
	}
	var since int64
	total := 0
	for {
		evs, next, dropped := l.Since(since, 10)
		if dropped {
			t.Fatal("dropped")
		}
		if len(evs) == 0 {
			if next != since {
				t.Fatalf("empty next=%d since=%d", next, since)
			}
			break
		}
		total += len(evs)
		since = next
	}
	if total != 25 || since != 25 {
		t.Fatalf("total=%d since=%d", total, since)
	}
}

func TestPresenceEvents(t *testing.T) {
	l := newEventLog(false)
	l.ingest(model.Event{Type: model.EventPresence, Data: model.Presence{IP: "10.0.0.5", Online: false}})
	l.ingest(model.Event{Type: model.EventPresence, Data: model.Presence{IP: "10.0.0.5", Online: true}})
	l.ingest(model.Event{Type: model.EventProgress, Data: model.Progress{}})
	l.ingest(model.Event{Type: model.EventLive, Data: model.LiveStatus{}})
	evs, _, _ := l.Since(0, 0)
	if got := types(evs); len(got) != 2 || got[0] != "offline" || got[1] != "online" || evs[0].IP != "10.0.0.5" {
		t.Fatalf("got %v", got)
	}
}

func TestPrinterChangeDetection(t *testing.T) {
	l := newEventLog(false)
	host := func(state string, alerts []model.Alert, pct int, st string) model.Event {
		return model.Event{Type: model.EventHost, Data: model.Host{IP: "10.0.0.9", IsPrinter: true, Printer: &model.PrinterInfo{
			State: state, Alerts: alerts,
			Supplies: []model.Supply{{Description: "Black toner", Percent: pct, State: st}},
		}}}
	}
	l.ingest(host("idle", nil, 80, "ok"))
	l.ingest(host("idle", nil, 80, "ok"))
	evs, _, _ := l.Since(0, 0)
	if got := types(evs); len(got) != 1 || got[0] != "discovered" {
		t.Fatalf("got %v", got)
	}
	l.ingest(host("idle", nil, 5, "low"))
	l.ingest(host("idle", nil, 5, "low")) // unchanged
	l.ingest(host("stopped", []model.Alert{{Description: "Paper jam"}}, 5, "low"))
	evs, _, _ = l.Since(0, 0)
	if got := types(evs); len(got) != 3 || got[1] != "printer" || got[2] != "printer" {
		t.Fatalf("got %v", got)
	}
	d := evs[2].Data.(PrinterEventData)
	if d.State != "stopped" || len(d.Alerts) != 1 || len(d.LowSupplies) != 1 || d.LowSupplies[0] != "Black toner 5%" {
		t.Fatalf("data %+v", d)
	}
}

func TestConflictHealthNoticeDone(t *testing.T) {
	l := newEventLog(false)
	c := model.Conflict{ID: "c1", Kind: model.ConflictMACFlap, IPs: []string{"10.0.0.2"}}
	l.ingest(model.Event{Type: model.EventConflict, Data: model.ConflictEvent{Conflict: c, Replay: true}})
	l.ingest(model.Event{Type: model.EventConflict, Data: model.ConflictEvent{Conflict: c, Resolved: true}})
	hr := model.HealthReport{Checks: []model.HealthCheck{{ID: "dns", Status: "fail"}, {ID: "x", Status: "warn"}}}
	l.ingest(model.Event{Type: model.EventHealth, Data: hr})
	l.ingest(model.Event{Type: model.EventHealth, Data: hr}) // unchanged
	l.ingest(model.Event{Type: model.EventDone, Data: model.ScanResult{State: "done"}})
	evs, _, _ := l.Since(0, 0)
	got := types(evs)
	want := []string{"conflict", "conflict_resolved", "health", "done"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v", got)
		}
	}
	if !evs[0].Replay || evs[1].Replay || evs[2].Summary != "health: 1 fail, 1 warn" {
		t.Fatalf("%+v", evs)
	}
	if !l.Finished() {
		t.Fatal("not finished")
	}
}

// An OK notice is a "fixed" event that names the key of the notice it closes.
func TestNoticeAndFixedEvents(t *testing.T) {
	l := newEventLog(false)
	key := "printerOffline|3C:52:82:11:22:33|"
	l.ingest(model.Event{Type: model.EventNotice, Data: model.NoticeEvent{Notice: model.Notice{
		ID: "n-1", Trigger: model.TriggerPrinterOffline, Severity: "warn", IP: "10.0.0.5",
		Title: "Printer offline", Message: "Printer stopped responding.", Key: key, At: "2026-10-17T10:30:00Z"}}})
	l.ingest(model.Event{Type: model.EventNotice, Data: model.NoticeEvent{Notice: model.Notice{
		ID: "n-2", Trigger: model.TriggerPrinterOffline, Severity: "info", IP: "10.0.0.5",
		Title: "Printer back online", Message: "Printer answers again.", Key: key, OK: true,
		OpenedAt: "2026-10-17T10:30:00Z", At: "2026-10-17T10:42:00Z"}}})
	evs, _, _ := l.Since(0, 0)
	if got := types(evs); len(got) != 2 || got[0] != "notice" || got[1] != "fixed" {
		t.Fatalf("types %v", got)
	}
	f := evs[1]
	data := f.Data.(map[string]any)
	if f.IP != "10.0.0.5" || f.Summary != "fixed: Printer back online: Printer answers again." ||
		data["key"] != key || data["openedAt"] != "2026-10-17T10:30:00Z" || evs[0].Data.(map[string]any)["key"] != key {
		t.Fatalf("%+v / %+v", f, evs[0])
	}
}

type aliveSet map[string]bool

func (a aliveSet) Ping(_ context.Context, ip netip.Addr, _ time.Duration) (time.Duration, bool, error) {
	return time.Millisecond, a[ip.String()], nil
}

func newAliveSvc(t *testing.T) *Service {
	t.Helper()
	s, _ := newSvc(t, false, nil)
	deps := scan.Deps{
		Pinger: aliveSet{"10.0.0.1": true, "10.0.0.2": true}, ARP: fakeARP{}, Ports: fakePorts{}, Web: fakeWeb{},
		SNMP: fakeSNMP{}, DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{},
	}
	return New(Config{Registry: scan.NewRegistry(scan.New(deps)), BaseCtx: context.Background(), Health: s.cfg.Health})
}

func waitDone(t *testing.T, sc *scan.Scan) {
	t.Helper()
	select {
	case <-sc.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("scan timeout")
	}
}

func TestEagerAttachDiscoversHosts(t *testing.T) {
	s := newAliveSvc(t)
	sc, err := s.Start(model.ScanRequest{CIDR: "10.0.0.0/30"})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, sc)
	id := sc.Snapshot().ID
	l, err := s.Events(id)
	if err != nil {
		t.Fatal(err)
	}
	<-l.closed
	evs, _, _ := l.Since(0, 0)
	disc := map[string]bool{}
	done := 0
	for _, e := range evs {
		if e.Type == "discovered" {
			disc[e.IP] = true
		}
		if e.Type == "done" {
			done++
		}
	}
	if !disc["10.0.0.1"] || !disc["10.0.0.2"] || done != 1 {
		t.Fatalf("events %v", types(evs))
	}
}

func TestLazyAttachFinishedScanOnlyDone(t *testing.T) {
	s := newAliveSvc(t)
	sc, err := s.cfg.Registry.Start(s.cfg.BaseCtx, model.ScanRequest{CIDR: "10.0.0.0/30"})
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, sc)
	if len(sc.Snapshot().Hosts) < 2 {
		t.Fatal("expected hosts")
	}
	l, err := s.Events(sc.Snapshot().ID)
	if err != nil {
		t.Fatal(err)
	}
	<-l.closed
	evs, _, _ := l.Since(0, 0)
	for _, e := range evs {
		if e.Type != "done" && !e.Replay {
			t.Fatalf("unexpected non-replay event %+v", e)
		}
		if e.Type == "discovered" {
			t.Fatalf("discovered on lazy attach")
		}
	}
	if len(evs) == 0 || evs[len(evs)-1].Type != "done" {
		t.Fatalf("events %v", types(evs))
	}
}

func TestEventsUnknownScan(t *testing.T) {
	s := newAliveSvc(t)
	_, err := s.Events("nope")
	if _, ok := err.(*NotFoundError); !ok || err.Error() != "unknown scan" {
		t.Fatalf("err=%v", err)
	}
}

func TestEventLogRace(t *testing.T) {
	l := newEventLog(false)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			l.ingest(model.Event{Type: model.EventPresence, Data: model.Presence{IP: "1.1.1.1"}})
		}
	}()
	go func() {
		defer wg.Done()
		var since int64
		for i := 0; i < 500; i++ {
			_, since, _ = l.Since(since, 50)
		}
	}()
	wg.Wait()
}
