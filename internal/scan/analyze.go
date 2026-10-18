package scan

import (
	"runtime"
	"slices"
	"sort"
	"time"

	"netdiag/internal/advice"
	"netdiag/internal/conflict"
	"netdiag/internal/model"
	"netdiag/internal/notify"
)

// The methods in this file run on the state goroutine. The exported-to-runner
// entry points (setHealth, observeMACs, analyze, applySettings) queue an op;
// the lower-case helpers they call must only run inside an op.

// mirror pushes health/conflicts/notices to the scan header.
func (s *state) mirror() {
	if s.onChange != nil {
		s.onChange(s.health, s.conflicts, s.notices)
	}
}

// addNotices records and publishes notices, keeping the last MaxNotices.
// Each fault gets its advice first (see advise).
func (s *state) addNotices(hosts map[string]*model.Host, ns []model.Notice) {
	if len(ns) == 0 {
		return
	}
	s.advise(hosts, ns)
	if s.recording() {
		s.ledger.Apply(s.network, ns)
	}
	all := make([]model.Notice, 0, len(s.notices)+len(ns))
	all = append(all, s.notices...)
	all = append(all, ns...)
	if len(all) > MaxNotices {
		all = all[len(all)-MaxNotices:]
	}
	s.notices = all
	for _, n := range ns {
		if s.emit != nil {
			s.emit(model.Event{Type: model.EventNotice, Data: model.NoticeEvent{Notice: n}})
		}
	}
	s.mirror()
}

// vendors returns the maker of each of macs that has a known one, by MAC,
// or nil.
func (s *state) vendors(macs []string) map[string]string {
	if s.vendor == nil {
		return nil
	}
	var out map[string]string
	for _, m := range macs {
		if v := s.vendor(m); v != "" {
			if out == nil {
				out = map[string]string{}
			}
			out[m] = v
		}
	}
	return out
}

// advise sets the advice on each fault notice in ns, in place: a conflict
// or a health check's own advice when the notice is about one the scan
// has, else the catalogue's answer for the notice and its device.
func (s *state) advise(hosts map[string]*model.Host, ns []model.Notice) {
	x := advice.Ctx{GOOS: runtime.GOOS, Gateway: s.gateway}
	for i := range ns {
		n := &ns[i]
		if n.OK || n.Advice != nil {
			continue
		}
		switch n.Trigger {
		case model.TriggerIPConflict:
			if j := slices.IndexFunc(s.conflicts, func(c model.Conflict) bool { return notify.ConflictKey(c) == n.Key }); j >= 0 {
				n.Advice = s.conflicts[j].Advice
			}
		case model.TriggerHealthFailed:
			if s.health != nil {
				if j := slices.IndexFunc(s.health.Checks, func(c model.HealthCheck) bool { return notify.HealthKey(c.ID) == n.Key }); j >= 0 {
					n.Advice = s.health.Checks[j].Advice
				}
			}
		}
		if n.Advice == nil {
			n.Advice = advice.Notice(*n, hosts[n.IP], x)
		}
	}
}

// setHealth stores a new health report, publishes it and feeds it to the
// notify engine (which reports a check after two failed runs in a row and
// says it is fixed after two good ones).
func (s *state) setHealth(rep model.HealthReport) {
	s.ops <- func(hosts map[string]*model.Host) {
		s.health = &rep
		if s.emit != nil {
			s.emit(model.Event{Type: model.EventHealth, Data: rep})
		}
		s.mirror()
		if s.notify != nil {
			s.addNotices(hosts, s.notify.ObserveHealth(rep))
		}
	}
}

// observeMACs records one MAC observation per IP (only when it differs from
// the last one) and, in live mode, attaches the history to the host. Call it
// before the updates that apply the ARP data.
func (s *state) observeMACs(seen map[string]string) {
	if len(seen) == 0 {
		return
	}
	s.ops <- func(hosts map[string]*model.Host) {
		at := s.now().UTC().Format(time.RFC3339)
		ips := make([]string, 0, len(seen))
		for ip := range seen {
			ips = append(ips, ip)
		}
		sort.Strings(ips)
		for _, ip := range ips {
			mac := seen[ip]
			l := s.obs[ip]
			if n := len(l); n > 0 && l[n-1].MAC == mac {
				continue
			}
			l = append(slices.Clone(l), model.MACObservation{MAC: mac, At: at})
			if len(l) > MaxMACHistory {
				l = l[len(l)-MaxMACHistory:]
			}
			s.obs[ip] = l
			// Hosts whose MAC is about to change pick the history up in
			// their next update (mutate syncs it).
			if h, ok := hosts[ip]; ok && s.keepHist && h.MAC == mac && !slices.Equal(h.MACHistory, l) {
				h.MACHistory = slices.Clone(l)
				s.touch(h)
			}
		}
	}
}

// analyze queues a conflict analysis (once per cycle, after its evidence).
// Once the first pass is reconciled it also tells the ledger netdiag is
// still on its network.
func (s *state) analyze() {
	s.ops <- func(hosts map[string]*model.Host) {
		s.analyzeOp(hosts, true)
		if s.recording() {
			s.ledger.Apply(s.network, nil)
		}
	}
}

