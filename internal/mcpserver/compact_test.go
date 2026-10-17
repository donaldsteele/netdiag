package mcpserver

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"netdiag/internal/model"
)

const secret = "s3cr3t-community"

func bigScan() model.ScanResult {
	r := model.ScanResult{
		ID: "scan1", State: model.StateDone,
		Request: model.ScanRequest{CIDR: "10.0.0.0/22", Community: secret},
		Stats:   model.Stats{Alive: 1024, Printers: 400},
		Warnings: func() []string {
			w := []string{}
			for i := 0; i < 50; i++ {
				w = append(w, strings.Repeat("w", 500))
			}
			return w
		}(),
		Health: &model.HealthReport{Checks: []model.HealthCheck{
			{ID: "dns", Status: "fail", Detail: "no answer"}, {ID: "wifi", Status: "warn"}, {ID: "gw", Status: "ok"},
		}},
	}
	for i := 0; i < 1024; i++ {
		h := model.Host{
			IP: fmt.Sprintf("10.0.%d.%d", i/256, i%256), MAC: fmt.Sprintf("AA:BB:CC:00:%02X:%02X", i/256, i%256),
			Alive: true, Online: true, Hostname: fmt.Sprintf("host-%d", i), OpenPorts: []int{80},
			DeviceType: "workstation",
		}
		if i%5 < 2 {
			h.IsPrinter = true
			h.DeviceType = "printer"
			p := &model.PrinterInfo{Model: strings.Repeat("M", 300), State: "stopped"}
			for j := 0; j < 20; j++ {
				p.Alerts = append(p.Alerts, model.Alert{Description: strings.Repeat("jam ", 100)})
				p.Supplies = append(p.Supplies, model.Supply{Description: "Toner", Percent: 3, State: "low"})
			}
			h.Printer = p
		}
		if i%10 == 0 {
			h.Conflicts = []string{fmt.Sprintf("c%d", i)}
			r.Conflicts = append(r.Conflicts, model.Conflict{
				ID: fmt.Sprintf("c%d", i), Kind: model.ConflictMACFlap, Severity: "warn",
				IPs: []string{h.IP}, MACs: []string{h.MAC}, Detail: strings.Repeat("d", 1000),
			})
		}
		if i%100 == 1 {
			h.Online = false
		}
		r.Hosts = append(r.Hosts, h)
	}
	// Shuffle-ish: reverse to prove sorting.
	for i, j := 0, len(r.Hosts)-1; i < j; i, j = i+1, j-1 {
		r.Hosts[i], r.Hosts[j] = r.Hosts[j], r.Hosts[i]
	}
	return r
}

func TestSummarizeBounded(t *testing.T) {
	r := bigScan()
	s := Summarize(r, 0)
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) >= 16384 {
		t.Fatalf("summary too large: %d", len(b))
	}
	if len(s.Conflicts) != 20 || !s.ConflictsTruncated || len(s.PrinterAlerts) != 20 || !s.PrinterAlertsTrunc || len(s.Warnings) != 10 {
		t.Fatalf("caps: %d %v %d %v %d", len(s.Conflicts), s.ConflictsTruncated, len(s.PrinterAlerts), s.PrinterAlertsTrunc, len(s.Warnings))
	}
	if s.Health.Summary != "1 fail, 1 warn" || len(s.Health.Failing) != 2 {
		t.Fatalf("health %+v", s.Health)
	}
	if !strings.Contains(s.NextStep, "list_devices") {
		t.Fatal(s.NextStep)
	}
	if strings.Contains(string(b), secret) {
		t.Fatal("secret leaked")
	}
}

