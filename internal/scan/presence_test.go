package scan

import (
	"context"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"

	"netdiag/internal/arp"
	"netdiag/internal/model"
)

// scriptPinger answers discovery sweeps from sweepAlive and presence checks
// (told apart by PresencePingTimeout) from a per-IP script indexed by check
// number; the last script entry repeats.
type scriptPinger struct {
	mu         sync.Mutex
	sweepAlive map[string]bool
	script     map[string][]bool
	presence   map[string]int
}

func (p *scriptPinger) Ping(ctx context.Context, ip netip.Addr, timeout time.Duration) (time.Duration, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	k := ip.String()
	if timeout != PresencePingTimeout {
		return time.Millisecond, p.sweepAlive[k], nil
	}
	i := p.presence[k]
	p.presence[k]++
	sc := p.script[k]
	if len(sc) == 0 {
		return 0, false, nil
	}
	return time.Millisecond, sc[min(i, len(sc)-1)], nil
}

func (p *scriptPinger) presenceCount(ip string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.presence[ip]
}

// scriptARP returns tables[call] (the last table repeats).
type scriptARP struct {
	mu     sync.Mutex
	tables [][]arp.Entry
	n      int
}

func (a *scriptARP) Table(context.Context) ([]arp.Entry, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.tables[min(a.n, len(a.tables)-1)]
	a.n++
	return t, nil
}

// countTCP is a presence PortChecker answering from open with call counts.
type countTCP struct {
	open  fakePorts
	mu    sync.Mutex
	calls map[string]int
}

func (c *countTCP) Reachable(ctx context.Context, ip string, port int, timeout time.Duration) bool {
	c.mu.Lock()
	c.calls[ip]++
	c.mu.Unlock()
	return c.open.Reachable(ctx, ip, port, timeout)
}

func (c *countTCP) count(ip string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[ip]
}

// runCycles runs a live scan of cidr until the given live cycle completed,
// then cancels it and returns every event seen plus the final result.
func runCycles(t *testing.T, d Deps, cidr string, cycles int) ([]model.Event, model.ScanResult) {
	t.Helper()
	d.LiveInterval = 15 * time.Millisecond
	s, err := New(d).Start(context.Background(), model.ScanRequest{CIDR: cidr, Live: true})
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := s.Subscribe()
	defer unsub()
	var evs []model.Event
	timeout := time.After(4 * time.Second)
wait:
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("stream closed early")
			}
			evs = append(evs, ev)
			if ev.Type == model.EventLive && ev.Data.(model.LiveStatus).Cycle >= cycles {
				break wait
			}
		case <-timeout:
			t.Fatalf("timed out before cycle %d", cycles)
		}
	}
	s.Cancel()
	evs = append(evs, collect(t, ch, 2*time.Second)...)
	return evs, s.Wait()
}

func presenceEvents(evs []model.Event, ip string) []model.Presence {
	var out []model.Presence
	for _, ev := range evs {
		if ev.Type == model.EventPresence {
			if p := ev.Data.(model.Presence); p.IP == ip {
				out = append(out, p)
			}
		}
	}
	return out
}

func hostEvents(evs []model.Event, ip string) []model.Host {
	var out []model.Host
	for _, ev := range evs {
		if ev.Type == model.EventHost {
			if h := ev.Data.(model.Host); h.IP == ip {
				out = append(out, h)
			}
		}
	}
	return out
}

func hostByIP(t *testing.T, res model.ScanResult, ip string) model.Host {
	t.Helper()
	for _, h := range res.Hosts {
		if h.IP == ip {
			return h
		}
	}
	t.Fatalf("host %s missing", ip)
	return model.Host{}
}

func presenceDeps() Deps {
	d := fakeDeps()
	d.ARP = fakeARP{}
	d.Ports = fakePorts{}
	d.TCP = fakePorts{}
	return d
}

func TestPresenceOfflineAfterTwoMissesAndBack(t *testing.T) {
	sp := &scriptPinger{
		sweepAlive: map[string]bool{"10.0.0.10": true},
		// check: 1 ok, 2 miss (single miss: no flap), 3 ok, 4 miss, 5 miss
		// (offline), 6 miss, 7 ok (back online), then ok.
		script:   map[string][]bool{"10.0.0.10": {true, false, true, false, false, false, true}},
		presence: map[string]int{},
	}
	cp := &countPorts{inner: fakePorts{}, calls: map[string]int{}}
	d := presenceDeps()
	d.Pinger = sp
	d.Ports = cp
	evs, res := runCycles(t, d, "10.0.0.0/28", 9)

	got := presenceEvents(evs, "10.0.0.10")
	if len(got) != 2 || got[0].Online || got[0].At == "" || !got[1].Online || got[1].Reason != "back online" {
		t.Fatalf("presence events = %+v", got)
	}
	var sawSingleMiss bool
	var firstOffline *model.Host
	for _, h := range hostEvents(evs, "10.0.0.10") {
		if h.Online && h.MissedChecks == 1 {
			sawSingleMiss = true
		}
		if !h.Online && firstOffline == nil {
			firstOffline = &h
		}
	}
	if !sawSingleMiss {
		t.Fatal("single miss not recorded while online")
	}
	if firstOffline == nil || firstOffline.MissedChecks != model.OfflineAfterMisses || firstOffline.OfflineSince == "" {
		t.Fatalf("first offline host = %+v", firstOffline)
	}
	sawOfflineCount := false
	for _, ev := range evs {
		if ev.Type == model.EventLive && ev.Data.(model.LiveStatus).Offline == 1 {
			sawOfflineCount = true
		}
	}
	if !sawOfflineCount {
		t.Fatal("no live status reported the offline host")
	}
	h := hostByIP(t, res, "10.0.0.10")
	if !h.Online || h.OfflineSince != "" || h.MissedChecks != 0 || h.LastSeen == "" || h.IsNew {
		t.Fatalf("final host = %+v", h)
	}
	if n := cp.calls["10.0.0.10"]; n != 1 {
		t.Fatalf("known host probed %d times across cycles", n)
	}
	if n := sp.presenceCount("10.0.0.10"); n < 9 {
		t.Fatalf("presence checks = %d, want one per cycle", n)
	}
}

