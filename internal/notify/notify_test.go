package notify

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"netdiag/internal/model"
)

type clk struct{ t time.Time }

func (c *clk) now() time.Time { return c.t }

func newEng() (*Engine, *clk) {
	c := &clk{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return New(DefaultConfig(), c.now), c
}

const prMAC = "3C:52:82:11:22:33"

func printer(online bool, pct int, alerts ...model.Alert) model.Host {
	return model.Host{IP: "10.0.0.5", MAC: prMAC, IsPrinter: true, Online: online,
		Printer: &model.PrinterInfo{
			Supplies: []model.Supply{{Description: "Black", Percent: pct}},
			Alerts:   alerts,
		}}
}

// check applies one presence check to h the way the scan's presenceUpdate
// does (15 s later on the clock) and observes the transition.
func check(e *Engine, c *clk, h *model.Host, answered bool) []model.Notice {
	prev := *h
	c.t = c.t.Add(15 * time.Second)
	if answered {
		h.LastSeen = c.t.UTC().Format(time.RFC3339)
		h.MissedChecks = 0
		h.Online = true
	} else {
		h.MissedChecks++
		if h.MissedChecks >= model.OfflineAfterMisses {
			h.Online = false
		}
	}
	return e.Observe(prev, *h)
}

// checks runs one presence check per answer and returns every notice.
func checks(e *Engine, c *clk, h *model.Host, answers ...bool) []model.Notice {
	var out []model.Notice
	for _, a := range answers {
		out = append(out, check(e, c, h, a)...)
	}
	return out
}

func onlinePrinter() model.Host {
	h := printer(true, 50)
	h.LastSeen = "2026-01-01T00:00:00Z"
	return h
}

// Acceptance: offline, then two good checks, gives one OK with the fault's key
// and OpenedAt.
func TestOfflineThenOnlineOK(t *testing.T) {
	e, c := newEng()
	h := onlinePrinter()
	n := checks(e, c, &h, false, false)
	if len(n) != 1 || n[0].Trigger != model.TriggerPrinterOffline || n[0].OK || n[0].ID != "n-1" {
		t.Fatalf("fault: %v", n)
	}
	fault := n[0]
	if fault.Key != "printerOffline|"+prMAC+"|" || fault.MAC != prMAC || fault.Severity != "warn" {
		t.Fatalf("fault fields: %+v", fault)
	}
	if got := e.Open(); len(got) != 1 || got[0].Key != fault.Key {
		t.Fatalf("open: %v", got)
	}
	// One good check is not enough (M=2).
	if n := checks(e, c, &h, true); len(n) != 0 {
		t.Fatalf("first good check: %v", n)
	}
	n = checks(e, c, &h, true)
	if len(n) != 1 || !n[0].OK || n[0].Key != fault.Key || n[0].OpenedAt != fault.At ||
		n[0].Title != "Printer back online" || n[0].Trigger != model.TriggerPrinterOffline || n[0].MAC != prMAC {
		t.Fatalf("ok: %+v (fault %+v)", n, fault)
	}
	if len(e.Open()) != 0 {
		t.Fatalf("still open: %v", e.Open())
	}
	// Nothing more while it stays online.
	if n := checks(e, c, &h, true, true); len(n) != 0 {
		t.Fatalf("after ok: %v", n)
	}
}

// A miss between the good checks starts the count over, and so does going
// offline again; neither reports the fault a second time.
func TestOnlineOKNeedsChecksInARow(t *testing.T) {
	e, c := newEng()
	h := onlinePrinter()
	if n := checks(e, c, &h, false, false); len(n) != 1 {
		t.Fatalf("fault: %v", n)
	}
	if n := checks(e, c, &h, true, false, true); len(n) != 0 {
		t.Fatalf("good, miss, good: %v", n)
	}
	if n := checks(e, c, &h, false, false, true); len(n) != 0 {
		t.Fatalf("offline again: %v", n)
	}
	n := checks(e, c, &h, true)
	if len(n) != 1 || !n[0].OK {
		t.Fatalf("two in a row: %v", n)
	}
	// Other updates (a printer poll, an mDNS record) leave LastSeen alone
	// and never count as a good check.
	e2, c2 := newEng()
	h2 := onlinePrinter()
	checks(e2, c2, &h2, false, false, true)
	for i := 0; i < 3; i++ {
		if n := e2.Observe(h2, h2); len(n) != 0 {
			t.Fatalf("same LastSeen: %v", n)
		}
	}
}

// Acceptance: no OK for a key that was gated (trigger off), held, or kept
// back by the cooldown, so nothing is fixed that nobody was told about.
func TestNoOKWithoutReportedFault(t *testing.T) {
	t.Run("gated", func(t *testing.T) {
		e, c := newEng()
		h := model.Host{IP: "10.0.0.9", Online: true, LastSeen: "2026-01-01T00:00:00Z"} // deviceOffline is off
		if n := checks(e, c, &h, false, false, true, true, true); len(n) != 0 {
			t.Fatalf("%v", n)
		}
	})
	t.Run("cooldown", func(t *testing.T) {
		e, c := newEng()
		h := onlinePrinter()
		e.last[Key(model.TriggerPrinterOffline, h, "")] = c.t // reported moments ago
		if n := checks(e, c, &h, false, false, true, true, true); len(n) != 0 {
			t.Fatalf("%v", n)
		}
	})
	t.Run("held", func(t *testing.T) {
		e, c := newEng()
		h := onlinePrinter()
		e.SetHold(true)
		if n := checks(e, c, &h, false, false); len(n) != 0 {
			t.Fatalf("held fault: %v", n)
		}
		e.SetHold(false)
		if n := checks(e, c, &h, true, true, true); len(n) != 0 {
			t.Fatalf("ok after held fault: %v", n)
		}
	})
	t.Run("supply and alert gated", func(t *testing.T) {
		e, _ := newEng()
		cfg := DefaultConfig()
		cfg.Triggers[model.TriggerSupplyLow] = false
		cfg.Triggers[model.TriggerPrinterAlert] = false
		e.SetConfig(cfg)
		jam := model.Alert{Severity: "critical", Code: "jam", Description: "Paper jam"}
		if n := e.Observe(printer(true, 40), printer(true, 10, jam)); len(n) != 0 {
			t.Fatalf("gated faults: %v", n)
		}
		e.SetConfig(DefaultConfig())
		if n := e.Observe(printer(true, 10, jam), printer(true, 90)); len(n) != 0 {
			t.Fatalf("ok for gated faults: %v", n)
		}
	})
}

// An OK closes the problem even with its trigger switched off and inside the
// cooldown, and forgets the cooldown so a relapse is reported at once.
func TestOKBypassesGateAndCooldown(t *testing.T) {
	e, c := newEng()
	h := onlinePrinter()
	checks(e, c, &h, false, false)
	cfg := DefaultConfig()
	cfg.Triggers[model.TriggerPrinterOffline] = false
	e.SetConfig(cfg)
	n := checks(e, c, &h, true, true) // 30 s later: inside the 300 s cooldown
	if len(n) != 1 || !n[0].OK {
		t.Fatalf("ok with trigger off: %v", n)
	}
	e.SetConfig(DefaultConfig())
	if _, ok := e.last[n[0].Key]; ok {
		t.Fatal("cooldown kept after the OK")
	}
	if n := checks(e, c, &h, false, false); len(n) != 1 || n[0].OK {
		t.Fatalf("relapse inside cooldown: %v", n)
	}
}

// While a problem is open the same fault is never reported again.
func TestOpenProblemNotRepeated(t *testing.T) {
	e, c := newEng()
	n := e.Observe(printer(true, 50), printer(false, 50))
	if len(n) != 1 || n[0].Trigger != model.TriggerPrinterOffline {
		t.Fatalf("%v", n)
	}
	c.t = c.t.Add(time.Hour) // well past the cooldown
	if n := e.Observe(printer(true, 50), printer(false, 50)); len(n) != 0 {
		t.Fatalf("repeat while open: %v", n)
	}
	// no transition, no notice
	if n := e.Observe(printer(false, 50), printer(false, 50)); len(n) != 0 {
		t.Fatalf("%v", n)
	}
}

func TestDeviceOfflineDisabledThenEnabled(t *testing.T) {
	e, _ := newEng()
	p := model.Host{IP: "10.0.0.9", Online: true}
	q := model.Host{IP: "10.0.0.9", Online: false}
	if n := e.Observe(p, q); len(n) != 0 {
		t.Fatalf("disabled: %v", n)
	}
	cfg := DefaultConfig()
	cfg.Triggers[model.TriggerDeviceOffline] = true
	e.SetConfig(cfg)
	n := e.Observe(p, q)
	if len(n) != 1 || n[0].Trigger != model.TriggerDeviceOffline || n[0].Severity != "info" ||
		n[0].Key != "deviceOffline|ip:10.0.0.9|" {
		t.Fatalf("%v", n)
	}
}

// A problem opened under the IP key while the MAC was unknown still closes
// after the MAC is learned, and is not reported again under the MAC key.
func TestMACKeyWithIPFallback(t *testing.T) {
	e, c := newEng()
	h := onlinePrinter()
	h.MAC = ""
	n := checks(e, c, &h, false, false)
	if len(n) != 1 || n[0].Key != "printerOffline|ip:10.0.0.5|" {
		t.Fatalf("ip key: %v", n)
	}
	h.MAC = strings.ToLower(prMAC)
	if Key(model.TriggerPrinterOffline, h, "") != "printerOffline|"+prMAC+"|" {
		t.Fatalf("mac key: %s", Key(model.TriggerPrinterOffline, h, ""))
	}
	if n := e.Observe(h, h); len(n) != 0 {
		t.Fatalf("re-keyed: %v", n)
	}
	n = checks(e, c, &h, true, true)
	if len(n) != 1 || !n[0].OK || n[0].Key != "printerOffline|ip:10.0.0.5|" {
		t.Fatalf("ok under the ip key: %v", n)
	}
}

func TestSupplyCrossing(t *testing.T) {
	e, _ := newEng()
	if n := e.Observe(printer(true, 40), printer(true, 30)); len(n) != 0 {
		t.Fatalf("above threshold: %v", n)
	}
	n := e.Observe(printer(true, 20), printer(true, 10))
	if len(n) != 1 || n[0].Trigger != model.TriggerSupplyLow || n[0].Severity != "warn" ||
		n[0].Key != "supplyLow|"+prMAC+"|"+shortHash("Black") {
		t.Fatalf("cross down: %v", n)
	}
	e2, _ := newEng()
	if n := e2.Observe(printer(true, 10), printer(true, 5)); len(n) != 0 {
		t.Fatalf("already below: %v", n)
	}
	if n := e2.Observe(printer(true, 5), printer(true, 90)); len(n) != 0 {
		t.Fatalf("upward without a fault: %v", n)
	}
}

// Acceptance: with threshold 15, a supply at 15, 18, 21 and 20 gives exactly
// one OK, at 21 (more than 5 above the threshold), and no new fault.
func TestSupplyHysteresis(t *testing.T) {
	e, _ := newEng()
	if n := e.Observe(printer(true, 40), printer(true, 15)); len(n) != 1 || n[0].OK {
		t.Fatalf("fault: %v", n)
	}
	levels := []int{15, 18, 21, 20}
	var oks []int
	for i := 1; i < len(levels); i++ {
		for _, n := range e.Observe(printer(true, levels[i-1]), printer(true, levels[i])) {
			if !n.OK {
				t.Fatalf("fault at %d: %v", levels[i], n)
			}
			oks = append(oks, levels[i])
			if n.Title != "Supply OK again" || n.Key != "supplyLow|"+prMAC+"|"+shortHash("Black") {
				t.Fatalf("ok: %+v", n)
			}
		}
	}
	if len(oks) != 1 || oks[0] != 21 {
		t.Fatalf("oks at %v, want [21]", oks)
	}
	// Back down to the threshold is a new problem.
	if n := e.Observe(printer(true, 20), printer(true, 14)); len(n) != 1 || n[0].OK {
		t.Fatalf("relapse: %v", n)
	}
}

func TestPrinterAlertNewOnly(t *testing.T) {
	e, _ := newEng()
	a := model.Alert{Severity: "critical", Code: "3", Description: "Paper jam"}
	n := e.Observe(printer(true, 50), printer(true, 50, a))
	if len(n) != 1 || n[0].Trigger != model.TriggerPrinterAlert || n[0].Severity != "critical" {
		t.Fatalf("%v", n)
	}
	if n := e.Observe(printer(true, 50, a), printer(true, 50, a)); len(n) != 0 {
		t.Fatalf("existing alert: %v", n)
	}
	w := model.Alert{Severity: "warning", Code: "4", Description: "Toner low"}
	if n := e.Observe(printer(true, 50, a), printer(true, 50, a, w)); len(n) != 1 || n[0].Severity != "warn" {
		t.Fatalf("warning alert: %v", n)
	}
}

func TestPrinterAlertClearedAndRelapse(t *testing.T) {
	e, c := newEng()
	door := model.Alert{Severity: "critical", Code: "hrPrinterDetectedErrorState.doorOpen", Description: "Door or cover open"}
	raised := e.Observe(printer(true, 50), printer(true, 50, door))
	if len(raised) != 1 {
		t.Fatalf("raise: %v", raised)
	}
	c.t = c.t.Add(30 * time.Second)
	n := e.Observe(printer(true, 50, door), printer(true, 50))
	if len(n) != 1 || !n[0].OK || n[0].Title != "Printer alert cleared" || n[0].Key != raised[0].Key ||
		n[0].OpenedAt != raised[0].At || !strings.Contains(n[0].Message, "Door or cover open") {
		t.Fatalf("cleared: %v", n)
	}
	// Relapse inside the cooldown window is still reported.
	c.t = c.t.Add(30 * time.Second)
	if n := e.Observe(printer(true, 50), printer(true, 50, door)); len(n) != 1 || n[0].Severity != "critical" || n[0].OK {
		t.Fatalf("relapse: %v", n)
	}
	// No notice when nothing was raised before.
	e2, _ := newEng()
	if n := e2.Observe(printer(true, 50), printer(true, 50)); len(n) != 0 {
		t.Fatalf("quiet: %v", n)
	}
}

// A real Zebra ZD621 clears its alerts one at a time when the cover closes:
// the door alert goes first, then offline. Each alert closes with its own OK.
func TestPrinterAlertsClearedInSteps(t *testing.T) {
	e, c := newEng()
	door := model.Alert{Severity: "critical", Code: "hrPrinterDetectedErrorState.doorOpen", Description: "Door or cover open"}
	off := model.Alert{Severity: "critical", Code: "hrPrinterDetectedErrorState.offline", Description: "Printer offline or paused"}
	raised := e.Observe(printer(true, 50), printer(true, 50, door, off))
	if len(raised) != 2 || raised[0].Key == raised[1].Key {
		t.Fatalf("raise: %v", raised)
	}
	c.t = c.t.Add(20 * time.Second)
	n := e.Observe(printer(true, 50, door, off), printer(true, 50, off))
	if len(n) != 1 || !n[0].OK || n[0].Key != raised[0].Key || !strings.HasSuffix(n[0].Message, "cleared Door or cover open.") {
		t.Fatalf("door cleared: %v", n)
	}
	c.t = c.t.Add(20 * time.Second)
	n = e.Observe(printer(true, 50, off), printer(true, 50))
	if len(n) != 1 || !n[0].OK || n[0].Key != raised[1].Key || !strings.HasSuffix(n[0].Message, "cleared Printer offline or paused.") {
		t.Fatalf("offline cleared: %v", n)
	}
	if len(e.Open()) != 0 {
		t.Fatalf("open: %v", e.Open())
	}
}

func TestNewDevice(t *testing.T) {
	e, _ := newEng()
	if n := e.Observe(model.Host{}, model.Host{IP: "10.0.0.7", IsNew: true, Online: true}); len(n) != 1 || n[0].Trigger != model.TriggerNewDevice || n[0].Severity != "info" {
		t.Fatalf("%v", n)
	}
	if len(e.Open()) != 0 {
		t.Fatalf("a new device is not a problem: %v", e.Open())
	}
	if n := e.Observe(model.Host{}, model.Host{IP: "10.0.0.8", Online: true}); len(n) != 0 {
		t.Fatalf("initial scan host: %v", n)
	}
	cfg := DefaultConfig()
	cfg.Triggers[model.TriggerNewDevice] = false
	e.SetConfig(cfg)
	if n := e.Observe(model.Host{}, model.Host{IP: "10.0.0.9", IsNew: true}); len(n) != 0 {
		t.Fatalf("disabled: %v", n)
	}
}

func multiIP(ips ...string) model.Conflict {
	return model.Conflict{ID: "c-" + strings.Join(ips, ""), Kind: model.ConflictMACMultiIP, Severity: "warn",
		IPs: ips, MACs: []string{"aa:bb:cc:00:00:01"}, Detail: "x"}
}

func TestConflictLifecycle(t *testing.T) {
	e, _ := newEng()
	c := multiIP("10.0.0.5", "10.0.0.6")
	n := e.ObserveConflicts([]model.Conflict{c})
	if len(n) != 1 || n[0].Trigger != model.TriggerIPConflict || n[0].IP != "10.0.0.5" ||
		n[0].Key != "ipConflict|mac_multi_ip|AA:BB:CC:00:00:01" || n[0].Severity != "warn" {
		t.Fatalf("raise: %v", n)
	}
	if n := e.ObserveConflicts([]model.Conflict{c}); len(n) != 0 {
		t.Fatalf("still there: %v", n)
	}
	// The conflict gains an IP: a new id, the same key, no notice at all.
	c2 := multiIP("10.0.0.5", "10.0.0.6", "10.0.0.7")
	if ConflictKey(c2) != ConflictKey(c) || c2.ID == c.ID {
		t.Fatal("anchor changed with the ip set")
	}
	if n := e.ObserveConflicts([]model.Conflict{c2}); len(n) != 0 {
		t.Fatalf("id changed: %v", n)
	}
	// Gone once is not enough; back again resets the count.
	if n := e.ObserveConflicts(nil); len(n) != 0 {
		t.Fatalf("gone once: %v", n)
	}
	if n := e.ObserveConflicts([]model.Conflict{c2}); len(n) != 0 {
		t.Fatalf("back: %v", n)
	}
	if n := e.ObserveConflicts(nil); len(n) != 0 {
		t.Fatalf("gone once again: %v", n)
	}
	n = e.ObserveConflicts(nil)
	if len(n) != 1 || !n[0].OK || n[0].Key != ConflictKey(c) || n[0].Title != "Network conflict cleared" {
		t.Fatalf("gone twice: %v", n)
	}
	if n := e.ObserveConflicts(nil); len(n) != 0 {
		t.Fatalf("after ok: %v", n)
	}
}

func TestConflictKeyFormats(t *testing.T) {
	flap := model.Conflict{ID: "c-1", Kind: model.ConflictMACFlap, Severity: "fail", IPs: []string{"192.168.1.50"},
		MACs: []string{"24:6F:28:AA:BB:CC", "3C:52:82:11:22:33"}}
	name := model.Conflict{ID: "c-2", Kind: model.ConflictDuplicateName, Severity: "warn", Name: "frontdesk",
		IPs: []string{"192.168.1.20", "192.168.1.21"}}
	for _, tc := range []struct {
		c    model.Conflict
		want string
	}{
		{flap, "ipConflict|mac_flap|192.168.1.50"},
		{multiIP("10.0.0.5", "10.0.0.6"), "ipConflict|mac_multi_ip|AA:BB:CC:00:00:01"},
		{name, "ipConflict|duplicate_name|" + shortHash("frontdesk")},
	} {
		if got := ConflictKey(tc.c); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.c.Kind, got, tc.want)
		}
	}
	// A flap that picks up a third MAC keeps its key.
	more := flap
	more.MACs = append([]string{"00:11:22:33:44:55"}, flap.MACs...)
	if ConflictKey(more) != ConflictKey(flap) {
		t.Fatal("flap key changed with the mac set")
	}
	if len(shortHash("frontdesk")) != 8 {
		t.Fatalf("short hash %q", shortHash("frontdesk"))
	}
}

