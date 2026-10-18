package scan

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"netdiag/internal/arp"
	"netdiag/internal/model"
	"netdiag/internal/notify"
	"netdiag/internal/store"
)

// wallClock is a fake wall clock: it moves a millisecond on every reading
// and jumps when told, as the real one does over a sleep.
type wallClock struct {
	mu sync.Mutex
	t  time.Time
}

func newWall() *wallClock { return &wallClock{t: time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)} }

func (w *wallClock) now() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.t = w.t.Add(time.Millisecond)
	return w.t
}

func (w *wallClock) jump(d time.Duration) {
	w.mu.Lock()
	w.t = w.t.Add(d)
	w.mu.Unlock()
}

// hookPinger is a scriptPinger that calls onPresence after each presence
// check with the address and how many checks it has had.
type hookPinger struct {
	*scriptPinger
	onPresence func(ip string, n int)
}

func (p hookPinger) Ping(ctx context.Context, ip netip.Addr, timeout time.Duration) (time.Duration, bool, error) {
	d, ok, err := p.scriptPinger.Ping(ctx, ip, timeout)
	if timeout == PresencePingTimeout && p.onPresence != nil {
		p.onPresence(ip.String(), p.presenceCount(ip.String()))
	}
	return d, ok, err
}

// liveStatuses returns the live statuses in evs.
func liveStatuses(evs []model.Event) []model.LiveStatus {
	var out []model.LiveStatus
	for _, ev := range evs {
		if ev.Type == model.EventLive {
			out = append(out, ev.Data.(model.LiveStatus))
		}
	}
	return out
}

// missed reports whether any host event for ip counted a missed check.
func missed(evs []model.Event, ip string) bool {
	for _, h := range hostEvents(evs, ip) {
		if h.MissedChecks > 0 || !h.Online {
			return true
		}
	}
	return false
}

// Acceptance (M6): the computer sleeps 10 minutes during a cycle's checks
// and the network is not back for the next one either. The misses of both
// cycles do not count, so the printer never goes offline; the same misses
// without the sleep take it offline.
func TestSleepGapNoOfflineFlips(t *testing.T) {
	for _, sleep := range []bool{true, false} {
		sp := &scriptPinger{
			sweepAlive: map[string]bool{"10.0.0.10": true},
			script:     map[string][]bool{"10.0.0.10": {true, false, false, true}},
			presence:   map[string]int{},
		}
		w := newWall()
		var jumped atomic.Bool
		d := presenceDeps()
		d.Pinger = hookPinger{sp, func(ip string, n int) {
			if sleep && n == 2 && !jumped.Swap(true) {
				w.jump(10 * time.Minute)
			}
		}}
		d.Wall = w.now
		evs, res := runCycles(t, d, "10.0.0.0/28", 6)
		off := presenceEvents(evs, "10.0.0.10")
		if sleep {
			if len(off) != 0 || missed(evs, "10.0.0.10") || len(notices(res, model.TriggerPrinterOffline)) != 0 {
				t.Fatalf("after sleep: presence %v, notices %v", off, res.Notices)
			}
			for _, st := range liveStatuses(evs) {
				if st.Held {
					t.Fatalf("a sleep is not a hold: %+v", st)
				}
			}
		} else if len(off) == 0 || off[0].Online {
			t.Fatalf("without sleep: presence %v", off)
		}
	}
}

// A gap between cycles longer than the interval plus SleepSlack is sleep:
// this cycle and the next do not count misses.
func TestGuardStartSleepGap(t *testing.T) {
	w := newWall()
	r := &runner{d: Deps{Env: fakeEnv{}, Wall: w.now}, inRange: map[string]bool{}}
	for _, tc := range []struct {
		gap   time.Duration
		quiet int
	}{
		{15 * time.Second, 0},
		{70 * time.Second, 0},
		{80 * time.Second, QuietCycles},
		{10 * time.Minute, QuietCycles},
	} {
		r.guard = stormGuard{lastEnd: w.now()}
		w.jump(tc.gap)
		r.guardStart(15 * time.Second)
		if r.guard.quiet != tc.quiet || r.guard.held() || !r.guard.linkOK {
			t.Fatalf("gap %v: %+v", tc.gap, r.guard)
		}
	}
}

