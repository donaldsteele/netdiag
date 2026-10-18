package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/arp"
	"netdiag/internal/health"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/problems"
	"netdiag/internal/scan"
	"netdiag/internal/service"
	"netdiag/internal/snmpprobe"
	"netdiag/internal/store"
)

const commSecret = "s3cr3t-comm"

type fakePinger struct {
	delay time.Duration
	gate  chan struct{} // when non-nil, pings block until closed
}

func (p fakePinger) Ping(ctx context.Context, ip netip.Addr, _ time.Duration) (time.Duration, bool, error) {
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return 0, false, ctx.Err()
		}
	}
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return 0, false, ctx.Err()
		}
	}
	return time.Millisecond, ip.String() == "10.0.0.10", nil
}

type fakeARP struct{}

func (fakeARP) Table(context.Context) ([]arp.Entry, error) { return nil, nil }

type fakePorts struct{}

func (fakePorts) ScanPorts(context.Context, string, []int) []int { return nil }

type fakeWeb struct{}

func (fakeWeb) ProbeAll(context.Context, string, []int) []model.WebService { return nil }

type fakeSNMP struct{}

func (fakeSNMP) Probe(context.Context, string, string) (*snmpprobe.Result, error) {
	return nil, snmpprobe.ErrNoResponse
}

type fakeDNS struct{}

func (fakeDNS) LookupAddr(context.Context, string) ([]string, error) { return nil, io.EOF }

type fakeVendors struct{}

func (fakeVendors) Lookup(string) string { return "" }

type fakeEnv struct{}

func (fakeEnv) Self(string) (model.SelfInfo, error) {
	return model.SelfInfo{IP: "10.0.0.30", Interface: "eth0"}, nil
}
func (fakeEnv) Gateway() (string, error) { return "10.0.0.1", nil }

type fakeHealth struct{}

func (fakeHealth) Run(context.Context, health.Input) model.HealthReport {
	return model.HealthReport{CheckedAt: time.Now().UTC().Format(time.RFC3339),
		Checks: []model.HealthCheck{{ID: "gw", Label: "Gateway", Status: "ok"}}}
}

type env struct {
	cs       *mcp.ClientSession
	svc      *service.Service
	st       *store.Store
	progress chan *mcp.ProgressNotificationClientRequest
	srv      *mcp.Server
	// ledger is the window's open-problem ledger (setupWindow only).
	ledger *problems.Ledger
}

func setup(t *testing.T, transport string, withStore bool, p fakePinger) *env {
	t.Helper()
	return setupWith(t, transport, withStore, p, false, nil)
}

// setupWindow wires the env as the netdiag window wires its own /mcp: a data
// folder, one open-problem ledger for the scans and the service, and the
// service told it serves the window (SetWindow). edit changes the scan
// dependencies (nil: none).
func setupWindow(t *testing.T, p fakePinger, edit func(*scan.Deps)) *env {
	t.Helper()
	return setupWith(t, "http", true, p, true, edit)
}

func setupWith(t *testing.T, transport string, withStore bool, p fakePinger, window bool, edit func(*scan.Deps)) *env {
	t.Helper()
	e := &env{progress: make(chan *mcp.ProgressNotificationClientRequest, 64)}
	deps := scan.Deps{
		Pinger: p, ARP: fakeARP{}, Ports: fakePorts{}, Web: fakeWeb{}, SNMP: fakeSNMP{},
		DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{}, Health: fakeHealth{},
		LocalPrefixes: func() ([]netip.Prefix, error) { return []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}, nil },
	}
	if withStore {
		st, err := store.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		e.st = st
		deps.Store, deps.Settings = st, st
	}
	if window {
		l, err := problems.OpenLedger(e.st)
		if err != nil {
			t.Fatal(err)
		}
		e.ledger, deps.Ledger = l, l
		// After the scans stop (cleanups run last first), before the
		// data folder goes.
		t.Cleanup(func() { _ = l.Flush() })
	}
	if edit != nil {
		edit(&deps)
	}
	reg := scan.NewRegistry(scan.New(deps))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.svc = service.New(service.Config{
		Registry: reg, Store: e.st, Health: fakeHealth{}, BaseCtx: ctx,
		Interfaces: func() ([]netinfo.Iface, error) {
			return []netinfo.Iface{{Name: "eth0", IP: "10.0.0.5", CIDR: "10.0.0.5/28", MAC: "aa:bb:cc:00:00:01", IsDefault: true}}, nil
		},
		Gateway: func() (string, error) { return "10.0.0.1", nil },
		Ledger:  e.ledger,
	})
	if window {
		e.svc.SetWindow(func() int { return 8787 })
	}
	e.srv = New(e.svc, Options{Transport: transport, Version: "test"})
	ct, st := mcp.NewInMemoryTransports()
	ss, err := e.srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
			select {
			case e.progress <- r:
			default:
			}
		},
	})
	e.cs, err = c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.cs.Close() })
	return e
}

