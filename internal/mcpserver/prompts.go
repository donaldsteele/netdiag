package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/service"
)

// PromptNames is the exact set of prompts the server registers.
var PromptNames = []string{"triage_network", "diagnose_printer", "explain_conflict", "device_missing", "what_changed"}

const promptFooter = "\n\nLimits: you cannot change settings, delete history, ignore conflicting MACs or quit netdiag. " +
	"If any of those would help, recommend the operator do it in the netdiag UI. " +
	"In your final answer use plain language suitable for non-technical staff: what is wrong, which device, and the next physical or UI action to take."

func registerPrompts(s *mcp.Server, _ *service.Service) {
	add := func(name, title, desc string, args []*mcp.PromptArgument, build func(a map[string]string) string) {
		s.AddPrompt(&mcp.Prompt{Name: name, Title: title, Description: desc, Arguments: args},
			func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
				a := map[string]string{}
				if req != nil && req.Params != nil {
					for k, v := range req.Params.Arguments {
						a[k] = strings.TrimSpace(v)
					}
				}
				for _, ar := range args {
					if ar.Required && a[ar.Name] == "" {
						return nil, fmt.Errorf("missing required argument %q", ar.Name)
					}
				}
				return &mcp.GetPromptResult{Description: desc, Messages: []*mcp.PromptMessage{
					{Role: "user", Content: &mcp.TextContent{Text: build(a) + promptFooter}},
				}}, nil
			})
	}

	add("triage_network", "Triage the network",
		"Scan the network and report what needs attention.",
		[]*mcp.PromptArgument{{Name: "cidr", Description: "network to scan, e.g. 10.0.0.0/24 (default: first interface)"}},
		func(a map[string]string) string {
			start := "start_scan with waitSec=60 (omit cidr to use the default interface)"
			if a["cidr"] != "" {
				start = fmt.Sprintf("start_scan with cidr=%q and waitSec=60", a["cidr"])
			}
			return "Triage this local network. Follow these steps:\n" +
				"1. Call " + start + ". If stillRunning is true, poll get_scan with the returned scanId until state is done.\n" +
				"2. Read the summary: stats (online/offline/new devices), health, conflicts and printer alerts.\n" +
				"3. Call get_health (or run_health_check if the report is missing or old). Note any check that is not ok: gateway, DNS, DHCP, internet, web server.\n" +
				"4. Call list_devices with scanId and filter=\"problems\". For each row call get_device (scanId, ip) and inspect conflicts, offline state, printer alerts and openPorts.\n" +
				"5. Interpret: a device seen only by ARP (no ping reply) usually blocks ping, it is present but filtered, not down. Conflicts of kind mac_flap, mac_multi_ip or duplicate_name often point to a Wi-Fi extender or mesh node, a rogue DHCP/router, or two devices sharing a name.\n" +
				"6. Summarise, most urgent first: health problems, conflicts, offline devices, printer problems."
		})

	add("diagnose_printer", "Diagnose a printer",
		"Check one printer: state, alerts, supplies and print ports.",
		[]*mcp.PromptArgument{{Name: "ip", Description: "printer IPv4 address", Required: true}},
		func(a map[string]string) string {
			ip := a["ip"]
			return fmt.Sprintf("Diagnose the printer at %s. Follow these steps:\n"+
				"1. Call list_history (limit=1) or start_scan to get a scanId that includes the printer.\n"+
				"2. Call get_device with scanId and ip=%q. Inspect the printer block: state, alerts, and supplies (percent for each toner/ink/drum; below about 15%% is low).\n"+
				"3. Check openPorts: 9100 (raw printing) and 631 (IPP) should be open on a working network printer. If both are closed the printer may be asleep, offline, or have printing services disabled.\n"+
				"4. Call probe_host with ip=%q for a fresh live check (sends real traffic, saves nothing) and compare with the scan.\n"+
				"5. Call get_events with the scanId to see recent printer changes (state, alerts, supplies).\n"+
				"6. If the device is offline, call list_devices with filter=\"conflicts\" to see whether its IP or MAC is in a conflict.\n"+
				"7. Conclude: working, degraded (low supplies or alerts) or unreachable, with the physical action (paper, toner, power, cable).", ip, ip, ip)
		})

	add("explain_conflict", "Explain a conflict",
		"Explain the IP/MAC/name conflict involving one device.",
		[]*mcp.PromptArgument{{Name: "ip", Description: "IPv4 address of a device involved in a conflict", Required: true}},
		func(a map[string]string) string {
			ip := a["ip"]
			return fmt.Sprintf("Explain the conflict involving %s. Follow these steps:\n"+
				"1. Get a scanId from list_history (limit=1) or start_scan.\n"+
				"2. Call get_device with scanId and ip=%q; read its conflicts list. Also call list_devices with filter=\"conflicts\" to see the other devices involved.\n"+
				"3. Interpret by kind: mac_flap means one MAC moves between IPs or the same IP answers from different MACs (a roaming device, Wi-Fi extender or mesh node, or an address clash); "+
				"mac_multi_ip means one MAC owns several IPs (often an extender/mesh node, router or a multi-homed device, usually harmless); "+
				"duplicate_name means two devices share a hostname (cloned machines or misconfigured devices).\n"+
				"4. Call compare_scans (currentId = this scanId) to see whether the conflict is new since the previous scan.\n"+
				"5. Say which devices are involved (IP, MAC, vendor, hostname) and whether it is likely harmless or needs action. If it is a known harmless extender/mesh MAC, recommend the operator ignore that MAC in the netdiag UI.", ip, ip)
		})

	add("device_missing", "Find a missing device",
		"Work out why a device is not visible.",
		[]*mcp.PromptArgument{{Name: "nameOrIp", Description: "hostname, vendor text, MAC fragment or IPv4 address", Required: true}},
		func(a map[string]string) string {
			q := a["nameOrIp"]
			return fmt.Sprintf("Find out why %q is missing. Follow these steps:\n"+
				"1. Call list_history (limit=5) to find the latest scan, or start_scan if there is none.\n"+
				"2. Call list_devices with scanId and query=%q and filter=\"all\" (the query matches ip, mac, hostname and vendor). Also try filter=\"offline\".\n"+
				"3. If it is listed but offline, call get_device for last seen time and conflicts. A device seen only by ARP blocks ping but is present.\n"+
				"4. If it looks like an IP address and is not listed, call probe_host with that ip for a live check (it must be on a local subnet).\n"+
				"5. Call compare_scans with currentId = latest scan to see if it was removed since the previous scan.\n"+
				"6. Check get_health: a gateway, DHCP or Wi-Fi problem can make many devices disappear at once.\n"+
				"7. Report: present, offline since when, never seen, or moved to another IP (check conflicts), with suggested physical checks (power, cable, Wi-Fi network).", q, q)
		})

	add("what_changed", "What changed between scans",
		"Compare two scans and explain the differences.",
		[]*mcp.PromptArgument{
			{Name: "baseId", Description: "baseline scan id (default: previous scan of the same network)"},
			{Name: "currentId", Description: "scan id to compare (default: latest scan)"},
		},
		func(a map[string]string) string {
			cur := "the latest scan id from list_history (limit=2)"
			if a["currentId"] != "" {
				cur = fmt.Sprintf("%q", a["currentId"])
			}
			base := "omit baseId so it defaults to the previous scan of the same CIDR"
			if a["baseId"] != "" {
				base = fmt.Sprintf("baseId=%q", a["baseId"])
			}
			return "Explain what changed between two scans. Follow these steps:\n" +
				"1. Call compare_scans with currentId = " + cur + " and " + base + ".\n" +
				"2. Read summary counts, then added (new devices), removed (gone devices) and changed (IP, hostname, port, printer or online-state changes). Rows are capped at 200; note any *Truncated flag.\n" +
				"3. For notable devices call get_device with the current scanId and ip.\n" +
				"4. Group findings: new devices, missing devices, changed devices, new conflicts.\n" +
				"5. For a scan started in this session, call get_events to see the timeline."
		})
}
