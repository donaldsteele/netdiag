package notify

import (
	"testing"
	"time"

	"netdiag/internal/model"
)

// firstPass observes hosts the way a scan's first pass does: each host
// first appears without its printer data, then the probe fills it in.
func firstPass(e *Engine, hosts ...model.Host) []model.Notice {
	var out []model.Notice
	for _, h := range hosts {
		bare := h
		bare.Printer = nil
		out = append(out, e.Observe(model.Host{}, bare)...)
		out = append(out, e.Observe(bare, h)...)
	}
	return out
}

// lastRun returns the open problems of an earlier run that saw the Zebra's
// cover open (what the ledger keeps across the restart).
func lastRun(t *testing.T) []OpenFault {
	t.Helper()
	e, _ := newEng()
	n := firstPass(e, printer(true, 50, door, off))
	if len(n) != 1 || n[0].Trigger != model.TriggerPrinterAlert {
		t.Fatalf("earlier run: %v", n)
	}
	return e.Open()
}

// Acceptance (M6): the cover was closed while netdiag was not running. The
// next first pass reports no fault and closes the earlier one with one OK
// that keeps its key and OpenedAt.
func TestReconcileFixedWhileClosed(t *testing.T) {
	seed := lastRun(t)
	e, c := newEng()
	c.t = c.t.Add(time.Hour)
	e.Defer()
	if n := firstPass(e, printer(true, 50)); len(n) != 0 {
		t.Fatalf("first pass: %v", n)
	}
	e.Seed(seed)
	n := e.Reconcile([]model.Host{printer(true, 50)}, nil, nil)
	if len(n) != 1 || !n[0].OK || n[0].Key != seed[0].Key || n[0].Title != "Printer alert cleared" ||
		n[0].OpenedAt != seed[0].OpenedAt.UTC().Format(time.RFC3339) || n[0].Message != "10.0.0.5: cleared Door or cover open." {
		t.Fatalf("reconcile: %v", n)
	}
	if len(e.Open()) != 0 {
		t.Fatalf("open: %v", e.Open())
	}
}

// Acceptance (M6): restarted with the cover still open. The first pass finds
// the alert again but gives no notice: it is the same problem, still open
// since the earlier run, and it closes normally later.
func TestReconcileStillOpen(t *testing.T) {
	seed := lastRun(t)
	e, c := newEng()
	c.t = c.t.Add(time.Hour)
	e.Defer()
	cur := printer(true, 50, door, off)
	if n := firstPass(e, cur); len(n) != 0 {
		t.Fatalf("first pass: %v", n)
	}
	e.Seed(seed)
	if n := e.Reconcile([]model.Host{cur}, nil, nil); len(n) != 0 {
		t.Fatalf("reconcile: %v", n)
	}
	open := e.Open()
	if len(open) != 1 || open[0].Key != seed[0].Key || !open[0].OpenedAt.Equal(seed[0].OpenedAt) {
		t.Fatalf("open: %v, seeded %v", open, seed)
	}
	if n := e.Observe(cur, cur); len(n) != 0 {
		t.Fatalf("same reading: %v", n)
	}
	n := e.Observe(cur, printer(true, 50))
	if len(n) != 1 || !n[0].OK || n[0].Key != seed[0].Key || n[0].OpenedAt != seed[0].OpenedAt.UTC().Format(time.RFC3339) {
		t.Fatalf("closed later: %v", n)
	}
}

