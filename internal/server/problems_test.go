package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"netdiag/internal/model"
	"netdiag/internal/problems"
)

// GET /api/problems (v0.6 #1) needs no session token. Without a scan it
// says so; a saved scan with only an offline phone (Any device goes offline
// off, the default) reads "Everything looks fine" while the phone is still
// one of the Problems tab's devices; an offline printer is on the card.
func TestProblemsRoute(t *testing.T) {
	_, u, st := startStore(t)
	get := func() problems.View {
		t.Helper()
		r, b := raw(t, "GET", u+"/api/problems", "", "")
		if r.StatusCode != 200 {
			t.Fatalf("GET /api/problems: %d %s", r.StatusCode, b)
		}
		var v problems.View
		if err := json.Unmarshal([]byte(b), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := get(); v.Source != problems.SourceNone || v.EverythingFine || v.Watching || len(v.Open) != 0 {
		t.Fatalf("no scan yet: %+v", v)
	}

	phone := model.Host{IP: "10.0.0.7", Alive: true, Online: false, MAC: "02:00:5E:00:00:07", DeviceType: "phone"}
	res := model.ScanResult{ID: "h-phone", Request: model.ScanRequest{CIDR: "10.0.0.0/24"}, StartedAt: "2026-10-17T10:40:00Z",
		FinishedAt: "2026-10-17T10:42:05Z", State: model.StateDone,
		Health: &model.HealthReport{CheckedAt: "2026-10-17T10:41:00Z", Checks: []model.HealthCheck{{ID: "gateway", Status: model.HealthOK}}},
		Hosts:  []model.Host{{IP: "10.0.0.1", Alive: true, Online: true}, phone}}
	if err := st.SaveScan(res); err != nil {
		t.Fatal(err)
	}
	v := get()
	if v.Source != problems.SourceSaved || !v.EverythingFine || len(v.Open) != 0 || v.CheckedAt != res.FinishedAt {
		t.Fatalf("offline phone only: %+v", v)
	}
	if is := problems.HostIssues(phone, model.Settings{}, nil); len(is) != 1 || is[0].Kind != problems.IssueOffline {
		t.Errorf("the phone is not on the Problems tab: %+v", is)
	}

	res.ID = "h-printer"
	res.FinishedAt = "2026-10-17T10:50:00Z"
	res.Hosts = append(res.Hosts, model.Host{IP: "10.0.0.9", Alive: true, Online: false, IsPrinter: true, MAC: "02:00:5E:00:00:09"})
	if err := st.SaveScan(res); err != nil {
		t.Fatal(err)
	}
	v = get()
	if v.EverythingFine || len(v.Open) != 1 || v.Open[0].Key != "printerOffline|02:00:5E:00:00:09|" || v.Open[0].Severity != problems.SevWarning {
		t.Fatalf("offline printer: %+v", v)
	}
}

// After a scan of this process the card speaks for it (latest), and a newer
// Check again stands in for its health report.
func TestProblemsLatestScanAndHealthRun(t *testing.T) {
	_, u, _ := startStore(t)
	r, b := doj(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/30"}`, "application/json")
	if r.StatusCode != 202 {
		t.Fatalf("start: %d %s", r.StatusCode, b)
	}
	waitState(t, u, idOf(t, b), "done")
	r, b = raw(t, "GET", u+"/api/problems", "", "")
	if r.StatusCode != 200 || !strings.Contains(b, `"source":"latest"`) || !strings.Contains(b, `"watching":false`) {
		t.Fatalf("latest: %d %s", r.StatusCode, b)
	}
	if r, b := doj(t, "POST", u+"/api/health/run", `{}`, "application/json"); r.StatusCode != 200 {
		t.Fatalf("health run: %d %s", r.StatusCode, b)
	}
	if _, b := raw(t, "GET", u+"/api/health", "", ""); !strings.Contains(b, `"gateway"`) {
		t.Errorf("GET /api/health after Check again: %s", b)
	}

	// A live scan: the card speaks for it and says netdiag is watching.
	r, b = doj(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/30","live":true,"liveIntervalSec":5}`, "application/json")
	if r.StatusCode != 202 {
		t.Fatalf("live start: %d %s", r.StatusCode, b)
	}
	id := idOf(t, b)
	waitState(t, u, id, "live")
	if _, b := raw(t, "GET", u+"/api/problems", "", ""); !strings.Contains(b, `"source":"live"`) || !strings.Contains(b, `"watching":true`) {
		t.Errorf("live: %s", b)
	}
}

// Every fixed /api route of the route table has a mock in the UI harness
// (tools/uitest/support/mock.mjs), where flow specs fail on unmatched
// requests.
func TestEveryAPIRouteMocked(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "tools", "uitest", "support", "mock.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{})
	for _, rt := range s.routes() {
		method, p, _ := strings.Cut(rt.pattern, " ")
		if !strings.HasPrefix(p, "/api/") {
			continue
		}
		ok := strings.Contains(string(src), "method === '"+method+"' && p === '"+p+"'")
		if strings.Contains(p, "{") {
			// A parameterised path is matched by a pattern ending in its
			// last fixed part (/events$/, remote\.rdp$/), or, when it ends in
			// a parameter, by its method next to that parameter's variable
			// (histId for /api/history/{id}) or its pattern.
			parts := strings.Split(p, "/")
			if tail := parts[len(parts)-1]; !strings.Contains(tail, "{") {
				ok = strings.Contains(string(src), strings.ReplaceAll(tail, ".", `\.`)+"$/")
			} else {
				v := parts[len(parts)-2]
				ok = regexp.MustCompile(`method === '` + method + `' && (` + regexp.QuoteMeta(v[:4]) + `Id|matchId\(/\^\\/api\\/` +
					regexp.QuoteMeta(v) + `\\/\(\[\^/\]\+\)\$/)`).Match(src)
			}
		}
		if !ok {
			t.Errorf("mock.mjs has no mock for %s", rt.pattern)
		}
	}
}