// flakyEnv fails Self on the calls listed in fail (1 is the scan's start).
type flakyEnv struct {
	n    atomic.Int32
	fail map[int32]bool
}

func (e *flakyEnv) Self(name string) (model.SelfInfo, error) {
	if e.fail[e.n.Add(1)] {
		return model.SelfInfo{}, errors.New("no usable IPv4 interface")
	}
	return fakeEnv{}.Self(name)
}

func (e *flakyEnv) Gateway() (string, error) { return fakeEnv{}.Gateway() }

// Acceptance (M6): this computer's link is gone for 2 cycles (Env.Self
// fails) while every check misses. One netdiagLink notice, no device
// offline, MissedChecks never moves, and one OK after 2 good cycles.
func TestEnvSelfErrorHolds(t *testing.T) {
	sp := &scriptPinger{
		sweepAlive: map[string]bool{"10.0.0.10": true},
		script:     map[string][]bool{"10.0.0.10": {true, false, false, false, true}},
		presence:   map[string]int{},
	}
	d := presenceDeps()
	d.Pinger = sp
	d.Env = &flakyEnv{fail: map[int32]bool{3: true, 4: true}} // cycles 2 and 3
	evs, res := runCycles(t, d, "10.0.0.0/28", 7)
	link := notices(res, model.TriggerNetdiagLink)
	if len(link) != 2 || link[0].OK || link[0].Title != "netdiag lost the network" || link[0].Severity != "warn" || link[0].Code != model.HeldLink || link[0].Advice == nil ||
		!link[1].OK || link[1].Key != link[0].Key || link[1].Title != "netdiag is back on the network" {
		t.Fatalf("netdiagLink notices: %+v", link)
	}
	if n := notices(res, model.TriggerPrinterOffline); len(n) != 0 || len(presenceEvents(evs, "10.0.0.10")) != 0 || missed(evs, "10.0.0.10") {
		t.Fatalf("offline: %+v", n)
	}
	sts := liveStatuses(evs)
	var heldAt, freeAt int
	for _, st := range sts {
		if st.Held && heldAt == 0 {
			heldAt = st.Cycle
			if st.HeldReason != model.HeldLink || st.HeldSince == "" {
				t.Fatalf("held status %+v", st)
			}
		}
		if heldAt > 0 && !st.Held && freeAt == 0 {
			freeAt = st.Cycle
		}
	}
	if heldAt != 2 || freeAt != 5 {
		t.Fatalf("held from cycle %d to %d: %+v", heldAt, freeAt, sts)
	}
}

