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

func printer(online bool, pct int, alerts ...model.Alert) model.Host {
	return model.Host{IP: "10.0.0.5", IsPrinter: true, Online: online,
		Printer: &model.PrinterInfo{
			Supplies: []model.Supply{{Description: "Black", Percent: pct}},
			Alerts:   alerts,
		}}
}

func TestPrinterOfflineAndDedupe(t *testing.T) {
	e, c := newEng()
	n := e.Observe(printer(true, 50), printer(false, 50))
	if len(n) != 1 || n[0].Trigger != model.TriggerPrinterOffline || n[0].ID != "n-1" {
		t.Fatalf("%v", n)
	}
	// repeated transition within cooldown is suppressed
	if n := e.Observe(printer(true, 50), printer(false, 50)); len(n) != 0 {
		t.Fatalf("dedupe: %v", n)
	}
	c.t = c.t.Add(301 * time.Second)
	if n := e.Observe(printer(true, 50), printer(false, 50)); len(n) != 1 {
		t.Fatalf("cooldown expiry: %v", n)
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
	if n := e.Observe(p, q); len(n) != 1 || n[0].Trigger != model.TriggerDeviceOffline {
		t.Fatalf("%v", n)
	}
}

func TestSupplyCrossing(t *testing.T) {
	e, _ := newEng()
	if n := e.Observe(printer(true, 40), printer(true, 30)); len(n) != 0 {
		t.Fatalf("above threshold: %v", n)
	}
	if n := e.Observe(printer(true, 20), printer(true, 10)); len(n) != 1 || n[0].Trigger != model.TriggerSupplyLow {
		t.Fatalf("cross down: %v", n)
	}
	e2, _ := newEng()
	if n := e2.Observe(printer(true, 10), printer(true, 5)); len(n) != 0 {
		t.Fatalf("already below: %v", n)
	}
	if n := e2.Observe(printer(true, 5), printer(true, 90)); len(n) != 0 {
		t.Fatalf("upward: %v", n)
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
}

func TestPrinterAlertClearedAndRelapse(t *testing.T) {
	e, c := newEng()
	door := model.Alert{Severity: "critical", Code: "hrPrinterDetectedErrorState.doorOpen", Description: "Door or cover open"}
	if n := e.Observe(printer(true, 50), printer(true, 50, door)); len(n) != 1 {
		t.Fatalf("raise: %v", n)
	}
	c.t = c.t.Add(30 * time.Second)
	n := e.Observe(printer(true, 50, door), printer(true, 50))
	if len(n) != 1 || n[0].Title != "Printer OK again" || n[0].Severity != "info" ||
		!strings.Contains(n[0].Message, "Door or cover open") {
		t.Fatalf("cleared: %v", n)
	}
	// Relapse inside the cooldown window is still reported.
	c.t = c.t.Add(30 * time.Second)
	if n := e.Observe(printer(true, 50), printer(true, 50, door)); len(n) != 1 || n[0].Severity != "critical" {
		t.Fatalf("relapse: %v", n)
	}
	// No notice when nothing was raised before.
	if n := e.Observe(printer(true, 50), printer(true, 50)); len(n) != 0 {
		t.Fatalf("quiet: %v", n)
	}
}

// A real Zebra ZD621 clears its alerts one at a time when the cover closes:
// the door alert goes first, then offline. Printer OK again must name both.
func TestPrinterAlertsClearedInSteps(t *testing.T) {
	e, c := newEng()
	door := model.Alert{Severity: "critical", Code: "hrPrinterDetectedErrorState.doorOpen", Description: "Door or cover open"}
	off := model.Alert{Severity: "critical", Code: "hrPrinterDetectedErrorState.offline", Description: "Printer offline or paused"}
	if n := e.Observe(printer(true, 50), printer(true, 50, door, off)); len(n) != 2 {
		t.Fatalf("raise: %v", n)
	}
	c.t = c.t.Add(20 * time.Second)
	if n := e.Observe(printer(true, 50, door, off), printer(true, 50, off)); len(n) != 0 {
		t.Fatalf("partial clear: %v", n)
	}
	c.t = c.t.Add(20 * time.Second)
	n := e.Observe(printer(true, 50, off), printer(true, 50))
	if len(n) != 1 || n[0].Title != "Printer OK again" ||
		!strings.Contains(n[0].Message, "cleared Door or cover open, Printer offline or paused.") {
		t.Fatalf("cleared: %v", n)
	}
	// The next fault starts a fresh episode: only its own alert is named.
	jam := model.Alert{Severity: "critical", Code: "hrPrinterDetectedErrorState.jammed", Description: "Paper jam"}
	c.t = c.t.Add(20 * time.Second)
	e.Observe(printer(true, 50), printer(true, 50, jam))
	c.t = c.t.Add(20 * time.Second)
	n = e.Observe(printer(true, 50, jam), printer(true, 50))
	if len(n) != 1 || !strings.HasSuffix(n[0].Message, "cleared Paper jam.") {
		t.Fatalf("second episode: %v", n)
	}
}

func TestNewDevice(t *testing.T) {
	e, _ := newEng()
	if n := e.Observe(model.Host{}, model.Host{IP: "10.0.0.7", IsNew: true, Online: true}); len(n) != 1 || n[0].Trigger != model.TriggerNewDevice {
		t.Fatalf("%v", n)
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

func TestConflict(t *testing.T) {
	e, _ := newEng()
	c := model.Conflict{ID: "c-1", Severity: "fail", IPs: []string{"10.0.0.5"}, Detail: "x"}
	if n := e.ObserveConflict(c, false); len(n) != 0 {
		t.Fatal("not new")
	}
	n := e.ObserveConflict(c, true)
	if len(n) != 1 || n[0].Trigger != model.TriggerIPConflict || n[0].IP != "10.0.0.5" {
		t.Fatalf("%v", n)
	}
	if n := e.ObserveConflict(c, true); len(n) != 0 {
		t.Fatal("dedupe")
	}
}

func report(status string) model.HealthReport {
	return model.HealthReport{Checks: []model.HealthCheck{{ID: "dns", Label: "DNS", Status: status}}}
}

func TestHealth(t *testing.T) {
	e, _ := newEng()
	ok := report(model.HealthOK)
	bad := report(model.HealthFail)
	if n := e.ObserveHealth(model.HealthReport{}, ok); len(n) != 0 {
		t.Fatal("ok")
	}
	n := e.ObserveHealth(ok, bad)
	if len(n) != 1 || n[0].Trigger != model.TriggerHealthFailed {
		t.Fatalf("%v", n)
	}
	if n := e.ObserveHealth(bad, bad); len(n) != 0 {
		t.Fatal("no transition")
	}
	warn := report(model.HealthWarn)
	cfg := DefaultConfig()
	cfg.Cooldown = 0
	e.SetConfig(cfg)
	if n := e.ObserveHealth(warn, bad); len(n) != 1 {
		t.Fatalf("warn->fail: %v", n)
	}
	cfg.Triggers[model.TriggerHealthFailed] = false
	e.SetConfig(cfg)
	if n := e.ObserveHealth(ok, bad); len(n) != 0 {
		t.Fatal("disabled")
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
