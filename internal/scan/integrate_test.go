package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"netdiag/internal/arp"
	"netdiag/internal/health"
	"netdiag/internal/ipp"
	"netdiag/internal/mdns"
	"netdiag/internal/model"
	"netdiag/internal/snmpprobe"
	"netdiag/internal/store"
)

type fakeMDNS struct {
	recs   []mdns.Record
	mu     sync.Mutex
	rounds []int
}

func (f *fakeMDNS) Browse(ctx context.Context, iface string, rounds int) ([]mdns.Record, error) {
	f.mu.Lock()
	f.rounds = append(f.rounds, rounds)
	f.mu.Unlock()
	return f.recs, nil
}

type fakeIPP struct {
	res   map[string]*ipp.Result
	mu    sync.Mutex
	calls map[string]int
}

func newFakeIPP(res map[string]*ipp.Result) *fakeIPP {
	return &fakeIPP{res: res, calls: map[string]int{}}
}

func (f *fakeIPP) GetPrinterAttributes(ctx context.Context, ip string, port int) (*ipp.Result, error) {
	f.mu.Lock()
	f.calls[ip]++
	f.mu.Unlock()
	if r, ok := f.res[ip]; ok {
		return r, nil
	}
	return nil, ipp.ErrNotPrinter
}

func (f *fakeIPP) count(ip string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[ip]
}

// fakeHealth returns reports[i] on call i (the last one repeats).
type fakeHealth struct {
	mu      sync.Mutex
	reports []model.HealthReport
	inputs  []health.Input
}

func (f *fakeHealth) Run(ctx context.Context, in health.Input) model.HealthReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, in)
	return f.reports[min(len(f.inputs)-1, len(f.reports)-1)]
}

func healthReport(status string) model.HealthReport {
	return model.HealthReport{CheckedAt: "2026-01-01T00:00:00Z", Checks: []model.HealthCheck{
		{ID: "gateway", Label: "Gateway reachable", Status: status},
		{ID: "dns", Label: "DNS resolving", Status: model.HealthOK},
	}}
}

type fakeStore struct {
	mu    sync.Mutex
	saved []model.ScanResult
	set   model.Settings
}

func (f *fakeStore) SaveScan(r model.ScanResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, r)
	return nil
}

func (f *fakeStore) LoadSettings() (model.Settings, error) { return f.set, nil }

func (f *fakeStore) list() []model.ScanResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.saved)
}

func notices(res model.ScanResult, trigger string) []model.Notice {
	var out []model.Notice
	for _, n := range res.Notices {
		if n.Trigger == trigger {
			out = append(out, n)
		}
	}
	return out
}