// Acceptance: a printer alert with a 250-character description has a key of
// at most 200 characters, and the same alert always gets the same key.
func TestLongKeysHashed(t *testing.T) {
	desc := strings.Repeat("d", 250)
	h := printer(true, 50)
	a := model.Alert{Severity: "warning", Code: "hrPrinterDetectedErrorState.serviceRequested", Description: desc}
	k := Key(model.TriggerPrinterAlert, h, alertPart(a))
	if len(k) > MaxKeyLen || k != Key(model.TriggerPrinterAlert, h, alertPart(a)) {
		t.Fatalf("%d %q", len(k), k)
	}
	e, _ := newEng()
	n := e.Observe(printer(true, 50), printer(true, 50, a))
	if len(n) != 1 || n[0].Key != k {
		t.Fatalf("notice key: %v", n)
	}
	// A key over the limit is hashed but keeps its trigger and device.
	long := model.Alert{Code: strings.Repeat("c", 250), Description: desc}
	k1 := Key(model.TriggerPrinterAlert, h, alertPart(long))
	k2 := Key(model.TriggerPrinterAlert, h, alertPart(long))
	if len(k1) > MaxKeyLen || k1 != k2 || !strings.HasPrefix(k1, "printerAlert|"+prMAC+"|h:") || len(k1) != len("printerAlert|"+prMAC+"|h:")+32 {
		t.Fatalf("hashed key %q", k1)
	}
	other := model.Alert{Code: strings.Repeat("c", 249) + "x", Description: desc}
	if Key(model.TriggerPrinterAlert, h, alertPart(other)) == k1 {
		t.Fatal("different alerts share a hashed key")
	}
	// A long hashed alert still closes.
	n = e.Observe(printer(true, 50, a), printer(true, 50, long))
	if len(n) != 2 || n[0].OK || n[0].Key != k1 || !n[1].OK || n[1].Key != k {
		t.Fatalf("swap alerts: %v", n)
	}
	if n := e.Observe(printer(true, 50, long), printer(true, 50)); len(n) != 1 || !n[0].OK || n[0].Key != k1 {
		t.Fatalf("hashed ok: %v", n)
	}
	if got := HealthKey("dns"); got != "healthFailed||dns" {
		t.Fatalf("health key %q", got)
	}
}

