package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netdiag/internal/health"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/problems"
	"netdiag/internal/scan"
	"netdiag/internal/store"
)

type fakeHealth struct{}

func (f fakeHealth) Run(_ context.Context, in health.Input) model.HealthReport {
	return model.HealthReport{CheckedAt: "2026-01-01T00:00:00Z", Checks: []model.HealthCheck{{ID: "gateway", Status: model.HealthOK}}}
}

func startStore(t *testing.T) (*Server, string, *store.Store) {
	t.Helper()
	return startStoreWith(t, nil)
}

// startStoreWith is startStore with the scan dependencies changed by edit
// (nil: none).
func startStoreWith(t *testing.T, edit func(*scan.Deps)) (*Server, string, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// As `netdiag ui` wires it: one ledger and profile store for every scan.
	ledger, err := problems.OpenLedger(st)
	if err != nil {
		t.Fatal(err)
	}
	deps := scan.Deps{
		Pinger: fakePinger{}, ARP: fakeARP{}, Ports: fakePorts{}, Web: fakeWeb{},
		SNMP: fakeSNMP{}, DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{},
		Store: st, Settings: st, Ledger: ledger, Profiles: scan.NewProfiles(st),
	}
	if edit != nil {
		edit(&deps)
	}
	reg := scan.NewRegistry(scan.New(deps))
	s := New(Config{
		Registry:     reg,
		Store:        st,
		Health:       fakeHealth{},
		Gateway:      func() (string, error) { return "10.0.0.1", nil },
		Interfaces:   func() ([]netinfo.Iface, error) { return []netinfo.Iface{{Name: "eth0", IP: "10.0.0.30"}}, nil },
		SessionToken: testToken,
		Ledger:       ledger,
		// The team chat manager says why a call to the chat server failed.
		Logf: t.Logf,
	})
	if err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	exited := make(chan struct{})
	go func() { _ = s.Serve(ctx); close(exited) }()
	t.Cleanup(func() { cancel(); <-exited })
	return s, s.URL(), st
}

