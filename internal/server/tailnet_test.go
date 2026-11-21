package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"testing/fstest"
	"time"

	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/scan"
	"netdiag/internal/tailscale"
)

type fakeTailscale struct{}

func (fakeTailscale) Status(context.Context) (tailscale.Status, error) {
	return tailscale.Status{
		State: tailscale.StateRunning, Suffix: "tail1234.ts.net", Tailnet: "someone@example.com",
		Self:    &tailscale.Device{ID: "self", Name: "office-pc", IPs: []string{"100.101.102.103"}, Online: true},
		Devices: []tailscale.Device{{ID: "n1", Name: "nas", OS: "linux", IPs: []string{"100.64.12.34"}, Online: true, Path: tailscale.PathIdle}},
		Health:  []string{},
	}, nil
}

func (fakeTailscale) Ping(_ context.Context, ip string) (tailscale.PingResult, error) {
	return tailscale.PingResult{IP: ip, Name: "nas", OK: true, Latency: 1, Path: tailscale.PathDirect, Endpoint: "192.168.1.50:41641"}, nil
}

func startTailnet(t *testing.T) string {
	t.Helper()
	reg := scan.NewRegistry(scan.New(scan.Deps{
		Pinger: fakePinger{}, ARP: fakeARP{}, Ports: fakePorts{}, Web: fakeWeb{},
		SNMP: fakeSNMP{}, DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{},
	}))
	s := New(Config{
		Registry:     reg,
		Interfaces:   func() ([]netinfo.Iface, error) { return []netinfo.Iface{{Name: "eth0", IP: "10.0.0.30"}}, nil },
		Static:       fstest.MapFS{"index.html": {Data: []byte("<html>hi</html>")}},
		SessionToken: testToken,
		Tailnet:      fakeTailscale{},
	})
	if err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = s.Serve(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("serve did not exit")
		}
	})
	return s.URL()
}

// TestTailnetRoutes: GET /api/tailnet needs no token and carries no email
// address; POST /api/tailnet/ping needs the session token, pings one
// tailnet device and refuses a second ping of it at once.
func TestTailnetRoutes(t *testing.T) {
	u := startTailnet(t)
	r, b := raw(t, "GET", u+"/api/tailnet", "", "")
	if r.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/tailnet: %d %s", r.StatusCode, b)
	}
	var v model.TailnetView
	if err := json.Unmarshal([]byte(b), &v); err != nil || v.State != model.TailnetRunning || len(v.Devices) != 1 || v.Card.ID != "tailscale" {
		t.Fatalf("view %s (%v)", b, err)
	}
	if contains(b, "example.com") {
		t.Fatalf("tailnet name in the view: %s", b)
	}

	r, b = raw(t, "POST", u+"/api/tailnet/ping", `{"ip":"100.64.12.34"}`, "")
	sessionRefused(t, "POST /api/tailnet/ping without the token", r, b)
	r, b = raw(t, "POST", u+"/api/tailnet/ping", `{"ip":"100.64.12.34"}`, "wrong")
	sessionRefused(t, "POST /api/tailnet/ping with another token", r, b)

	r, b = raw(t, "POST", u+"/api/tailnet/ping", `{"ip":"100.64.12.34"}`, testToken)
	var p model.TailnetPing
	if r.StatusCode != http.StatusOK || json.Unmarshal([]byte(b), &p) != nil || !p.OK || p.Path != model.TailnetPathDirect {
		t.Fatalf("ping: %d %s", r.StatusCode, b)
	}
	if r, b := raw(t, "POST", u+"/api/tailnet/ping", `{"ip":"100.64.12.34"}`, testToken); r.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second ping: %d %s", r.StatusCode, b)
	}
	if r, b := raw(t, "POST", u+"/api/tailnet/ping", `{"ip":"8.8.8.8"}`, testToken); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("ping to a non-tailnet address: %d %s", r.StatusCode, b)
	}
	if r, b := raw(t, "POST", u+"/api/tailnet/ping", `{}`, testToken); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("ping without an address: %d %s", r.StatusCode, b)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
