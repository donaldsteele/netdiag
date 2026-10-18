package scan

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"netdiag/internal/health"
	"netdiag/internal/model"
	"netdiag/internal/notify"
	"netdiag/internal/problems"
	"netdiag/internal/store"
)

// The Zebra ZD621's cover alerts (v0.6-D25: one problem).
var (
	coverOpen = []model.Alert{
		{Severity: "critical", Code: "hrPrinterDetectedErrorState.doorOpen", Description: "Door or cover open"},
		{Severity: "critical", Code: "hrPrinterDetectedErrorState.offline", Description: "Printer offline or paused"},
	}
	gwMAC = "00:11:22:33:44:01" // the router's MAC in fakeDeps' ARP table
)

// zebraDeps is fakeDeps with the printer at 10.0.0.10 reporting alerts.
func zebraDeps(alerts []model.Alert) Deps {
	d := fakeDeps()
	d.SNMP = fakeSNMP{"10.0.0.10": {
		Snmp:    &model.SnmpInfo{Version: "v2c", SysDescr: "Zebra ZD621", HrPrinterStatus: "other"},
		Printer: &model.PrinterInfo{Model: "ZD621", Alerts: alerts},
	}}
	return d
}

// openLedger opens the ledger in dir, as `netdiag ui` does at start.
func openLedger(t *testing.T, st *store.Store) *problems.Ledger {
	t.Helper()
	l, err := problems.OpenLedger(st)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// oneShot runs a one-shot scan of 10.0.0.0/24 with the ledger and flushes
// it, as Quit does.
func oneShot(t *testing.T, d Deps, l *problems.Ledger) model.ScanResult {
	t.Helper()
	d.Ledger = l
	res, err := New(d).Run(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
	return res
}

// Acceptance (M6): quit with the printer's cover open, close it, start
// again: the first pass gives one OK and no fault, and the ledger's fixed
// list gets one entry. Restarted with the cover still open: no notice, no
// duplicate.
func TestLedgerAcrossRestarts(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	net := model.NetworkID("", "10.0.0.1", gwMAC)

	res := oneShot(t, zebraDeps(coverOpen), openLedger(t, st))
	first := notices(res, model.TriggerPrinterAlert)
	if len(first) != 1 || first[0].OK {
		t.Fatalf("first run: %+v", first)
	}

	// Restart with the cover still open.
	l := openLedger(t, st)
	res = oneShot(t, zebraDeps(coverOpen), l)
	if n := notices(res, model.TriggerPrinterAlert); len(n) != 0 {
		t.Fatalf("still open: %+v", n)
	}
	open, fixed := l.Snapshot()
	if len(open) != 1 || open[0].Key != first[0].Key || open[0].OpenedAt != first[0].At || open[0].Network != net || len(fixed) != 0 {
		t.Fatalf("ledger: open %+v fixed %+v", open, fixed)
	}

	// The cover was closed while netdiag was not running.
	l = openLedger(t, st)
	res = oneShot(t, zebraDeps(nil), l)
	n := notices(res, model.TriggerPrinterAlert)
	if len(n) != 1 || !n[0].OK || n[0].Key != first[0].Key || n[0].OpenedAt != first[0].At {
		t.Fatalf("fixed while closed: %+v", n)
	}
	open, fixed = l.Snapshot()
	if len(open) != 0 || len(fixed) != 1 || fixed[0].Key != first[0].Key || fixed[0].Network != net {
		t.Fatalf("ledger: open %+v fixed %+v", open, fixed)
	}
}

// recLedger records what a scan asks of its ledger.
type recLedger struct {
	mu      sync.Mutex
	open    []notify.OpenFault
	opened  []string // OpenFor calls
	applied []model.Notice
	applies int
}

func (l *recLedger) Apply(network string, ns []model.Notice) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.applies++
	l.applied = append(l.applied, ns...)
}

func (l *recLedger) OpenFor(network string) []notify.OpenFault {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.opened = append(l.opened, network)
	return l.open
}

// The reconcile runs once, right after the first pass's analysis, for live
// and one-shot scans, and the ledger is handed every notice of the scan. A
// conflict is checked only when the target covers this computer's whole
// network: otherwise the analysis may have left one of its devices out, and
// the scan lists it as adopted unchecked (Scan.Adopted).
func TestReconcileOnceAndApplyEvery(t *testing.T) {
	conflict := notify.OpenFault{Key: "ipConflict|mac_flap|10.0.0.99", Trigger: model.TriggerIPConflict, IP: "10.0.0.99"}
	for _, tc := range []struct {
		live bool
		cidr string // this computer's prefix ("": unknown, so not covered)
		ok   bool   // the conflict, gone, gets its OK
	}{
		{false, "", false},
		{true, "", false},
		{false, "10.0.0.0/24", true},
		{true, "10.0.0.0/24", true},
		{false, "10.0.0.0/23", false},
	} {
		rl := &recLedger{open: []notify.OpenFault{conflict}}
		d := zebraDeps(coverOpen)
		d.Env = selfEnv{cidr: tc.cidr}
		d.Ledger = rl
		d.LiveInterval = 10 * time.Millisecond
		s, err := New(d).Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24", Live: tc.live})
		if err != nil {
			t.Fatal(err)
		}
		if tc.live {
			ch, unsub := s.Subscribe()
			waitLive(t, ch, func(st model.LiveStatus) bool { return st.Cycle >= 2 })
			unsub()
			s.Cancel()
		}
		res := s.Wait()
		rl.mu.Lock()
		want := NewFingerprint(tc.cidr, "10.0.0.1", gwMAC).ID
		if len(rl.opened) != 1 || rl.opened[0] != want {
			t.Fatalf("%+v: OpenFor calls %v", tc, rl.opened)
		}
		if len(rl.applied) != len(res.Notices) || len(res.Notices) == 0 {
			t.Fatalf("%+v: applied %v, notices %v", tc, rl.applied, res.Notices)
		}
		for i := range res.Notices {
			if rl.applied[i].ID != res.Notices[i].ID {
				t.Fatalf("%+v: applied %v, notices %v", tc, rl.applied, res.Notices)
			}
		}
		rl.mu.Unlock()
		if got := len(notices(res, model.TriggerIPConflict)) == 1; got != tc.ok {
			t.Fatalf("%+v: conflict notices %v", tc, notices(res, model.TriggerIPConflict))
		}
		// A conflict the pass did not check is the one problem it adopted.
		var adopted []string
		if !tc.ok {
			adopted = []string{conflict.Key}
		}
		if got := s.Adopted(); !slices.Equal(got, adopted) {
			t.Fatalf("%+v: adopted %v", tc, got)
		}
	}
}

// latePinger answers the first sweep ping of late with no reply and every
// other ping of it: the device is off during the first pass and back for
// the live cycles.
type latePinger struct {
	late string
	n    atomic.Int32
}

func (p *latePinger) Ping(_ context.Context, ip netip.Addr, timeout time.Duration) (time.Duration, bool, error) {
	if ip.String() != p.late {
		return 0, false, nil
	}
	if timeout != PresencePingTimeout && p.n.Add(1) == 1 {
		return 0, false, nil
	}
	return time.Millisecond, true, nil
}

// A printer offline when netdiag last ran (kept under its MAC) is not found
// by the first pass and answers the sweep of a later live cycle, before the
// ARP read has its MAC: it gets its OK at once, and no "New device".
func TestSeededOfflineBackInLiveCycle(t *testing.T) {
	const key = "printerOffline|3C:52:82:11:22:33|"
	rl := &recLedger{open: []notify.OpenFault{{Key: key, Trigger: model.TriggerPrinterOffline, IP: "10.0.0.10",
		MAC: "3C:52:82:11:22:33", Name: "ZD621", Title: "Printer offline"}}}
	d := presenceDeps()
	d.Pinger = &latePinger{late: "10.0.0.10"}
	d.ARP = fakeARP{{IP: "10.0.0.1", MAC: gwMAC}}
	d.Ledger = rl
	_, res := runCycles(t, d, "10.0.0.0/28", 2)
	if n := notices(res, model.TriggerNewDevice); len(n) != 0 {
		t.Fatalf("new device: %+v", n)
	}
	n := notices(res, model.TriggerPrinterOffline)
	if len(n) != 1 || !n[0].OK || n[0].Key != key || n[0].Title != "Printer back online" {
		t.Fatalf("offline notices: %+v", n)
	}
}

// selfEnv is fakeEnv with this computer's address prefix.
type selfEnv struct{ cidr string }

func (e selfEnv) Self(string) (model.SelfInfo, error) {
	return model.SelfInfo{IP: "10.0.0.30", MAC: "aa:bb:cc:00:00:30", Interface: "eth0", CIDR: e.cidr}, nil
}
func (selfEnv) Gateway() (string, error) { return "10.0.0.1", nil }

// networkHealth names the network it checked the way health.Runner does:
// model.NetworkID of the input's prefix, the router and the router's MAC
// from the ARP cache.
type networkHealth struct{ mac string }

func (h networkHealth) Run(ctx context.Context, in health.Input) model.HealthReport {
	return model.HealthReport{CheckedAt: "2026-10-17T10:00:00Z", Network: model.NetworkID(in.CIDR, in.Gateway, h.mac),
		Checks: []model.HealthCheck{{ID: "gateway", Label: "Gateway reachable", Status: model.HealthOK}}}
}

// The fingerprint is taken at the end of the first pass, saved in
// network-profile.json and shown in the live status, and its id is the id
// the health report names for the same network (the D15 switch's id).
func TestFingerprintSaved(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prof := NewProfiles(st)
	d := fakeDeps()
	d.Env = selfEnv{cidr: "10.0.0.0/24"}
	d.Health = networkHealth{mac: gwMAC}
	d.Profiles = prof
	d.LiveInterval = 10 * time.Millisecond
	s, err := New(d).Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24", Live: true})
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := s.Subscribe()
	defer unsub()
	stat := waitLive(t, ch, func(model.LiveStatus) bool { return true })
	s.Cancel()
	res := s.Wait()
	want := model.NetworkID("10.0.0.0/24", "10.0.0.1", gwMAC)
	if stat.Network != want || res.Health == nil || res.Health.Network != want || s.Network() != want {
		t.Fatalf("live network %q, health network %v, scan network %q, want %q", stat.Network, res.Health, s.Network(), want)
	}
	list, err := prof.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("profiles %+v (%v)", list, err)
	}
	if p := list[0]; p.ID != want || p.CIDR != "10.0.0.0/24" || p.Gateway != "10.0.0.1" || p.GatewayMAC != gwMAC || p.FirstSeen == "" || p.LastSeen == "" {
		t.Fatalf("profile %+v", p)
	}
}