// tickClock is a clock that moves one second on every reading, so each
// presence check stamps a new LastSeen even when cycles are milliseconds
// apart.
func tickClock() func() time.Time {
	var mu sync.Mutex
	t := time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		t = t.Add(time.Second)
		return t
	}
}

// A printer that misses two checks is reported offline once; after two
// good checks in a row it gets one OK under the same key (v0.6-D18).
func TestPresenceOfflineThenFixed(t *testing.T) {
	sp := &scriptPinger{
		sweepAlive: map[string]bool{"10.0.0.10": true},
		// check: 1 ok, 2-3 miss (offline), 4 ok (back), 5 ok (fixed), then ok.
		script:   map[string][]bool{"10.0.0.10": {true, false, false, true, true}},
		presence: map[string]int{},
	}
	d := presenceDeps()
	d.Pinger = sp
	d.Now = tickClock()
	_, res := runCycles(t, d, "10.0.0.0/28", 7)
	n := notices(res, model.TriggerPrinterOffline)
	if len(n) != 2 || n[0].OK || n[0].Severity != "warn" || n[0].Key != "printerOffline|ip:10.0.0.10|" ||
		!n[1].OK || n[1].Key != n[0].Key || n[1].OpenedAt != n[0].At || n[1].Title != "Printer back online" {
		t.Fatalf("notices = %+v", n)
	}
}

func TestPresenceTCPFallbackAndARP(t *testing.T) {
	sp := &scriptPinger{
		sweepAlive: map[string]bool{"10.0.0.11": true, "10.0.0.13": true},
		script:     map[string][]bool{"10.0.0.11": {false}, "10.0.0.13": {true}}, // .11 never answers presence pings
		presence:   map[string]int{},
	}
	macA := "00:11:22:33:44:12"
	withHost := []arp.Entry{{IP: "10.0.0.12", MAC: macA}}
	// ARP call 0 is the initial harvest; calls 1..2 are cycles 1..2.
	sa := &scriptARP{tables: [][]arp.Entry{withHost, withHost, withHost, nil}}
	open := fakePorts{"10.0.0.11": {9100, 80}, "10.0.0.13": {443}}
	ct := &countTCP{open: open, calls: map[string]int{}}
	d := presenceDeps()
	d.Pinger = sp
	d.ARP = sa
	d.Ports = open
	d.TCP = ct
	evs, res := runCycles(t, d, "10.0.0.0/28", 6)

	// .11: ICMP fails every cycle, TCP to port 80 keeps it online.
	if h := hostByIP(t, res, "10.0.0.11"); !h.Online || h.MissedChecks != 0 {
		t.Fatalf("tcp fallback host = %+v", h)
	}
	if n := ct.count("10.0.0.11"); n < 6 {
		t.Fatalf("tcp fallback used %d times", n)
	}
	if len(presenceEvents(evs, "10.0.0.11")) != 0 {
		t.Fatal("presence event for a host that never went offline")
	}
	// .13 answers ICMP: no TCP check needed.
	if n := ct.count("10.0.0.13"); n != 0 {
		t.Fatalf("icmp host tcp-checked %d times", n)
	}
	// .12: ARP-only, present while its IP+MAC is in the table, offline after
	// two cycles without it.
	got := presenceEvents(evs, "10.0.0.12")
	if len(got) != 1 || got[0].Online {
		t.Fatalf(".12 presence events = %+v", got)
	}
	if h := hostByIP(t, res, "10.0.0.12"); h.Online || h.OfflineSince == "" || h.MAC != macA {
		t.Fatalf("arp host = %+v", h)
	}
	if res.Stats.Offline != 1 || res.Stats.Alive != 3 {
		t.Fatalf("stats = %+v", res.Stats)
	}
}

