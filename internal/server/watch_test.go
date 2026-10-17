package server

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// GET /api/scans/running is {"scan":null} when nothing runs, and names the
// running scan (without its community) so a reloaded page can attach again.
func TestRunningRoute(t *testing.T) {
	_, u, _ := start(t, nil)
	r, b := do(t, "GET", u+"/api/scans/running", "", "")
	if r.StatusCode != 200 || strings.TrimSpace(b) != `{"scan":null}` {
		t.Fatalf("idle: %d %q", r.StatusCode, b)
	}

	r, b = do(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/28","interface":"eth0","community":"s3cr3t-run","live":true,"liveIntervalSec":30}`, "")
	if r.StatusCode != 202 {
		t.Fatalf("live start: %d %q", r.StatusCode, b)
	}
	id := idOf(t, b)
	waitState(t, u, id, "live")

	r, b = do(t, "GET", u+"/api/scans/running", "", "")
	if r.StatusCode != 200 || strings.Contains(b, "s3cr3t-run") {
		t.Fatalf("running: %d %q", r.StatusCode, b)
	}
	var got struct {
		Scan *struct {
			ID, State, StartedAt, LiveSince string
			Request                         struct {
				CIDR, Interface string
				Live            bool
				LiveIntervalSec int
			}
		}
	}
	if err := json.Unmarshal([]byte(b), &got); err != nil || got.Scan == nil {
		t.Fatalf("decode %q: %v", b, err)
	}
	sc := got.Scan
	if sc.ID != id || sc.State != "live" || sc.StartedAt == "" || sc.LiveSince == "" ||
		sc.Request.CIDR != "10.0.0.0/28" || sc.Request.Interface != "eth0" || !sc.Request.Live || sc.Request.LiveIntervalSec != 30 {
		t.Fatalf("running scan = %+v", sc)
	}
	if strings.Contains(b, "community") || strings.Contains(b, "replaceLive") {
		t.Errorf("running request has more than the page needs: %s", b)
	}

	// Stop live: nothing is running again.
	if r, _ := do(t, "POST", u+"/api/scans/"+id+"/cancel", "", ""); r.StatusCode != 204 {
		t.Fatalf("cancel: %d", r.StatusCode)
	}
	waitState(t, u, id, "done") // a stopped live scan ends as done
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, b := do(t, "GET", u+"/api/scans/running", "", "")
		if strings.TrimSpace(b) == `{"scan":null}` {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after stop: %q", b)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// GET /api/identity is the page's heartbeat: it names the app, this process
// and an instance id that changes when netdiag restarts. It needs no token
// but passes the Host check.
func TestIdentity(t *testing.T) {
	s, u, _ := start(t, nil)
	r, b := do(t, "GET", u+"/api/identity", "", "")
	if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("identity: %d %q %v", r.StatusCode, b, r.Header)
	}
	var id identity
	if err := json.Unmarshal([]byte(b), &id); err != nil {
		t.Fatal(err)
	}
	if id.App != "netdiag" || id.Version != "dev" || id.PID != os.Getpid() || id.InstanceID != s.instanceID ||
		!strings.HasPrefix(id.InstanceID, "i-") || id.StartedAt == "" {
		t.Fatalf("identity = %+v", id)
	}
	if again := New(Config{}); again.instanceID == s.instanceID {
		t.Fatal("two servers share an instance id")
	}
	if s := New(Config{InstanceID: "i-fixed"}); s.instanceID != "i-fixed" {
		t.Fatalf("configured instance id = %q", s.instanceID)
	}
	if r, _ := do(t, "GET", u+"/api/identity", "", "evil.example.com"); r.StatusCode != 403 {
		t.Fatalf("foreign host: %d", r.StatusCode)
	}
}

// The page attaches to a running scan, asks before it replaces watching,
// shows the stopped and restarted banners from the heartbeat, rebuilds
// replayed banners silently and lets only one tab beep (F9).
func TestWatchGuardUI(t *testing.T) {
	html, err := readAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<dialog id="restart-dialog"`, `id="restart-btn"`, "Restart watching", "Keep watching",
		"netdiag is watching this network", `id="backend-stopped"`, "<strong>netdiag has stopped.</strong> Open netdiag again to keep watching.",
		`id="backend-restarted"`, "<strong>netdiag was restarted.</strong>", "Reload page", "icons.svg#i-refresh-cw"} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html missing %q", want)
		}
	}
	for _, want := range []string{"fetch('/api/scans/running')", "fetch('/api/identity'", "body.replaceLive = true",
		"r.status === 409 && j.watching", "new BroadcastChannel('netdiag')", "if (n.replay || !this.isBeeper()) return;",
		"this.hbSchedule(this.sseErr || this.hbFails ? 5000 : 15000)", "this.hbFails >= 2"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js missing %q", want)
		}
	}
	// Alpine calls init() itself; a second call from x-init would open a
	// second tab channel and heartbeat.
	if strings.Contains(string(html), `x-init="init()"`) {
		t.Error(`index.html calls init() from x-init; Alpine already calls it`)
	}
	// replaceLive is sent only by Restart watching.
	if n := strings.Count(string(js), "replaceLive"); n != 1 {
		t.Errorf("app.js names replaceLive %d times, want once (in start)", n)
	}
	// Only a tab that follows the running scan beeps, and the other tabs
	// follow a scan another tab started or restarted.
	for _, want := range []string{"attached() { return !!this.es && this.running; }", "isBeeper() { return this.attached() &&",
		"this.tellTabs({type: 'started', id: j.id});", "m.type === 'started'", "this.follow(false, id);", "return this.reattach(skip, true);"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js missing %q", want)
		}
	}
	// A replayed OK notice flips its banner to Fixed before any log line or
	// desktop notification, and only the beeping tab raises one.
	src := string(js)
	i := strings.Index(src, "onFixed(n, bid) {")
	j := strings.Index(src, "bannerText(b) {")
	if i < 0 || j < i {
		t.Fatal("onFixed not found")
	}
	body := src[i:j]
	replay, log := strings.Index(body, "if (n.replay) return;"), strings.Index(body, "this.logPush(")
	beeper, note := strings.Index(body, "if (!this.isBeeper()) return;"), strings.Index(body, "new Notification(")
	if replay < 0 || log < replay || beeper < log || note < beeper {
		t.Errorf("onFixed must return on a replay before logPush, and check isBeeper before new Notification")
	}
}