func TestNewFingerprint(t *testing.T) {
	for _, tc := range []struct {
		cidr, gw, mac, want string
	}{
		{"10.0.0.0/24", "10.0.0.1", gwMAC, model.NetworkID("10.0.0.0/24", "10.0.0.1", gwMAC)},
		{"10.0.0.0/24", "", "", model.NetworkID("10.0.0.0/24", "", "")},
		{"10.0.0.0/24", "10.0.0.1", "", ""}, // a router netdiag cannot tell apart
		{"", "", "", ""},
	} {
		if got := NewFingerprint(tc.cidr, tc.gw, tc.mac).ID; got != tc.want {
			t.Errorf("NewFingerprint(%q, %q, %q) = %q, want %q", tc.cidr, tc.gw, tc.mac, got, tc.want)
		}
	}
	fp := NewFingerprint("10.0.0.0/24", "10.0.0.1", gwMAC)
	for _, tc := range []struct {
		cidr, mac string
		same      bool
	}{
		{"10.0.0.0/24", gwMAC, true},
		{"10.0.0.30/24", strings.ToLower(gwMAC), true},
		{"", "", true},
		{"10.0.1.0/24", gwMAC, false},
		{"10.0.0.0/24", "00:11:22:33:44:02", false},
	} {
		if got := fp.samePrefix(tc.cidr) && fp.sameRouter(tc.mac); got != tc.same {
			t.Errorf("sameNetwork(%q, %q) = %v", tc.cidr, tc.mac, got)
		}
	}
}

