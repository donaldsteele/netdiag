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
	s := Summarize(r)
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
	s := Summarize(r)
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
		p, err := ListDevices(r, "all", "", "", 200, cursor)
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
		{IP: "10.0.0.1", Online: true, DeviceType: "router", Web: []model.WebService{{URL: "http://10.0.0.1"}}, MDNS: &model.MDNSInfo{Instance: "Gateway"}},
		{IP: "10.0.0.2", Online: true, IsPrinter: true, DeviceType: "printer", Printer: &model.PrinterInfo{State: "idle"}},
		{IP: "10.0.0.3", Online: true, IsPrinter: true, DeviceType: "printer", Printer: &model.PrinterInfo{Supplies: []model.Supply{{State: "low"}}}},
		{IP: "10.0.0.4", Online: false},
		{IP: "10.0.0.5", Online: true, Conflicts: []string{"c"}},
		{IP: "10.0.0.6", Online: true, IsNew: true, Hostname: "NewBox", Snmp: &model.SnmpInfo{SysName: "SwitchX"}},
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
		p, err := ListDevices(r, f, "", "", 0, "")
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
	if _, err := ListDevices(r, "bogus", "", "", 0, ""); err == nil {
		t.Fatal("expected unknown filter error")
	}
	p, _ := ListDevices(r, "all", "printer", "", 0, "")
	if p.Total != 2 {
		t.Fatalf("deviceType: %d", p.Total)
	}
	for q, want := range map[string]string{"newbox": "10.0.0.6", "switchx": "10.0.0.6", "GATEWAY": "10.0.0.1", "0.0.4": "10.0.0.4"} {
		p, _ := ListDevices(r, "", "", q, 0, "")
		if p.Total != 1 || p.Items[0].IP != want {
			t.Fatalf("query %q: %+v", q, p)
		}
	}
}

func TestInvalidCursor(t *testing.T) {
	r := bigScan()
	for _, c := range []string{"!!!", "bm9wZQ", "bzotMQ"} { // garbage, "nope", "o:-1"
		if _, err := ListDevices(r, "all", "", "", 10, c); err == nil {
			t.Fatalf("cursor %q should fail", c)
		}
	}
	p, err := ListDevices(r, "all", "", "", 1000, "")
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