func (e *env) call(t *testing.T, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := e.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	return res
}

func (e *env) ok(t *testing.T, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res := e.call(t, name, args)
	if res.IsError {
		t.Fatalf("%s: unexpected tool error: %s", name, textOf(res))
	}
	if res.StructuredContent == nil {
		t.Fatalf("%s: no structuredContent", name)
	}
	if out != nil {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
	}
	return res
}

func textOf(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func (e *env) failing(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	res := e.call(t, name, args)
	if !res.IsError {
		t.Fatalf("%s: expected IsError, got %s", name, textOf(res))
	}
	return textOf(res)
}

func TestListToolsExact(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	res, err := e.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tl := range res.Tools {
		got = append(got, tl.Name)
		if tl.Title == "" || tl.Annotations == nil {
			t.Errorf("%s: missing title/annotations", tl.Name)
		}
	}
	want := slices.Clone(ToolNames)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) || len(got) != 16 {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}

func TestToolAnnotations(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	res, err := e.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mutating := map[string]bool{"start_scan": true, "run_health_check": true, "probe_host": true, "cancel_scan": true}
	for _, tl := range res.Tools {
		a := tl.Annotations
		if mutating[tl.Name] {
			if a.ReadOnlyHint {
				t.Errorf("%s must not be readOnly", tl.Name)
			}
			if a.DestructiveHint == nil || *a.DestructiveHint {
				t.Errorf("%s destructiveHint must be false", tl.Name)
			}
			wantOpen := tl.Name != "cancel_scan"
			if a.OpenWorldHint == nil || *a.OpenWorldHint != wantOpen {
				t.Errorf("%s openWorldHint wrong", tl.Name)
			}
			if tl.Name == "cancel_scan" && !a.IdempotentHint {
				t.Errorf("cancel_scan should be idempotent")
			}
		} else {
			if !a.ReadOnlyHint {
				t.Errorf("%s must be readOnly", tl.Name)
			}
			if a.OpenWorldHint == nil || *a.OpenWorldHint {
				t.Errorf("%s openWorldHint must be false", tl.Name)
			}
		}
	}
}

func TestToolsHappyPaths(t *testing.T) {
	e := setup(t, "stdio", true, fakePinger{})

	var ifs listInterfacesOut
	e.ok(t, "list_interfaces", nil, &ifs)
	if len(ifs.Interfaces) != 1 || ifs.SuggestedTarget == "" {
		t.Fatalf("interfaces = %+v", ifs)
	}

	var s1 startScanOut
	e.ok(t, "start_scan", map[string]any{"waitSec": 20}, &s1) // default cidr
	if s1.ScanID == "" || s1.StillRunning || s1.AlreadyRunning || s1.State != model.StateDone {
		t.Fatalf("start_scan = %+v", s1)
	}
	if s1.Stats.Alive < 1 {
		t.Fatalf("alive = %d", s1.Stats.Alive)
	}

	var gs ScanSummary
	e.ok(t, "get_scan", map[string]any{"scanId": s1.ScanID}, &gs)
	if gs.ScanID != s1.ScanID {
		t.Fatalf("get_scan id = %s", gs.ScanID)
	}

	var page DevicePage
	e.ok(t, "list_devices", map[string]any{"scanId": s1.ScanID, "filter": "all", "query": "10.0.0.10"}, &page)
	if page.Total != 1 || page.Items[0].IP != "10.0.0.10" {
		t.Fatalf("devices = %+v", page)
	}

	var dd DeviceDetail
	e.ok(t, "get_device", map[string]any{"scanId": s1.ScanID, "ip": "10.0.0.10"}, &dd)
	if dd.Host.IP != "10.0.0.10" {
		t.Fatalf("device = %+v", dd.Host)
	}

	var ev getEventsOut
	e.ok(t, "get_events", map[string]any{"scanId": s1.ScanID}, &ev)
	if ev.ScanID != s1.ScanID || len(ev.Events) == 0 || !ev.Finished || ev.NextSeq == 0 {
		t.Fatalf("events = %+v", ev)
	}

	var h healthOut
	e.ok(t, "get_health", nil, &h)
	if h.None || h.Report == nil {
		t.Fatalf("health = %+v", h)
	}
	var rh healthOut
	e.ok(t, "run_health_check", nil, &rh)
	if rh.Report == nil || len(rh.Report.Checks) == 0 {
		t.Fatalf("run_health_check = %+v", rh)
	}

	var pr DeviceDetail
	e.ok(t, "probe_host", map[string]any{"ip": "10.0.0.10"}, &pr)
	if pr.Host.IP != "10.0.0.10" {
		t.Fatalf("probe = %+v", pr.Host)
	}

	var cn cancelOut
	e.ok(t, "cancel_scan", map[string]any{"scanId": s1.ScanID}, &cn)
	if cn.Cancelled || cn.State != model.StateDone {
		t.Fatalf("cancel finished scan = %+v", cn)
	}

	var s2 startScanOut
	e.ok(t, "start_scan", map[string]any{"cidr": "10.0.0.0/28", "waitSec": 20}, &s2)
	if s2.State != model.StateDone {
		t.Fatalf("second scan = %+v", s2)
	}

	var hist listHistoryOut
	e.ok(t, "list_history", map[string]any{"limit": 5}, &hist)
	if hist.Total < 2 {
		t.Fatalf("history = %+v", hist)
	}

	var cmp compareOut
	e.ok(t, "compare_scans", map[string]any{"baseId": s1.ScanID, "currentId": s2.ScanID}, &cmp)
	if cmp.Base.ID != s1.ScanID || cmp.Current.ID != s2.ScanID {
		t.Fatalf("compare = %+v", cmp)
	}
	e.call(t, "compare_scans", map[string]any{"currentId": s2.ScanID}) // default baseline (may be "no previous" within one second)

	var set model.Settings
	e.ok(t, "get_settings", nil, &set)
}

func TestToolErrorPaths(t *testing.T) {
	e := setup(t, "stdio", true, fakePinger{})
	if msg := e.failing(t, "get_scan", map[string]any{"scanId": "nope"}); !strings.Contains(msg, "list_history") {
		t.Errorf("get_scan error not actionable: %q", msg)
	}
	if msg := e.failing(t, "start_scan", map[string]any{"live": true}); !strings.Contains(msg, "HTTP endpoint") {
		t.Errorf("live on stdio: %q", msg)
	}
	e.failing(t, "start_scan", map[string]any{"cidr": "10.0.0.0/8"})
	e.failing(t, "probe_host", map[string]any{"ip": "8.8.8.8"})
	e.failing(t, "cancel_scan", map[string]any{"scanId": "nope"})
	e.failing(t, "list_devices", map[string]any{"scanId": "nope"})
	e.failing(t, "get_events", map[string]any{"scanId": "nope"})
	e.failing(t, "compare_scans", map[string]any{"currentId": "nope"})
	// missing required argument is a tool error, not a protocol error
	e.failing(t, "get_scan", map[string]any{})

	var s startScanOut
	e.ok(t, "start_scan", map[string]any{"waitSec": 20}, &s)
	e.failing(t, "get_device", map[string]any{"scanId": s.ScanID})
	e.failing(t, "get_device", map[string]any{"scanId": s.ScanID, "ip": "10.0.0.99"})
	e.failing(t, "list_devices", map[string]any{"scanId": s.ScanID, "filter": "bogus"})
}

func TestNoStoreErrors(t *testing.T) {
	e := setup(t, "stdio", false, fakePinger{})
	for _, name := range []string{"list_history", "get_settings"} {
		if msg := e.failing(t, name, nil); !strings.Contains(msg, "history unavailable") {
			t.Errorf("%s: %q", name, msg)
		}
	}
	if msg := e.failing(t, "compare_scans", map[string]any{"currentId": "x"}); msg == "" {
		t.Error("compare_scans: empty error")
	}
	// scan and health tools still work
	var s startScanOut
	e.ok(t, "start_scan", map[string]any{"waitSec": 20}, &s)
	e.ok(t, "run_health_check", nil, nil)
}

func TestStartScanAlreadyRunning(t *testing.T) {
	gate := make(chan struct{})
	e := setup(t, "stdio", true, fakePinger{gate: gate})
	var first startScanOut
	e.ok(t, "start_scan", map[string]any{"waitSec": 0}, &first)
	if !first.StillRunning || first.AlreadyRunning {
		t.Fatalf("first = %+v", first)
	}
	var second startScanOut
	e.ok(t, "start_scan", map[string]any{"waitSec": 0}, &second)
	if !second.AlreadyRunning || second.ScanID != first.ScanID {
		t.Fatalf("second = %+v", second)
	}
	var cn cancelOut
	e.ok(t, "cancel_scan", map[string]any{"scanId": first.ScanID}, &cn)
	if !cn.Cancelled {
		t.Fatalf("cancel = %+v", cn)
	}
	close(gate)
}

func TestStartScanStillRunningAfterWait(t *testing.T) {
	gate := make(chan struct{})
	e := setup(t, "stdio", true, fakePinger{gate: gate})
	defer close(gate)
	start := time.Now()
	var out startScanOut
	// waitSec is clamped to whole seconds, so use 1.
	e.ok(t, "start_scan", map[string]any{"waitSec": 1}, &out)
	if !out.StillRunning || time.Since(start) < 900*time.Millisecond {
		t.Fatalf("out = %+v after %v", out, time.Since(start))
	}
	if !strings.Contains(out.NextStep, "get_scan") {
		t.Errorf("nextStep = %q", out.NextStep)
	}
}

func TestStartScanProgressNotifications(t *testing.T) {
	e := setup(t, "stdio", true, fakePinger{delay: 300 * time.Millisecond})
	res, err := e.cs.CallTool(context.Background(), &mcp.CallToolParams{
		Meta:      mcp.Meta{"progressToken": "tok-1"},
		Name:      "start_scan",
		Arguments: map[string]any{"cidr": "10.0.0.0/28", "waitSec": 20},
	})
	if err != nil || res.IsError {
		t.Fatalf("err=%v res=%v", err, res)
	}
	select {
	case n := <-e.progress:
		if n.Params.ProgressToken != "tok-1" || n.Params.Message == "" {
			t.Fatalf("notification = %+v", n.Params)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no progress notification received")
	}
}

func TestToolOutputsNeverLeakCommunity(t *testing.T) {
	e := setup(t, "stdio", true, fakePinger{})
	set, err := e.st.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	const urlSecret = "web-pa55word"
	set.Community = commSecret
	set.WebServer.URL = "http://admin:" + urlSecret + "@10.0.0.10/"
	if _, err := e.st.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	var s startScanOut
	e.ok(t, "start_scan", map[string]any{"waitSec": 20, "community": commSecret}, &s)
	var s2 startScanOut
	e.ok(t, "start_scan", map[string]any{"waitSec": 20, "community": commSecret}, &s2)

	calls := []struct {
		name string
		args map[string]any
	}{
		{"list_interfaces", nil},
		{"start_scan", map[string]any{"waitSec": 20, "community": commSecret}},
		{"get_scan", map[string]any{"scanId": s.ScanID}},
		{"cancel_scan", map[string]any{"scanId": s.ScanID}},
		{"list_devices", map[string]any{"scanId": s.ScanID}},
		{"get_device", map[string]any{"scanId": s.ScanID, "ip": "10.0.0.10"}},
		{"get_events", map[string]any{"scanId": s.ScanID}},
		{"get_health", nil},
		{"run_health_check", nil},
		{"probe_host", map[string]any{"ip": "10.0.0.10", "community": commSecret}},
		{"list_history", nil},
		{"compare_scans", map[string]any{"baseId": s.ScanID, "currentId": s2.ScanID}},
		{"get_settings", nil},
		{"get_status", nil},
		{"list_problems", nil},
		{"get_runbook", nil},
		{"get_runbook", map[string]any{"name": "diagnose_printer", "arguments": map[string]any{"ip": "10.0.0.10"}}},
	}
	if len(calls) < len(ToolNames)+1 {
		t.Fatalf("every tool must be called: %d calls for %d tools", len(calls), len(ToolNames))
	}
	for _, c := range calls {
		res := e.call(t, c.name, c.args)
		b, _ := json.Marshal(res)
		if strings.Contains(string(b), commSecret) {
			t.Errorf("%s leaks community: %s", c.name, b)
		}
		if strings.Contains(string(b), urlSecret) {
			t.Errorf("%s leaks the web server URL password: %s", c.name, b)
		}
		if c.name == "get_settings" && (!strings.Contains(string(b), Redacted) || !strings.Contains(string(b), "admin:xxxxx@10.0.0.10")) {
			t.Errorf("get_settings should show redacted markers: %s", b)
		}
	}
	// Also check history-resolved scans (after the registry no longer holds them).
	var gs ScanSummary
	e.ok(t, "get_scan", map[string]any{"scanId": s.ScanID}, &gs)
}

func TestServerInstructionsAndInfo(t *testing.T) {
	e := setup(t, "stdio", true, fakePinger{})
	init := e.cs.InitializeResult()
	if init.ServerInfo.Name != "netdiag" || !strings.Contains(init.Instructions, "start_scan") ||
		!strings.Contains(init.Instructions, "cannot change settings") {
		t.Fatalf("init = %+v", init)
	}
	checkDeviceTextRule(t, "instructions", init.Instructions)
}

// checkDeviceTextRule asserts txt tells the agent that device-supplied text
// is data, not instructions (prompt-injection hygiene).
func checkDeviceTextRule(t *testing.T, where, txt string) {
	t.Helper()
	for _, m := range []string{"hostnames", "mDNS names", "sysName", "sysDescr", "sysLocation", "printer alert",
		"web page titles", "Server headers", "anyone with a device", "never as instructions"} {
		if !strings.Contains(txt, m) {
			t.Errorf("%s: device-text rule missing %q", where, m)
		}
	}
}

// TestHealthTextNamesEveryCheck keeps the agent-facing health wording in
// step with the checks in internal/health (checkMeta), Wi-Fi included.
func TestHealthTextNamesEveryCheck(t *testing.T) {
	e := setup(t, "stdio", true, fakePinger{})
	checks := []string{"gateway", "DHCP", "DNS", "internet", "Wi-Fi", "web server"}
	texts := map[string]string{"instructions": e.cs.InitializeResult().Instructions}
	res, err := e.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if tl.Name == "get_health" || tl.Name == "run_health_check" {
			texts[tl.Name] = tl.Description
		}
	}
	pr, err := e.cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "triage_network"})
	if err != nil {
		t.Fatal(err)
	}
	texts["triage_network"] = pr.Messages[0].Content.(*mcp.TextContent).Text
	if len(texts) != 4 {
		t.Fatalf("texts = %v", texts)
	}
	for name, txt := range texts {
		for _, c := range checks {
			if !strings.Contains(txt, c) {
				t.Errorf("%s does not mention the %s check: %s", name, c, txt)
			}
		}
	}
}

