package scan

import (
	"context"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"netdiag/internal/arp"
	"netdiag/internal/model"
)

// recPinger records how often each address is pinged by discovery sweeps
// (calls) and by presence checks (presence, told apart by their timeout).
type recPinger struct {
	mu       sync.Mutex
	calls    map[string]int
	presence map[string]int
	alive    func(ip string) bool
}

func newRecPinger(alive func(string) bool) *recPinger {
	return &recPinger{calls: map[string]int{}, presence: map[string]int{}, alive: alive}
}

func (p *recPinger) Ping(ctx context.Context, ip netip.Addr, timeout time.Duration) (time.Duration, bool, error) {
	p.mu.Lock()
	if timeout == PresencePingTimeout {
		p.presence[ip.String()]++
	} else {
		p.calls[ip.String()]++
	}
	p.mu.Unlock()
	if p.alive(ip.String()) {
		return time.Millisecond, true, nil
	}
	return 0, false, nil
}

func (p *recPinger) count(ip string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[ip]
}

func (p *recPinger) presenceCount(ip string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.presence[ip]
}

type countPorts struct {
	inner fakePorts
	mu    sync.Mutex
	calls map[string]int
}

func (c *countPorts) ScanPorts(ctx context.Context, ip string, ports []int) []int {
	c.mu.Lock()
	c.calls[ip]++
	c.mu.Unlock()
	return c.inner.ScanPorts(ctx, ip, ports)
}

type countARP struct {
	inner fakeARP
	n     atomic.Int32
}

func (c *countARP) Table(ctx context.Context) ([]arp.Entry, error) {
	c.n.Add(1)
	return c.inner.Table(ctx)
}

var initialAlive = map[string]bool{
	"10.0.0.1": true, "10.0.0.10": true, "10.0.0.11": true,
	"10.0.0.20": true, "10.0.0.21": true, "10.0.0.30": true,
}

// waitLive reads events until a live status satisfying ok arrives.
func waitLive(t *testing.T, ch <-chan model.Event, ok func(model.LiveStatus) bool) model.LiveStatus {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev, open := <-ch:
			if !open {
				t.Fatal("event stream closed while waiting for live status")
			}
			if ev.Type == model.EventLive {
				if st := ev.Data.(model.LiveStatus); ok(st) {
					return st
				}
			}
		case <-timeout:
			t.Fatal("timed out waiting for live status")
		}
	}
}

func TestLiveDetectsNewHostOnlyOnce(t *testing.T) {
	var appear atomic.Bool
	rp := newRecPinger(func(ip string) bool {
		return initialAlive[ip] || (ip == "10.0.0.77" && appear.Load())
	})
	cp := &countPorts{inner: fakePorts{"10.0.0.77": {80}}, calls: map[string]int{}}
	d := fakeDeps()
	d.Pinger = rp
	d.Ports = cp
	d.LiveInterval = 10 * time.Millisecond
	o := New(d)
	s, err := o.Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24", Live: true})
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := s.Subscribe()
	defer unsub()

	first := waitLive(t, ch, func(model.LiveStatus) bool { return true })
	if st := s.State(); st != model.StateLive {
		t.Fatalf("state = %q, want live", st)
	}
	if first.Unseen != 254-7 || first.NewHosts != 0 {
		t.Fatalf("initial live status = %+v", first)
	}
	if snap := s.Snapshot(); snap.State != model.StateLive || snap.Stats.Alive != 7 || snap.Request.LiveIntervalSec != 15 {
		t.Fatalf("live snapshot = %+v %+v", snap.State, snap.Stats)
	}
	waitLive(t, ch, func(st model.LiveStatus) bool { return st.Cycle >= 1 })
	appear.Store(true) // host shows up from cycle 2 on
	got := waitLive(t, ch, func(st model.LiveStatus) bool { return st.NewHosts >= 1 })
	waitLive(t, ch, func(st model.LiveStatus) bool { return st.Cycle >= got.Cycle+2 })
	s.Cancel()
	res := s.Wait()

	if res.State != model.StateDone {
		t.Fatalf("final state = %q", res.State)
	}
	if res.Stats.Alive != 8 {
		t.Fatalf("stats = %+v", res.Stats)
	}
	for _, h := range res.Hosts {
		if h.FirstSeen == "" {
			t.Fatalf("%s: firstSeen unset", h.IP)
		}
		if h.IP == "10.0.0.77" {
			if !h.IsNew || !slices.Contains(h.FoundBy, "icmp") || len(h.Web) != 1 {
				t.Fatalf("new host = %+v", h)
			}
		} else if h.IsNew {
			t.Fatalf("%s marked new", h.IP)
		}
		if n := cp.calls[h.IP]; n != 1 {
			t.Fatalf("%s probed %d times", h.IP, n)
		}
	}
	for ip := range initialAlive {
		if n := rp.count(ip); n != 1 {
			t.Fatalf("known host %s swept %d times", ip, n)
		}
	}
	if n := rp.presenceCount("10.0.0.30"); n != 0 {
		t.Fatalf("self host presence-checked %d times", n)
	}
	if n := rp.presenceCount("10.0.0.1"); n < 3 {
		t.Fatalf("known host presence-checked %d times", n)
	}
	if n := rp.count("10.0.0.40"); n != 1 { // ARP-only host: known after initial scan
		t.Fatalf("arp host pinged %d times", n)
	}
	if n := rp.count("10.0.0.77"); n < 3 {
		t.Fatalf("late host pinged %d times", n)
	}
	if rp.count("10.0.0.200") < 4 {
		t.Fatal("unknown address not re-swept")
	}
}

