package scan

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"netdiag/internal/arp"
	"netdiag/internal/model"
)

// The storm guard's network check (v0.6 F9c, D36): another network stops
// all alerting until Restart watching, and only another network does.

// movingEnv is this computer on the prefix cidr until Self call from (1 is
// the scan's start), then at the address to.
type movingEnv struct {
	n    atomic.Int32
	from int32
	cidr string
	to   model.SelfInfo
}

func (e *movingEnv) Self(string) (model.SelfInfo, error) {
	if e.n.Add(1) >= e.from {
		return e.to, nil
	}
	return model.SelfInfo{IP: "10.0.0.30", MAC: "aa:bb:cc:00:00:30", Interface: "eth0", CIDR: e.cidr}, nil
}

func (*movingEnv) Gateway() (string, error) { return "10.0.0.1", nil }

// heldReasons returns the reason of each live status in order ("" when not
// held).
func heldReasons(evs []model.Event) []string {
	var out []string
	for _, st := range liveStatuses(evs) {
		out = append(out, st.HeldReason)
	}
	return out
}

// This computer moves to a network with another address prefix. Whether
// or not the target held its address, the guard holds network_changed
// after AwayCycles cycles (lost link in the cycle between, when its new
// address left the target), never lets go, and its netdiagLink problem
// says so under one key: the banner that said "lost the network" says
// "different network" and offers Restart watching.
func TestOtherPrefixHoldsNetworkChanged(t *testing.T) {
	for _, tc := range []struct {
		target string
		codes  []string // the netdiagLink notices' codes
	}{
		{"10.0.0.0/27", []string{model.HeldLink, model.HeldNetworkChanged}}, // holds 10.0.0.30
		{"10.0.0.0/28", []string{model.HeldNetworkChanged}},                 // leaves it out
	} {
		d := presenceDeps()
		d.ARP = fakeARP{{IP: "10.0.0.1", MAC: gwMAC}}
		d.Env = &movingEnv{from: 3, cidr: "10.0.0.0/24", // from cycle 2
			to: model.SelfInfo{IP: "192.168.1.30", Interface: "eth0", CIDR: "192.168.1.0/24"}}
		evs, res := runCycles(t, d, tc.target, 6)
		want := model.NetworkID("10.0.0.0/24", "10.0.0.1", gwMAC)
		link := notices(res, model.TriggerNetdiagLink)
		if len(link) != len(tc.codes) {
			t.Fatalf("%s: netdiagLink notices %+v", tc.target, link)
		}
		for i, n := range link {
			if n.OK || n.Code != tc.codes[i] || n.Key != "netdiagLink||"+want || n.Advice == nil {
				t.Fatalf("%s: notice %d: %+v", tc.target, i, n)
			}
		}
		last := link[len(link)-1]
		if last.Title != "This computer is on a different network" || !strings.Contains(last.Advice.Meaning, "different network") {
			t.Fatalf("%s: last notice %+v", tc.target, last)
		}
		reasons := heldReasons(evs)
		if reasons[len(reasons)-1] != model.HeldNetworkChanged {
			t.Fatalf("%s: reasons %v", tc.target, reasons)
		}
		for i, r := range reasons {
			if r == model.HeldNetworkChanged && slices.Contains(reasons[i:], "") {
				t.Fatalf("%s: network_changed let go: %v", tc.target, reasons)
			}
		}
	}
}

// The usual way onto another network goes through a lost link: Wi-Fi drops
// (Self fails for a cycle), then this computer joins a network with the
// same prefix and another router. The netdiagLink problem gets a second
// notice under the same key that says the stronger reason, with no OK in
// between.
func TestHoldReasonRaised(t *testing.T) {
	a := []arp.Entry{{IP: "10.0.0.1", MAC: gwMAC}}
	b := []arp.Entry{{IP: "10.0.0.1", MAC: "00:11:22:33:44:02"}}
	d := presenceDeps()
	d.Pinger = &scriptPinger{sweepAlive: map[string]bool{}, script: map[string][]bool{}, presence: map[string]int{}}
	d.ARP = &scriptARP{tables: [][]arp.Entry{a, a, nil, b}} // harvest, cycles 1, 2, 3...
	d.Env = &flakyEnv{fail: map[int32]bool{3: true}}        // cycle 2
	evs, res := runCycles(t, d, "10.0.0.0/28", 6)
	link := notices(res, model.TriggerNetdiagLink)
	if len(link) != 2 || link[0].OK || link[1].OK || link[0].Key != link[1].Key ||
		link[0].Code != model.HeldLink || link[0].Title != "This computer lost the network" ||
		link[1].Code != model.HeldNetworkChanged || link[1].Title != "This computer is on a different network" ||
		link[1].Advice == nil || !strings.Contains(link[1].Advice.Meaning, "different network") {
		t.Fatalf("netdiagLink notices: %+v", link)
	}
	reasons := heldReasons(evs)
	if !slices.Contains(reasons, model.HeldLink) || reasons[len(reasons)-1] != model.HeldNetworkChanged {
		t.Fatalf("reasons %v", reasons)
	}
	for _, st := range liveStatuses(evs) {
		if st.Held && st.HeldSince == "" {
			t.Fatalf("held without a time: %+v", st)
		}
	}
}