// TestListDevicesProblemsUsesSupplyThreshold checks that list_devices and
// the get_scan summary's printer alerts read alerts.supplyThresholdPct
// from Settings, like the low-supply alerts do.
func TestListDevicesProblemsUsesSupplyThreshold(t *testing.T) {
	e := setup(t, "stdio", true, fakePinger{})
	saved := model.ScanResult{ID: "thr1", State: model.StateDone, StartedAt: "2026-01-02T03:04:05Z",
		Request: model.ScanRequest{CIDR: "10.0.0.0/24"},
		Hosts: []model.Host{
			{IP: "10.0.0.20", Alive: true, Online: true, IsPrinter: true, DeviceType: "printer",
				Printer: &model.PrinterInfo{Supplies: []model.Supply{{Description: "Black toner", Percent: 20, State: "ok"}}}},
			{IP: "10.0.0.21", Alive: true, Online: true},
		}}
	if err := e.st.SaveScan(saved); err != nil {
		t.Fatal(err)
	}
	problems := func() []string {
		var page DevicePage
		e.ok(t, "list_devices", map[string]any{"scanId": "thr1", "filter": "problems"}, &page)
		var ips []string
		for _, it := range page.Items {
			ips = append(ips, it.IP)
		}
		return ips
	}
	alerts := func() []PrinterAlert {
		var sum ScanSummary
		e.ok(t, "get_scan", map[string]any{"scanId": "thr1"}, &sum)
		return sum.PrinterAlerts
	}
	if got := problems(); len(got) != 0 {
		t.Fatalf("default threshold 15: problems = %v, want none", got)
	}
	if got := alerts(); len(got) != 0 {
		t.Fatalf("default threshold 15: printer alerts = %+v, want none", got)
	}
	set, err := e.st.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	set.Alerts.SupplyThresholdPct = 25
	if _, err := e.st.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if got := problems(); !slices.Equal(got, []string{"10.0.0.20"}) {
		t.Fatalf("threshold 25: problems = %v, want [10.0.0.20]", got)
	}
	if got := alerts(); len(got) != 1 || got[0].IP != "10.0.0.20" || !slices.Equal(got[0].LowSupplies, []string{"Black toner 20%"}) {
		t.Fatalf("threshold 25: printer alerts = %+v, want 10.0.0.20 with Black toner 20%%", got)
	}
}