func report(status string) model.HealthReport {
	return model.HealthReport{Checks: []model.HealthCheck{{ID: "dns", Label: "DNS", Status: status}}}
}

// Health: two failed runs in a row before the fault, two good runs in a row
// before the OK (N=2, M=2).
func TestHealthDebounce(t *testing.T) {
	e, _ := newEng()
	ok, bad, warn := report(model.HealthOK), report(model.HealthFail), report(model.HealthWarn)
	if n := e.ObserveHealth(ok); len(n) != 0 {
		t.Fatal("ok")
	}
	if n := e.ObserveHealth(bad); len(n) != 0 {
		t.Fatalf("one failed run: %v", n)
	}
	if n := e.ObserveHealth(ok); len(n) != 0 {
		t.Fatalf("recovered before the second: %v", n)
	}
	if n := e.ObserveHealth(bad); len(n) != 0 {
		t.Fatalf("failed runs not in a row: %v", n)
	}
	n := e.ObserveHealth(bad)
	if len(n) != 1 || n[0].Trigger != model.TriggerHealthFailed || n[0].Key != "healthFailed||dns" || n[0].Severity != "warn" {
		t.Fatalf("two failed runs: %v", n)
	}
	fault := n[0]
	if n := e.ObserveHealth(bad); len(n) != 0 {
		t.Fatalf("still failing: %v", n)
	}
	if n := e.ObserveHealth(warn); len(n) != 0 {
		t.Fatalf("one good run: %v", n)
	}
	if n := e.ObserveHealth(bad); len(n) != 0 {
		t.Fatalf("failing again: %v", n)
	}
	if n := e.ObserveHealth(warn); len(n) != 0 {
		t.Fatalf("good runs not in a row: %v", n)
	}
	n = e.ObserveHealth(warn) // warn is not fail: it counts as good
	if len(n) != 1 || !n[0].OK || n[0].Key != fault.Key || n[0].OpenedAt != fault.At || n[0].Title != "Health check OK again" {
		t.Fatalf("two good runs: %v", n)
	}
	// A missing check keeps its state.
	if n := e.ObserveHealth(model.HealthReport{}); len(n) != 0 {
		t.Fatal("empty report")
	}
	cfg := DefaultConfig()
	cfg.Triggers[model.TriggerHealthFailed] = false
	e.SetConfig(cfg)
	if n := append(e.ObserveHealth(bad), e.ObserveHealth(bad)...); len(n) != 0 {
		t.Fatal("disabled")
	}
}

