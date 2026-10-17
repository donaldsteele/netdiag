package server

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"netdiag/internal/netinfo"
	"netdiag/internal/remote"
	"netdiag/internal/scan"
)

// remotePorts reports SSH and Remote Desktop open on 10.0.0.10 and Remote
// Desktop on this computer (10.0.0.30).
type remotePorts struct{}

func (remotePorts) ScanPorts(_ context.Context, ip string, ports []int) []int {
	switch ip {
	case "10.0.0.10":
		return []int{22, 3389}
	case "10.0.0.30":
		return []int{3389}
	}
	return nil
}

// remotePinger answers for 10.0.0.10 and this computer.
type remotePinger struct{}

func (remotePinger) Ping(_ context.Context, ip netip.Addr, _ time.Duration) (time.Duration, bool, error) {
	return time.Millisecond, ip.String() == "10.0.0.10" || ip.String() == "10.0.0.30", nil
}

type fakeOpener struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeOpener) Caps() remote.Caps {
	return remote.Caps{Platform: "windows", RDP: true, RDPApp: "Remote Desktop Connection"}
}

func (f *fakeOpener) Open(k remote.Kind, ip string, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, string(k)+" "+ip+" "+strconv.Itoa(port))
	return f.err
}

func startRemote(t *testing.T, op remote.Opener) string {
	t.Helper()
	reg := scan.NewRegistry(scan.New(scan.Deps{
		Pinger: remotePinger{}, ARP: fakeARP{}, Ports: remotePorts{}, Web: fakeWeb{},
		SNMP: fakeSNMP{}, DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{},
	}))
	s := New(Config{
		Registry:   reg,
		Interfaces: func() ([]netinfo.Iface, error) { return []netinfo.Iface{{Name: "eth0", IP: "10.0.0.30"}}, nil },
		Static:     fstest.MapFS{"index.html": {Data: []byte("<html>hi</html>")}},
		Remote:     op,
	})
	if err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	exited := make(chan struct{})
	go func() { s.Serve(ctx); close(exited) }()
	t.Cleanup(func() { cancel(); <-exited })
	return s.URL()
}

func TestRemoteOpen(t *testing.T) {
	op := &fakeOpener{}
	u := startRemote(t, op)
	if r, b := do(t, "GET", u+"/api/remote", "", ""); r.StatusCode != 200 || !strings.Contains(b, `"rdp":true`) {
		t.Fatalf("caps: %d %s", r.StatusCode, b)
	}
	_, b := do(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/27"}`, "")
	id := idOf(t, b)
	waitState(t, u, id, "done")
	base := u + "/api/scans/" + id

	for _, tc := range []struct {
		name, path, body string
		code             int
	}{
		{"unknown scan", u + "/api/scans/nope/remote", `{"ip":"10.0.0.10","kind":"rdp"}`, 404},
		{"unknown device", base + "/remote", `{"ip":"10.0.0.11","kind":"rdp"}`, 404},
		{"port not open", base + "/remote", `{"ip":"10.0.0.10","kind":"vnc"}`, 400},
		{"ssh is never opened", base + "/remote", `{"ip":"10.0.0.10","kind":"ssh"}`, 400},
		{"unknown kind", base + "/remote", `{"ip":"10.0.0.10","kind":"telnet"}`, 400},
		{"bad body", base + "/remote", `nope`, 400},
		{"not a local address", base + "/remote", `{"ip":"203.0.113.7","kind":"rdp"}`, 400},
		{"this computer", base + "/remote", `{"ip":"10.0.0.30","kind":"rdp"}`, 400},
	} {
		if r, b := do(t, "POST", tc.path, tc.body, ""); r.StatusCode != tc.code {
			t.Errorf("%s: %d %s, want %d", tc.name, r.StatusCode, b, tc.code)
		}
	}
	if _, b := do(t, "POST", base+"/remote", `{"ip":"10.0.0.30","kind":"rdp"}`, ""); !strings.Contains(b, "is this computer") {
		t.Errorf("this computer: %s", b)
	}
	if len(op.calls) != 0 {
		t.Fatalf("opened on a refused request: %v", op.calls)
	}
	if r, b := do(t, "POST", base+"/remote", `{"ip":"10.0.0.10","kind":"rdp"}`, ""); r.StatusCode != 204 {
		t.Fatalf("open: %d %s", r.StatusCode, b)
	}
	if want := []string{"rdp 10.0.0.10 3389"}; len(op.calls) != 1 || op.calls[0] != want[0] {
		t.Fatalf("calls %v want %v", op.calls, want)
	}
	op.err = remote.ErrNoApp
	if r, b := do(t, "POST", base+"/remote", `{"ip":"10.0.0.10","kind":"rdp"}`, ""); r.StatusCode != 409 || !strings.Contains(b, `"noApp":true`) {
		t.Fatalf("no app: %d %s", r.StatusCode, b)
	}

	r, b := do(t, "GET", base+"/remote.rdp?ip=10.0.0.10", "", "")
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/x-rdp" ||
		!strings.Contains(r.Header.Get("Content-Disposition"), `filename="10.0.0.10.rdp"`) ||
		!strings.Contains(b, "full address:s:10.0.0.10\r\n") {
		t.Fatalf("rdp file: %d %v %q", r.StatusCode, r.Header, b)
	}
	if r, _ := do(t, "GET", base+"/remote.rdp?ip=10.0.0.12", "", ""); r.StatusCode != 404 {
		t.Fatalf("rdp file for unknown device: %d", r.StatusCode)
	}
	// Cross-site pages cannot start a session: the guard wants JSON.
	req, _ := http.NewRequest("POST", base+"/remote", strings.NewReader(`{"ip":"10.0.0.10","kind":"rdp"}`))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 415 {
		t.Fatalf("text/plain: %d", resp.StatusCode)
	}
}