// The router missing its check together with most online devices is this
// computer losing the network: held with reason gateway, nobody offline.
func TestGatewayMassMissHolds(t *testing.T) {
	down := []bool{true, false, false, false, true}
	sp := &scriptPinger{
		sweepAlive: map[string]bool{"10.0.0.1": true, "10.0.0.10": true, "10.0.0.11": true, "10.0.0.12": true},
		script:     map[string][]bool{"10.0.0.1": down, "10.0.0.10": down, "10.0.0.11": down, "10.0.0.12": down},
		presence:   map[string]int{},
	}
	d := presenceDeps()
	d.Pinger = sp
	d.Settings = &fakeStore{set: deviceOfflineOn()}
	evs, res := runCycles(t, d, "10.0.0.0/28", 8)
	for _, ip := range []string{"10.0.0.1", "10.0.0.10", "10.0.0.11", "10.0.0.12"} {
		if p := presenceEvents(evs, ip); len(p) != 0 || missed(evs, ip) {
			t.Fatalf("%s: presence %v", ip, p)
		}
	}
	if n := append(notices(res, model.TriggerPrinterOffline), notices(res, model.TriggerDeviceOffline)...); len(n) != 0 {
		t.Fatalf("offline notices: %+v", n)
	}
	// The title blames the router, as the live pill and the card do, not
	// this computer.
	if link := notices(res, model.TriggerNetdiagLink); len(link) != 2 || link[0].OK || !link[1].OK ||
		link[0].Title != "The router stopped answering" || link[0].Code != model.HeldGateway {
		t.Fatalf("netdiagLink notices: %+v", link)
	}
	reasons := map[string]bool{}
	for _, st := range liveStatuses(evs) {
		if st.Held {
			reasons[st.HeldReason] = true
		}
	}
	if len(reasons) != 1 || !reasons[model.HeldGateway] {
		t.Fatalf("reasons %v", reasons)
	}

	// One device missing with the router up is that device: it goes offline.
	sp = &scriptPinger{
		sweepAlive: map[string]bool{"10.0.0.1": true, "10.0.0.10": true, "10.0.0.11": true},
		script:     map[string][]bool{"10.0.0.1": {true}, "10.0.0.10": {true, false}, "10.0.0.11": {true}},
		presence:   map[string]int{},
	}
	d.Pinger = sp
	evs, _ = runCycles(t, d, "10.0.0.0/28", 4)
	if p := presenceEvents(evs, "10.0.0.10"); len(p) != 1 || p[0].Online {
		t.Fatalf("one device down: %v", p)
	}
}

func deviceOfflineOn() model.Settings {
	set := store.DefaultSettings()
	set.Alerts.Triggers.DeviceOffline = true
	return set
}

// Another router MAC is another network: held with reason network_changed,
// which does not end by itself, even when the old MAC is back.
func TestNetworkChangedStaysHeld(t *testing.T) {
	a := []arp.Entry{{IP: "10.0.0.1", MAC: "00:11:22:33:44:01"}}
	b := []arp.Entry{{IP: "10.0.0.1", MAC: "00:11:22:33:44:02"}}
	sp := &scriptPinger{sweepAlive: map[string]bool{}, script: map[string][]bool{}, presence: map[string]int{}}
	d := presenceDeps()
	d.Pinger = sp
	d.ARP = &scriptARP{tables: [][]arp.Entry{a, a, b, b, a}} // harvest, then cycles 1, 2, ...
	evs, res := runCycles(t, d, "10.0.0.0/28", 7)
	sts := liveStatuses(evs)
	last := sts[len(sts)-1]
	want := model.NetworkID("", "10.0.0.1", "00:11:22:33:44:01")
	if !last.Held || last.HeldReason != model.HeldNetworkChanged || last.Network != want {
		t.Fatalf("last status %+v", last)
	}
	link := notices(res, model.TriggerNetdiagLink)
	if len(link) != 1 || link[0].OK || link[0].Title != "This computer is on a different network" || link[0].Key != "netdiagLink||"+want ||
		link[0].Code != model.HeldNetworkChanged || link[0].Advice == nil || !strings.Contains(link[0].Advice.Meaning, "different network") {
		t.Fatalf("netdiagLink notices: %+v", link)
	}
}

// The uitest fixtures show the storm guard's notices word for word.
func TestUITestFixtureHeld(t *testing.T) {
	const fx = "n-5f2c0a9d3e71"
	src, err := os.ReadFile(filepath.Join("..", "..", "tools", "uitest", "fixtures", "index.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{model.HeldLink, model.HeldNetworkChanged} {
		title, msg := heldNotice(reason)
		for _, want := range []string{"title: '" + title + "'", "const message = '" + msg + "'"} {
			if !strings.Contains(string(src), want) {
				t.Errorf("fixtures missing %q", want)
			}
		}
	}
	e := notify.New(notify.DefaultConfig(), nil)
	e.LinkLost(fx, model.HeldLink, "", "")
	for _, want := range []string{"title: '" + e.LinkBack(fx)[0].Title + "'"} {
		if !strings.Contains(string(src), want) {
			t.Errorf("fixtures missing %q", want)
		}
	}
}