// A seeded problem the first pass did not find again is checked: a seeded
// one whose device was not found stays open.
func TestReconcileSeeded(t *testing.T) {
	opened := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	gone := multiIP("10.0.0.7", "10.0.0.8")
	stays := model.Conflict{ID: "c2", Kind: model.ConflictMACFlap, IPs: []string{"10.0.0.9"}, MACs: []string{"AA:00:00:00:00:01", "AA:00:00:00:00:02"}}
	seed := []OpenFault{
		{Key: "printerOffline|" + prMAC + "|", Trigger: model.TriggerPrinterOffline, IP: "10.0.0.5", MAC: prMAC, OpenedAt: opened},
		{Key: "deviceOffline|ip:10.0.0.77|", Trigger: model.TriggerDeviceOffline, IP: "10.0.0.77", OpenedAt: opened},
		{Key: ConflictKey(gone), Trigger: model.TriggerIPConflict, IP: "10.0.0.7", OpenedAt: opened},
		{Key: ConflictKey(stays), Trigger: model.TriggerIPConflict, IP: "10.0.0.9", OpenedAt: opened},
		{Key: HealthKey("dns"), Trigger: model.TriggerHealthFailed, OpenedAt: opened},
		{Key: HealthKey("gateway"), Trigger: model.TriggerHealthFailed, OpenedAt: opened},
		{Key: HealthKey("wifi"), Trigger: model.TriggerHealthFailed, OpenedAt: opened},
	}
	e, _ := newEng()
	e.Defer()
	pr := printer(true, 50)
	firstPass(e, pr)
	e.Seed(seed)
	rep := &model.HealthReport{Checks: []model.HealthCheck{
		{ID: "dns", Label: "DNS", Status: model.HealthWarn},
		{ID: "gateway", Label: "Gateway", Status: model.HealthFail},
		{ID: "wifi", Label: "Wi-Fi", Status: model.HealthUnavailable},
	}}
	n := e.Reconcile([]model.Host{pr}, []model.Conflict{stays}, rep)
	want := map[string]string{
		"printerOffline|" + prMAC + "|": "Printer back online",
		ConflictKey(gone):               "Network conflict cleared",
		HealthKey("dns"):                "Health check OK again",
	}
	if len(n) != len(want) {
		t.Fatalf("reconcile: %v", n)
	}
	for _, x := range n {
		if !x.OK || want[x.Key] != x.Title || x.OpenedAt != "2026-01-01T00:00:00Z" {
			t.Fatalf("notice %+v", x)
		}
	}
	open := map[string]bool{}
	for _, f := range e.Open() {
		open[f.Key] = true
	}
	for _, k := range []string{"deviceOffline|ip:10.0.0.77|", ConflictKey(stays), HealthKey("gateway"), HealthKey("wifi")} {
		if !open[k] {
			t.Fatalf("%s closed; open %v", k, e.Open())
		}
	}
	// Without a health report nothing is known about the checks.
	e, _ = newEng()
	e.Seed(seed[4:5])
	if n := e.Reconcile(nil, nil, nil); len(n) != 0 || len(e.Open()) != 1 {
		t.Fatalf("no report: %v %v", n, e.Open())
	}
}

// A device that was offline when netdiag last ran and turns up in a later
// live cycle is back: one OK, no "New device".
func TestPrevEmptyOK(t *testing.T) {
	e, _ := newEng()
	e.Defer()
	e.Seed([]OpenFault{{Key: "deviceOffline|ip:10.0.0.77|", Trigger: model.TriggerDeviceOffline, IP: "10.0.0.77"}})
	if n := e.Reconcile(nil, nil, nil); len(n) != 0 {
		t.Fatalf("reconcile: %v", n)
	}
	h := model.Host{IP: "10.0.0.77", Alive: true, Online: true, IsNew: true}
	n := e.Observe(model.Host{}, h)
	if len(n) != 1 || !n[0].OK || n[0].Key != "deviceOffline|ip:10.0.0.77|" || n[0].Title != "Device back online" {
		t.Fatalf("back: %v", n)
	}
	if n := e.Observe(model.Host{}, model.Host{IP: "10.0.0.78", Online: true, IsNew: true}); len(n) != 1 || n[0].Trigger != model.TriggerNewDevice {
		t.Fatalf("a new device is still new: %v", n)
	}
}