// SetHold drops presence and health faults and records nothing: once the
// hold is off, a failure needs its full count again and nothing is fixed.
func TestSetHold(t *testing.T) {
	e, c := newEng()
	e.SetHold(true)
	bad := report(model.HealthFail)
	h := onlinePrinter()
	var n []model.Notice
	n = append(n, e.ObserveHealth(bad)...)
	n = append(n, e.ObserveHealth(bad)...)
	n = append(n, checks(e, c, &h, false, false)...)
	if len(n) != 0 || len(e.Open()) != 0 || len(e.last) != 0 || len(e.bad) != 0 {
		t.Fatalf("held: %v open %v last %v bad %v", n, e.Open(), e.last, e.bad)
	}
	// Printer alerts and conflicts are not presence or health: they still go out.
	jam := model.Alert{Severity: "critical", Code: "jam", Description: "Paper jam"}
	if n := e.Observe(printer(true, 50), printer(true, 50, jam)); len(n) != 1 {
		t.Fatalf("alert while held: %v", n)
	}
	e.SetHold(false)
	if n := e.ObserveHealth(bad); len(n) != 0 {
		t.Fatalf("first run after the hold: %v", n)
	}
	if n := e.ObserveHealth(bad); len(n) != 1 {
		t.Fatalf("second run after the hold: %v", n)
	}
	// An open problem still closes while held.
	e.SetHold(true)
	good := report(model.HealthOK)
	if n := append(e.ObserveHealth(good), e.ObserveHealth(good)...); len(n) != 1 || !n[0].OK {
		t.Fatalf("ok while held: %v", n)
	}
}

