package mcpserver

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPrompts(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	pl, err := e.cs.ListPrompts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range pl.Prompts {
		got = append(got, p.Name)
	}
	slices.Sort(got)
	want := slices.Clone(PromptNames)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("prompts = %v, want %v", got, want)
	}

	cases := []struct {
		name string
		args map[string]string
		must []string
	}{
		{"triage_network", map[string]string{"cidr": "10.0.0.0/24"}, []string{"start_scan", "10.0.0.0/24", "list_devices", "get_device", "get_health", "mac_flap", "ARP"}},
		{"diagnose_printer", map[string]string{"ip": "10.0.0.50"}, []string{"get_device", "10.0.0.50", "9100", "631", "supplies", "probe_host", "get_events"}},
		{"explain_conflict", map[string]string{"ip": "10.0.0.50"}, []string{"mac_flap", "mac_multi_ip", "duplicate_name", "compare_scans", "extender"}},
		{"device_missing", map[string]string{"nameOrIp": "printer-2"}, []string{"printer-2", "list_devices", "compare_scans", "probe_host"}},
		{"what_changed", map[string]string{"baseId": "a1", "currentId": "b2"}, []string{"compare_scans", "a1", "b2"}},
	}
	for _, c := range cases {
		res, err := e.cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: c.name, Arguments: c.args})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if res.Description == "" || len(res.Messages) != 1 || res.Messages[0].Role != "user" {
			t.Fatalf("%s: bad result %+v", c.name, res)
		}
		txt := res.Messages[0].Content.(*mcp.TextContent).Text
		for _, m := range c.must {
			if !strings.Contains(txt, m) {
				t.Errorf("%s: text missing %q", c.name, m)
			}
		}
		if !strings.Contains(txt, "netdiag UI") {
			t.Errorf("%s: missing UI reminder", c.name)
		}
	}
	// optional args may be omitted
	for _, n := range []string{"triage_network", "what_changed"} {
		if _, err := e.cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: n}); err != nil {
			t.Errorf("%s without args: %v", n, err)
		}
	}
}

func TestPromptMissingRequired(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	for _, n := range []string{"diagnose_printer", "explain_conflict", "device_missing"} {
		if _, err := e.cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: n}); err == nil {
			t.Errorf("%s: expected error without required arg", n)
		}
		if _, err := e.cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: n, Arguments: map[string]string{"ip": " "}}); err == nil {
			t.Errorf("%s: expected error with blank arg", n)
		}
	}
}