// recProfiles records the networks a scan says it saw.
type recProfiles struct {
	mu   sync.Mutex
	seen []string
}

func (p *recProfiles) Seen(fp Fingerprint, _ time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, fp.ID)
	return nil
}

// hookARP is a scriptARP that calls hook just before it serves read at.
type hookARP struct {
	*scriptARP
	at   int
	hook func()
}

func (a *hookARP) Table(ctx context.Context) ([]arp.Entry, error) {
	a.mu.Lock()
	n := a.n
	a.mu.Unlock()
	if n == a.at {
		a.hook()
	}
	return a.scriptARP.Table(ctx)
}

// Another router at the same prefix, then new devices and a printer that
// reports its cover open: netdiag tells nobody (no "New device", no printer
// alert, only the netdiagLink notice), and the ledger and
// network-profile.json hear nothing more under the old network's id from
// the ARP read that saw the other router on.
func TestOtherNetworkStopsAlerts(t *testing.T) {
	a := []arp.Entry{{IP: "10.0.0.1", MAC: gwMAC}}
	b := []arp.Entry{{IP: "10.0.0.1", MAC: "00:11:22:33:44:02"},
		{IP: "10.0.0.12", MAC: "00:07:4D:00:00:12"}, {IP: "10.0.0.13", MAC: "00:07:4D:00:00:13"}}
	rl := &recLedger{}
	rp := &recProfiles{}
	var before atomic.Int32 // the ledger's Apply calls before the other router
	d := fakeDeps()
	d.ARP = &hookARP{scriptARP: &scriptARP{tables: [][]arp.Entry{a, a, b}}, at: 2, hook: func() {
		rl.mu.Lock()
		before.Store(int32(rl.applies))
		rl.mu.Unlock()
	}}
	d.SNMP = fakeSNMP{"10.0.0.12": {
		Snmp:    &model.SnmpInfo{Version: "v2c", SysDescr: "Zebra ZD621", HrPrinterStatus: "other"},
		Printer: &model.PrinterInfo{Model: "ZD621", Alerts: coverOpen},
	}}
	d.Ledger = rl
	d.Profiles = rp
	d.CheckpointInterval = time.Nanosecond // a checkpoint every cycle
	evs, res := runCycles(t, d, "10.0.0.0/28", 6)
	for _, n := range res.Notices {
		if n.Trigger != model.TriggerNetdiagLink {
			t.Fatalf("a notice on the other network: %+v", n)
		}
	}
	if link := notices(res, model.TriggerNetdiagLink); len(link) != 1 || link[0].Code != model.HeldNetworkChanged {
		t.Fatalf("netdiagLink notices: %+v", link)
	}
	if hostByIP(t, res, "10.0.0.12").Printer == nil {
		t.Fatal("the printer was not probed")
	}
	rl.mu.Lock()
	applies, applied := rl.applies, slices.Clone(rl.applied)
	rl.mu.Unlock()
	if applies != int(before.Load()) || len(applied) != 0 {
		t.Fatalf("ledger: %d Apply calls (%d before the other router), notices %+v", applies, before.Load(), applied)
	}
	rp.mu.Lock()
	defer rp.mu.Unlock()
	want := model.NetworkID("", "10.0.0.1", gwMAC)
	if len(rp.seen) != 2 || rp.seen[0] != want || rp.seen[1] != want { // the first pass and cycle 1's checkpoint
		t.Fatalf("profiles seen %v", rp.seen)
	}
	if r := heldReasons(evs); r[len(r)-1] != model.HeldNetworkChanged {
		t.Fatalf("reasons %v", r)
	}
}

