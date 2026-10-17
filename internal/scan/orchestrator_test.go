package scan

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"netdiag/internal/arp"
	"netdiag/internal/discovery"
	"netdiag/internal/model"
	"netdiag/internal/snmpprobe"
)

type fakePinger struct {
	alive map[string]bool
	block bool
}

func (f fakePinger) Ping(ctx context.Context, ip netip.Addr, timeout time.Duration) (time.Duration, bool, error) {
	if f.block {
		select {
		case <-ctx.Done():
			return 0, false, ctx.Err()
		case <-time.After(timeout):
			return 0, false, nil
		}
	}
	if f.alive[ip.String()] {
		return 2 * time.Millisecond, true, nil
	}
	return 0, false, nil
}

type fakeARP []arp.Entry

func (f fakeARP) Table(context.Context) ([]arp.Entry, error) { return f, nil }

// ifaceARP records the interface IP passed to TableFor.
type ifaceARP struct {
	fakeARP
	mu    sync.Mutex
	iface []string
}

func (a *ifaceARP) TableFor(_ context.Context, ifaceIP string) ([]arp.Entry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.iface = append(a.iface, ifaceIP)
	return a.fakeARP, nil
}

func TestARPPrefersScannedInterface(t *testing.T) {
	d := fakeDeps()
	fa := &ifaceARP{fakeARP: d.ARP.(fakeARP)}
	d.ARP = fa
	s, err := New(d).Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.Subscribe()
	defer cancel()
	collect(t, ch, 2*time.Second)
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if len(fa.iface) == 0 || fa.iface[0] != "10.0.0.30" {
		t.Fatalf("TableFor calls = %v, want scanned iface 10.0.0.30", fa.iface)
	}
}

type fakePorts map[string][]int

func (f fakePorts) ScanPorts(ctx context.Context, ip string, ports []int) []int {
	var out []int
	for _, p := range f[ip] {
		if slices.Contains(ports, p) {
			out = append(out, p)
		}
	}
	return out
}

// Reachable lets fakePorts serve as the presence PortChecker.
func (f fakePorts) Reachable(ctx context.Context, ip string, port int, timeout time.Duration) bool {
	return slices.Contains(f[ip], port)
}

type fakeWeb struct{}

func (fakeWeb) ProbeAll(ctx context.Context, ip string, open []int) []model.WebService {
	var out []model.WebService
	for _, p := range open {
		switch p {
		case 80:
			out = append(out, model.WebService{Port: p, Scheme: "http", URL: "http://" + ip + "/", Status: 200, Title: "Router"})
		case 443:
			out = append(out, model.WebService{Port: p, Scheme: "https", URL: "https://" + ip + "/", Status: 200, Title: "NAS"})
		}
	}
	return out
}

type fakeSNMP map[string]*snmpprobe.Result

func (f fakeSNMP) Probe(ctx context.Context, ip, community string) (*snmpprobe.Result, error) {
	if r, ok := f[ip]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("%w: timeout", snmpprobe.ErrNoResponse)
}

type fakeDNS map[string]string

func (f fakeDNS) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	if n, ok := f[addr]; ok {
		return []string{n}, nil
	}
	return nil, errors.New("not found")
}

type fakeVendors map[string]string

func (f fakeVendors) Lookup(mac string) string { return f[mac] }

type fakeEnv struct{}

func (fakeEnv) Self(string) (model.SelfInfo, error) {
	return model.SelfInfo{IP: "10.0.0.30", MAC: "aa:bb:cc:00:00:30", Interface: "eth0"}, nil
}
func (fakeEnv) Gateway() (string, error) { return "10.0.0.1", nil }

