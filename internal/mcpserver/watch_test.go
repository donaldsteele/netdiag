package mcpserver

import (
	"strings"
	"testing"
	"time"

	"netdiag/internal/model"
	"netdiag/internal/scan"
)

// waitScanState polls sc until it reaches state.
func waitScanState(t *testing.T, sc *scan.Scan, state string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for sc.State() != state {
		if time.Now().After(deadline) {
			t.Fatalf("scan %s is %q, want %q", sc.ID, sc.State(), state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startUILive starts a live scan the way the netdiag window does and waits
// until it is watching.
func startUILive(t *testing.T, e *env) *scan.Scan {
	t.Helper()
	sc, err := e.svc.Start(model.ScanRequest{CIDR: "10.0.0.0/28", Live: true, LiveIntervalSec: 300}.WithOrigin(model.OriginUI))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Cancel(); <-sc.Done() })
	waitScanState(t, sc, model.StateLive)
	return sc
}

// start_scan never replaces a scan that is watching the network: it returns
// that scan's summary with alreadyWatching, and the scan stays live (F9).
func TestStartScanAlreadyWatching(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	live := startUILive(t, e)
	for _, args := range []map[string]any{{"waitSec": 0}, {"live": true, "waitSec": 0}, {"cidr": "10.0.0.0/30", "waitSec": 5}} {
		var out startScanOut
		e.ok(t, "start_scan", args, &out)
		if !out.AlreadyWatching || !out.AlreadyRunning || out.StillRunning || out.ScanID != live.ID || out.State != model.StateLive {
			t.Fatalf("start_scan %v = %+v", args, out)
		}
		if live.State() != model.StateLive {
			t.Fatalf("start_scan %v stopped the live scan: %q", args, live.State())
		}
	}
	if run, ok := e.svc.Registry().Running(); !ok || run != live {
		t.Fatal("the live scan is no longer the running one")
	}
}

// cancel_scan refuses a live scan started from the window and says to ask
// the person (D19); the scan stays live.
func TestCancelScanRefusesUILive(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	live := startUILive(t, e)
	var out cancelOut
	e.ok(t, "cancel_scan", map[string]any{"scanId": live.ID}, &out)
	if out.Cancelled || out.State != model.StateLive || out.Note != noteWatchingFromWindow {
		t.Fatalf("cancel_scan = %+v", out)
	}
	if !strings.Contains(out.Note, "Stop live") {
		t.Fatalf("note = %q", out.Note)
	}
	time.Sleep(50 * time.Millisecond)
	if live.State() != model.StateLive {
		t.Fatalf("live scan state = %q", live.State())
	}
}

// A live-mode scan from the window that is still in its first pass is about
// to watch the network, so cancel_scan leaves it running too (v0.6-D26).
func TestCancelScanRefusesUILiveFirstPass(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	e := setup(t, "http", true, fakePinger{gate: gate})
	sc, err := e.svc.Start(model.ScanRequest{CIDR: "10.0.0.0/28", Live: true}.WithOrigin(model.OriginUI))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Cancel() })
	var out cancelOut
	e.ok(t, "cancel_scan", map[string]any{"scanId": sc.ID}, &out)
	if out.Cancelled || out.State != model.StateRunning || out.Note != noteFirstPassFromWindow {
		t.Fatalf("cancel_scan = %+v", out)
	}
	// The window shows Cancel until the first scan ends.
	if !strings.Contains(out.Note, "click Cancel") || !strings.Contains(out.Note, "Stop live") {
		t.Fatalf("note = %q", out.Note)
	}
	if sc.State() != model.StateRunning {
		t.Fatalf("scan state = %q", sc.State())
	}
}

// A live scan an agent started can still be cancelled by the agent.
func TestCancelScanStopsAgentLive(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	var started startScanOut
	e.ok(t, "start_scan", map[string]any{"cidr": "10.0.0.0/28", "live": true, "liveIntervalSec": 300, "waitSec": 10}, &started)
	if started.AlreadyWatching || started.State != model.StateLive {
		t.Fatalf("start_scan = %+v", started)
	}
	var out cancelOut
	e.ok(t, "cancel_scan", map[string]any{"scanId": started.ScanID}, &out)
	if !out.Cancelled || out.Note != "" {
		t.Fatalf("cancel_scan = %+v", out)
	}
}

// A one-shot scan started from the window can still be cancelled: only
// watching is protected.
func TestCancelScanStopsUIOneShot(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	e := setup(t, "http", true, fakePinger{gate: gate})
	sc, err := e.svc.Start(model.ScanRequest{CIDR: "10.0.0.0/28"}.WithOrigin(model.OriginUI))
	if err != nil {
		t.Fatal(err)
	}
	var out cancelOut
	e.ok(t, "cancel_scan", map[string]any{"scanId": sc.ID}, &out)
	if !out.Cancelled || out.Note != "" {
		t.Fatalf("cancel_scan = %+v", out)
	}
}
