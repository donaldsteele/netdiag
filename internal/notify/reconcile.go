package notify

import (
	"fmt"
	"slices"
	"sort"

	"netdiag/internal/model"
)

// The open-problem ledger (v0.6 F2b) keeps the problems netdiag reported
// across restarts. A scan with a ledger runs its first pass deferred: the
// engine notes every problem it finds but holds the notices back. At the
// end of the first pass the scan seeds the engine with the ledger's open
// problems for this network and reconciles: a problem the ledger had that is
// gone now gets its OK, one that is still there stays open without a second
// notice, and the held-back notices of new problems go out.

// Defer holds fault notices back until Reconcile or Release, for the first
// pass of a scan with a ledger. A problem that opens and closes while
// deferred is never reported at all.
func (e *Engine) Defer() {
	e.mu.Lock()
	e.deferred = true
	e.mu.Unlock()
}

// Seed adopts the ledger's open problems for this network. A problem this
// run already found (its notice held back by Defer) is the same problem:
// it keeps the ledger's OpenedAt, and Reconcile drops its held notice. The
// others are open again as they were, for Reconcile to check.
func (e *Engine) Seed(open []OpenFault) { e.seed(open, true) }

// Adopt is Seed for problems the first pass could not tell about (a scan
// whose target leaves their device out): they are open again as they were,
// and Reconcile does not check them.
func (e *Engine) Adopt(open []OpenFault) { e.seed(open, false) }

func (e *Engine) seed(open []OpenFault, check bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, o := range open {
		if o.Key == "" {
			continue
		}
		if f := e.open[o.Key]; f != nil {
			if !o.OpenedAt.IsZero() {
				f.OpenedAt = o.OpenedAt
			}
			f.again = true
			continue
		}
		e.open[o.Key] = &fault{OpenFault: o, seeded: check, outside: !check}
	}
}

// Reconcile ends the deferred first pass. Each seeded problem whose
// condition is gone from what the first pass found (hosts, conflicts and the
// health report, which may be nil) gets its OK notice; the previous run is
// over, so this one look is enough. A seeded problem that is still there,
// or that the pass could not tell about (its device was not found, the
// health checks did not run), stays open with no new notice. Then the
// held-back notices of problems the ledger did not have go out.
func (e *Engine) Reconcile(hosts []model.Host, cs []model.Conflict, h *model.HealthReport) []model.Notice {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []model.Notice
	for _, f := range e.sorted() {
		if !f.seeded {
			continue
		}
		f.seeded = false
		if title, msg, gone := e.gone(f, hosts, cs, h); gone {
			out = e.emitOK(out, f, title, msg)
		}
	}
	return append(out, e.release()...)
}

// Release ends the deferred first pass without checking the seeded problems,
// for a first pass that was cancelled and saw too little to tell: they stay
// open. The held-back notices go out, except those the ledger had.
func (e *Engine) Release() []model.Notice {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, f := range e.open {
		f.seeded = false
	}
	return e.release()
}

func (e *Engine) release() []model.Notice {
	var out []model.Notice
	for _, n := range e.waiting {
		if f := e.open[n.Key]; f != nil && f.again {
			continue
		}
		out = append(out, n)
	}
	for _, f := range e.open {
		f.again = false
	}
	e.waiting, e.deferred = nil, false
	return out
}