// recording reports whether notices go into the ledger: once the first
// pass is reconciled, and not while this computer may be on another network.
func (s *state) recording() bool { return s.ledger != nil && s.reconciled && !s.away }

// setAway tells the state whether the storm guard saw this computer on
// another network: the ledger hears nothing under this network's id while
// it did.
func (s *state) setAway(on bool) {
	s.ops <- func(map[string]*model.Host) { s.away = on }
}

// reconcile ends the first pass (finishFirstPass): it seeds the alert engine
// with the ledger's open problems for network, checks those in check
// against what the pass found (all of the hosts, the conflicts and the
// health report) and keeps those in keep open unchecked. A cancelled pass
// (complete false) checks nothing. The notices the first pass held back go
// out then, and from here on every notice goes into the ledger under
// network. done runs last, once the ledger has heard them, with the keys
// the engine adopted unchecked (notify.Engine.Adopted; finishFirstPass
// passes Scan.markReconciled only for a complete pass).
func (s *state) reconcile(network string, check, keep []notify.OpenFault, complete bool, done func(adopted []string)) {
	s.ops <- func(hosts map[string]*model.Host) {
		var adopted []string
		defer func() { done(adopted) }()
		s.network = network
		s.reconciled = true
		if s.notify == nil {
			return
		}
		s.notify.Seed(check)
		s.notify.Adopt(keep)
		adopted = s.notify.Adopted()
		var ns []model.Notice
		if complete {
			ns = s.notify.Reconcile(hostList(hosts), s.conflicts, s.health)
		} else {
			ns = s.notify.Release()
		}
		if s.ledger != nil {
			s.ledger.Apply(network, nil) // this network's problems are seen again
		}
		s.addNotices(hosts, ns)
	}
}

// post queues fn on the state goroutine and records the notices it returns
// (the storm guard's own notices).
func (s *state) post(fn func() []model.Notice) {
	s.ops <- func(hosts map[string]*model.Host) { s.addNotices(hosts, fn()) }
}

// applySettings updates the alert config and ignore list, then re-analyzes.
// It reports false if the scan has already finished.
func (s *state) applySettings(set model.Settings) bool {
	ign := slices.Clone(set.IgnoredConflictMACs)
	cfg := notifyConfig(set)
	return s.trySend(func(hosts map[string]*model.Host) {
		if s.notify != nil {
			s.notify.SetConfig(cfg)
		}
		s.ignored = ign
		s.analyzeOp(hosts, false)
	})
}

// analyzeOp runs the pure conflict analyzer over the current hosts and MAC
// observations, publishes conflict events for new and resolved conflicts,
// feeds the result to the notify engine and updates Host.Conflicts. cycle is
// false for the re-analysis after a settings save: it has no new evidence,
// so it does not count toward a conflict's OK (notify.ObserveConflicts).
func (s *state) analyzeOp(hosts map[string]*model.Host, cycle bool) {
	now := s.now()
	list := analysisHosts(hostList(hosts))
	cur := conflict.Analyze(list, s.obs, s.gateway, s.self.IP, s.ignored, now)
	x := advice.Ctx{GOOS: runtime.GOOS, Gateway: s.gateway, Hosts: map[string]model.Host{}}
	for _, c := range cur {
		for _, ip := range c.IPs {
			if h, ok := hosts[ip]; ok {
				x.Hosts[ip] = *h
			}
		}
	}

	old := make(map[string]model.Conflict, len(s.conflicts))
	for _, c := range s.conflicts {
		old[c.ID] = c
	}
	keep := make(map[string]bool, len(cur))
	var added []model.Conflict
	for i := range cur {
		keep[cur[i].ID] = true
		cur[i].Vendors = s.vendors(cur[i].MACs)
		cur[i].Advice = advice.Conflict(cur[i], x)
		if o, ok := old[cur[i].ID]; ok {
			if o.FirstSeen != "" && o.FirstSeen < cur[i].FirstSeen {
				cur[i].FirstSeen = o.FirstSeen
			}
			continue
		}
		added = append(added, cur[i])
	}
	for _, c := range s.conflicts {
		if !keep[c.ID] {
			if s.emit != nil {
				s.emit(model.Event{Type: model.EventConflict, Data: model.ConflictEvent{Conflict: c, Resolved: true}})
			}
		}
	}
	for _, c := range added {
		if s.emit != nil {
			s.emit(model.Event{Type: model.EventConflict, Data: model.ConflictEvent{Conflict: c}})
		}
	}
	if len(cur) == 0 {
		cur = nil
	}
	s.conflicts = cur

	// Per-host conflict ids.
	byIP := map[string][]string{}
	for _, c := range cur {
		for _, ip := range c.IPs {
			byIP[ip] = append(byIP[ip], c.ID)
		}
	}
	for ip, h := range hosts {
		ids := byIP[ip]
		sort.Strings(ids)
		if !slices.Equal(h.Conflicts, ids) {
			h.Conflicts = ids
			s.touch(h)
		}
	}
	s.mirror()
	if s.notify != nil {
		// Once per analysis: new conflicts are reported, and a conflict
		// gone from two cycle analyses in a row gets its OK notice.
		s.addNotices(hosts, s.notify.ObserveConflicts(cur, cycle))
	}
}
