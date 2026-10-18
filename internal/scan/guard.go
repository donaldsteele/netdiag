package scan

import (
	"net/netip"
	"slices"
	"time"

	"netdiag/internal/arp"
	"netdiag/internal/model"
)

// The storm guard (v0.6 F9c) keeps live mode from reporting every device
// offline when the fault is this computer's: it slept, lost its cable or
// Wi-Fi, or moved to another network. While it holds, missed presence
// checks do not count (nothing goes offline, so no wave of "back online"
// follows), the alert engine drops new presence and health faults, and one
// local notice (netdiagLink, never sent to team chat, v0.6-D16) says why.
// On another network it stops all alerting until Restart watching: the
// network netdiag watched is not the one it sees.

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
	// AwayCycles is how many cycles in a row must see another network
	// (another address prefix, or another router MAC) before the guard
	// holds network_changed. In the cycles before, notices wait (the alert
	// engine defers them) and misses do not count; a cycle that sees the
	// network again lets the notices out.
	AwayCycles = 2
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
	// away counts the cycles in a row that saw another network (see
	// AwayCycles); seen is set when this cycle saw it. home is the devices
	// netdiag knew (by IP) before the first of them saw another router: the
	// ones found since may be the other network's.
	away int
	seen bool
	home map[string]model.Host

	// The current cycle: this computer's link and address, and whether the
	// router answered (true when it is not checked).
	linkOK    bool
	self      model.SelfInfo
	gatewayOK bool
}

func (g *stormGuard) held() bool { return g.reason != "" }

// left reports whether netdiag stopped watching: this computer is on
// another network.
func (g *stormGuard) left() bool { return g.reason == model.HeldNetworkChanged }

// counts reports whether this cycle's missed checks count.
func (g *stormGuard) counts() bool { return !g.held() && g.quiet == 0 && g.away == 0 }

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
		return "The router stopped answering", "The router and most devices stopped answering at once, so netdiag paused its checks instead of reporting every device offline."
	}
	return "netdiag lost the network", "This computer lost the local network, so netdiag paused its checks. They start again by themselves when the network is back."
}

// guardStart begins a live cycle: a wall-clock gap since the last cycle
// longer than interval plus SleepSlack means this computer slept, and
// this computer's link is checked. Env.Self failing, giving no IPv4
// address (or a self-assigned 169.254 one where the scan began with a
// lease: the lease is gone) or giving one outside the target it was in is
// a lost link; an address on another prefix than the fingerprint's is
// another network.
func (r *runner) guardStart(interval time.Duration) {
	g := &r.guard
	if slept(g.lastEnd, r.d.Wall(), interval) {
		g.quiet = QuietCycles
	}
	self, err := r.d.Env.Self(r.req.Interface)
	a, perr := netip.ParseAddr(self.IP)
	g.self = self
	up := err == nil && perr == nil && a.Is4() && (!a.IsLinkLocalUnicast() || linkLocal(r.self.IP))
	g.linkOK = up && (!r.selfInRange || r.inRange[a.String()])
	g.gatewayOK = true
	if !g.linkOK {
		r.hold(model.HeldLink)
	}
	if up && !r.fp.samePrefix(self.CIDR) {
		r.otherNetwork()
	}
}

// linkLocal reports whether ip is a self-assigned (169.254) address.
func linkLocal(ip string) bool {
	a, err := netip.ParseAddr(ip)
	return err == nil && a.IsLinkLocalUnicast()
}

// guardNetwork checks the cycle's ARP read against the network fingerprint.
// Another MAC at the router's address is another network when the
// fingerprint's router MAC is nowhere in the read, no device netdiag knew
// (known, by IP, or home once the router changed) still answers at its
// address with its MAC, and the router's address is not taking turns
// between MACs (an address clash, which the conflict analysis reports as a
// mac_flap).
func (r *runner) guardNetwork(entries []arp.Entry, known map[string]model.Host) {
	g := &r.guard
	if r.fp.sameRouter(gatewayMAC(entries, r.gateway)) {
		return
	}
	home := known
	if g.away > 0 && g.home != nil {
		home = g.home
	}
	for _, e := range entries {
		mac, ok := arp.NormalizeMAC(e.MAC)
		if !ok {
			continue
		}
		if r.fp.sameRouter(mac) {
			return // the router is still here
		}
		if h, ok := home[e.IP]; ok && e.IP != r.gateway && !h.IsSelf && h.MAC != "" && h.MAC == mac {
			return // so is a device netdiag knew
		}
	}
	if r.gatewayFlaps() {
		return
	}
	if g.away == 0 {
		g.home = known
	}
	r.otherNetwork()
}

// gatewayFlaps reports whether the last conflict analysis found the
// router's address taking turns between MACs.
func (r *runner) gatewayFlaps() bool {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return slices.ContainsFunc(r.s.header.Conflicts, func(c model.Conflict) bool {
		return c.Kind == model.ConflictMACFlap && slices.Contains(c.IPs, r.gateway)
	})
}

// otherNetwork notes that this cycle saw another network. The first such
// cycle holds the alert engine's notices back and keeps the ledger and
// network-profile.json out of it; AwayCycles in a row hold network_changed.
func (r *runner) otherNetwork() {
	g := &r.guard
	if g.seen || g.left() {
		return
	}
	g.seen = true
	g.away++
	if g.away == 1 {
		r.notify.Defer()
		r.st.setAway(true)
	}
	if g.away >= AwayCycles {
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

// guardEnd ends a live cycle. After one that saw another network, a cycle
// with this computer's link fine that did not lets the held-back notices
// out; one without a link tells nothing, so they wait for the next. A hold
// other than network_changed ends after GuardClearCycles good cycles in a
// row.
func (r *runner) guardEnd() {
	g := &r.guard
	if g.away > 0 && !g.seen && !g.left() && g.linkOK {
		g.away, g.home = 0, nil
		r.st.setAway(false)
		r.st.post(r.notify.Release)
	}
	if g.held() && !g.left() {
		if g.linkOK && g.gatewayOK && g.away == 0 {
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
	g.seen = false
	g.lastEnd = r.d.Wall()
}

// hold turns the guard on for reason, or makes a stronger reason the
// current one while it holds and says so (the same netdiagLink problem,
// with the new reason). network_changed also stops all alerting (leave).
func (r *runner) hold(reason string) {
	g := &r.guard
	g.good = 0
	if g.held() && rank(reason) <= rank(g.reason) {
		return
	}
	if !g.held() {
		g.since = r.d.Now()
		r.notify.SetHold(true)
	}
	g.reason = reason
	if reason == model.HeldNetworkChanged {
		r.leave()
	}
	title, msg := heldNotice(reason)
	network := r.fp.ID
	r.st.post(func() []model.Notice { return r.notify.LinkLost(network, reason, title, msg) })
}

// leave stops all alerting for the network netdiag was watching: this
// computer is on another one. The alert engine drops every notice from
// now on (and the ones it held back), the ledger and network-profile.json
// hear nothing more under the old network's id, and the live loop runs no
// more checks: Restart watching starts a scan of the network this computer
// is on now.
func (r *runner) leave() {
	r.stopped.Store(true)
	r.notify.Stop()
	r.st.setAway(true)
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