func TestLiveEmptyCandidatesSkipsSweep(t *testing.T) {
	rp := newRecPinger(func(ip string) bool { return ip == "10.0.0.1" })
	ca := &countARP{}
	d := fakeDeps()
	d.Pinger = rp
	d.ARP = ca
	d.LiveInterval = 5 * time.Millisecond
	s, err := New(d).Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.1", Live: true})
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := s.Subscribe()
	defer unsub()
	st := waitLive(t, ch, func(st model.LiveStatus) bool { return st.Cycle >= 3 })
	s.Cancel()
	s.Wait()
	if st.Unseen != 0 || st.LastCheckAt == "" || st.NextCheckAt == "" {
		t.Fatalf("status = %+v", st)
	}
	if n := rp.count("10.0.0.1"); n != 1 {
		t.Fatalf("pinged %d times, want 1", n)
	}
	if n := rp.presenceCount("10.0.0.1"); n < 3 {
		t.Fatalf("presence-checked %d times, want one per cycle", n)
	}
	if n := ca.n.Load(); int(n) < 1+st.Cycle {
		t.Fatalf("arp read %d times, want once per cycle", n)
	}
}

func TestLiveCancelEndsDone(t *testing.T) {
	d := fakeDeps()
	d.LiveInterval = time.Hour // stays idle between cycles
	s, err := New(d).Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/28", Live: true})
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := s.Subscribe()
	defer unsub()
	waitLive(t, ch, func(model.LiveStatus) bool { return true })
	select {
	case <-s.Done():
		t.Fatal("live scan finished by itself")
	default:
	}
	t0 := time.Now()
	s.Cancel()
	select {
	case <-s.Done():
	case <-time.After(time.Second):
		t.Fatal("live scan did not stop")
	}
	if el := time.Since(t0); el > 300*time.Millisecond {
		t.Fatalf("cancel took %v", el)
	}
	if st := s.Snapshot().State; st != model.StateDone {
		t.Fatalf("state = %q", st)
	}
	// Late subscriber after finish: snapshot replay ends with done.
	ch2, unsub2 := s.Subscribe()
	defer unsub2()
	evs := collect(t, ch2, time.Second)
	if evs[len(evs)-1].Type != model.EventDone {
		t.Fatalf("last replay event = %v", evs[len(evs)-1].Type)
	}
}

func TestLiveIntervalClamp(t *testing.T) {
	for in, want := range map[int]int{0: 15, 1: 5, 60: 60, 9999: 300} {
		req, _, err := Validate(model.ScanRequest{CIDR: "10.0.0.1", Live: true, LiveIntervalSec: in})
		if err != nil || req.LiveIntervalSec != want {
			t.Fatalf("interval %d -> %d (%v), want %d", in, req.LiveIntervalSec, err, want)
		}
	}
	if req, _, _ := Validate(model.ScanRequest{CIDR: "10.0.0.1"}); req.LiveIntervalSec != 0 {
		t.Fatal("interval set without live")
	}
}

func TestRegistryReplacesLiveScan(t *testing.T) {
	d := fakeDeps()
	d.LiveInterval = time.Hour
	reg := NewRegistry(New(d))
	ctx := context.Background()
	s1, err := reg.Start(ctx, model.ScanRequest{CIDR: "10.0.0.0/28", Live: true})
	if err != nil {
		t.Fatal(err)
	}
	ch, unsub := s1.Subscribe()
	waitLive(t, ch, func(model.LiveStatus) bool { return true })
	unsub()
	if _, err := reg.Start(ctx, model.ScanRequest{CIDR: "bogus"}); err == nil {
		t.Fatal("bad request accepted")
	}
	if s1.State() != model.StateLive {
		t.Fatal("bad request stopped the live scan")
	}
	s2, err := reg.Start(ctx, model.ScanRequest{CIDR: "10.0.0.0/28"})
	if err != nil {
		t.Fatal(err)
	}
	if s1.State() != model.StateDone {
		t.Fatalf("old scan state = %q", s1.State())
	}
	s2.Wait()
}