func TestSummarizeStatesAndOK(t *testing.T) {
	r := model.ScanResult{State: model.StateRunning, Health: &model.HealthReport{Checks: []model.HealthCheck{{ID: "a", Status: "ok"}}}}
	s := Summarize(r, 0)
	if s.Health.Summary != "all ok" || !strings.Contains(s.NextStep, "still running") {
		t.Fatalf("%+v", s)
	}
	if s.Conflicts == nil || s.PrinterAlerts == nil || s.Warnings == nil {
		t.Fatal("nil slices")
	}
}

func TestPaginationCoversAll(t *testing.T) {
	r := bigScan()
	seen := map[string]int{}
	cursor, prev := "", ""
	pages := 0
	for {
		p, err := ListDevices(r, ListOptions{Filter: "all", Limit: 200, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if p.Total != 1024 {
			t.Fatalf("total %d", p.Total)
		}
		for _, it := range p.Items {
			if prev != "" && ipNum(prev) >= ipNum(it.IP) {
				t.Fatalf("order %s then %s", prev, it.IP)
			}
			prev = it.IP
			seen[it.IP]++
		}
		pages++
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if len(seen) != 1024 || pages != 6 {
		t.Fatalf("seen=%d pages=%d", len(seen), pages)
	}
	for ip, n := range seen {
		if n != 1 {
			t.Fatalf("%s seen %d times", ip, n)
		}
	}
}

func ipNum(s string) int {
	var a, b, c, d int
	fmt.Sscanf(s, "%d.%d.%d.%d", &a, &b, &c, &d)
	return ((a*256+b)*256+c)*256 + d
}

func TestFilters(t *testing.T) {
	r := model.ScanResult{ID: "x", Hosts: []model.Host{
		{IP: "10.0.0.1", Alive: true, Online: true, DeviceType: "router", Web: []model.WebService{{URL: "http://10.0.0.1", Status: 200}}, MDNS: &model.MDNSInfo{Instance: "Gateway", Services: []string{"_bubbles._tcp"},
			Details: []model.MDNSService{{Type: "_bubbles._tcp", Instance: "Team chat", Port: 28225}}}},
		{IP: "10.0.0.2", Alive: true, Online: true, IsPrinter: true, DeviceType: "printer", Printer: &model.PrinterInfo{State: "idle"}},
		{IP: "10.0.0.3", Alive: true, Online: true, IsPrinter: true, DeviceType: "printer", Printer: &model.PrinterInfo{Supplies: []model.Supply{{State: "low", Percent: -1}}}},
		{IP: "10.0.0.4", Alive: true, Online: false},
		{IP: "10.0.0.5", Alive: true, Online: true, Conflicts: []string{"c"}},
		{IP: "10.0.0.6", Alive: true, Online: true, IsNew: true, Hostname: "NewBox", Snmp: &model.SnmpInfo{SysName: "SwitchX"}},
	}}
	cases := map[string][]string{
		"all":       {"10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5", "10.0.0.6"},
		"printers":  {"10.0.0.2", "10.0.0.3"},
		"web":       {"10.0.0.1"},
		"offline":   {"10.0.0.4"},
		"conflicts": {"10.0.0.5"},
		"new":       {"10.0.0.6"},
		"mdns":      {"10.0.0.1"},
		"problems":  {"10.0.0.3", "10.0.0.4", "10.0.0.5"},
	}
	for f, want := range cases {
		p, err := ListDevices(r, ListOptions{Filter: f})
		if err != nil {
			t.Fatal(f, err)
		}
		var got []string
		for _, it := range p.Items {
			got = append(got, it.IP)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: got %v want %v", f, got, want)
		}
	}
	if _, err := ListDevices(r, ListOptions{Filter: "bogus"}); err == nil {
		t.Fatal("expected unknown filter error")
	}
	p, _ := ListDevices(r, ListOptions{Filter: "all", DeviceType: "printer"})
	if p.Total != 2 {
		t.Fatalf("deviceType: %d", p.Total)
	}
	for q, want := range map[string]string{"newbox": "10.0.0.6", "switchx": "10.0.0.6", "GATEWAY": "10.0.0.1", "0.0.4": "10.0.0.4",
		// mdns.services and the instance names in mdns.details are searched too.
		"_bubbles._tcp": "10.0.0.1", "team chat": "10.0.0.1"} {
		p, _ := ListDevices(r, ListOptions{Query: q})
		if p.Total != 1 || p.Items[0].IP != want {
			t.Fatalf("query %q: %+v", q, p)
		}
	}
}

// TestProblemsMatchesUI pins filter=problems to the UI's Problems tab
// (web/app.js isProblem), with the supply threshold taken from the caller.
func TestProblemsMatchesUI(t *testing.T) {
	printer := func(ip string, p model.PrinterInfo) model.Host {
		return model.Host{IP: ip, Alive: true, Online: true, IsPrinter: true, Printer: &p}
	}
	supply := func(pct int, state string) model.PrinterInfo {
		return model.PrinterInfo{Supplies: []model.Supply{{Description: "Black toner", Percent: pct, State: state}}}
	}
	web := func(ip string, w model.WebService) model.Host {
		return model.Host{IP: ip, Alive: true, Online: true, Web: []model.WebService{w}}
	}
	r := model.ScanResult{ID: "p",
		Conflicts: []model.Conflict{{ID: "k1", Kind: model.ConflictMACFlap, IPs: []string{"10.0.1.3"}}},
		Hosts: []model.Host{
			{IP: "10.0.1.1", Alive: true, Online: true},                            // healthy
			{IP: "10.0.1.2", Alive: true, Online: false},                           // offline
			{IP: "10.0.1.3", Alive: true, Online: true},                            // named by a conflict only
			{IP: "10.0.1.4", Alive: true, Online: true, Conflicts: []string{"k9"}}, // own conflict id
			{IP: "10.0.1.5", Alive: true, Online: false, IsSelf: true},             // this computer: never offline
			{IP: "10.0.1.6", Alive: false, Online: false},                          // never found alive
			printer("10.0.2.1", model.PrinterInfo{Alerts: []model.Alert{{Description: "Cover open"}}}),
			printer("10.0.2.2", supply(20, "ok")),      // above 15, at or below 25
			printer("10.0.2.3", supply(15, "ok")),      // at the default threshold
			printer("10.0.2.4", supply(-1, "unknown")), // unknown level
			printer("10.0.2.5", supply(-1, "empty")),
			printer("10.0.2.6", supply(40, "low")),                                                                              // device says low
			printer("10.0.2.7", model.PrinterInfo{State: "stopped"}),                                                            // the UI only counts alerts and supplies
			{IP: "10.0.2.8", Alive: true, Online: true, Printer: &model.PrinterInfo{Alerts: []model.Alert{{Description: "x"}}}}, // not a printer
			web("10.0.3.1", model.WebService{URL: "http://10.0.3.1", Status: 200}),
			web("10.0.3.2", model.WebService{URL: "http://10.0.3.2", Status: 301}),
			web("10.0.3.3", model.WebService{URL: "http://10.0.3.3", Status: 404}),
			web("10.0.3.4", model.WebService{URL: "https://10.0.3.4", Status: 503}),
			web("10.0.3.5", model.WebService{URL: "http://10.0.3.5", Error: "timeout"}),
			web("10.0.3.6", model.WebService{URL: "http://10.0.3.6"}), // no status
		}}
	base := []string{"10.0.1.2", "10.0.1.3", "10.0.1.4", "10.0.2.1", "10.0.2.3", "10.0.2.5", "10.0.2.6",
		"10.0.3.3", "10.0.3.4", "10.0.3.5", "10.0.3.6"}
	with20 := []string{"10.0.1.2", "10.0.1.3", "10.0.1.4", "10.0.2.1", "10.0.2.2", "10.0.2.3", "10.0.2.5", "10.0.2.6",
		"10.0.3.3", "10.0.3.4", "10.0.3.5", "10.0.3.6"}
	lowOnly := []string{"10.0.1.2", "10.0.1.3", "10.0.1.4", "10.0.2.1", "10.0.2.5", "10.0.2.6",
		"10.0.3.3", "10.0.3.4", "10.0.3.5", "10.0.3.6"}
	for _, c := range []struct {
		pct  int
		want []string
	}{{0, base}, {15, base}, {25, with20}, {20, with20}, {14, lowOnly}, {1, lowOnly}} {
		p, err := ListDevices(r, ListOptions{Filter: "problems", SupplyPct: c.pct})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, it := range p.Items {
			got = append(got, it.IP)
		}
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("threshold %d: got %v want %v", c.pct, got, c.want)
		}
	}
	for f, want := range map[string]string{"offline": "10.0.1.2", "conflicts": "10.0.1.3,10.0.1.4"} {
		p, _ := ListDevices(r, ListOptions{Filter: f})
		var got []string
		for _, it := range p.Items {
			got = append(got, it.IP)
		}
		if strings.Join(got, ",") != want {
			t.Errorf("%s: got %v want %s", f, got, want)
		}
	}
}

// TestSummarizePrinterAlertsMatchProblems pins the summary's printer
// alerts to the printers filter=problems lists (and so to the UI's
// Problems tab), with the same supply threshold.
func TestSummarizePrinterAlertsMatchProblems(t *testing.T) {
	printer := func(ip string, p model.PrinterInfo) model.Host {
		return model.Host{IP: ip, Alive: true, Online: true, IsPrinter: true, Printer: &p}
	}
	supply := func(pct int, state string) model.PrinterInfo {
		return model.PrinterInfo{Supplies: []model.Supply{{Description: "Black toner", Percent: pct, State: state}}}
	}
	r := model.ScanResult{ID: "pa", State: model.StateDone, Hosts: []model.Host{
		printer("10.0.2.1", model.PrinterInfo{State: "idle", Alerts: []model.Alert{{Description: "Cover open"}}}),
		printer("10.0.2.2", supply(20, "ok")),
		printer("10.0.2.3", supply(15, "ok")),
		printer("10.0.2.4", supply(-1, "unknown")),
		printer("10.0.2.5", supply(-1, "empty")),
		printer("10.0.2.6", supply(40, "low")),
		printer("10.0.2.7", model.PrinterInfo{State: "stopped"}),
		{IP: "10.0.2.8", Alive: true, Online: true, Printer: &model.PrinterInfo{Alerts: []model.Alert{{Description: "x"}}}}, // not a printer
		printer("10.0.2.9", model.PrinterInfo{Supplies: []model.Supply{
			{Description: "Cyan", Percent: -1, State: "low"}, {Description: "Magenta", Percent: 60, State: "ok"},
			{Description: "Yellow", Percent: 0, State: "empty"}}}),
	}}
	labels := map[string][]string{"10.0.2.1": {}, "10.0.2.2": {"Black toner 20%"}, "10.0.2.3": {"Black toner 15%"},
		"10.0.2.5": {"Black toner empty"}, "10.0.2.6": {"Black toner 40%"}, "10.0.2.9": {"Cyan low", "Yellow empty"}}
	for _, c := range []struct {
		pct  int
		want string
	}{
		{0, "10.0.2.1,10.0.2.3,10.0.2.5,10.0.2.6,10.0.2.9"},
		{15, "10.0.2.1,10.0.2.3,10.0.2.5,10.0.2.6,10.0.2.9"},
		{25, "10.0.2.1,10.0.2.2,10.0.2.3,10.0.2.5,10.0.2.6,10.0.2.9"},
		{14, "10.0.2.1,10.0.2.5,10.0.2.6,10.0.2.9"},
	} {
		var got []string
		for _, a := range Summarize(r, c.pct).PrinterAlerts {
			got = append(got, a.IP)
			if want := labels[a.IP]; strings.Join(a.LowSupplies, "|") != strings.Join(want, "|") {
				t.Errorf("threshold %d, %s: low supplies %q, want %q", c.pct, a.IP, a.LowSupplies, want)
			}
		}
		if strings.Join(got, ",") != c.want {
			t.Errorf("threshold %d: printer alerts %v, want %s", c.pct, got, c.want)
		}
		page, err := ListDevices(r, ListOptions{Filter: "problems", SupplyPct: c.pct})
		if err != nil {
			t.Fatal(err)
		}
		var problems []string
		for _, it := range page.Items {
			problems = append(problems, it.IP)
		}
		if strings.Join(problems, ",") != c.want {
			t.Errorf("threshold %d: problems %v, printer alerts %v", c.pct, problems, got)
		}
	}
}

func TestInvalidCursor(t *testing.T) {
	r := bigScan()
	for _, c := range []string{"!!!", "bm9wZQ", "bzotMQ"} { // garbage, "nope", "o:-1"
		if _, err := ListDevices(r, ListOptions{Filter: "all", Limit: 10, Cursor: c}); err == nil {
			t.Fatalf("cursor %q should fail", c)
		}
	}
	p, err := ListDevices(r, ListOptions{Filter: "all", Limit: 1000})
	if err != nil || len(p.Items) != 200 {
		t.Fatalf("limit clamp: %v %d", err, len(p.Items))
	}
}

func TestRedaction(t *testing.T) {
	r := bigScan()
	red := RedactScan(r)
	if r.Request.Community != secret {
		t.Fatal("input mutated")
	}
	b, _ := json.Marshal(red)
	if strings.Contains(string(b), secret) || red.Request.Community != Redacted {
		t.Fatal("not redacted")
	}
	if got := RedactSettings(model.Settings{Community: secret}).Community; got != Redacted {
		t.Fatal(got)
	}
	if got := RedactSettings(model.Settings{}).Community; got != "" {
		t.Fatal(got)
	}
	in := model.Settings{WebServer: model.SettingsWebServer{URL: "https://admin:pa55@nas.local/"}}
	if got := RedactSettings(in).WebServer.URL; got != "https://admin:xxxxx@nas.local/" {
		t.Fatalf("web server URL = %q", got)
	}
	if in.WebServer.URL != "https://admin:pa55@nas.local/" {
		t.Fatal("input mutated")
	}
	if got := RedactSettings(model.Settings{WebServer: model.SettingsWebServer{URL: "http://nas.local/"}}).WebServer.URL; got != "http://nas.local/" {
		t.Fatalf("URL without a password changed: %q", got)
	}
}

func TestDetail(t *testing.T) {
	r := bigScan()
	d, err := Detail(r, "10.0.0.0", "")
	if err != nil || d.Host.IP != "10.0.0.0" || len(d.Conflicts) != 1 {
		t.Fatalf("%v %+v", err, d.Conflicts)
	}
	d2, err := Detail(r, "", strings.ToLower(d.Host.MAC))
	if err != nil || d2.Host.IP != "10.0.0.0" {
		t.Fatalf("mac lookup: %v", err)
	}
	if _, err := Detail(r, "9.9.9.9", ""); err == nil {
		t.Fatal("expected not found")
	}
	if _, err := Detail(r, "", ""); err == nil {
		t.Fatal("expected arg error")
	}
}

func TestCapText(t *testing.T) {
	if CapText("short", 10) != "short" {
		t.Fatal("short")
	}
	got := CapText(strings.Repeat("é", 100), 51)
	if !strings.Contains(got, "truncated; use cursor/limit") || !strings.HasPrefix(got, strings.Repeat("é", 25)) {
		t.Fatalf("%q", got)
	}
	if len(CapText(strings.Repeat("a", 30000), 0)) > DefaultTextCap+40 {
		t.Fatal("default cap")
	}
}
