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