func TestPresenceMACChangeReprobesOnce(t *testing.T) {
	sp := &scriptPinger{sweepAlive: map[string]bool{}, script: map[string][]bool{}, presence: map[string]int{}}
	macA, macB := "00:11:22:33:44:0A", "00:11:22:33:44:0B"
	a := []arp.Entry{{IP: "10.0.0.5", MAC: macA}}
	b := []arp.Entry{{IP: "10.0.0.5", MAC: macB}}
	sa := &scriptARP{tables: [][]arp.Entry{a, a, b}} // MAC changes in cycle 2
	cp := &countPorts{inner: fakePorts{"10.0.0.5": {80}}, calls: map[string]int{}}
	d := presenceDeps()
	d.Pinger = sp
	d.ARP = sa
	d.Ports = cp
	d.TCP = fakePorts{"10.0.0.5": {80}}
	evs, res := runCycles(t, d, "10.0.0.0/28", 6)

	if n := cp.calls["10.0.0.5"]; n != 2 {
		t.Fatalf("probed %d times, want 2 (initial + MAC change)", n)
	}
	h := hostByIP(t, res, "10.0.0.5")
	if !h.IsNew || h.MAC != macB || !h.Online || h.FirstSeen == "" || len(h.Web) != 1 || len(h.Errors) != 0 {
		t.Fatalf("reset host = %+v", h)
	}
	if len(presenceEvents(evs, "10.0.0.5")) != 0 {
		t.Fatal("MAC change produced presence events")
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	var newEvents int
	for _, ev := range hostEvents(evs, "10.0.0.5") {
		if ev.IsNew && ev.MAC == macB && len(ev.OpenPorts) == 0 && len(ev.FoundBy) == 1 {
			newEvents++ // the reset record, before its probe
		}
	}
	if newEvents != 1 {
		t.Fatalf("reset published %d times", newEvents)
	}
}

// blockPinger: nothing answers the initial sweep; in live sweeps .5 answers
// at once while every other address blocks until cancelled.
type blockPinger struct {
	mu sync.Mutex
	n  map[string]int
}

func (p *blockPinger) Ping(ctx context.Context, ip netip.Addr, timeout time.Duration) (time.Duration, bool, error) {
	p.mu.Lock()
	p.n[ip.String()]++
	first := p.n[ip.String()] == 1
	p.mu.Unlock()
	switch {
	case first:
		return 0, false, nil
	case ip.String() == "10.0.0.5":
		return time.Millisecond, true, nil
	}
	<-ctx.Done()
	return 0, false, ctx.Err()
}

func TestLiveCancelMidSweepFlagsUnprobedHost(t *testing.T) {
	cp := &countPorts{inner: fakePorts{"10.0.0.5": {80}}, calls: map[string]int{}}
	d := presenceDeps()
	d.Pinger = &blockPinger{n: map[string]int{}}
	d.Ports = cp
	d.LiveInterval = 5 * time.Millisecond
	s, err := New(d).Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/28", Live: true})
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := s.Subscribe()
	defer unsub()
	timeout := time.After(3 * time.Second)
wait:
	for {
		select {
		case ev := <-ch:
			if ev.Type == model.EventHost && ev.Data.(model.Host).IP == "10.0.0.5" && ev.Data.(model.Host).IsNew {
				break wait
			}
		case <-timeout:
			t.Fatal("new host never marked")
		}
	}
	s.Cancel()
	res := s.Wait()
	h := hostByIP(t, res, "10.0.0.5")
	if !h.IsNew || !slices.Equal(h.Errors, []string{"probe: cancelled"}) {
		t.Fatalf("cancelled host = %+v", h)
	}
	if n := cp.calls["10.0.0.5"]; n != 0 {
		t.Fatalf("probed %d times after cancel", n)
	}
	if res.State != model.StateDone {
		t.Fatalf("state = %q", res.State)
	}
}

func TestPresenceUpdateTransitionsOnly(t *testing.T) {
	h := &model.Host{IP: "10.0.0.9", Online: true}
	var evs []*model.Event
	for i, seen := range []bool{false, false, false, true, true} {
		evs = append(evs, presenceUpdate(seen, "t"+string(rune('0'+i)))(h))
	}
	if evs[0] != nil || evs[1] == nil || evs[2] != nil || evs[3] == nil || evs[4] != nil {
		t.Fatalf("events = %v", evs)
	}
	if p := evs[1].Data.(model.Presence); p.Online || p.At != "t1" {
		t.Fatalf("offline event = %+v", p)
	}
	if !h.Online || h.LastSeen != "t4" || h.MissedChecks != 0 || h.OfflineSince != "" {
		t.Fatalf("host = %+v", h)
	}
}

func TestPresencePortAvoidsRemoteAccess(t *testing.T) {
	for _, tc := range []struct {
		open []int
		want int
	}{
		{nil, 0},
		{[]int{80, 443}, 80},
		{[]int{22, 80}, 80},
		{[]int{22, 3389, 9100}, 9100},
		{[]int{3389, 22}, 0},
		{[]int{5900}, 0},
	} {
		if got := presencePort(tc.open); got != tc.want {
			t.Errorf("presencePort(%v) = %d, want %d", tc.open, got, tc.want)
		}
	}
}