func TestMDNSOnlyHostDiscoveredAndProbedOnce(t *testing.T) {
	d := fakeDeps()
	cp := &countPorts{inner: fakePorts{"10.0.0.60": {631}}, calls: map[string]int{}}
	d.Ports = cp
	fm := &fakeMDNS{recs: []mdns.Record{
		{IP: "10.0.0.60", Info: model.MDNSInfo{Instance: "Brother HL", Hostname: "brn1.local", Services: []string{"_ipp._tcp"}}},
		{IP: "10.0.0.10", Info: model.MDNSInfo{Instance: "Office", Services: []string{"_http._tcp"}}},
		{IP: "192.168.5.5", Info: model.MDNSInfo{Instance: "Elsewhere"}}, // out of range
	}}
	d.MDNS = fm
	res, err := New(d).Run(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	h := hostByIP(t, res, "10.0.0.60")
	if !h.Alive || !slices.Contains(h.FoundBy, "mdns") || h.MDNS == nil || h.MDNS.Instance != "Brother HL" {
		t.Fatalf("mdns host = %+v", h)
	}
	if n := cp.calls["10.0.0.60"]; n != 1 {
		t.Fatalf("mdns-only host probed %d times", n)
	}
	if n := cp.calls["10.0.0.10"]; n != 1 {
		t.Fatalf("icmp+mdns host probed %d times", n)
	}
	if !h.IsPrinter {
		t.Fatalf("not classified: %+v", h)
	}
	for _, x := range res.Hosts {
		if x.IP == "192.168.5.5" {
			t.Fatal("out-of-range mdns record added")
		}
	}
	if res.Stats.MDNS != 2 {
		t.Fatalf("stats = %+v", res.Stats)
	}
	if !slices.Equal(fm.rounds, []int{MDNSRoundsScan}) {
		t.Fatalf("rounds = %v", fm.rounds)
	}
}

func TestMDNSPrinterServiceClassifies(t *testing.T) {
	h := model.Host{IP: "x", MDNS: &model.MDNSInfo{Services: []string{"_http._tcp", "_ipp._tcp"}}}
	classify(&h, "")
	if !h.IsPrinter || h.PrinterReason != "mdns service _ipp._tcp" {
		t.Fatalf("got %v %q", h.IsPrinter, h.PrinterReason)
	}
}

func TestIPPFillsPrinterWithSNMPOff(t *testing.T) {
	d := fakeDeps()
	d.Ports = fakePorts{"10.0.0.40": {631}, "10.0.0.10": {631}, "10.0.0.1": {80}}
	ippPrinter := &model.PrinterInfo{
		Model: "IPP Model", Source: ipp.SourceIPP, State: "idle",
		Supplies: []model.Supply{{Description: "Black", Percent: 40, State: "ok"}},
	}
	fi := newFakeIPP(map[string]*ipp.Result{
		"10.0.0.40": {Printer: ippPrinter, Source: ipp.SourceIPP, State: "idle"},
		"10.0.0.10": {Printer: ippPrinter, Source: ipp.SourceIPP, State: "processing", StateReasons: []string{"media-low-warning"}},
	})
	d.IPP = fi
	res, err := New(d).Run(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	h := hostByIP(t, res, "10.0.0.40")
	if h.Printer == nil || h.Printer.Model != "IPP Model" || h.Printer.Source != "ipp" || h.Printer.State != "idle" || len(h.Printer.Supplies) != 1 {
		t.Fatalf("ipp-only printer = %+v", h.Printer)
	}
	if !h.IsPrinter || h.PrinterReason != "ipp: Get-Printer-Attributes answered" {
		t.Fatalf("classification = %v %q", h.IsPrinter, h.PrinterReason)
	}
	// SNMP printer: SNMP wins, IPP fills gaps.
	m := hostByIP(t, res, "10.0.0.10")
	if m.Printer == nil || m.Printer.Model != "HL-L2350DW" || m.Printer.Source != "snmp+ipp" ||
		len(m.Printer.Supplies) != 1 || m.Printer.State != "idle" {
		t.Fatalf("merged printer = %+v", m.Printer)
	}
	if fi.count("10.0.0.40") != 1 || fi.count("10.0.0.10") != 1 || fi.count("10.0.0.1") != 0 {
		t.Fatalf("ipp calls = %v", fi.calls)
	}
	if len(h.Errors) != 0 {
		t.Fatalf("errors = %v", h.Errors)
	}
}

func TestSNMPOnlyPrinterSource(t *testing.T) {
	res, err := New(fakeDeps()).Run(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if h := hostByIP(t, res, "10.0.0.10"); h.Printer == nil || h.Printer.Source != "snmp" {
		t.Fatalf("printer = %+v", h.Printer)
	}
}

func TestMergePrinterKeepsSNMPImmutable(t *testing.T) {
	snmp := &model.PrinterInfo{Model: "S"}
	out := mergePrinter(snmp, &ipp.Result{Printer: &model.PrinterInfo{Model: "I", Serial: "123"}, State: "stopped"})
	if snmp.Source != "" || snmp.Serial != "" {
		t.Fatal("input mutated")
	}
	if out.Model != "S" || out.Serial != "123" || out.State != "stopped" || out.Source != "snmp+ipp" {
		t.Fatalf("merged = %+v", out)
	}
}

func TestFlapProducesConflictNoticeAndSingleReprobe(t *testing.T) {
	sp := &scriptPinger{sweepAlive: map[string]bool{}, script: map[string][]bool{}, presence: map[string]int{}}
	macA, macB := "00:11:22:33:44:0A", "00:11:22:33:44:0B"
	a := []arp.Entry{{IP: "10.0.0.5", MAC: macA}}
	b := []arp.Entry{{IP: "10.0.0.5", MAC: macB}}
	sa := &scriptARP{tables: [][]arp.Entry{a, a, b, a, b, a, b}}
	cp := &countPorts{inner: fakePorts{"10.0.0.5": {80}}, calls: map[string]int{}}
	d := presenceDeps()
	d.Pinger = sp
	d.ARP = sa
	d.Ports = cp
	d.TCP = fakePorts{"10.0.0.5": {80}}
	evs, res := runCycles(t, d, "10.0.0.0/28", 6)

	if n := cp.calls["10.0.0.5"]; n != 2 {
		t.Fatalf("probed %d times, want 2 (initial + first MAC change)", n)
	}
	var flap *model.Conflict
	for i, c := range res.Conflicts {
		if c.Kind == model.ConflictMACFlap {
			flap = &res.Conflicts[i]
		}
	}
	if flap == nil {
		t.Fatalf("no flap conflict: %+v", res.Conflicts)
	}
	if res.Stats.Conflicts != len(res.Conflicts) {
		t.Fatalf("stats = %+v", res.Stats)
	}
	h := hostByIP(t, res, "10.0.0.5")
	if !slices.Contains(h.Conflicts, flap.ID) || len(h.MACHistory) < 3 {
		t.Fatalf("host = %+v", h)
	}
	if n := len(notices(res, model.TriggerIPConflict)); n != 1 {
		t.Fatalf("ipConflict notices = %d (%+v)", n, res.Notices)
	}
	var conflictEvents, noticeEvents int
	for _, ev := range evs {
		switch ev.Type {
		case model.EventConflict:
			if c := ev.Data.(model.ConflictEvent); c.Conflict.ID == flap.ID && !c.Resolved && !c.Replay {
				conflictEvents++
			}
		case model.EventNotice:
			if n := ev.Data.(model.NoticeEvent); n.Trigger == model.TriggerIPConflict && !n.Replay {
				noticeEvents++
			}
		}
	}
	if conflictEvents != 1 || noticeEvents != 1 {
		t.Fatalf("conflict events %d, notice events %d", conflictEvents, noticeEvents)
	}
}

func TestHealthWorseningProducesNotice(t *testing.T) {
	fh := &fakeHealth{reports: []model.HealthReport{healthReport(model.HealthOK), healthReport(model.HealthFail)}}
	d := presenceDeps()
	d.Health = fh
	evs, res := runCycles(t, d, "10.0.0.0/28", 3)
	if n := len(notices(res, model.TriggerHealthFailed)); n != 1 {
		t.Fatalf("healthFailed notices = %d (%+v)", n, res.Notices)
	}
	if res.Health == nil || res.Health.Checks[0].Status != model.HealthFail {
		t.Fatalf("health = %+v", res.Health)
	}
	healthEvents := 0
	for _, ev := range evs {
		if ev.Type == model.EventHealth {
			healthEvents++
		}
	}
	if healthEvents < 2 {
		t.Fatalf("health events = %d", healthEvents)
	}
	fh.mu.Lock()
	in := fh.inputs[0]
	runs := len(fh.inputs)
	fh.mu.Unlock()
	if runs < 4 || in.Gateway != "10.0.0.1" || in.IfaceIP != "10.0.0.30" || in.Iface != "eth0" {
		t.Fatalf("runs %d, input %+v", runs, in)
	}
}

func TestLiveNeverIPPsKnownHosts(t *testing.T) {
	d := fakeDeps()
	ports := fakePorts{"10.0.0.40": {631}, "10.0.0.41": {631}}
	d.Ports = ports
	d.TCP = ports
	base := []arp.Entry{{IP: "10.0.0.40", MAC: "00:1B:A9:00:00:40"}}
	later := append(slices.Clone(base), arp.Entry{IP: "10.0.0.41", MAC: "00:1B:A9:00:00:41"})
	d.ARP = &scriptARP{tables: [][]arp.Entry{base, base, later}}
	fi := newFakeIPP(map[string]*ipp.Result{
		"10.0.0.40": {Printer: &model.PrinterInfo{Model: "A", Source: "ipp"}},
		"10.0.0.41": {Printer: &model.PrinterInfo{Model: "B", Source: "ipp"}},
	})
	d.IPP = fi
	fm := &fakeMDNS{}
	d.MDNS = fm
	_, res := runCycles(t, d, "10.0.0.0/26", 5)
	if fi.count("10.0.0.40") != 1 || fi.count("10.0.0.41") != 1 {
		t.Fatalf("ipp calls = %v", fi.calls)
	}
	if h := hostByIP(t, res, "10.0.0.41"); h.Printer == nil || h.Printer.Model != "B" {
		t.Fatalf("new live host = %+v", h)
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if fm.rounds[0] != MDNSRoundsScan || len(fm.rounds) < 2 || fm.rounds[1] != MDNSRoundsLive {
		t.Fatalf("mdns rounds = %v", fm.rounds)
	}
}

func TestAutoSaveOnDone(t *testing.T) {
	d := fakeDeps()
	fs := &fakeStore{set: store.DefaultSettings()}
	fs.set.Community = "private"
	d.Store, d.Settings = fs, fs
	res, err := New(d).Run(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	saved := fs.list()
	if len(saved) != 1 || saved[0].ID != res.ID || saved[0].State != model.StateDone || saved[0].SchemaVersion != model.HistorySchemaVersion {
		t.Fatalf("saved = %d", len(saved))
	}
	if res.Request.Community != model.Redacted || saved[0].Request.Community != model.Redacted || res.SchemaVersion != model.HistorySchemaVersion {
		t.Fatalf("request = %+v, schema %d", res.Request, res.SchemaVersion)
	}

	// A cancelled scan is not saved.
	d.Pinger = fakePinger{block: true}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	res, _ = New(d).Run(ctx, model.ScanRequest{CIDR: "10.0.0.0/24"})
	if res.State != model.StateCancelled || len(fs.list()) != 1 {
		t.Fatalf("state %s, saves %d", res.State, len(fs.list()))
	}
}

type failStore struct{}

func (failStore) SaveScan(model.ScanResult) error { return errors.New("disk full") }

func TestAutoSaveFailureIsWarning(t *testing.T) {
	d := fakeDeps()
	d.Store = failStore{}
	res, err := New(d).Run(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24"})
	if err != nil || res.State != model.StateDone {
		t.Fatalf("err %v state %s", err, res.State)
	}
	if !slices.Contains(res.Warnings, "history: disk full") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
}

func TestAutoSaveLiveCheckpointsAndStop(t *testing.T) {
	d := presenceDeps()
	fs := &fakeStore{set: store.DefaultSettings()}
	d.Store = fs
	d.CheckpointInterval = time.Nanosecond
	_, res := runCycles(t, d, "10.0.0.0/28", 3)
	saved := fs.list()
	if len(saved) < 2 {
		t.Fatalf("saves = %d", len(saved))
	}
	for _, s := range saved[:len(saved)-1] {
		if s.State != model.StateLive || s.ID != res.ID {
			t.Fatalf("checkpoint state %s", s.State)
		}
	}
	if last := saved[len(saved)-1]; last.State != model.StateDone || last.FinishedAt == "" {
		t.Fatalf("final save = %s", last.State)
	}
}

// recordSNMP records the community each SNMP probe was given.
type recordSNMP struct {
	fakeSNMP
	mu   sync.Mutex
	seen []string
}

func (f *recordSNMP) Probe(ctx context.Context, ip, community string) (*snmpprobe.Result, error) {
	f.mu.Lock()
	f.seen = append(f.seen, community)
	f.mu.Unlock()
	return f.fakeSNMP.Probe(ctx, ip, community)
}

// F11: a scan hides the SNMP community where its outputs are made (every
// snapshot, live checkpoint, the saved result and the done event), while
// SNMP itself still gets the real community from the settings.
func TestScanOutputsRedactedAtSource(t *testing.T) {
	const secret = "s3cr3t-x"
	d := presenceDeps()
	sn := &recordSNMP{fakeSNMP: d.SNMP.(fakeSNMP)}
	d.SNMP = sn
	fs := &fakeStore{set: store.DefaultSettings()}
	fs.set.Community = secret
	d.Store, d.Settings = fs, fs
	d.CheckpointInterval = time.Nanosecond
	evs, res := runCycles(t, d, "10.0.0.0/28", 3)

	noLeak := func(what string, v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), secret) {
			t.Errorf("%s holds the community: %s", what, b)
		}
	}
	var snaps, done int
	for _, ev := range evs {
		switch ev.Type {
		case model.EventSnapshot:
			snaps++
			if r := ev.Data.(model.ScanResult); r.Request.Community != model.Redacted {
				t.Errorf("snapshot community = %q", r.Request.Community)
			}
		case model.EventDone:
			done++
			if r := ev.Data.(model.ScanResult); r.Request.Community != model.Redacted {
				t.Errorf("done community = %q", r.Request.Community)
			}
		}
		noLeak(ev.Type+" event", ev.Data)
	}
	if snaps == 0 || done != 1 {
		t.Fatalf("snapshot events %d, done events %d", snaps, done)
	}
	saved := fs.list()
	if len(saved) < 2 {
		t.Fatalf("saves = %d, want checkpoints and the final save", len(saved))
	}
	for i, s := range saved {
		if s.Request.Community != model.Redacted {
			t.Errorf("save %d community = %q", i, s.Request.Community)
		}
		noLeak("saved result", s)
	}
	if res.Request.Community != model.Redacted {
		t.Errorf("result community = %q", res.Request.Community)
	}
	noLeak("result", res)

	sn.mu.Lock()
	defer sn.mu.Unlock()
	if len(sn.seen) == 0 {
		t.Fatal("SNMP never probed")
	}
	for _, c := range sn.seen {
		if c != secret {
			t.Fatalf("SNMP got community %q, want the real one", c)
		}
	}
}

func TestReplayedNoticesFlagged(t *testing.T) {
	d := fakeDeps()
	d.Health = &fakeHealth{reports: []model.HealthReport{healthReport(model.HealthFail)}}
	s, err := New(d).Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := s.Subscribe()
	defer unsub()
	live := collect(t, ch, 5*time.Second)
	res := s.Wait()
	if len(notices(res, model.TriggerHealthFailed)) != 1 {
		t.Fatalf("notices = %+v", res.Notices)
	}

	ch2, unsub2 := s.Subscribe() // late subscriber: replay only
	defer unsub2()
	replay := collect(t, ch2, 2*time.Second)
	count := func(evs []model.Event) (n, replayed, health int) {
		for _, ev := range evs {
			switch ev.Type {
			case model.EventNotice:
				n++
				if ev.Data.(model.NoticeEvent).Replay {
					replayed++
				}
			case model.EventHealth:
				health++
			}
		}
		return
	}
	if n, r, _ := count(live); n != 1 || r != 0 {
		t.Fatalf("live notices %d replayed %d", n, r)
	}
	if n, r, h := count(replay); n != 1 || r != 1 || h != 1 {
		t.Fatalf("replay notices %d replayed %d health %d", n, r, h)
	}
}

func TestApplySettingsIgnoresMACAndResolvesConflict(t *testing.T) {
	d := presenceDeps()
	shared := "00:11:22:33:44:EE"
	d.ARP = fakeARP{{IP: "10.0.0.5", MAC: shared}, {IP: "10.0.0.6", MAC: shared}}
	d.LiveInterval = 15 * time.Millisecond
	s, err := New(d).Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/28", Live: true})
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := s.Subscribe()
	defer unsub()
	waitLive(t, ch, func(model.LiveStatus) bool { return true })
	if snap := s.Snapshot(); len(snap.Conflicts) != 1 || snap.Conflicts[0].Kind != model.ConflictMACMultiIP {
		t.Fatalf("conflicts = %+v", snap.Conflicts)
	}
	set := store.DefaultSettings()
	set.IgnoredConflictMACs = []string{shared}
	if !s.ApplySettings(set) {
		t.Fatal("apply refused on running scan")
	}
	waitLive(t, ch, func(st model.LiveStatus) bool { return st.Cycle >= 1 })
	if snap := s.Snapshot(); len(snap.Conflicts) != 0 {
		t.Fatalf("conflicts after ignore = %+v", snap.Conflicts)
	}
	s.Cancel()
	s.Wait()
	if s.ApplySettings(set) {
		t.Fatal("apply accepted after finish")
	}
}

// Compile-time checks that the production types satisfy the interfaces.
var (
	_ HealthChecker  = (*health.Runner)(nil)
	_ IPPClient      = (*ipp.Client)(nil)
	_ HistoryStore   = (*store.Store)(nil)
	_ SettingsSource = (*store.Store)(nil)
	_ MDNSBrowser    = mdnsBrowser{}
)

func TestClassifySetsDeviceType(t *testing.T) {
	h := model.Host{IP: "10.0.0.1"}
	classify(&h, "10.0.0.1")
	if h.DeviceType != "gateway" || h.DeviceTypeReason == "" {
		t.Fatalf("gateway: %q %q", h.DeviceType, h.DeviceTypeReason)
	}
	p := model.Host{IP: "10.0.0.9"}
	classify(&p, "10.0.0.1")
	if p.DeviceType == "gateway" || p.DeviceType == "" {
		t.Fatalf("plain host type = %q", p.DeviceType)
	}
	before := p.DeviceType
	p.MDNS = &model.MDNSInfo{Services: []string{"_ipp._tcp"}}
	classify(&p, "10.0.0.1")
	if p.DeviceType != "printer" || p.DeviceType == before {
		t.Fatalf("after mDNS: %q (before %q)", p.DeviceType, before)
	}
}

// scriptSNMP answers for one IP; the first `faulty` calls report an open door,
// later calls a healthy printer.
type scriptSNMP struct {
	mu          sync.Mutex
	ip          string
	faulty      int
	calls       int
	faultyPolls int
	polls       int
}

func (f *scriptSNMP) Probe(ctx context.Context, ip, community string) (*snmpprobe.Result, error) {
	if ip != f.ip {
		return nil, fmt.Errorf("%w: timeout", snmpprobe.ErrNoResponse)
	}
	f.mu.Lock()
	f.calls++
	bad := f.calls <= f.faulty
	f.mu.Unlock()
	p := &model.PrinterInfo{Model: "ZD621"}
	si := &model.SnmpInfo{Version: "v2c", SysDescr: "Zebra ZD621", HrDeviceStatus: "running", HrPrinterStatus: "idle"}
	if bad {
		p.Alerts = []model.Alert{{Severity: "critical", Code: "hrPrinterDetectedErrorState.doorOpen", Description: "Door or cover open"}}
		si.HrDeviceStatus, si.HrPrinterDetectedErrorState = "down", []string{"doorOpen"}
	}
	return &snmpprobe.Result{Snmp: si, Printer: p, Status: &snmpprobe.StatusTarget{Version: "v2c", DevIdx: "1", PrtIdx: "1"}}, nil
}

// PollStatus reports the open door for the first faultyPolls polls, then a
// healthy printer.
func (f *scriptSNMP) PollStatus(ctx context.Context, ip, community string, t snmpprobe.StatusTarget) (snmpprobe.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls++
	if f.polls <= f.faultyPolls {
		return snmpprobe.Status{DeviceStatus: "down", PrinterStatus: "idle", ErrorState: []string{"doorOpen"}}, nil
	}
	return snmpprobe.Status{DeviceStatus: "running", PrinterStatus: "idle"}, nil
}

func TestLivePollClearsPrinterAlert(t *testing.T) {
	d := fakeDeps()
	fs := &scriptSNMP{ip: "10.0.0.10", faulty: 1, faultyPolls: 1 << 30}
	d.SNMP = fs
	d.PrinterPoll = time.Nanosecond
	_, res := runCycles(t, d, "10.0.0.0/26", 4)
	if h := hostByIP(t, res, "10.0.0.10"); h.Printer == nil || len(h.Printer.Alerts) != 0 {
		t.Fatalf("printer after polls = %+v", h.Printer)
	}
	var raised, cleared int
	for _, n := range res.Notices {
		if n.IP == "10.0.0.10" && n.Trigger == model.TriggerPrinterAlert {
			if n.Title == "Printer OK again" {
				cleared++
			} else {
				raised++
			}
		}
	}
	if raised != 1 || cleared != 1 {
		t.Fatalf("raised %d cleared %d (%+v)", raised, cleared, res.Notices)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.calls < 2 {
		t.Fatalf("snmp calls = %d", fs.calls)
	}
}

func TestFastStatusPollClearsPrinterAlert(t *testing.T) {
	d := fakeDeps()
	fs := &scriptSNMP{ip: "10.0.0.10", faulty: 1, faultyPolls: 1}
	d.SNMP = fs
	d.PrinterPoll = time.Hour // only the fast status poll can notice the fix
	d.StatusPoll = 5 * time.Millisecond
	_, res := runCycles(t, d, "10.0.0.0/26", 6)
	if h := hostByIP(t, res, "10.0.0.10"); h.Printer == nil || len(h.Printer.Alerts) != 0 || h.Snmp.HrDeviceStatus != "running" {
		t.Fatalf("printer after fast poll = %+v / %+v", h.Printer, h.Snmp)
	}
	cleared := 0
	for _, n := range res.Notices {
		if n.IP == "10.0.0.10" && n.Title == "Printer OK again" {
			cleared++
		}
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if cleared != 1 || fs.polls < 2 || fs.calls != 2 {
		t.Fatalf("cleared %d, polls %d, full probes %d", cleared, fs.polls, fs.calls)
	}
}

func TestStatusEveryFollowsSettings(t *testing.T) {
	r := &runner{settings: model.Settings{PrinterStatusPollSec: 7}}
	if got := r.statusEvery(); got != 7*time.Second {
		t.Fatalf("from settings: %v", got)
	}
	r.settings = model.Settings{}
	if got := r.statusEvery(); got != StatusPollInterval {
		t.Fatalf("default: %v", got)
	}
	r.d.StatusPoll = time.Millisecond
	if got := r.statusEvery(); got != time.Millisecond {
		t.Fatalf("deps override: %v", got)
	}
}
