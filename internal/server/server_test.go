package server

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"netdiag/internal/arp"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/scan"
	"netdiag/internal/snmpprobe"
)

type fakePinger struct{ block chan struct{} }

func (f fakePinger) Ping(ctx context.Context, ip netip.Addr, timeout time.Duration) (time.Duration, bool, error) {
	if f.block != nil {
		select {
		case <-ctx.Done():
			return 0, false, ctx.Err()
		case <-f.block:
		}
	}
	return time.Millisecond, ip.String() == "10.0.0.10", nil
}

type fakeARP struct{}

func (fakeARP) Table(context.Context) ([]arp.Entry, error) { return nil, nil }

type fakePorts struct{}

func (fakePorts) ScanPorts(context.Context, string, []int) []int { return nil }

type fakeWeb struct{}

func (fakeWeb) ProbeAll(context.Context, string, []int) []model.WebService { return nil }

type fakeSNMP struct{}

func (fakeSNMP) Probe(context.Context, string, string) (*snmpprobe.Result, error) {
	return nil, snmpprobe.ErrNoResponse
}

type fakeDNS struct{}

func (fakeDNS) LookupAddr(context.Context, string) ([]string, error) { return nil, io.EOF }

type fakeVendors struct{}

func (fakeVendors) Lookup(string) string { return "" }

type fakeEnv struct{}

func (fakeEnv) Self(string) (model.SelfInfo, error) {
	return model.SelfInfo{IP: "10.0.0.30", Interface: "eth0"}, nil
}
func (fakeEnv) Gateway() (string, error) { return "10.0.0.1", nil }

func start(t *testing.T, block chan struct{}) (*Server, string, chan error) {
	t.Helper()
	reg := scan.NewRegistry(scan.New(scan.Deps{
		Pinger: fakePinger{block}, ARP: fakeARP{}, Ports: fakePorts{}, Web: fakeWeb{},
		SNMP: fakeSNMP{}, DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{},
	}))
	s := New(Config{
		Registry:   reg,
		Interfaces: func() ([]netinfo.Iface, error) { return []netinfo.Iface{{Name: "eth0", IP: "10.0.0.30"}}, nil },
		Static:     fstest.MapFS{"index.html": {Data: []byte("<html>hi</html>")}},
	})
	s.keepalive = 50 * time.Millisecond
	if err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() { done <- s.Serve(ctx); close(exited) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("serve did not exit")
		}
	})
	return s, s.URL(), done
}

func do(t *testing.T, method, url, body, host string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func idOf(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `"id":"`)
	if i < 0 {
		t.Fatalf("no id in %q", body)
	}
	rest := body[i+6:]
	return rest[:strings.Index(rest, `"`)]
}

func TestHostGuardAndStatic(t *testing.T) {
	_, u, _ := start(t, nil)
	if r, _ := do(t, "GET", u+"/api/interfaces", "", "evil.example.com"); r.StatusCode != 403 {
		t.Fatalf("foreign host: %d", r.StatusCode)
	}
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(u, "http://"))
	if r, _ := do(t, "GET", u+"/api/interfaces", "", "localhost:"+port); r.StatusCode != 200 {
		t.Fatalf("localhost: %d", r.StatusCode)
	}
	if r, b := do(t, "GET", u+"/", "", ""); r.StatusCode != 200 || !strings.Contains(b, "hi") {
		t.Fatalf("static: %d %q", r.StatusCode, b)
	}
	if r, b := do(t, "GET", u+"/api/interfaces", "", ""); r.StatusCode != 200 || !strings.Contains(b, `"eth0"`) {
		t.Fatalf("interfaces: %d %q", r.StatusCode, b)
	}
	req, _ := http.NewRequest("POST", u+"/api/scans", strings.NewReader(`{"cidr":"10.0.0.0/24"}`))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 415 {
		t.Fatalf("content-type guard: %d", resp.StatusCode)
	}
}