// The one severity map (v0.6 §4.7, README §8.2), for banners and chat.
func TestSeverityMap(t *testing.T) {
	for _, tc := range []struct {
		trigger string
		ctx     sevCtx
		want    string
	}{
		{model.TriggerPrinterOffline, sevCtx{}, "warn"},
		{model.TriggerDeviceOffline, sevCtx{}, "info"},
		{model.TriggerSupplyLow, sevCtx{}, "warn"},
		{model.TriggerPrinterAlert, sevCtx{printer: "critical"}, "critical"},
		{model.TriggerPrinterAlert, sevCtx{printer: "error"}, "critical"},
		{model.TriggerPrinterAlert, sevCtx{printer: "warning"}, "warn"},
		{model.TriggerPrinterAlert, sevCtx{}, "warn"},
		{model.TriggerIPConflict, sevCtx{kind: model.ConflictMACFlap, swap: true}, "critical"},
		{model.TriggerIPConflict, sevCtx{kind: model.ConflictMACFlap}, "warn"},
		{model.TriggerIPConflict, sevCtx{kind: model.ConflictMACMultiIP, swap: true}, "warn"},
		{model.TriggerIPConflict, sevCtx{kind: model.ConflictDuplicateName}, "warn"},
		{model.TriggerHealthFailed, sevCtx{check: "gateway"}, "critical"},
		{model.TriggerHealthFailed, sevCtx{check: "dhcp"}, "warn"},
		{model.TriggerHealthFailed, sevCtx{check: "dns"}, "warn"},
		{model.TriggerHealthFailed, sevCtx{check: "webserver"}, "warn"},
		{model.TriggerHealthFailed, sevCtx{check: "wifi"}, "warn"},
		{model.TriggerNewDevice, sevCtx{}, "info"},
		{model.TriggerNetdiagLink, sevCtx{}, "warn"},
		{model.TriggerChatTest, sevCtx{}, "info"},
	} {
		if got := severityFor(tc.trigger, tc.ctx); got != tc.want {
			t.Errorf("%s %+v: %s, want %s", tc.trigger, tc.ctx, got, tc.want)
		}
	}
	// The notices carry it: a failed gateway is critical, a mac_flap conflict
	// (always a seen swap) too.
	e, _ := newEng()
	gw := model.HealthReport{Checks: []model.HealthCheck{{ID: "gateway", Label: "Gateway", Status: model.HealthFail}}}
	e.ObserveHealth(gw)
	if n := e.ObserveHealth(gw); len(n) != 1 || n[0].Severity != "critical" {
		t.Fatalf("gateway: %v", n)
	}
	flap := model.Conflict{ID: "c-1", Kind: model.ConflictMACFlap, Severity: "fail", IPs: []string{"10.0.0.5"}}
	if n := e.ObserveConflicts([]model.Conflict{flap}); len(n) != 1 || n[0].Severity != "critical" {
		t.Fatalf("flap: %v", n)
	}
}