// One ARP read with another MAC at the router's address is not another
// network: the next read has the router back, nothing is held, and the
// notices of the cycle in between go out (to the ledger too). Nor is a
// router address taking turns between two MACs (an address clash the
// conflict analysis reports), however many reads in a row show the other
// one.
func TestRouterMACBlipNotHeld(t *testing.T) {
	a := []arp.Entry{{IP: "10.0.0.1", MAC: gwMAC}}
	b := []arp.Entry{{IP: "10.0.0.1", MAC: "00:11:22:33:44:02"}, {IP: "10.0.0.12", MAC: "00:07:4D:00:00:12"}}
	for _, tc := range []struct {
		name   string
		tables [][]arp.Entry
		cycles int
	}{
		{"one read", [][]arp.Entry{a, a, b, a}, 5},
		{"taking turns", [][]arp.Entry{a, a, b, a, b, b}, 8},
	} {
		rl := &recLedger{}
		d := presenceDeps()
		d.Pinger = &scriptPinger{sweepAlive: map[string]bool{}, script: map[string][]bool{}, presence: map[string]int{}}
		d.ARP = &scriptARP{tables: tc.tables}
		d.Ledger = rl
		evs, res := runCycles(t, d, "10.0.0.0/28", tc.cycles)
		for _, st := range liveStatuses(evs) {
			if st.Held {
				t.Fatalf("%s: held %+v", tc.name, st)
			}
		}
		if n := notices(res, model.TriggerNetdiagLink); len(n) != 0 {
			t.Fatalf("%s: netdiagLink %+v", tc.name, n)
		}
		found := false
		for _, n := range notices(res, model.TriggerNewDevice) {
			found = found || n.IP == "10.0.0.12"
		}
		rl.mu.Lock()
		inLedger := slices.ContainsFunc(rl.applied, func(n model.Notice) bool {
			return n.Trigger == model.TriggerNewDevice && n.IP == "10.0.0.12"
		})
		rl.mu.Unlock()
		if !found || !inLedger {
			t.Fatalf("%s: the new device's notice was not let out (ledger %v): %+v", tc.name, inLedger, res.Notices)
		}
		if tc.name == "taking turns" && !slices.ContainsFunc(res.Conflicts, func(c model.Conflict) bool {
			return c.Kind == model.ConflictMACFlap && slices.Contains(c.IPs, "10.0.0.1")
		}) {
			t.Fatalf("%s: no mac_flap at the router: %+v", tc.name, res.Conflicts)
		}
	}
}

// llEnv is this computer with a self-assigned address only.
type llEnv struct{}

func (llEnv) Self(string) (model.SelfInfo, error) {
	return model.SelfInfo{IP: "169.254.7.30", Interface: "eth0", CIDR: "169.254.0.0/16"}, nil
}

func (llEnv) Gateway() (string, error) { return "", errors.New("no default gateway") }

// A self-assigned 169.254 address where the scan began with a lease is a
// lost link (the lease is gone), not another network; where the scan began
// with one, it is just this computer's address.
func TestSelfAssignedAddress(t *testing.T) {
	d := presenceDeps()
	d.ARP = fakeARP{{IP: "10.0.0.1", MAC: gwMAC}}
	d.Env = &movingEnv{from: 3, cidr: "10.0.0.0/24",
		to: model.SelfInfo{IP: "169.254.7.30", Interface: "eth0", CIDR: "169.254.0.0/16"}}
	evs, res := runCycles(t, d, "10.0.0.0/28", 5)
	if r := heldReasons(evs); r[len(r)-1] != model.HeldLink || slices.Contains(r, model.HeldNetworkChanged) {
		t.Fatalf("lease gone: reasons %v", r)
	}
	if link := notices(res, model.TriggerNetdiagLink); len(link) != 1 || link[0].Code != model.HeldLink {
		t.Fatalf("lease gone: %+v", link)
	}

	d = presenceDeps()
	d.Env = llEnv{}
	evs, _ = runCycles(t, d, "169.254.7.16/28", 4)
	for _, st := range liveStatuses(evs) {
		if st.Held {
			t.Fatalf("began self-assigned: held %+v", st)
		}
	}
}

// Another router, then a cycle without a link (Wi-Fi dropped), then the
// next cycle tells: the same router again lets the held-back notices out
// (the new device is announced), another router again holds
// network_changed and drops them.
func TestOtherRouterAcrossLinkLoss(t *testing.T) {
	a := []arp.Entry{{IP: "10.0.0.1", MAC: gwMAC}}
	b := []arp.Entry{{IP: "10.0.0.1", MAC: "00:11:22:33:44:02"}, {IP: "10.0.0.12", MAC: "00:07:4D:00:00:12"}}
	for _, tc := range []struct {
		name  string
		after []arp.Entry // the ARP read of cycle 4
		codes []string
		isNew bool
	}{
		{"back", a, []string{model.HeldLink}, true},
		{"other", b, []string{model.HeldLink, model.HeldNetworkChanged}, false},
	} {
		d := presenceDeps()
		d.Pinger = &scriptPinger{sweepAlive: map[string]bool{}, script: map[string][]bool{}, presence: map[string]int{}}
		d.ARP = &scriptARP{tables: [][]arp.Entry{a, a, b, nil, tc.after}} // harvest, cycles 1 to 4
		d.Env = &flakyEnv{fail: map[int32]bool{4: true}}                  // cycle 3
		_, res := runCycles(t, d, "10.0.0.0/28", 6)
		var codes []string
		for _, n := range notices(res, model.TriggerNetdiagLink) {
			if !n.OK {
				codes = append(codes, n.Code)
			}
		}
		isNew := slices.ContainsFunc(notices(res, model.TriggerNewDevice), func(n model.Notice) bool { return n.IP == "10.0.0.12" })
		if !slices.Equal(codes, tc.codes) || isNew != tc.isNew {
			t.Fatalf("%s: link codes %v, new device %v: %+v", tc.name, codes, isNew, res.Notices)
		}
	}
}