func fakeDeps() Deps {
	return Deps{
		Pinger: fakePinger{alive: map[string]bool{
			"10.0.0.1": true, "10.0.0.10": true, "10.0.0.11": true,
			"10.0.0.20": true, "10.0.0.21": true, "10.0.0.30": true,
		}},
		ARP: fakeARP{
			{IP: "10.0.0.1", MAC: "00:11:22:33:44:01"},
			{IP: "10.0.0.40", MAC: "00:1B:A9:00:00:40"},   // ICMP-silent printer
			{IP: "192.168.9.9", MAC: "00:11:22:33:44:99"}, // out of range
			{IP: "10.0.0.50", MAC: "FF:FF:FF:FF:FF:FF"},   // broadcast, ignored
		},
		Ports: fakePorts{
			"10.0.0.1":  {80},
			"10.0.0.11": {9100},
			"10.0.0.20": {443},
			"10.0.0.40": {631},
		},
		TCP: fakePorts{
			"10.0.0.1":  {80},
			"10.0.0.11": {9100},
			"10.0.0.20": {443},
			"10.0.0.40": {631},
		},
		Web: fakeWeb{},
		SNMP: fakeSNMP{"10.0.0.10": {
			Snmp:    &model.SnmpInfo{Version: "v2c", SysDescr: "Brother HL", HrPrinterStatus: "idle"},
			Printer: &model.PrinterInfo{Model: "HL-L2350DW"},
		}},
		DNS:          fakeDNS{"10.0.0.1": "router.lan."},
		Vendors:      fakeVendors{"00:1B:A9:00:00:40": "Brother Industries"},
		Env:          fakeEnv{},
		Sweep:        discovery.Config{Rate: -1, Retries: -1, Timeout: 50 * time.Millisecond},
		ProbeWorkers: 64,
	}
}

func collect(t *testing.T, ch <-chan model.Event, within time.Duration) []model.Event {
	t.Helper()
	var evs []model.Event
	timer := time.After(within)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, ev)
		case <-timer:
			t.Fatalf("event stream not closed within %v (%d events)", within, len(evs))
		}
	}
}

func TestEndToEndFake24(t *testing.T) {
	o := New(fakeDeps())
	start := time.Now()
	s, err := o.Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := s.Subscribe()
	defer cancel()
	evs := collect(t, ch, 2*time.Second)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("scan took %v, want < 1s", d)
	}

	if len(evs) == 0 || evs[len(evs)-1].Type != model.EventDone {
		t.Fatalf("last event must be done, got %+v", evs[len(evs)-1:])
	}
	var stages []string
	sawHost := false
	for _, ev := range evs[:len(evs)-1] {
		switch ev.Type {
		case model.EventStage:
			stages = append(stages, ev.Data.(model.StageInfo).Stage)
		case model.EventHost:
			sawHost = true
		case model.EventDone, model.EventError:
			t.Fatalf("unexpected terminal event before end: %v", ev.Type)
		}
	}
	if !sawHost {
		t.Fatal("no host events")
	}
	// The subscriber may join after icmp started; the stage sequence must be
	// a suffix of icmp, arp, probe.
	want := []string{model.StageICMP, model.StageARP, model.StageProbe}
	if len(stages) == 0 || !slices.Equal(stages, want[len(want)-len(stages):]) {
		t.Fatalf("stages = %v", stages)
	}

	res := evs[len(evs)-1].Data.(model.ScanResult)
	if res.State != model.StateDone {
		t.Fatalf("state = %q", res.State)
	}
	wantStats := model.Stats{Scanned: 254, Alive: 7, Printers: 3, Web: 2}
	if res.Stats != wantStats {
		t.Fatalf("stats = %+v, want %+v", res.Stats, wantStats)
	}
	if res.Gateway != "10.0.0.1" || res.Self.IP != "10.0.0.30" || res.Self.MAC != "AA:BB:CC:00:00:30" {
		t.Fatalf("self/gateway = %+v %q", res.Self, res.Gateway)
	}
	if res.Request.Community != "public" || !slices.Equal(res.Request.Ports, model.DefaultPorts) {
		t.Fatalf("request defaults not applied: %+v", res.Request)
	}
	byIP := map[string]model.Host{}
	var ips []string
	for _, h := range res.Hosts {
		byIP[h.IP] = h
		ips = append(ips, h.IP)
	}
	if strings.Join(ips, ",") != "10.0.0.1,10.0.0.10,10.0.0.11,10.0.0.20,10.0.0.21,10.0.0.30,10.0.0.40" {
		t.Fatalf("hosts = %v", ips)
	}
	h40 := byIP["10.0.0.40"]
	if !slices.Equal(h40.FoundBy, []string{"arp", "tcp"}) || h40.Vendor != "Brother Industries" || !h40.IsPrinter || h40.RttMs != 0 {
		t.Fatalf("arp-only host = %+v", h40)
	}
	if h := byIP["10.0.0.1"]; h.Hostname != "router.lan" || h.MAC != "00:11:22:33:44:01" || len(h.Web) != 1 || h.RttMs <= 0 {
		t.Fatalf("router = %+v", h)
	}
	if h := byIP["10.0.0.10"]; !h.IsPrinter || h.Printer == nil || h.Snmp == nil {
		t.Fatalf("snmp printer = %+v", h)
	}
	if h := byIP["10.0.0.11"]; !h.IsPrinter || !strings.Contains(h.PrinterReason, "9100") {
		t.Fatalf("9100 printer = %+v", h)
	}
	if h := byIP["10.0.0.30"]; !h.IsSelf || h.MAC != "AA:BB:CC:00:00:30" {
		t.Fatalf("self host = %+v", h)
	}
	for _, h := range res.Hosts {
		if len(h.Errors) != 0 {
			t.Fatalf("%s: unexpected errors %v", h.IP, h.Errors)
		}
		if h.UpdatedAt == "" {
			t.Fatalf("%s: UpdatedAt unset", h.IP)
		}
	}
	if snap := s.Snapshot(); snap.Stats != wantStats || snap.FinishedAt == "" {
		t.Fatalf("snapshot after done = %+v", snap.Stats)
	}
}

