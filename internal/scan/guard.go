package scan

import (
	"net/netip"
	"time"

	"netdiag/internal/model"
)

// The storm guard (v0.6 F9c) keeps live mode from reporting every device
// offline when the fault is this computer's: it slept, lost its cable or
// Wi-Fi, or moved to another network. While it holds, missed presence
// checks do not count (nothing goes offline, so no wave of "back online"
// follows), the alert engine drops new presence and health faults, and one
// local notice (netdiagLink, never sent to team chat, v0.6-D16) says why.

// Storm guard tuning.
const (
	// SleepSlack is how much longer than expected a wall-clock gap may be
	// before netdiag takes it for sleep: the timer between cycles, or the
	// presence checks of one cycle.
	SleepSlack = 60 * time.Second
	// QuietCycles is how many cycles after a sleep (the one that noticed it
	// and the next) have their misses not counted: the network may still be
	// coming back.
	QuietCycles = 2
	// GuardClearCycles is how many good cycles in a row (this computer's
	// link fine and the router answering) end a hold.
	GuardClearCycles = 2
	// MassMissMin and MassMissShare: the router missing its check together
	// with at least MassMissMin online devices and at least half of them
	// is this computer losing the network, not the devices going away.
	MassMissMin   = 3
	MassMissShare = 0.5
)

// stormGuard is the guard's state; live goroutine only.
type stormGuard struct {
	reason  string    // "" when not holding; model.Held*
	since   time.Time // when the hold began (Deps.Now)
	good    int       // good cycles in a row while holding
	quiet   int       // cycles left whose misses do not count
	lastEnd time.Time // wall clock at the end of the previous cycle

	// The current cycle: this computer's link and address, and whether the
	// router answered (true when it is not checked).
	linkOK    bool
	self      model.SelfInfo
	gatewayOK bool
}

func (g *stormGuard) held() bool { return g.reason != "" }

// counts reports whether this cycle's missed checks count.
func (g *stormGuard) counts() bool { return !g.held() && g.quiet == 0 }

// slept reports whether the wall clock moved more than expect plus
// SleepSlack from from to to. The wall clock has no monotonic reading, so
// it includes the time this computer slept.
func slept(from, to time.Time, expect time.Duration) bool {
	return !from.IsZero() && to.Sub(from) > expect+SleepSlack
}

// rank orders the reasons: a stronger one replaces a weaker one while
// holding.
func rank(reason string) int {
	switch reason {
	case model.HeldNetworkChanged:
		return 3
	case model.HeldLink:
		return 2
	case model.HeldGateway:
		return 1
	}
	return 0
}

// heldNotice is the netdiagLink notice's title and message for reason.
func heldNotice(reason string) (title, msg string) {
	switch reason {
	case model.HeldNetworkChanged:
		return "This computer is on a different network", "netdiag stopped its alerts for the network it was watching. Press Restart watching to watch this one."
	case model.HeldGateway:
		return "netdiag lost the network", "The router and most devices stopped answering at once, so netdiag paused its checks instead of reporting every device offline."
	}
	return "netdiag lost the network", "This computer lost the local network, so netdiag paused its checks. They start again by themselves when the network is back."
}

// guardStart begins a live cycle: a wall-clock gap since the last cycle
// longer than interval plus SleepSlack means this computer slept, and
// this computer's link is checked (Env.Self fails, has no IPv4 address, or
// the address left the target it was in).
func (r *runner) guardStart(interval time.Duration) {
	g := &r.guard
	if slept(g.lastEnd, r.d.Wall(), interval) {
		g.quiet = QuietCycles
	}
	self, err := r.d.Env.Self(r.req.Interface)
	a, perr := netip.ParseAddr(self.IP)
	g.self = self
	g.linkOK = err == nil && perr == nil && a.Is4() && (!r.selfInRange || r.inRange[a.String()])
	g.gatewayOK = true
	if !g.linkOK {
		r.hold(model.HeldLink)
	}
}

// guardNetwork checks the cycle's ARP read against the network fingerprint:
// another address prefix or another router MAC is another network.
func (r *runner) guardNetwork(gatewayMAC string) {
	g := &r.guard
	if g.linkOK && !r.fp.sameNetwork(g.self.CIDR, gatewayMAC) {
		r.hold(model.HeldNetworkChanged)
	}
}

// guardPresence looks at one cycle's presence results before they are
// applied: the router missing its check together with most online
// devices holds, and so does a check that took far too long (this computer
// slept during it). It reports whether the misses count.
func (r *runner) guardPresence(hosts []model.Host, checked []string, ok map[string]bool, started time.Time) bool {
	g := &r.guard
	if slept(started, r.d.Wall(), 0) {
		g.quiet = QuietCycles
	}
	online := map[string]bool{}
	for _, h := range hosts {
		online[h.IP] = h.Online
	}
	gw := false
	missed, up := 0, 0
	for _, ip := range checked {
		gw = gw || ip == r.gateway
		if online[ip] {
			up++
			if !ok[ip] {
				missed++
			}
		}
	}
	if gw && !ok[r.gateway] {
		g.gatewayOK = false
		if missed >= MassMissMin && float64(missed) >= MassMissShare*float64(up) {
			r.hold(model.HeldGateway)
		}
	}
	return g.counts()
}

// guardEnd ends a live cycle: a hold other than network_changed ends after
// GuardClearCycles good cycles in a row.
func (r *runner) guardEnd() {
	g := &r.guard
	if g.held() && g.reason != model.HeldNetworkChanged {
		if g.linkOK && g.gatewayOK {
			g.good++
		} else {
			g.good = 0
		}
		if g.good >= GuardClearCycles {
			r.unhold()
		}
	}
	if g.quiet > 0 {
		g.quiet--
	}
	g.lastEnd = r.d.Wall()
}

// hold turns the guard on for reason, or makes a stronger reason the
// current one while it holds.
func (r *runner) hold(reason string) {
	g := &r.guard
	g.good = 0
	if g.held() {
		if rank(reason) > rank(g.reason) {
			g.reason = reason
		}
		return
	}
	g.reason, g.since = reason, r.d.Now()
	r.notify.SetHold(true)
	title, msg := heldNotice(reason)
	network := r.fp.ID
	r.st.post(func() []model.Notice { return r.notify.LinkLost(network, reason, title, msg) })
}

// unhold turns the guard off: checks count again and the netdiagLink
// problem closes.
func (r *runner) unhold() {
	g := &r.guard
	g.reason, g.since, g.good = "", time.Time{}, 0
	r.notify.SetHold(false)
	network := r.fp.ID
	r.st.post(func() []model.Notice { return r.notify.LinkBack(network) })
}

// guardStatus copies the guard's state into a live status.
func (r *runner) guardStatus(st *model.LiveStatus) {
	g := &r.guard
	st.Held, st.HeldReason, st.HeldSince = g.held(), g.reason, ""
	if g.held() {
		st.HeldSince = g.since.UTC().Format(time.RFC3339)
	}
	st.Network = r.fp.ID
}