// Notices name the device the best way netdiag can.
func TestNoticeName(t *testing.T) {
	h := printer(true, 50)
	h.Hostname = "zd621"
	h.Snmp = &model.SnmpInfo{SysName: "ZD621-Front"}
	h.MDNS = &model.MDNSInfo{Instance: "Front desk labels"}
	for _, tc := range []struct {
		mut  func(*model.Host)
		want string
	}{
		{func(*model.Host) {}, "Front desk labels"},
		{func(h *model.Host) { h.MDNS = nil }, "ZD621-Front"},
		{func(h *model.Host) { h.MDNS, h.Snmp = nil, nil }, "zd621"},
		{func(h *model.Host) { h.MDNS, h.Snmp, h.Hostname = nil, nil, ""; h.Printer.Model = "ZD621" }, "ZD621"},
		{func(h *model.Host) { h.MDNS, h.Snmp, h.Hostname = nil, nil, "" }, ""},
	} {
		x := h
		p := *h.Printer
		x.Printer = &p
		tc.mut(&x)
		if got := bestName(x); got != tc.want {
			t.Errorf("bestName = %q, want %q", got, tc.want)
		}
	}
	e, _ := newEng()
	n := e.Observe(h, func() model.Host { x := h; x.Online = false; return x }())
	if len(n) != 1 || n[0].Name != "Front desk labels" || n[0].MAC != prMAC || n[0].IP != "10.0.0.5" {
		t.Fatalf("%+v", n)
	}
}