func TestLateSubscriberGetsSnapshot(t *testing.T) {
	o := New(fakeDeps())
	res, err := o.Run(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	_ = res
	s, _ := o.Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24"})
	s.Wait()
	ch, cancel := s.Subscribe()
	defer cancel()
	evs := collect(t, ch, time.Second)
	hosts := 0
	for _, ev := range evs {
		if ev.Type == model.EventHost {
			hosts++
		}
	}
	if hosts != 7 || evs[len(evs)-1].Type != model.EventDone {
		t.Fatalf("replay hosts=%d last=%v", hosts, evs[len(evs)-1].Type)
	}
}

func TestCancellation(t *testing.T) {
	d := fakeDeps()
	d.Pinger = fakePinger{block: true}
	d.Sweep.Timeout = 10 * time.Second
	o := New(d)
	s, err := o.Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if st := s.Snapshot().State; st != model.StateRunning {
		t.Fatalf("running state = %q", st)
	}
	t0 := time.Now()
	s.Cancel()
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("scan did not stop after cancel")
	}
	if d := time.Since(t0); d > 300*time.Millisecond {
		t.Fatalf("cancel took %v", d)
	}
	if st := s.Snapshot().State; st != model.StateCancelled {
		t.Fatalf("state = %q", st)
	}
}

func TestRegistry(t *testing.T) {
	d := fakeDeps()
	d.Pinger = fakePinger{block: true}
	d.Sweep.Timeout = 10 * time.Second
	reg := NewRegistry(New(d))
	ctx := context.Background()

	if _, err := reg.Start(ctx, model.ScanRequest{CIDR: "10.0.0.0/21"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("oversize: err = %v", err)
	}
	if _, err := reg.Start(ctx, model.ScanRequest{CIDR: "bogus"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("bogus: err = %v", err)
	}
	s1, err := reg.Start(ctx, model.ScanRequest{CIDR: "10.0.0.0/30"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Start(ctx, model.ScanRequest{CIDR: "10.0.0.0/30"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent: err = %v", err)
	}
	if got, ok := reg.Running(); !ok || got != s1 {
		t.Fatal("Running mismatch")
	}
	if !reg.Cancel(s1.ID) || reg.Cancel("nope") {
		t.Fatal("Cancel result mismatch")
	}
	<-s1.Done()

	reg2 := NewRegistry(New(fakeDeps()))
	var ids []string
	for range MaxRetained + 2 {
		s, err := reg2.Start(ctx, model.ScanRequest{CIDR: "10.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		s.Wait()
		ids = append(ids, s.ID)
	}
	if len(reg2.List()) != MaxRetained {
		t.Fatalf("retained %d", len(reg2.List()))
	}
	if _, ok := reg2.Get(ids[0]); ok {
		t.Fatal("oldest scan not evicted")
	}
	if _, ok := reg2.Get(ids[len(ids)-1]); !ok {
		t.Fatal("newest scan missing")
	}
}

func TestBusSlowSubscriberDoesNotBlock(t *testing.T) {
	b := NewBus()
	_, cancel := b.Subscribe() // never read
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := range 5000 {
			b.Publish(model.Event{Type: model.EventHost, Data: model.Host{IP: fmt.Sprintf("10.0.%d.%d", i/256, i%256)}})
		}
		b.Publish(model.Event{Type: model.EventDone, Data: model.ScanResult{}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked on slow subscriber")
	}
}
