package problems

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"netdiag/internal/model"
	"netdiag/internal/notify"
	"netdiag/internal/store"
)

// golden is testdata/problems/hosts.json, which the page's hostIssues
// (tools/uitest problems.spec) is tested against too.
type golden struct {
	Conflicts []model.Conflict `json:"conflicts"`
	Hosts     []model.Host     `json:"hosts"`
	Cases     []struct {
		Name      string              `json:"name"`
		Threshold int                 `json:"supplyThresholdPct"`
		Issues    map[string][]string `json:"issues"`
	} `json:"cases"`
	Card []cardItem `json:"card"`
}

type cardItem struct {
	Key      string `json:"key"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	IP       string `json:"ip,omitempty"`
}

func readGolden(t *testing.T) golden {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "problems", "hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Hosts) == 0 || len(g.Cases) == 0 {
		t.Fatal("golden file has no hosts or cases")
	}
	return g
}

// TestHostIssuesGolden: HostIssues gives every golden case's issues, in
// order, so the Problems tab (whose hostIssues the uitest checks against
// the same file) and MCP filter=problems agree.
func TestHostIssuesGolden(t *testing.T) {
	g := readGolden(t)
	for _, c := range g.Cases {
		set := store.DefaultSettings()
		set.Alerts.SupplyThresholdPct = c.Threshold
		got := map[string][]string{}
		for _, h := range g.Hosts {
			for _, is := range HostIssues(h, set, g.Conflicts) {
				got[h.IP] = append(got[h.IP], is.Kind)
			}
		}
		if !reflect.DeepEqual(got, c.Issues) {
			t.Errorf("%s:\n got %v\nwant %v", c.Name, got, c.Issues)
		}
	}
}

// TestCurrentGolden: with the default Alerts settings the card lists the
// golden card items, most urgent first. The offline phone is on the
// Problems tab (TestHostIssuesGolden) but not on the card.
func TestCurrentGolden(t *testing.T) {
	g := readGolden(t)
	set := store.DefaultSettings()
	got := []cardItem{}
	for _, p := range Current(model.ScanResult{Hosts: g.Hosts, Conflicts: g.Conflicts}, set) {
		it := cardItem{Key: p.Key, Kind: p.Kind, Severity: p.Severity, Title: p.Title}
		if p.Device != nil {
			it.IP = p.Device.IP
		}
		got = append(got, it)
	}
	if !reflect.DeepEqual(got, g.Card) {
		b, _ := json.Marshal(got)
		t.Errorf("card:\n got %s", b)
	}
	for _, it := range got {
		if it.IP == "10.0.1.2" {
			t.Errorf("the offline phone is on the card: %+v", it)
		}
	}
}

// TestCurrentKeysMatchEngine: the card's keys are the alert engine's for
// the same problems, so a card item, its banner and its chat card share
// one key.
func TestCurrentKeysMatchEngine(t *testing.T) {
	g := readGolden(t)
	e := notify.New(notify.DefaultConfig(), nil)
	engine := map[string]bool{}
	for _, cur := range g.Hosts {
		if !cur.Alive || cur.IsSelf {
			continue
		}
		// Before: online, with no alerts and every supply full.
		prev := cur
		prev.Online = true
		if cur.Printer != nil {
			pp := *cur.Printer
			pp.Alerts = nil
			pp.Supplies = nil
			for _, s := range cur.Printer.Supplies {
				s.Percent, s.State = 90, "ok"
				pp.Supplies = append(pp.Supplies, s)
			}
			prev.Printer = &pp
		}
		for _, n := range e.Observe(prev, cur) {
			engine[n.Key] = true
		}
	}
	for _, n := range e.ObserveConflicts(g.Conflicts, true) {
		engine[n.Key] = true
	}
	// The engine opens a supply only when its level crosses the threshold;
	// the card also lists a supply the printer itself calls low or empty.
	crossed := map[string]bool{}
	for _, h := range g.Hosts {
		if h.Printer != nil {
			for _, s := range h.Printer.Supplies {
				if s.Percent >= 0 && s.Percent <= notify.DefaultSupplyThresholdPct {
					crossed[h.IP] = true
				}
			}
		}
	}
	for _, p := range Current(model.ScanResult{Hosts: g.Hosts, Conflicts: g.Conflicts}, store.DefaultSettings()) {
		if p.Kind == model.TriggerSupplyLow && !crossed[p.Device.IP] {
			continue
		}
		if !engine[p.Key] {
			t.Errorf("card key %s is not the engine's (%v)", p.Key, engine)
		}
	}
}

// TestUITestFixtureProblems: the UI harness's card items for its fixture
// scan (tools/uitest/fixtures/index.mjs problemItems) are what Current
// gives for the same devices and conflict, so the card specs show what
// netdiag really sends.
func TestUITestFixtureProblems(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "tools", "uitest", "fixtures", "index.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	brother := model.Host{IP: "192.168.2.10", Alive: true, Online: true, IsPrinter: true, MAC: "00:1B:A9:12:34:56",
		Snmp: &model.SnmpInfo{SysName: "BRW001BA9123456"},
		Printer: &model.PrinterInfo{Model: "HL-L2350DW",
			Supplies: []model.Supply{{Description: "Black Toner", Percent: 15, State: "low"}, {Description: `Drum, "Long Life"`, Percent: -1, State: "unknown"}},
			Alerts:   []model.Alert{{Severity: "warning", Code: "toner-low", Description: "Toner low, replace soon"}}}}
	epson := model.Host{IP: "192.168.2.20", Alive: true, Online: true, IsPrinter: true, MAC: "64:EB:8C:20:00:20",
		Printer: &model.PrinterInfo{Model: "WF-4745",
			Supplies: []model.Supply{{Description: "Cyan Ink", Percent: 0, State: "empty"}, {Description: "Magenta Ink", Percent: 70, State: "ok"}}}}
	nas := model.Host{IP: "192.168.2.30", Alive: true, Online: true, MAC: "00:1B:A9:AA:00:30", Conflicts: []string{"c-1"}}
	c := model.Conflict{ID: "c-1", Kind: model.ConflictMACMultiIP, Severity: "warn", IPs: []string{"192.168.2.30", "192.168.2.31"},
		MACs: []string{"00:1B:A9:AA:00:30"}}
	ps := Current(model.ScanResult{Hosts: []model.Host{brother, epson, nas}, Conflicts: []model.Conflict{c}}, store.DefaultSettings())
	if len(ps) != 4 {
		t.Fatalf("items: %v", kinds(ps))
	}
	for _, p := range ps {
		want := "key: '" + p.Key + "', kind: '" + p.Kind + "', severity: '" + p.Severity + "', title: '" + p.Title + "'"
		if !strings.Contains(string(src), want) {
			t.Errorf("fixtures lack %s", want)
		}
		if p.Kind != model.TriggerIPConflict && !strings.Contains(string(src), "detail: '"+p.Detail+"', device: { ") {
			t.Errorf("fixtures lack the detail %q", p.Detail)
		}
	}
}

func hc(id, status, reason string) model.HealthCheck {
	c := model.HealthCheck{ID: id, Label: id, Status: status}
	if reason != "" {
		c.Data = map[string]any{"reason": reason}
	}
	return c
}

// fineHealth is a report whose six checks are all fine.
func fineHealth() *model.HealthReport {
	return &model.HealthReport{CheckedAt: "2026-10-17T10:42:00Z", Checks: []model.HealthCheck{
		hc("gateway", model.HealthOK, ""), hc("dhcp", model.HealthOK, ""), hc("dns", model.HealthOK, ""),
		hc("webserver", model.HealthInfo, model.ReasonNotSet), hc("internet", model.HealthOK, ""), hc("wifi", model.HealthOK, "")}}
}

func kinds(ps []Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Kind+":"+p.Severity)
	}
	return out
}

// TestCurrentRules: each item follows its Alerts trigger and the severity
// map; health warnings are notes and checks that could not run are warnings.
func TestCurrentRules(t *testing.T) {
	phone := model.Host{IP: "10.0.0.7", Alive: true, Online: false, MAC: "02:00:5e:00:00:07", OfflineSince: "2026-10-17T10:30:00Z"}
	printer := model.Host{IP: "10.0.0.9", Alive: true, Online: false, IsPrinter: true, MAC: "02:00:5E:00:00:09"}
	h := fineHealth()
	h.Checks[0] = hc("gateway", model.HealthFail, model.ReasonNoReply)
	h.Checks[2] = hc("dns", model.HealthFail, model.ReasonNoReply)
	h.Checks[5] = hc("wifi", model.HealthWarn, model.ReasonWeak)
	h.Checks[4] = hc("internet", model.HealthUnavailable, model.ReasonTimeout)
	r := model.ScanResult{Health: h, Hosts: []model.Host{phone, printer}}

	set := store.DefaultSettings()
	ps := Current(r, set)
	want := []string{"healthFailed:critical", "healthFailed:warning", "healthFailed:warning", "printerOffline:warning", "healthFailed:info"}
	if got := kinds(ps); !reflect.DeepEqual(got, want) {
		t.Fatalf("default triggers: %v", got)
	}
	if ps[0].Key != notify.HealthKey("gateway") || ps[0].Check != "gateway" || ps[0].Title != "Health check failed" {
		t.Errorf("gateway item: %+v", ps[0])
	}
	if ps[2].Title != TitleHealthNotRun || ps[4].Title != TitleHealthWarn {
		t.Errorf("titles: %q %q", ps[2].Title, ps[4].Title)
	}
	if ps[3].Key != "printerOffline|02:00:5E:00:00:09|" || ps[3].Device.IP != printer.IP {
		t.Errorf("printer item: %+v", ps[3])
	}
	if ps[0].Advice == nil || ps[3].Advice == nil {
		t.Errorf("advice missing: %+v %+v", ps[0].Advice, ps[3].Advice)
	}

	// Any device goes offline on: the phone is a note, with its key and start.
	set.Alerts.Triggers.DeviceOffline = true
	ps = Current(r, set)
	last := ps[len(ps)-1]
	if last.Kind != model.TriggerDeviceOffline || last.Severity != SevInfo || last.Key != "deviceOffline|02:00:5E:00:00:07|" ||
		last.Since != phone.OfflineSince {
		t.Errorf("phone with deviceOffline on: %+v", last)
	}

	// Every trigger off: nothing.
	set.Alerts.Triggers = model.SettingsTriggers{}
	if ps := Current(r, set); len(ps) != 0 {
		t.Errorf("triggers off: %v", kinds(ps))
	}
}

// TestCurrentPrinterAndConflicts: a printer's general offline flag next to
// a stopping error is part of that error (one item); supplies follow the
// threshold; a mac_flap is critical only when the swap was seen.
func TestCurrentPrinterAndConflicts(t *testing.T) {
	zebra := model.Host{IP: "10.0.0.5", Alive: true, Online: true, IsPrinter: true, MAC: "02:00:5E:00:00:05",
		Printer: &model.PrinterInfo{Model: "ZD621", Alerts: []model.Alert{
			{Severity: "critical", Code: "hrPrinterDetectedErrorState.doorOpen", Description: "Cover open"},
			{Severity: "warning", Code: "hrPrinterDetectedErrorState.offline", Description: "Offline"}},
			Supplies: []model.Supply{{Description: "Labels", Percent: 30, State: "ok"}}}}
	flap := model.Conflict{ID: "c1", Kind: model.ConflictMACFlap, Severity: "fail", IPs: []string{"10.0.0.44"}}
	r := model.ScanResult{Hosts: []model.Host{zebra}, Conflicts: []model.Conflict{flap}}
	set := store.DefaultSettings()
	ps := Current(r, set)
	if got := kinds(ps); !reflect.DeepEqual(got, []string{"printerAlert:critical", "ipConflict:critical"}) {
		t.Fatalf("items: %v", got)
	}
	if ps[0].Key != notify.HostKeys(model.TriggerPrinterAlert, zebra, notify.AlertPart(zebra.Printer.Alerts[0]))[0] ||
		ps[0].Detail != "Cover open" || ps[0].Device.Name != "ZD621" {
		t.Errorf("printer alert: %+v", ps[0])
	}
	if ps[1].Key != "ipConflict|mac_flap|10.0.0.44" || ps[1].Title != "Network conflict" || ps[1].Device.IP != "10.0.0.44" {
		t.Errorf("conflict: %+v", ps[1])
	}
	set.Alerts.SupplyThresholdPct = 30
	flap.Severity = "warn"
	r.Conflicts = []model.Conflict{flap}
	ps = Current(r, set)
	if got := kinds(ps); !reflect.DeepEqual(got, []string{"printerAlert:critical", "supplyLow:warning", "ipConflict:warning"}) {
		t.Fatalf("threshold 30: %v", got)
	}
	if ps[1].Detail != "Labels at 30%" || ps[2].Title != TitlePossible {
		t.Errorf("supply %q, conflict %q", ps[1].Detail, ps[2].Title)
	}
}

// TestEverythingFine: only an offline phone (Any device goes offline off,
// the default) leaves everything fine, and so does a local network without
// internet once the "This network has no internet" switch is on (D15: its
// DNS and internet results are info); a check that could not run does not.
func TestEverythingFine(t *testing.T) {
	g := readGolden(t)
	var hosts []model.Host
	for _, h := range g.Hosts {
		if h.IP == "10.0.1.1" || h.IP == "10.0.1.2" {
			hosts = append(hosts, h)
		}
	}
	set := store.DefaultSettings()
	now := time.Date(2026, 10, 17, 10, 42, 0, 0, time.UTC)
	view := func(h *model.HealthReport) View {
		return NewView(Input{Source: SourceLatest, Result: &model.ScanResult{Hosts: hosts, Health: h}, Settings: set, Now: now})
	}
	if v := view(fineHealth()); !v.EverythingFine || len(v.Open) != 0 {
		t.Errorf("only an offline phone: %+v", v)
	}
	noInternet := fineHealth()
	noInternet.Checks[2] = hc("dns", model.HealthInfo, model.ReasonNoInternet)
	noInternet.Checks[4] = hc("internet", model.HealthInfo, model.ReasonNoResponse)
	if v := view(noInternet); !v.EverythingFine {
		t.Errorf("no internet, switch on: %+v", v)
	}
	noInternet.Checks[2] = hc("dns", model.HealthWarn, model.ReasonNoInternet)
	if v := view(noInternet); !v.EverythingFine || len(v.Open) != 1 || v.Open[0].Severity != SevInfo {
		t.Errorf("a warning is a note: %+v", v)
	}
	notRun := fineHealth()
	notRun.Checks[0] = hc("gateway", model.HealthUnavailable, model.ReasonNoGateway)
	if v := view(notRun); v.EverythingFine {
		t.Errorf("a check that could not run: %+v", v)
	}
	hidden := fineHealth()
	hidden.Checks[5] = hc("wifi", model.HealthUnavailable, model.ReasonHiddenLocation)
	if v := view(hidden); !v.EverythingFine {
		t.Errorf("Wi-Fi details this computer doesn't share: %+v", v)
	}
	// Without a health report nothing speaks for the checks, unless health
	// alerts are off.
	if v := view(nil); v.EverythingFine {
		t.Errorf("no health report: %+v", v)
	}
	set.Alerts.Triggers.HealthFailed = false
	if v := view(nil); !v.EverythingFine {
		t.Errorf("no health report, health alerts off: %+v", v)
	}
	if v := NewView(Input{Source: SourceLive, Settings: set}); v.Source != SourceNone || v.EverythingFine || v.Open == nil || v.Fixed == nil {
		t.Errorf("no scan: %+v", v)
	}
}

// TestViewLedger: an open problem the ledger has under its "ip:" key takes
// that key and its start; the fixed list is this network's, newest first,
// within FixedFor, without problems that are open again.
func TestViewLedger(t *testing.T) {
	now := time.Date(2026, 10, 17, 10, 42, 0, 0, time.UTC)
	pr := model.Host{IP: "10.0.0.9", Alive: true, Online: false, IsPrinter: true, MAC: "02:00:5E:00:00:09", OfflineSince: "2026-10-17T10:40:00Z"}
	r := &model.ScanResult{Hosts: []model.Host{pr}, Health: fineHealth()}
	const net = "n-0123456789ab"
	in := Input{Source: SourceLive, Watching: true, CheckedAt: now.Format(time.RFC3339), Result: r,
		Settings: store.DefaultSettings(), Network: net, Now: now,
		Open: []Entry{
			{Key: "printerOffline|ip:10.0.0.9|", Network: net, OpenedAt: "2026-10-17T10:30:00Z"},
			{Key: "printerOffline|ip:10.0.0.9|", Network: "n-other", OpenedAt: "2026-10-16T10:30:00Z"},
		},
		Fixed: []Fixed{
			{Key: "healthFailed||dns", Title: "Health check OK again", Network: net, OpenedAt: "2026-10-17T10:05:00Z", FixedAt: "2026-10-17T10:12:00Z"},
			{Key: "supplyLow|02:00:5E:00:00:09|abc", Title: "Supply OK again", IP: "10.0.0.9", Name: "Laser", Network: net, FixedAt: "2026-10-17T10:20:00Z"},
			{Key: "healthFailed||wifi", Title: "Health check OK again", Network: "n-other", FixedAt: "2026-10-17T10:21:00Z"},
			{Key: "healthFailed||gateway", Title: "Health check OK again", Network: net, FixedAt: "2026-10-16T09:00:00Z"},
			{Key: "printerOffline|ip:10.0.0.9|", Title: "Printer back online", Network: net, FixedAt: "2026-10-17T10:25:00Z"},
		}}
	v := NewView(in)
	if v.Source != SourceLive || !v.Watching || v.CheckedAt != in.CheckedAt || v.EverythingFine {
		t.Errorf("view: %+v", v)
	}
	if len(v.Open) != 1 || v.Open[0].Key != "printerOffline|ip:10.0.0.9|" || v.Open[0].Since != "2026-10-17T10:30:00Z" {
		t.Fatalf("open: %+v", v.Open)
	}
	var keys []string
	for _, f := range v.Fixed {
		keys = append(keys, f.Key)
	}
	if strings.Join(keys, " ") != "supplyLow|02:00:5E:00:00:09|abc healthFailed||dns" {
		t.Fatalf("fixed: %v", keys)
	}
	if f := v.Fixed[1]; f.Kind != model.TriggerHealthFailed || f.DownForSec != 420 || f.Device != nil {
		t.Errorf("dns fixed: %+v", f)
	}
	if f := v.Fixed[0]; f.Device == nil || f.Device.IP != "10.0.0.9" || f.Device.Name != "Laser" || f.DownForSec != 0 {
		t.Errorf("supply fixed: %+v", f)
	}
	// The JSON the card reads.
	b, _ := json.Marshal(v)
	for _, want := range []string{`"source":"live"`, `"watching":true`, `"everythingFine":false`, `"open":[{"key":"printerOffline|ip:10.0.0.9|"`,
		`"severity":"warning"`, `"downForSec":420`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON lacks %s: %s", want, b)
		}
	}
}
