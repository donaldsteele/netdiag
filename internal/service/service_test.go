package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"netdiag/internal/arp"
	"netdiag/internal/chat"
	"netdiag/internal/health"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/problems"
	"netdiag/internal/scan"
	"netdiag/internal/snmpprobe"
	"netdiag/internal/store"
)

type fakePinger struct{}

func (fakePinger) Ping(_ context.Context, ip netip.Addr, _ time.Duration) (time.Duration, bool, error) {
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

type fakeHealth struct{ at string }

func (f fakeHealth) Run(context.Context, health.Input) model.HealthReport {
	return model.HealthReport{CheckedAt: f.at}
}

func newSvc(t *testing.T, withStore bool, h scan.HealthChecker) (*Service, *store.Store) {
	t.Helper()
	var st *store.Store
	deps := scan.Deps{
		Pinger: fakePinger{}, ARP: fakeARP{}, Ports: fakePorts{}, Web: fakeWeb{},
		SNMP: fakeSNMP{}, DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{}, Health: h,
	}
	if withStore {
		var err error
		if st, err = store.Open(t.TempDir()); err != nil {
			t.Fatal(err)
		}
		deps.Store, deps.Settings = st, st
	}
	reg := scan.NewRegistry(scan.New(deps))
	return New(Config{Registry: reg, Store: st, Health: h, BaseCtx: context.Background()}), st
}

func runScan(t *testing.T, s *Service) model.ScanResult {
	t.Helper()
	sc, err := s.Start(model.ScanRequest{CIDR: "10.0.0.0/30"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-sc.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("scan timeout")
	}
	return sc.Snapshot()
}

func TestResolveScan(t *testing.T) {
	s, st := newSvc(t, true, nil)
	res := runScan(t, s)
	if got, err := s.ResolveScan(res.ID); err != nil || got.ID != res.ID {
		t.Fatalf("registry resolve: %v %v", got.ID, err)
	}
	saved := model.ScanResult{ID: "saved-1", Request: model.ScanRequest{CIDR: "10.9.0.0/24"}, State: "done"}
	if err := st.SaveScan(saved); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ResolveScan("saved-1"); err != nil || got.ID != "saved-1" {
		t.Fatalf("store resolve: %v %v", got.ID, err)
	}
	if _, err := s.ResolveScan("nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	ns, _ := newSvc(t, false, nil)
	if _, err := ns.ResolveScan("x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no store: want ErrNotFound, got %v", err)
	}
	if _, err := ns.ListHistory(); !errors.Is(err, ErrNoStore) {
		t.Fatalf("want ErrNoStore, got %v", err)
	}
}

// F11: every scan the service hands out, live or saved (including a file
// saved before v0.5.8 that still holds the community), is redacted.
func TestServiceScansRedacted(t *testing.T) {
	const secret = "s3cr3t-x"
	s, st := newSvc(t, true, nil)
	sc, err := s.Start(model.ScanRequest{CIDR: "10.0.0.0/30", Community: secret})
	if err != nil {
		t.Fatal(err)
	}
	if got := sc.Snapshot(); got.Request.Community != model.Redacted {
		t.Fatalf("running snapshot community = %q", got.Request.Community)
	}
	<-sc.Done()
	old := `{"id":"old-1","schemaVersion":2,"startedAt":"2020-01-01T10:00:00Z","state":"done","request":{"cidr":"10.0.0.0/30","community":"` + secret + `"}}`
	if err := os.WriteFile(filepath.Join(st.Dir(), "history", "20200101T100000Z-old-1.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{sc.ID, "old-1"} {
		got, err := s.ResolveScan(id)
		if err != nil || got.Request.Community != model.Redacted {
			t.Fatalf("ResolveScan(%s) community = %q, %v", id, got.Request.Community, err)
		}
		if got, err = s.LoadScan(id); err != nil || got.Request.Community != model.Redacted {
			t.Fatalf("LoadScan(%s) community = %q, %v", id, got.Request.Community, err)
		}
	}
}

func TestCompare(t *testing.T) {
	s, st := newSvc(t, true, nil)
	for _, id := range []string{"a-1", "b-2"} {
		r := model.ScanResult{ID: id, Request: model.ScanRequest{CIDR: "10.9.0.0/24"}, State: "done",
			StartedAt: "2026-01-0" + id[len(id)-1:] + "T00:00:00Z"}
		if err := st.SaveScan(r); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Compare("", "b-2"); err != nil {
		t.Fatalf("default baseline: %v", err)
	}
	var bad *BadRequestError
	if _, err := s.Compare("b-2", "b-2"); !errors.As(err, &bad) || bad.Msg != "base and current must differ" {
		t.Fatalf("same id: %v", err)
	}
	if _, err := s.Compare("a-1", ""); !errors.As(err, &bad) || bad.Msg != "current is required" {
		t.Fatalf("no current: %v", err)
	}
	var nf *NotFoundError
	if _, err := s.Compare("a-1", "missing"); !errors.As(err, &nf) || nf.Msg != "current scan not found" {
		t.Fatalf("missing current: %v", err)
	}
	if _, err := s.Compare("missing", "b-2"); !errors.As(err, &nf) || nf.Msg != "base scan not found" {
		t.Fatalf("missing base: %v", err)
	}
}

func TestLatestHealth(t *testing.T) {
	s, st := newSvc(t, true, fakeHealth{at: "2026-02-02T00:00:00Z"})
	if _, ok := s.LatestHealth(); ok {
		t.Fatal("expected none")
	}
	old := model.ScanResult{ID: "h-1", Request: model.ScanRequest{CIDR: "10.9.0.0/24"}, State: "done",
		Health: &model.HealthReport{CheckedAt: "2099-01-01T00:00:00Z"}}
	if err := st.SaveScan(old); err != nil {
		t.Fatal(err)
	}
	if h, ok := s.LatestHealth(); !ok || h.CheckedAt != "2099-01-01T00:00:00Z" {
		t.Fatalf("history fallback: %+v %v", h, ok)
	}
	runScan(t, s)
	// A listed (registry) report wins over history even if history is newer.
	if h, ok := s.LatestHealth(); !ok || h.CheckedAt != "2026-02-02T00:00:00Z" {
		t.Fatalf("listed precedence: %+v %v", h, ok)
	}
}

// recordHealth records the input of every on-demand health run.
type recordHealth struct {
	mu sync.Mutex
	in []health.Input
}

func (r *recordHealth) Run(_ context.Context, in health.Input) model.HealthReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.in = append(r.in, in)
	return model.HealthReport{CheckedAt: "now"}
}

func (r *recordHealth) last() health.Input {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.in[len(r.in)-1]
}

// RunHealth without an interface checks the network of the scan on screen,
// else the default interface; never simply the first one listed. The
// router is used only on its own adapter's network: the default route's
// gateway (192.168.1.1, on wlan0) is not paired with eth0 or vpn0.
func TestRunHealthTarget(t *testing.T) {
	deps := scan.Deps{
		Pinger: fakePinger{}, ARP: fakeARP{}, Ports: fakePorts{}, Web: fakeWeb{},
		SNMP: fakeSNMP{}, DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{},
	}
	rec := &recordHealth{}
	ifaces := []netinfo.Iface{
		{Name: "vpn0", IP: "172.16.0.5", CIDR: "172.16.0.0/16"},
		{Name: "wlan0", IP: "192.168.1.20", CIDR: "192.168.1.0/24", IsDefault: true},
		{Name: "eth0", IP: "10.0.0.30", CIDR: "10.0.0.0/24"},
	}
	var ifErr error
	s := New(Config{Registry: scan.NewRegistry(scan.New(deps)), Health: rec, BaseCtx: context.Background(),
		Interfaces: func() ([]netinfo.Iface, error) { return ifaces, ifErr },
		Gateway:    func() (string, error) { return "192.168.1.1", nil }})
	check := func(name, iface string, want health.Input) {
		t.Helper()
		s.RunHealth(context.Background(), iface)
		if got := rec.last(); got.Iface != want.Iface || got.IfaceIP != want.IfaceIP || got.Gateway != want.Gateway || got.CIDR != want.CIDR {
			t.Errorf("%s: got %s %s %s gw %q, want %s %s %s gw %q", name, got.Iface, got.IfaceIP, got.CIDR, got.Gateway,
				want.Iface, want.IfaceIP, want.CIDR, want.Gateway)
		}
	}
	def := health.Input{Iface: "wlan0", IfaceIP: "192.168.1.20", Gateway: "192.168.1.1", CIDR: "192.168.1.0/24"}
	check("no scan: default interface", "", def)
	check("named interface, router on another network", "eth0", health.Input{Iface: "eth0", IfaceIP: "10.0.0.30", CIDR: "10.0.0.0/24"})
	check("named VPN adapter", "vpn0", health.Input{Iface: "vpn0", IfaceIP: "172.16.0.5", CIDR: "172.16.0.0/16"})
	check("unknown name: default interface", "nope", def)
	// The default-route adapter keeps its router even when its first address
	// is on another subnet (a second static address, or a /32 link).
	ifaces[1].CIDR = "192.168.50.7/32"
	wide := def
	wide.CIDR = "192.168.50.7/32"
	check("default adapter, router outside its first prefix", "", wide)
	ifaces[1].CIDR = "192.168.1.0/24"
	ifErr = errors.New("cannot list")
	check("no scan, interfaces unreadable: default route", "", health.Input{Gateway: "192.168.1.1"})
	ifErr = nil

	runScan(t, s) // fakeEnv: eth0 10.0.0.30, gateway 10.0.0.1
	fromScan := health.Input{Iface: "eth0", IfaceIP: "10.0.0.30", Gateway: "10.0.0.1", CIDR: "10.0.0.0/24"}
	check("latest scan", "", fromScan)
	check("latest scan, named", "eth0", fromScan)
	check("another interface named", "wlan0", def)
	ifErr = errors.New("cannot list")
	check("interfaces unreadable", "", fromScan)
	ifErr = nil

	// The scan's router is not on its adapter's network (10.0.0.16/28).
	ifaces[2].CIDR = "10.0.0.16/28"
	check("scan router off the network", "", health.Input{Iface: "eth0", IfaceIP: "10.0.0.30", CIDR: "10.0.0.16/28"})
	ifaces[2].CIDR = "10.0.0.0/24"

	// The scan's interface now has another address: another network.
	ifaces[2].IP = "10.9.9.9"
	check("moved network", "", def)
	ifaces = ifaces[:2]
	check("scan interface gone", "", def)
}

// tcpOnlyHealth reports a router that answers only on its web port.
type tcpOnlyHealth struct{ recordHealth }

func (h *tcpOnlyHealth) Run(ctx context.Context, in health.Input) model.HealthReport {
	h.recordHealth.Run(ctx, in)
	return model.HealthReport{CheckedAt: "now", Network: "n-0123456789ab", Checks: []model.HealthCheck{{ID: "gateway", Status: model.HealthOK,
		Detail: "the router answers on port 80", Data: map[string]any{"reason": model.ReasonTCPOnly}}}}
}

// RunHealth attaches the advice, with the router page of the gateway it
// checked, and passes the networks the "no internet" switch is on for to
// the checks, with the network's address prefix (v0.6-D15).
func TestRunHealthAdvice(t *testing.T) {
	h := &tcpOnlyHealth{}
	s, st := newSvc(t, true, h)
	s.cfg.Interfaces = func() ([]netinfo.Iface, error) {
		return []netinfo.Iface{{Name: "wlan0", IP: "192.168.1.20", CIDR: "192.168.1.0/24", IsDefault: true}}, nil
	}
	s.cfg.Gateway = func() (string, error) { return "192.168.1.1", nil }
	set := store.DefaultSettings()
	set.NoInternetNetworks = []string{"n-0123456789ab"}
	if _, err := st.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	rep := s.RunHealth(context.Background(), "")
	a := rep.Checks[0].Advice
	if a == nil || len(a.Actions) != 1 || a.Actions[0].Kind != model.ActionOpenRouter || a.Actions[0].Target != "http://192.168.1.1/" {
		t.Fatalf("advice = %+v", a)
	}
	if rep.Network != "n-0123456789ab" {
		t.Fatalf("network = %q", rep.Network)
	}
	if in := h.last(); !slices.Equal(in.Config.NoInternetNetworks, []string{"n-0123456789ab"}) || in.CIDR != "192.168.1.0/24" {
		t.Fatalf("the checks got %+v", in)
	}
}

func TestSettings(t *testing.T) {
	s, _ := newSvc(t, true, nil)
	var bad *BadRequestError
	in := model.Settings{WebServer: model.SettingsWebServer{URL: "ftp://bad"}}
	if _, err := s.SaveSettings(in); !errors.As(err, &bad) {
		t.Fatalf("want bad request, got %v", err)
	}
	if err := s.IgnoreMAC("zz"); !errors.As(err, &bad) || bad.Msg != "invalid mac" {
		t.Fatalf("invalid mac: %v", err)
	}
	if err := s.IgnoreMAC("aa:bb:cc:dd:ee:ff"); err != nil {
		t.Fatal(err)
	}
	if err := s.IgnoreMAC(" AA-BB-CC-DD-EE-FF "); err != nil {
		t.Fatal(err)
	}
	set, err := s.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if len(set.IgnoredConflictMACs) != 1 || set.IgnoredConflictMACs[0] != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("macs = %v", set.IgnoredConflictMACs)
	}
	set.Community = "secret"
	set.WebServer.URL = "http://admin:pa55@nas.local/"
	if _, err := s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	red, err := s.RedactedSettings()
	if err != nil || red.Community != "***" || red.WebServer.URL != "http://admin:xxxxx@nas.local/" {
		t.Fatalf("redacted = %q %q %v", red.Community, red.WebServer.URL, err)
	}
	if raw, _ := s.LoadSettings(); raw.Community != "secret" || raw.WebServer.URL != "http://admin:pa55@nas.local/" {
		t.Fatalf("raw settings changed: %q %q", raw.Community, raw.WebServer.URL)
	}
	ns, _ := newSvc(t, false, nil)
	if _, err := ns.RedactedSettings(); !errors.Is(err, ErrNoStore) {
		t.Fatalf("want ErrNoStore, got %v", err)
	}
}

// Every OnSettingsSaved hook runs, in the order added, after each successful
// save (IgnoreMAC included), and none runs when the save is refused.
func TestOnSettingsSavedList(t *testing.T) {
	s, _ := newSvc(t, true, nil)
	var calls []string
	s.OnSettingsSaved(func(set model.Settings) { calls = append(calls, "mcp:"+set.Community) })
	s.OnSettingsSaved(func(set model.Settings) { calls = append(calls, "chat:"+set.Chat.MinSeverity) })
	set, _ := s.LoadSettings()
	set.Community = "c1"
	if _, err := s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	set.Alerts.CooldownSec = 1
	if _, err := s.SaveSettings(set); err == nil {
		t.Fatal("bad settings saved")
	}
	if err := s.IgnoreMAC("aa:bb:cc:dd:ee:ff"); err != nil {
		t.Fatal(err)
	}
	want := []string{"mcp:c1", "chat:warning", "mcp:c1", "chat:warning"}
	if !slices.Equal(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

// Saves are one step each: a hook always sees the settings that are in the
// file at that moment, so the last hook call matches settings.json (an MCP
// gate or a chat router never stays out of step with Settings), and
// IgnoreMAC's load-modify-save never undoes another save.
func TestSettingsSavesInOrder(t *testing.T) {
	s, st := newSvc(t, true, nil)
	var mu sync.Mutex
	var last string
	var stale []string
	s.OnSettingsSaved(func(set model.Settings) {
		onDisk, _ := st.LoadSettings()
		mu.Lock()
		defer mu.Unlock()
		last = set.Community
		if onDisk.Community != set.Community {
			stale = append(stale, set.Community+"!="+onDisk.Community)
		}
	})
	base, _ := s.LoadSettings()
	const n = 24
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			in := base
			in.Community = "c" + strconv.Itoa(i)
			if _, err := s.SaveSettings(in); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, _ := st.LoadSettings()
	if len(stale) > 0 || got.Community != last {
		t.Fatalf("hooks out of step with the file: %v (file %q, last hook %q)", stale, got.Community, last)
	}
	for i := range n {
		wg.Go(func() {
			if err := s.IgnoreMAC(fmt.Sprintf("aa:bb:cc:dd:ee:%02x", i)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got, _ = st.LoadSettings(); len(got.IgnoredConflictMACs) != n || got.Community != last || len(stale) > 0 {
		t.Fatalf("after %d IgnoreMAC: %d MACs, community %q, stale %v", n, len(got.IgnoredConflictMACs), got.Community, stale)
	}
}

func TestLoadSettingsRepaired(t *testing.T) {
	s, st := newSvc(t, true, nil)
	doc := `{"community":"keep","alerts":{"supplyThresholdPct":0,"cooldownSec":120}}`
	if err := os.WriteFile(filepath.Join(st.Dir(), "settings.json"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	set, repaired, err := s.LoadSettingsRepaired()
	if err != nil || !slices.Equal(repaired, []string{"alerts.supplyThresholdPct"}) ||
		set.Alerts.SupplyThresholdPct != 15 || set.Alerts.CooldownSec != 120 || set.Community != "keep" {
		t.Fatalf("%+v %v %v", set.Alerts, repaired, err)
	}
	ns, _ := newSvc(t, false, nil)
	if _, _, err := ns.LoadSettingsRepaired(); !errors.Is(err, ErrNoStore) {
		t.Fatalf("want ErrNoStore, got %v", err)
	}
}

// Close saves the open-problem ledger at once (Quit), and is harmless
// without one.
func TestCloseFlushesLedger(t *testing.T) {
	if err := New(Config{}).Close(); err != nil {
		t.Fatalf("no ledger: %v", err)
	}
	// Team chat that is not connected: nothing to wait for.
	m := chat.NewManager(chat.Options{})
	defer m.Close()
	start := time.Now()
	if err := New(Config{Chat: m}).Close(); err != nil || time.Since(start) > time.Second {
		t.Fatalf("chat not connected: %v after %v", err, time.Since(start))
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l, err := problems.OpenLedger(st)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Store: st, Ledger: l})
	l.Apply("n-0123456789ab", []model.Notice{{Trigger: model.TriggerSupplyLow, Severity: "warn", IP: "10.0.0.10",
		Title: "Supply low", Key: "supplyLow|ip:10.0.0.10|1a2b3c4d", At: "2026-10-17T10:00:00Z"}})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	l2, err := problems.OpenLedger(st)
	if err != nil {
		t.Fatal(err)
	}
	if got := l2.OpenFor("n-0123456789ab"); len(got) != 1 || got[0].Key != "supplyLow|ip:10.0.0.10|1a2b3c4d" {
		t.Fatalf("after Close: %+v", got)
	}
}
