package service

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"sync"
	"testing"
	"time"

	"netdiag/internal/arp"
	"netdiag/internal/health"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
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
// else the default interface; never simply the first one listed.
func TestRunHealthTarget(t *testing.T) {
	deps := scan.Deps{
		Pinger: fakePinger{}, ARP: fakeARP{}, Ports: fakePorts{}, Web: fakeWeb{},
		SNMP: fakeSNMP{}, DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{},
	}
	rec := &recordHealth{}
	ifaces := []netinfo.Iface{
		{Name: "vpn0", IP: "172.16.0.5"},
		{Name: "wlan0", IP: "192.168.1.20", IsDefault: true},
		{Name: "eth0", IP: "10.0.0.30"},
	}
	var ifErr error
	s := New(Config{Registry: scan.NewRegistry(scan.New(deps)), Health: rec, BaseCtx: context.Background(),
		Interfaces: func() ([]netinfo.Iface, error) { return ifaces, ifErr },
		Gateway:    func() (string, error) { return "192.168.1.1", nil }})
	check := func(name, iface string, want health.Input) {
		t.Helper()
		s.RunHealth(context.Background(), iface)
		if got := rec.last(); got.Iface != want.Iface || got.IfaceIP != want.IfaceIP || got.Gateway != want.Gateway {
			t.Errorf("%s: got %s %s gw %s, want %s %s gw %s", name, got.Iface, got.IfaceIP, got.Gateway, want.Iface, want.IfaceIP, want.Gateway)
		}
	}
	def := health.Input{Iface: "wlan0", IfaceIP: "192.168.1.20", Gateway: "192.168.1.1"}
	check("no scan: default interface", "", def)
	check("named interface", "eth0", health.Input{Iface: "eth0", IfaceIP: "10.0.0.30", Gateway: "192.168.1.1"})
	check("unknown name: default interface", "nope", def)

	runScan(t, s) // fakeEnv: eth0 10.0.0.30, gateway 10.0.0.1
	fromScan := health.Input{Iface: "eth0", IfaceIP: "10.0.0.30", Gateway: "10.0.0.1"}
	check("latest scan", "", fromScan)
	check("latest scan, named", "eth0", fromScan)
	check("another interface named", "wlan0", def)
	ifErr = errors.New("cannot list")
	check("interfaces unreadable", "", fromScan)
	ifErr = nil

	// The scan's interface now has another address: another network.
	ifaces[2].IP = "10.9.9.9"
	check("moved network", "", def)
	ifaces = ifaces[:2]
	check("scan interface gone", "", def)
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
	if _, err := s.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	red, err := s.RedactedSettings()
	if err != nil || red.Community != "***" {
		t.Fatalf("redacted = %q %v", red.Community, err)
	}
	if raw, _ := s.LoadSettings(); raw.Community != "secret" {
		t.Fatalf("raw community changed: %q", raw.Community)
	}
	ns, _ := newSvc(t, false, nil)
	if _, err := ns.RedactedSettings(); !errors.Is(err, ErrNoStore) {
		t.Fatalf("want ErrNoStore, got %v", err)
	}
}