// The cooldown map forgets keys once their cooldown has passed, so a long
// live scan does not keep one entry for every device it ever saw.
func TestCooldownMapIsPruned(t *testing.T) {
	e, c := newEng()
	newDev := func(ip string) []model.Notice {
		return e.Observe(model.Host{}, model.Host{IP: ip, IsNew: true})
	}
	for i := 0; i < 100; i++ {
		if n := newDev(fmt.Sprintf("10.0.1.%d", i)); len(n) != 1 {
			t.Fatalf("device %d: %v", i, n)
		}
	}
	if len(e.last) != 100 {
		t.Fatalf("entries = %d", len(e.last))
	}

	c.t = c.t.Add(200 * time.Second)
	newDev("10.0.2.1") // inside the first cooldown: no sweep yet
	if len(e.last) != 101 {
		t.Fatalf("entries before cooldown = %d", len(e.last))
	}

	c.t = c.t.Add(101 * time.Second) // 301 s after the first 100
	newDev("10.0.2.2")
	if len(e.last) != 2 {
		t.Fatalf("entries after cooldown = %d, want 2 (10.0.2.1 and 10.0.2.2)", len(e.last))
	}
	// The key still inside its cooldown still holds back a repeat.
	if n := newDev("10.0.2.1"); len(n) != 0 {
		t.Fatalf("repeat inside cooldown: %v", n)
	}
	// A pruned key notifies again.
	if n := newDev("10.0.1.5"); len(n) != 1 {
		t.Fatalf("pruned key: %v", n)
	}

	// With no cooldown nothing needs remembering.
	cfg := DefaultConfig()
	cfg.Cooldown = 0
	e.SetConfig(cfg)
	for i := 0; i < 10; i++ {
		c.t = c.t.Add(time.Second)
		newDev(fmt.Sprintf("10.0.3.%d", i))
	}
	if len(e.last) > 1 {
		t.Fatalf("entries with no cooldown = %d", len(e.last))
	}
}