// sorted returns the open problems sorted by key.
func (e *Engine) sorted() []*fault {
	out := make([]*fault, 0, len(e.open))
	for _, f := range e.open {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// hostWith returns the host in hosts that the problem f belongs to under
// the key part part (see belongs), or false.
func (e *Engine) hostWith(f *fault, hosts []model.Host, part string) (model.Host, bool) {
	for _, h := range hosts {
		for _, id := range hostIDs(h) {
			if keyFor(f.Trigger, id, part) == f.Key && e.belongs(f, h) {
				return h, true
			}
		}
	}
	return model.Host{}, false
}

// label names f's device for an OK message when the pass did not find it.
func (f *fault) label() string {
	switch {
	case f.Name != "" && f.IP != "":
		return f.Name + " (" + f.IP + ")"
	case f.IP != "":
		return f.IP
	}
	return f.Name
}

// gone reports whether the condition of the seeded problem f is gone from
// the first pass's findings, with the OK notice's title and message.
func (e *Engine) gone(f *fault, hosts []model.Host, cs []model.Conflict, rep *model.HealthReport) (title, msg string, ok bool) {
	switch f.Trigger {
	case model.TriggerPrinterOffline, model.TriggerDeviceOffline:
		if h, found := e.hostWith(f, hosts, ""); found && h.Online {
			return backTitle(f.Trigger), backMessage(f.Trigger, label(h)), true
		}
	case model.TriggerSupplyLow:
		return e.supplyGone(f, hosts)
	case model.TriggerPrinterAlert:
		return e.alertGone(f, hosts)
	case model.TriggerIPConflict:
		if slices.ContainsFunc(cs, func(c model.Conflict) bool { return ConflictKey(c) == f.Key }) {
			return "", "", false
		}
		msg := "The conflict is gone."
		if f.IP != "" {
			msg = fmt.Sprintf("The conflict at %s is gone.", f.IP)
		}
		return "Network conflict cleared", msg, true
	case model.TriggerHealthFailed:
		if rep == nil {
			return "", "", false
		}
		for _, c := range rep.Checks {
			if HealthKey(c.ID) != f.Key {
				continue
			}
			if c.Status == model.HealthFail || c.Status == model.HealthUnavailable {
				return "", "", false
			}
			name := c.Label
			if name == "" {
				name = c.ID
			}
			return "Health check OK again", fmt.Sprintf("%s check is OK again.", name), true
		}
	case model.TriggerNetdiagLink:
		// About a run that is over: this computer is on the network now.
		return linkBackTitle, linkBackMessage, true
	}
	return "", "", false
}

// supplyGone checks a seeded low supply against the printer the first pass
// read: fixed when the supply is above the threshold plus the hysteresis
// (or reports some left without a level), or no longer listed by a printer
// that lists supplies. A printer read without supplies says nothing.
func (e *Engine) supplyGone(f *fault, hosts []model.Host) (string, string, bool) {
	okAt := min(e.cfg.SupplyThresholdPct+SupplyHysteresisPct+1, 100)
	for _, h := range hosts {
		if h.Printer == nil || len(h.Printer.Supplies) == 0 || !e.belongs(f, h) {
			continue
		}
		mine := false
		for _, id := range hostIDs(h) {
			if hasPrefixKey(f.Key, f.Trigger, id) {
				mine = true
			}
		}
		if !mine {
			continue
		}
		l := label(h)
		for i, part := range supplyParts(h.Printer.Supplies) {
			if _, found := e.hostWith(f, []model.Host{h}, part); !found {
				continue
			}
			s := h.Printer.Supplies[i]
			switch {
			case s.Percent >= okAt:
				return "Supply OK again", fmt.Sprintf("%s: %s at %d%%.", l, s.Description, s.Percent), true
			case s.Percent < 0 && s.State == "ok":
				return "Supply OK again", fmt.Sprintf("%s: %s is no longer low.", l, s.Description), true
			}
			return "", "", false
		}
		msg := fmt.Sprintf("%s no longer reports the low supply.", l)
		if f.Detail != "" {
			msg = fmt.Sprintf("%s no longer reports %s.", l, f.Detail)
		}
		return "Supply OK again", msg, true
	}
	return "", "", false
}

// alertGone checks a seeded printer alert against the printer the first
// pass read: fixed when the printer no longer reports it. A printer with no
// status read says nothing.
func (e *Engine) alertGone(f *fault, hosts []model.Host) (string, string, bool) {
	for _, h := range hosts {
		if h.Printer == nil || !e.belongs(f, h) {
			continue
		}
		mine := false
		for _, id := range hostIDs(h) {
			if hasPrefixKey(f.Key, f.Trigger, id) {
				mine = true
			}
		}
		if !mine {
			continue
		}
		for _, a := range h.Printer.Alerts {
			if _, found := e.hostWith(f, []model.Host{h}, alertPart(a)); found {
				return "", "", false
			}
		}
		l := label(h)
		msg := fmt.Sprintf("%s: the printer alert is cleared.", l)
		if f.Detail != "" {
			msg = fmt.Sprintf("%s: cleared %s.", l, f.Detail)
		}
		return "Printer alert cleared", msg, true
	}
	return "", "", false
}

// hasPrefixKey reports whether key is one of trigger's keys for the device
// id: <trigger>|<id>|...
func hasPrefixKey(key, trigger, id string) bool {
	p := trigger + "|" + id + "|"
	return len(key) >= len(p) && key[:len(p)] == p
}

// The storm guard's local problem (netdiagLink, never sent to team chat,
// v0.6-D16).
const (
	linkBackTitle   = "netdiag is back on the network"
	linkBackMessage = "netdiag checks the local network again."
)

// LinkKey is the key of the storm guard's problem on a network
// (netdiagLink||<network id>).
func LinkKey(network string) string {
	return keyFor(model.TriggerNetdiagLink, "", network)
}

// LinkLost opens the storm guard's problem for network and returns its
// notice, which carries reason (model.Held*) as its Code, or nothing when it
// is already open. It is local only and needs no trigger switch: it says why
// netdiag reports nothing while it holds.
func (e *Engine) LinkLost(network, reason, title, msg string) []model.Notice {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := LinkKey(network)
	if _, ok := e.open[key]; ok {
		return nil
	}
	now := e.now()
	n := model.Notice{Trigger: model.TriggerNetdiagLink, Severity: severityFor(model.TriggerNetdiagLink, sevCtx{}),
		Title: title, Message: msg, Key: key, Code: reason}
	n.ID, n.At = e.next(now)
	e.open[key] = &fault{OpenFault: OpenFault{Key: key, Trigger: n.Trigger, Severity: n.Severity,
		Title: title, OpenedAt: now}}
	return []model.Notice{n}
}

// LinkBack closes the storm guard's problem for network with its OK notice,
// or returns nothing when it is not open.
func (e *Engine) LinkBack(network string) []model.Notice {
	e.mu.Lock()
	defer e.mu.Unlock()
	f := e.open[LinkKey(network)]
	if f == nil {
		return nil
	}
	return e.emitOK(nil, f, linkBackTitle, linkBackMessage)
}
