package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"netdiag/internal/model"
	"netdiag/internal/tailscale"
)

// fakeTailnet is a TailnetSource with a settable state.
type fakeTailnet struct {
	mu     sync.Mutex
	st     tailscale.Status
	err    error
	reads  int
	pinged []string
}

func (f *fakeTailnet) Status(context.Context) (tailscale.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.st, f.err
}

func (f *fakeTailnet) Ping(_ context.Context, ip string) (tailscale.PingResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pinged = append(f.pinged, ip)
	return tailscale.PingResult{IP: ip, Name: "nas", OK: true, Latency: 1.2, Path: tailscale.PathDirect, Endpoint: "192.168.1.50:41641"}, nil
}

var tnow = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

func runningTailnet() tailscale.Status {
	return tailscale.Status{
		State: tailscale.StateRunning, Version: "1.90.0", Tailnet: "someone@example.com", MagicDNS: true, Suffix: "tail1234.ts.net",
		Self: &tailscale.Device{ID: "self", Name: "office-pc", OS: "windows", IPs: []string{"100.101.102.103"}, Online: true,
			KeyExpiry: tnow.Add(90 * 24 * time.Hour)},
		Devices: []tailscale.Device{
			{ID: "n1", Name: "nas", OS: "linux", IPs: []string{"100.64.12.34"}, Online: true, Path: tailscale.PathDirect, Endpoint: "192.168.1.50:41641"},
			{ID: "p1", Name: "phone", OS: "android", IPs: []string{"100.64.0.20"}, Path: tailscale.PathOffline, LastSeen: tnow.Add(-time.Hour)},
			{ID: "v1", Name: "cloud-vm", OS: "linux", IPs: []string{"100.70.1.2"}, Online: true, Path: tailscale.PathRelayed, Relay: "fra",
				KeyExpiry: tnow.Add(3 * 24 * time.Hour)},
		},
		Health: []string{},
	}
}

func TestTailnetView(t *testing.T) {
	f := &fakeTailnet{st: runningTailnet()}
	clock := tnow
	s := New(Config{Tailnet: f, Now: func() time.Time { return clock }})
	v := s.Tailnet(context.Background())
	if v.State != model.TailnetRunning || v.Suffix != "tail1234.ts.net" || v.AdminTarget == "" || len(v.Devices) != 3 {
		t.Fatalf("view %+v", v)
	}
	// The tailnet's own name (an email address for personal tailnets) is
	// never in the view.
	if strings.Contains(strings.ToLower(jsonOf(t, v)), "example.com") {
		t.Fatal("tailnet name leaked into the view")
	}
	by := map[string]model.TailnetDevice{}
	for _, d := range v.Devices {
		by[d.Name] = d
	}
	if !by["phone"].Phone || by["nas"].Phone || !by["cloud-vm"].KeyExpiresSoon || by["nas"].KeyExpiresSoon || by["phone"].LastSeen == "" {
		t.Fatalf("devices %+v", by)
	}
	if v.Card.ID != "tailscale" || v.Card.Status != model.HealthOK || !strings.Contains(v.Card.Detail, "2 of 3 other devices online") {
		t.Fatalf("card %+v", v.Card)
	}
	// Cached for a few seconds.
	s.Tailnet(context.Background())
	if f.reads != 1 {
		t.Fatalf("not cached: %d reads", f.reads)
	}
	clock = clock.Add(5 * time.Second)
	s.Tailnet(context.Background())
	if f.reads != 2 {
		t.Fatalf("stale cache: %d reads", f.reads)
	}
}

func TestTailnetCard(t *testing.T) {
	cases := []struct {
		name   string
		st     tailscale.Status
		err    error
		status string
		detail string
	}{
		{"not installed", tailscale.Status{State: tailscale.StateNotInstalled}, nil, model.HealthInfo, "isn't on this computer"},
		{"stopped", tailscale.Status{State: tailscale.StateStopped}, nil, model.HealthWarn, "not connected"},
		{"needs login", tailscale.Status{State: tailscale.StateNeedsLogin}, nil, model.HealthWarn, "sign in"},
		{"unreadable", tailscale.Status{}, errors.New("tailscale: 403 Forbidden: access denied"), model.HealthUnavailable, "couldn't read"},
	}
	for _, c := range cases {
		v := TailnetViewOf(c.st, c.err, tnow)
		if v.Card.Status != c.status || !strings.Contains(v.Card.Detail, c.detail) {
			t.Errorf("%s: %+v", c.name, v.Card)
		}
		if c.status == model.HealthWarn && (v.Card.Advice == nil || v.Card.Advice.Meaning == "" || len(v.Card.Advice.Steps) == 0 || len(v.Card.Advice.Steps) > 3) {
			t.Errorf("%s: advice %+v", c.name, v.Card.Advice)
		}
	}
	st := runningTailnet()
	st.Self.KeyExpiry = tnow.Add(2 * 24 * time.Hour)
	if v := TailnetViewOf(st, nil, tnow); v.Card.Status != model.HealthWarn || !strings.Contains(v.Card.Detail, "expires in 2 days") {
		t.Fatalf("key expiring: %+v", v.Card)
	}
	st = runningTailnet()
	st.Health = []string{"Some peers are advertising routes but --accept-routes is false"}
	if v := TailnetViewOf(st, nil, tnow); v.Card.Status != model.HealthWarn || !strings.HasPrefix(v.Card.Advice.Technical, "raw") {
		t.Fatalf("health warning: %+v", v.Card)
	}
}

func TestPingTailnet(t *testing.T) {
	f := &fakeTailnet{st: runningTailnet()}
	clock := tnow
	s := New(Config{Tailnet: f, Now: func() time.Time { return clock }})
	res, err := s.PingTailnet(context.Background(), "100.64.12.34")
	if err != nil || !res.OK || res.Path != model.TailnetPathDirect || res.LatencyMs != 1.2 {
		t.Fatalf("ping %+v %v", res, err)
	}
	if _, err := s.PingTailnet(context.Background(), "100.70.1.2"); !errors.Is(err, ErrTailnetPingTooSoon) {
		t.Fatalf("second ping within a second: %v", err)
	}
	clock = clock.Add(2 * time.Second)
	if _, err := s.PingTailnet(context.Background(), "100.64.12.34"); !errors.Is(err, ErrTailnetPingTooSoon) {
		t.Fatalf("same device within 5 s: %v", err)
	}
	if _, err := s.PingTailnet(context.Background(), "100.70.1.2"); err != nil {
		t.Fatalf("another device after a second: %v", err)
	}
	clock = clock.Add(10 * time.Second)
	for _, bad := range []string{"192.168.1.1", "100.99.99.99", "100.101.102.103"} {
		if _, err := s.PingTailnet(context.Background(), bad); !errors.Is(err, ErrNotTailscaleAddr) {
			t.Errorf("ping %s: %v", bad, err)
		}
	}
	if len(f.pinged) != 2 {
		t.Fatalf("pinged %v", f.pinged)
	}
	stopped := New(Config{Tailnet: &fakeTailnet{st: tailscale.Status{State: tailscale.StateStopped}}})
	if _, err := stopped.PingTailnet(context.Background(), "100.64.12.34"); err == nil {
		t.Fatal("ping while Tailscale is stopped")
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