func doj(t *testing.T, method, url, body, ctype string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.Header.Set(SessionHeader, testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func seed(t *testing.T, st *store.Store, id, cidr, start string, ips ...string) {
	t.Helper()
	res := model.ScanResult{ID: id, Request: model.ScanRequest{CIDR: cidr}, StartedAt: start, FinishedAt: start, State: model.StateDone,
		Health: &model.HealthReport{CheckedAt: start}}
	for _, ip := range ips {
		res.Hosts = append(res.Hosts, model.Host{IP: ip, Alive: true, Online: true})
	}
	if err := st.SaveScan(res); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsEndpoints(t *testing.T) {
	_, u, _ := startStore(t)
	r, b := doj(t, "GET", u+"/api/settings", "", "")
	if r.StatusCode != 200 || !strings.Contains(b, `"cooldownSec":300`) {
		t.Fatalf("get: %d %s", r.StatusCode, b)
	}
	var set model.Settings
	_ = json.Unmarshal([]byte(b), &set)

	bad := []func(*model.Settings){
		func(s *model.Settings) { s.WebServer.URL = "ftp://x" },
		func(s *model.Settings) { s.Alerts.SupplyThresholdPct = 0 },
		func(s *model.Settings) { s.Alerts.SupplyThresholdPct = 101 },
		func(s *model.Settings) { s.Alerts.CooldownSec = 10 },
		func(s *model.Settings) { s.Alerts.CooldownSec = 4000 },
		func(s *model.Settings) { s.InternetCheckURL = "not a url" },
		func(s *model.Settings) { s.WebServer.TrustedCert = "not a fingerprint" },
	}
	for i, f := range bad {
		c := set
		f(&c)
		body, _ := json.Marshal(c)
		if r, b := doj(t, "PUT", u+"/api/settings", string(body), "application/json"); r.StatusCode != 400 {
			t.Errorf("case %d: %d %s", i, r.StatusCode, b)
		}
	}
	if r, _ := doj(t, "PUT", u+"/api/settings", "{", "application/json"); r.StatusCode != 400 {
		t.Errorf("bad json: %d", r.StatusCode)
	}
	if r, _ := doj(t, "PUT", u+"/api/settings", "{}", "text/plain"); r.StatusCode != 415 {
		t.Errorf("put content-type: %d", r.StatusCode)
	}
	set.Community = "secret"
	set.Alerts.CooldownSec = 60
	set.WebServer.TrustedCert = strings.Repeat("AB:", 31) + "AB"
	body, _ := json.Marshal(set)
	if r, b := doj(t, "PUT", u+"/api/settings", string(body), "application/json"); r.StatusCode != 200 || !strings.Contains(b, `"secret"`) {
		t.Fatalf("put: %d %s", r.StatusCode, b)
	}
	_, b = doj(t, "GET", u+"/api/settings", "", "")
	if !strings.Contains(b, `"cooldownSec":60`) || !strings.Contains(b, `"trustedCert":"`+strings.Repeat("ab", 32)+`"`) {
		t.Fatalf("not persisted: %s", b)
	}
}

// A settings file with a field that is not valid loads with that field reset
// and lists it in repaired; PUT stays strict, and a good save clears the list.
func TestSettingsRepairedView(t *testing.T) {
	_, u, st := startStore(t)
	r, b := doj(t, "GET", u+"/api/settings", "", "")
	if r.StatusCode != 200 || strings.Contains(b, `"repaired"`) ||
		!strings.Contains(b, `"chat":{"enabled":false,"minSeverity":"warning"}`) {
		t.Fatalf("defaults: %d %s", r.StatusCode, b)
	}
	doc := `{"schemaVersion":1,"community":"keep","alerts":{"sound":true,"cooldownSec":120,"supplyThresholdPct":0},"chat":{"enabled":true,"minSeverity":"loud"}}`
	if err := os.WriteFile(filepath.Join(st.Dir(), "settings.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	_, b = doj(t, "GET", u+"/api/settings", "", "")
	var view struct {
		model.Settings
		Repaired []string `json:"repaired"`
	}
	if err := json.Unmarshal([]byte(b), &view); err != nil {
		t.Fatal(err)
	}
	if strings.Join(view.Repaired, ",") != "alerts.supplyThresholdPct,chat.minSeverity" ||
		view.Alerts.SupplyThresholdPct != 15 || view.Alerts.CooldownSec != 120 || view.Community != "keep" ||
		!view.Chat.Enabled || view.Chat.MinSeverity != "warning" {
		t.Fatalf("repaired view: %s", b)
	}
	bad := view.Settings
	bad.Chat.MinSeverity = "loud"
	body, _ := json.Marshal(bad)
	if r, b := doj(t, "PUT", u+"/api/settings", string(body), "application/json"); r.StatusCode != 400 || !strings.Contains(b, "chat.minSeverity") {
		t.Fatalf("strict put: %d %s", r.StatusCode, b)
	}
	// The page sends the view back as it got it, repaired list included.
	r, b = doj(t, "PUT", u+"/api/settings", `{"repaired":["x"],`+strings.TrimPrefix(mustJSON(t, view.Settings), "{"), "application/json")
	if r.StatusCode != 200 || strings.Contains(b, `"repaired"`) {
		t.Fatalf("put: %d %s", r.StatusCode, b)
	}
	if _, b = doj(t, "GET", u+"/api/settings", "", ""); strings.Contains(b, `"repaired"`) || !strings.Contains(b, `"supplyThresholdPct":15`) {
		t.Fatalf("after save: %s", b)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestIgnoreMAC(t *testing.T) {
	_, u, _ := startStore(t)
	if r, _ := doj(t, "POST", u+"/api/conflicts/ignore", `{"mac":"zz"}`, "application/json"); r.StatusCode != 400 {
		t.Fatalf("bad mac: %d", r.StatusCode)
	}
	for i := 0; i < 2; i++ {
		if r, b := doj(t, "POST", u+"/api/conflicts/ignore", `{"mac":"aa:bb:cc:dd:ee:ff"}`, "application/json"); r.StatusCode != 204 {
			t.Fatalf("ignore: %d %s", r.StatusCode, b)
		}
	}
	_, b := doj(t, "GET", u+"/api/settings", "", "")
	if strings.Count(b, "AA:BB:CC:DD:EE:FF") != 1 {
		t.Fatalf("settings: %s", b)
	}
	if r, _ := doj(t, "POST", u+"/api/conflicts/ignore", `{}`, "text/plain"); r.StatusCode != 415 {
		t.Fatalf("content type: %d", r.StatusCode)
	}
}

func TestHistoryEndpoints(t *testing.T) {
	_, u, st := startStore(t)
	if _, b := doj(t, "GET", u+"/api/history", "", ""); strings.TrimSpace(b) != "[]" {
		t.Fatalf("empty list: %s", b)
	}
	seed(t, st, "a1", "10.0.0.0/24", "2026-01-01T10:00:00Z", "10.0.0.5")
	seed(t, st, "a2", "10.0.0.0/24", "2026-01-01T11:00:00Z", "10.0.0.5", "10.0.0.6")
	_, b := doj(t, "GET", u+"/api/history", "", "")
	if i1, i2 := strings.Index(b, `"a1"`), strings.Index(b, `"a2"`); i1 < 0 || i2 < 0 || i2 > i1 {
		t.Fatalf("order: %s", b)
	}
	if r, b := doj(t, "GET", u+"/api/history/a1", "", ""); r.StatusCode != 200 || !strings.Contains(b, "10.0.0.5") {
		t.Fatalf("get: %d %s", r.StatusCode, b)
	}
	if r, _ := doj(t, "GET", u+"/api/history/nope", "", ""); r.StatusCode != 404 {
		t.Fatalf("get missing: %d", r.StatusCode)
	}
	if r, _ := doj(t, "DELETE", u+"/api/history/a1", "", ""); r.StatusCode != 204 {
		t.Fatalf("delete: %d", r.StatusCode)
	}
	if r, _ := doj(t, "DELETE", u+"/api/history/a1", "", ""); r.StatusCode != 404 {
		t.Fatalf("delete again: %d", r.StatusCode)
	}
	// DELETE is subject to the Host guard.
	req, _ := http.NewRequest("DELETE", u+"/api/history/a2", nil)
	req.Host = "evil.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("delete foreign host: %d", resp.StatusCode)
	}
}

// A scan saved by an earlier run (only in History, not in memory) exports too.
func TestExportSavedScan(t *testing.T) {
	_, u, st := startStore(t)
	seed(t, st, "s1", "10.0.0.0/24", "2026-01-01T10:00:00Z", "10.0.0.5", "10.0.0.6")
	r, b := doj(t, "GET", u+"/api/scans/s1/export?format=csv", "", "")
	if r.StatusCode != 200 || !strings.HasPrefix(b, "ip,mac,") || !strings.Contains(b, "10.0.0.6") ||
		!strings.HasSuffix(r.Header.Get("Content-Disposition"), `.csv"`) {
		t.Fatalf("csv: %d %v %q", r.StatusCode, r.Header, b)
	}
	r, b = doj(t, "GET", u+"/api/scans/s1/export?format=json", "", "")
	if r.StatusCode != 200 || !strings.Contains(b, `"id": "s1"`) || !strings.Contains(b, "10.0.0.5") {
		t.Fatalf("json: %d %q", r.StatusCode, b)
	}
	if r, _ := doj(t, "GET", u+"/api/scans/nope/export?format=json", "", ""); r.StatusCode != 404 {
		t.Fatalf("unknown id: %d", r.StatusCode)
	}
	if r, _ := doj(t, "GET", u+"/api/scans/s1/export?format=xml", "", ""); r.StatusCode != 400 {
		t.Fatalf("bad format: %d", r.StatusCode)
	}
	if err := os.WriteFile(filepath.Join(st.Dir(), "history", "20260101T110000Z-bad.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r, b := doj(t, "GET", u+"/api/scans/bad/export?format=json", "", ""); r.StatusCode != 500 || !strings.Contains(b, "could not read the saved scan") {
		t.Fatalf("unreadable file: %d %s", r.StatusCode, b)
	}
}

func TestCompareEndpoints(t *testing.T) {
	_, u, st := startStore(t)
	seed(t, st, "b1", "10.0.0.0/24", "2026-01-01T10:00:00Z", "10.0.0.5")
	seed(t, st, "b2", "10.0.0.0/24", "2026-01-01T11:00:00Z", "10.0.0.5", "10.0.0.6")
	seed(t, st, "other", "192.168.0.0/24", "2026-01-01T09:00:00Z", "192.168.0.2")

	// default base = previous scan of the same CIDR
	r, b := doj(t, "GET", u+"/api/compare?current=b2", "", "")
	if r.StatusCode != 200 || !strings.Contains(b, `"added":1`) || !strings.Contains(b, "10.0.0.6") {
		t.Fatalf("default base: %d %s", r.StatusCode, b)
	}
	// explicit base
	if r, b := doj(t, "GET", u+"/api/compare?base=b1&current=b2", "", ""); r.StatusCode != 200 {
		t.Fatalf("explicit: %d %s", r.StatusCode, b)
	}
	if r, _ := doj(t, "GET", u+"/api/compare?base=b2&current=b2", "", ""); r.StatusCode != 400 {
		t.Fatalf("same: %d", r.StatusCode)
	}
	if r, _ := doj(t, "GET", u+"/api/compare?base=zzz&current=b2", "", ""); r.StatusCode != 404 {
		t.Fatalf("missing base: %d", r.StatusCode)
	}
	if r, _ := doj(t, "GET", u+"/api/compare?current=nope", "", ""); r.StatusCode != 404 {
		t.Fatalf("missing current: %d", r.StatusCode)
	}
	if r, _ := doj(t, "GET", u+"/api/compare?current=other", "", ""); r.StatusCode != 404 {
		t.Fatalf("no previous: %d", r.StatusCode)
	}
	if r, _ := doj(t, "GET", u+"/api/compare", "", ""); r.StatusCode != 400 {
		t.Fatalf("no current: %d", r.StatusCode)
	}

	// export headers
	r, b = doj(t, "GET", u+"/api/compare/export?base=b1&current=b2&format=csv", "", "")
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/csv") ||
		!strings.Contains(r.Header.Get("Content-Disposition"), "netdiag-diff-") ||
		!strings.HasSuffix(strings.TrimSuffix(r.Header.Get("Content-Disposition"), `"`), ".csv") ||
		!strings.HasPrefix(b, "change,ip,mac,vendor,field,from,to") {
		t.Fatalf("csv export: %d %v %q", r.StatusCode, r.Header, b)
	}
	r, _ = doj(t, "GET", u+"/api/compare/export?base=b1&current=b2&format=json", "", "")
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/json" ||
		!strings.Contains(r.Header.Get("Content-Disposition"), ".json") {
		t.Fatalf("json export: %d %v", r.StatusCode, r.Header)
	}
	if r, _ := doj(t, "GET", u+"/api/compare/export?base=b1&current=b2&format=xml", "", ""); r.StatusCode != 400 {
		t.Fatalf("bad format: %d", r.StatusCode)
	}
}

func TestCompareLiveCurrent(t *testing.T) {
	_, u, st := startStore(t)
	seed(t, st, "old", "10.0.0.0/24", "2020-01-01T10:00:00Z", "10.0.0.99")
	r, b := doj(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/24"}`, "application/json")
	if r.StatusCode != 202 {
		t.Fatalf("start: %d %s", r.StatusCode, b)
	}
	id := idOf(t, b)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, g := doj(t, "GET", u+"/api/scans/"+id, "", "")
		if strings.Contains(g, `"state":"done"`) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	r, b = doj(t, "GET", u+"/api/compare?base=old&current="+id, "", "")
	if r.StatusCode != 200 || !strings.Contains(b, `"removed"`) {
		t.Fatalf("live current: %d %s", r.StatusCode, b)
	}
}

func TestHealthEndpoints(t *testing.T) {
	_, u, st := startStore(t)
	if r, _ := doj(t, "GET", u+"/api/health", "", ""); r.StatusCode != 404 {
		t.Fatalf("no report: %d", r.StatusCode)
	}
	seed(t, st, "h1", "10.0.0.0/24", "2026-01-01T10:00:00Z", "10.0.0.5")
	if r, b := doj(t, "GET", u+"/api/health", "", ""); r.StatusCode != 200 || !strings.Contains(b, "2026-01-01T10:00:00Z") {
		t.Fatalf("saved report: %d %s", r.StatusCode, b)
	}
	if r, b := doj(t, "POST", u+"/api/health/run", `{"interface":"eth0"}`, "application/json"); r.StatusCode != 200 || !strings.Contains(b, `"gateway"`) {
		t.Fatalf("run: %d %s", r.StatusCode, b)
	}
	if r, _ := doj(t, "POST", u+"/api/health/run", ``, "application/json"); r.StatusCode != 200 {
		t.Fatalf("run empty body: %d", r.StatusCode)
	}
	if r, _ := doj(t, "POST", u+"/api/health/run", `{}`, "text/plain"); r.StatusCode != 415 {
		t.Fatalf("content type: %d", r.StatusCode)
	}
}

func TestNoStore503(t *testing.T) {
	_, u, _ := start(t, nil)
	if r, _ := doj(t, "GET", u+"/api/settings", "", ""); r.StatusCode != 503 {
		t.Fatalf("settings: %d", r.StatusCode)
	}
	if r, _ := doj(t, "GET", u+"/api/history", "", ""); r.StatusCode != 503 {
		t.Fatalf("history: %d", r.StatusCode)
	}
}

// Quit saves the open-problem ledger's last changes at once, without
// waiting for its debounced save.
func TestQuitSavesLedger(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := problems.OpenLedger(st)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Registry: scan.NewRegistry(scan.New(scan.Deps{Env: fakeEnv{}})), Store: st, Health: fakeHealth{},
		Gateway: func() (string, error) { return "10.0.0.1", nil }, Ledger: ledger, SessionToken: testToken})
	if err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- s.Serve(context.Background()) }()
	ledger.Apply("n-0123456789ab", []model.Notice{{Trigger: model.TriggerHealthFailed, Severity: "critical",
		Title: "Health check failed", Key: "healthFailed||gateway", At: "2026-10-17T10:00:00Z"}})
	if r, _ := doj(t, "POST", s.URL()+"/api/quit", "{}", "application/json"); r.StatusCode != 204 {
		t.Fatalf("quit: %d", r.StatusCode)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not exit")
	}
	data, err := os.ReadFile(filepath.Join(st.Dir(), store.FileOpenProblems))
	if err != nil || !strings.Contains(string(data), "healthFailed||gateway") {
		t.Fatalf("ledger after quit: %s (%v)", data, err)
	}
}

// Quitting while a live scan runs must not exit before the scan's final
// history save (an exit mid-write leaves only a .tmp file behind).
func TestQuitWaitsForLiveSave(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := scan.NewRegistry(scan.New(scan.Deps{
		Pinger: fakePinger{}, ARP: fakeARP{}, Ports: fakePorts{}, Web: fakeWeb{},
		SNMP: fakeSNMP{}, DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{},
		Store: st, Settings: st,
	}))
	s := New(Config{Registry: reg, Store: st, Health: fakeHealth{}, SessionToken: testToken,
		Gateway: func() (string, error) { return "10.0.0.1", nil }})
	if err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- s.Serve(context.Background()) }()
	u := s.URL()

	r, b := doj(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/30","live":true}`, "application/json")
	if r.StatusCode != 202 {
		t.Fatalf("start: %d %s", r.StatusCode, b)
	}
	var started struct{ ID string }
	json.Unmarshal([]byte(b), &started)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if sc, ok := reg.Get(started.ID); ok && sc.State() == model.StateLive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan never reached live")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r, _ := doj(t, "POST", u+"/api/quit", "{}", "application/json"); r.StatusCode != 204 {
		t.Fatalf("quit: %d", r.StatusCode)
	}
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("server did not exit")
	}
	if _, err := st.LoadScan(started.ID); err != nil {
		t.Fatalf("live scan not saved before exit: %v", err)
	}
}
