package scan

import (
	"slices"
	"sort"
	"time"

	"netdiag/internal/conflict"
	"netdiag/internal/model"
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
func (s *state) addNotices(ns []model.Notice) {
	if len(ns) == 0 {
		return
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

// setHealth stores a new health report, publishes it and feeds it to the
// notify engine (which reports a check after two failed runs in a row and
// says it is fixed after two good ones).
func (s *state) setHealth(rep model.HealthReport) {
	s.ops <- func(map[string]*model.Host) {
		s.health = &rep
		if s.emit != nil {
			s.emit(model.Event{Type: model.EventHealth, Data: rep})
		}
		s.mirror()
		if s.notify != nil {
			s.addNotices(s.notify.ObserveHealth(rep))
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
func (s *state) analyze() {
	s.ops <- func(hosts map[string]*model.Host) { s.analyzeOp(hosts, true) }
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

	old := make(map[string]model.Conflict, len(s.conflicts))
	for _, c := range s.conflicts {
		old[c.ID] = c
	}
	keep := make(map[string]bool, len(cur))
	var added []model.Conflict
	for i := range cur {
		keep[cur[i].ID] = true
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
		s.addNotices(s.notify.ObserveConflicts(cur, cycle))
	}
}
