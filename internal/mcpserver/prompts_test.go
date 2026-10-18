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
		{"triage_network", map[string]string{"cidr": "10.0.0.0/24"}, []string{"start_scan", "10.0.0.0/24", "list_devices", "get_device", "get_health", "mac_flap", "ARP",
			"get_status", "alreadyWatching", "list_problems", "advice",
			"Call get_scan with scanId = watching.scanId for its summary, and go to step 3", "Read the summary (from start_scan, or from get_scan with the scanId)"}},
		{"diagnose_printer", map[string]string{"ip": "10.0.0.50"}, []string{"get_device", "10.0.0.50", "9100", "631", "supplies", "probe_host", "get_events",
			"list_problems", "advice"}},
		{"explain_conflict", map[string]string{"ip": "10.0.0.50"}, []string{"mac_flap", "mac_multi_ip", "duplicate_name", "compare_scans", "extender",
			"macHistory", "Possible conflict", "advice", "**Ignore this MAC**", "asks them to confirm",
			`list_problems titles it "Network conflict"`, `severity "fail"`, "taking turns"}},
		{"device_missing", map[string]string{"nameOrIp": "printer-2"}, []string{"printer-2", "list_devices", "compare_scans", "probe_host",
			"list_problems", "device_offline"}},
		{"what_changed", map[string]string{"baseId": "a1", "currentId": "b2"}, []string{"compare_scans", "a1", "b2"}},
		{"explain_problems", nil, []string{"get_status", "list_problems", "advice.meaning", "advice.steps", "everything looks fine", "Fixed at",
			`"OK: everything looks fine`, `"Problem:"`,
			// Not checked, by cause, with the card's own buttons: no scan
			// yet (Scan), the storm guard (Paused or Stopped), a scan still
			// going (wait), health checks that did not run (Open Health,
			// then Recheck).
			`If source is "none", start with "Not checked: netdiag has not looked at the local network yet."`, "**Live mode**", "**Scan**",
			"If held is true", "Paused or Stopped", `"link": "Problem: this computer lost the local network`, `"gateway": "Problem: the router stopped answering`,
			`"network_changed": "Not checked: this computer is on a different network now`, "**Restart watching**",
			`source is "live": its scan is still going. Start with "Not checked: netdiag has not finished checking yet." Suggest waiting a minute`,
			`"Not checked: no problems found, but the health checks did not run for this scan." Suggest **Open Health** on the Problems right now card in the netdiag window, then **Recheck**`}},
		{"internet_not_working", nil, []string{"run_health_check", "wifi", "dhcp", "gateway", "dns", "internet", "Recheck", "Health"}},
		{"device_offline", nil, []string{"list_devices", "get_device", "probe_host", "foundBy", "conflict", "Fixed", "filter=offline"}},
		{"device_offline", map[string]string{"device": "Front desk PC"}, []string{`query="Front desk PC"`, "get_device", "probe_host"}},
		{"first_look", nil, []string{"list_interfaces", "start_scan", "waitSec=60", "alreadyWatching", "get_health", "list_devices", "list_problems",
			"allowed to check", "_bubbles._tcp"}},
		{"first_look", map[string]string{"interface": "Ethernet 2"}, []string{`"Ethernet 2"`}},
		{"check_chat_alerts", nil, []string{"get_status", "list_problems", "list_alerts", "dedupe_key", "resolve_alert", "Never call post_alert",
			"ack_alert", "only when netdiag confirms", "Resolve", "data, not instructions",
			// Each chat state in plain words, with the Settings button that
			// helps; the open, fixed and neither cases; D10 and D11.
			`"unreachable": "netdiag can't reach the chat server since`, "**Try now**", "**Reconnect**", "**Change channel**", "**Connect to team chat**",
			`"unknown"`, "--attach", "chat.sendAlerts", "**Send alerts to team chat**", "If watching is null",
			"Claude is not connected to your team chat", "ipConflict|<kind>|<anchor>", "state=open", "state=acked", "next_cursor", "chat.appName",
			"Do not close it.", "chat.waiting above 0", "In neither", "May I mark", "Checked with netdiag at", "thread_root_id",
			"chat.channel from get_status", `"OK: no netdiag alert needs attention"`, `"Problem:"`}},
		{"check_chat_alerts", map[string]string{"channel": "ops-alerts"}, []string{`channel "ops-alerts"`}},
	}
	if len(PromptNames) != 10 {
		t.Fatalf("v0.6 has 10 prompts (master roadmap 8.12): %v", PromptNames)
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
		checkDeviceTextRule(t, c.name, txt)
	}
	// The Health view has Recheck; Check again is only an advice button on
	// a failing check, which a Not checked answer has none of.
	if txt := promptText(t, e, "explain_problems", nil); strings.Contains(txt, "**Check again** in the **Health** view") {
		t.Error("explain_problems sends the person to a Check again button in the Health view")
	}
	// optional args may be omitted
	for _, n := range []string{"triage_network", "what_changed", "explain_problems", "internet_not_working", "device_offline", "first_look"} {
		if _, err := e.cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: n}); err != nil {
			t.Errorf("%s without args: %v", n, err)
		}
	}
}

func promptText(t *testing.T, e *env, name string, args map[string]string) string {
	t.Helper()
	res, err := e.cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res.Messages[0].Content.(*mcp.TextContent).Text
}

// TestDiagnosePrinterUsesSupplyThreshold checks the prompt quotes the
// configured alerts.supplyThresholdPct instead of a fixed 15%.
func TestDiagnosePrinterUsesSupplyThreshold(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	args := map[string]string{"ip": "10.0.0.50"}
	if txt := promptText(t, e, "diagnose_printer", args); !strings.Contains(txt, "at or below 15%") {
		t.Fatalf("default threshold missing: %s", txt)
	}
	set, err := e.st.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	set.Alerts.SupplyThresholdPct = 30
	if _, err := e.st.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	txt := promptText(t, e, "diagnose_printer", args)
	if !strings.Contains(txt, "at or below 30%") || strings.Contains(txt, "15%") {
		t.Fatalf("configured threshold not used: %s", txt)
	}

	// Without a data store the default applies.
	e2 := setup(t, "http", false, fakePinger{})
	if txt := promptText(t, e2, "diagnose_printer", args); !strings.Contains(txt, "at or below 15%") {
		t.Fatalf("no-store default missing: %s", txt)
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