func TestBadRange(t *testing.T) {
	_, u, _ := start(t, nil)
	r, b := do(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/21"}`, "")
	if r.StatusCode != 400 || !strings.Contains(b, "error") {
		t.Fatalf("/21: %d %q", r.StatusCode, b)
	}
	if r, _ := do(t, "POST", u+"/api/scans", `not json`, ""); r.StatusCode != 400 {
		t.Fatalf("bad json: %d", r.StatusCode)
	}
	if r, b := do(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/30","ports":[80,99999]}`, ""); r.StatusCode != 400 || !strings.Contains(b, "99999 is not a port number") {
		t.Fatalf("bad port: %d %q", r.StatusCode, b)
	}
	if r, _ := do(t, "GET", u+"/api/scans/nope", "", ""); r.StatusCode != 404 {
		t.Fatalf("unknown: %d", r.StatusCode)
	}
}

func TestConcurrentScanConflictAndCancel(t *testing.T) {
	block := make(chan struct{})
	_, u, _ := start(t, block)
	r, b := do(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/30"}`, "")
	if r.StatusCode != 202 {
		t.Fatalf("first: %d %q", r.StatusCode, b)
	}
	id := idOf(t, b)
	if r, _ := do(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/30"}`, ""); r.StatusCode != 409 {
		t.Fatalf("second: %d", r.StatusCode)
	}
	if r, _ := do(t, "POST", u+"/api/scans/"+id+"/cancel", "", ""); r.StatusCode != 204 {
		t.Fatalf("cancel: %d", r.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, b := do(t, "GET", u+"/api/scans/"+id, "", "")
		if strings.Contains(b, `"state":"cancelled"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not cancelled: %s", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSSEExportAndQuit(t *testing.T) {
	_, u, done := start(t, nil)
	_, b := do(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/28"}`, "")
	id := idOf(t, b)

	resp, err := http.Get(u + "/api/scans/" + id + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	var types []string
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
			types = append(types, v)
			if v == "done" {
				break
			}
		}
	}
	if len(types) == 0 || types[len(types)-1] != "done" {
		t.Fatalf("events: %v (err %v)", types, sc.Err())
	}

	// Late subscriber still gets snapshot then done.
	resp2, err := http.Get(u + "/api/scans/" + id + "/events")
	if err != nil {
		t.Fatal(err)
	}
	all, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if !strings.Contains(string(all), "event: host") || !strings.Contains(string(all), "event: done") {
		t.Fatalf("replay: %s", all)
	}

	r, body := do(t, "GET", u+"/api/scans/"+id+"/export?format=csv", "", "")
	if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Disposition"), "attachment; filename=\"netdiag-") ||
		!strings.HasSuffix(r.Header.Get("Content-Disposition"), `.csv"`) || !strings.HasPrefix(body, "ip,mac,") {
		t.Fatalf("csv export: %d %v %q", r.StatusCode, r.Header, body)
	}
	r, body = do(t, "GET", u+"/api/scans/"+id+"/export?format=json", "", "")
	if r.StatusCode != 200 || !strings.HasSuffix(r.Header.Get("Content-Disposition"), `.json"`) ||
		!strings.Contains(body, "10.0.0.10") || !strings.Contains(r.Header.Get("Content-Type"), "json") {
		t.Fatalf("json export: %d %v", r.StatusCode, r.Header)
	}
	if r, _ := do(t, "GET", u+"/api/scans/"+id+"/export?format=xml", "", ""); r.StatusCode != 400 {
		t.Fatalf("bad format: %d", r.StatusCode)
	}

	if r, _ := do(t, "POST", u+"/api/quit", "", ""); r.StatusCode != 204 {
		t.Fatalf("quit: %d", r.StatusCode)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
	if _, err := http.Get(u + "/"); err == nil {
		t.Fatal("server still accepting")
	}
}

func waitState(t *testing.T, u, id, state string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, b := do(t, "GET", u+"/api/scans/"+id, "", "")
		if strings.Contains(b, `"state":"`+state+`"`) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("scan %s not %s: %s", id, state, b)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLiveScanReplacedAndExport(t *testing.T) {
	_, u, _ := start(t, nil)
	r, b := do(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/28","live":true,"liveIntervalSec":5}`, "")
	if r.StatusCode != 202 {
		t.Fatalf("live start: %d %q", r.StatusCode, b)
	}
	id := idOf(t, b)
	waitState(t, u, id, "live")

	// Export and SSE work while live; the stream reports live, not done.
	if r, body := do(t, "GET", u+"/api/scans/"+id+"/export?format=csv", "", ""); r.StatusCode != 200 || !strings.Contains(body, "10.0.0.10") {
		t.Fatalf("export while live: %d %q", r.StatusCode, body)
	}
	resp, err := http.Get(u + "/api/scans/" + id + "/events")
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(resp.Body)
	var types []string
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
			types = append(types, v)
			if v == "live" {
				break
			}
		}
	}
	resp.Body.Close()
	if len(types) == 0 || types[len(types)-1] != "live" || slices.Contains(types, "done") {
		t.Fatalf("live stream events: %v", types)
	}

	// A new scan replaces the live one.
	r, b = do(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/30"}`, "")
	if r.StatusCode != 202 {
		t.Fatalf("replace live: %d %q", r.StatusCode, b)
	}
	waitState(t, u, id, "done")
	waitState(t, u, idOf(t, b), "done")
}