// While deferred, notices wait for Reconcile; a problem that opens and
// closes while deferred is never reported.
func TestDeferHoldsNotices(t *testing.T) {
	e, c := newEng()
	e.Defer()
	h := printer(true, 50, door)
	if n := firstPass(e, h); len(n) != 0 {
		t.Fatalf("deferred: %v", n)
	}
	bad := report(model.HealthFail)
	e.ObserveHealth(bad)
	e.ObserveHealth(bad)
	n := e.ObserveHealth(report(model.HealthOK))
	n = append(n, e.ObserveHealth(report(model.HealthOK))...)
	if len(n) != 0 {
		t.Fatalf("deferred health: %v", n)
	}
	c.t = c.t.Add(time.Second)
	n = e.Reconcile([]model.Host{h}, nil, &model.HealthReport{})
	if len(n) != 1 || n[0].Trigger != model.TriggerPrinterAlert || n[0].OK {
		t.Fatalf("released: %v", n)
	}
	if n := e.Observe(h, printer(true, 50)); len(n) != 1 || !n[0].OK {
		t.Fatalf("after reconcile: %v", n)
	}
}

// Release ends the deferred pass without checking the seeded problems.
func TestRelease(t *testing.T) {
	seed := lastRun(t)
	e, _ := newEng()
	e.Defer()
	firstPass(e, printer(true, 50))
	e.Seed(seed)
	n := e.Release()
	if len(n) != 0 || len(e.Open()) != 1 || e.Open()[0].Key != seed[0].Key {
		t.Fatalf("release: %v, open %v", n, e.Open())
	}
	e.Defer()
	firstPass(e, printer(true, 50, door, off), model.Host{IP: "10.0.0.6", MAC: "AA:00:00:00:00:06", IsPrinter: true, Online: true,
		Printer: &model.PrinterInfo{Alerts: []model.Alert{door}}})
	n = e.Release()
	if len(n) != 1 || n[0].IP != "10.0.0.6" {
		t.Fatalf("second release: %v", n)
	}
}

// A seeded low supply closes when the first pass reads it above the
// threshold plus the hysteresis, or no longer listed; it stays open while
// low or when the printer listed no supplies.
func TestReconcileSupply(t *testing.T) {
	low := func(t *testing.T) []OpenFault {
		e, c := newEng()
		e.Observe(printer(true, 50), printer(true, 50))
		c.t = c.t.Add(time.Second)
		if n := e.Observe(printer(true, 50), printer(true, 10)); len(n) != 1 || n[0].Trigger != model.TriggerSupplyLow {
			t.Fatalf("low: %v", n)
		}
		return e.Open()
	}
	for _, tc := range []struct {
		name string
		h    model.Host
		ok   bool
	}{
		{"refilled", printer(true, 80), true},
		{"just above", printer(true, 21), true},
		{"still low", printer(true, 18), false},
		{"other supply", supplied(model.Supply{Description: "Cyan", Percent: 50}), true},
		{"no supplies read", supplied(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newEng()
			e.Defer()
			firstPass(e, tc.h)
			e.Seed(low(t))
			n := e.Reconcile([]model.Host{tc.h}, nil, nil)
			if got := len(n) == 1 && n[0].OK && n[0].Title == "Supply OK again"; got != tc.ok || (!tc.ok && len(n) != 0) {
				t.Fatalf("notices: %v", n)
			}
		})
	}
}

// The storm guard's problem: one notice when it opens, one OK when it
// closes, whatever the trigger switches say.
func TestLinkNotices(t *testing.T) {
	e, _ := newEng()
	cfg := DefaultConfig()
	clear(cfg.Triggers)
	e.SetConfig(cfg)
	n := e.LinkLost("n-0123456789ab", "netdiag lost the network", "x")
	if len(n) != 1 || n[0].Key != "netdiagLink||n-0123456789ab" || n[0].Severity != "warn" || n[0].Trigger != model.TriggerNetdiagLink {
		t.Fatalf("lost: %v", n)
	}
	if n := e.LinkLost("n-0123456789ab", "netdiag lost the network", "x"); len(n) != 0 {
		t.Fatalf("again: %v", n)
	}
	n = e.LinkBack("n-0123456789ab")
	if len(n) != 1 || !n[0].OK || n[0].Key != "netdiagLink||n-0123456789ab" || n[0].Title != "netdiag is back on the network" {
		t.Fatalf("back: %v", n)
	}
	if n := e.LinkBack("n-0123456789ab"); len(n) != 0 {
		t.Fatalf("back again: %v", n)
	}
}