// network-profile.json keeps at most MaxProfiles networks, the one seen
// last first; an unreadable file is set aside.
func TestProfiles(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := NewProfiles(st)
	t0 := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	for i := range MaxProfiles + 3 {
		fp := NewFingerprint("10.0."+string(rune('0'+i%10))+".0/24", "", "")
		fp.Gateway = string(rune('a' + i)) // distinct entries
		fp.ID = model.NetworkID(fp.CIDR, fp.Gateway, "x")
		if err := p.Seen(fp, t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	first := NewFingerprint("10.9.0.0/24", "", "")
	if err := p.Seen(first, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := p.Seen(first, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := p.Seen(Fingerprint{CIDR: "10.0.0.0/24", Gateway: "10.0.0.1"}, t0); err != nil {
		t.Fatal(err) // no id: not recorded
	}
	list, err := p.List()
	if err != nil || len(list) != MaxProfiles || list[0].ID != first.ID ||
		list[0].FirstSeen != "2026-10-17T11:00:00Z" || list[0].LastSeen != "2026-10-17T12:00:00Z" {
		t.Fatalf("list %d %+v (%v)", len(list), list[0], err)
	}
	if err := st.WriteFile(store.FileNetworkProfile, []byte("[")); err != nil {
		t.Fatal(err)
	}
	if list, err := p.List(); err == nil || len(list) != 0 || !strings.Contains(err.Error(), ".bad-") {
		t.Fatalf("corrupt: %v %v", list, err)
	}
	if err := p.Seen(first, t0); err != nil {
		t.Fatal(err)
	}
	if list, err := p.List(); err != nil || len(list) != 1 {
		t.Fatalf("after set aside: %v %v", list, err)
	}
}
