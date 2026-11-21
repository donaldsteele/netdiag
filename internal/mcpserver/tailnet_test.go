package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"netdiag/internal/model"
	"netdiag/internal/tailscale"
)

// fakeTailnet is the tailnet every MCP test sees (never the real one).
type fakeTailnet struct{}

func (fakeTailnet) Status(context.Context) (tailscale.Status, error) {
	return tailscale.Status{State: tailscale.StateRunning, Suffix: "tail1234.ts.net", Tailnet: "someone@example.com",
		Self:    &tailscale.Device{ID: "self", Name: "office-pc", IPs: []string{"100.101.102.103"}, Online: true},
		Devices: []tailscale.Device{{ID: "n1", Name: "nas", OS: "linux", IPs: []string{"100.64.12.34"}, Online: true, Path: tailscale.PathIdle}},
		Health:  []string{}}, nil
}

func (fakeTailnet) Ping(_ context.Context, ip string) (tailscale.PingResult, error) {
	return tailscale.PingResult{IP: ip, Name: "nas", OK: true, Latency: 1.5, Path: tailscale.PathDirect, Endpoint: "192.168.1.50:41641"}, nil
}

func TestTailnetTools(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	var v model.TailnetView
	e.ok(t, "get_tailnet", nil, &v)
	if v.State != model.TailnetRunning || v.Suffix != "tail1234.ts.net" || len(v.Devices) != 1 || v.Card.ID != "tailscale" {
		t.Fatalf("get_tailnet %+v", v)
	}
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "example.com") {
		t.Fatal("get_tailnet leaks the tailnet's name")
	}
	var p model.TailnetPing
	e.ok(t, "ping_tailnet_device", map[string]any{"ip": "100.64.12.34"}, &p)
	if !p.OK || p.Path != model.TailnetPathDirect || p.LatencyMs != 1.5 {
		t.Fatalf("ping %+v", p)
	}
	res := e.call(t, "ping_tailnet_device", map[string]any{"ip": "100.64.12.34"})
	if !res.IsError || !strings.Contains(textOf(res), "wait 5 seconds") {
		t.Fatalf("second ping: %+v", res)
	}
	res = e.call(t, "ping_tailnet_device", map[string]any{"ip": "10.0.0.10"})
	if !res.IsError || !strings.Contains(textOf(res), "get_tailnet") {
		t.Fatalf("non-tailnet address: %+v", res)
	}
}
