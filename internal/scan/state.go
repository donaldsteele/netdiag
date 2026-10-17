package scan

import (
	"slices"
	"sync"
	"time"

	"netdiag/internal/advice"
	"netdiag/internal/model"
	"netdiag/internal/notify"
)

// state owns the authoritative host map plus the derived scan-level data
// (MAC observations, conflicts, notices, health). All mutations arrive over a
// channel and are applied by a single goroutine, so no locking is required.
type state struct {
	ops     chan func(map[string]*model.Host)
	now     func() time.Time
	publish func(model.Host)
	self    model.SelfInfo
	// emit publishes non-host events (presence, conflict, notice, health) in
	// order with host events.
	emit func(model.Event)

	// Fields below are owned by the run goroutine: only touch them inside ops.
	notify    *notify.Engine          // nil: no notices
	vendor    func(mac string) string // a MAC's maker, for conflicts (nil: none)
	gateway   string
	ignored   []string
	keepHist  bool // attach MACHistory to hosts (live mode)
	fresh     bool // set by an update fn that replaced the host with a new device
	health    *model.HealthReport
	conflicts []model.Conflict
	notices   []model.Notice
	obs       map[string][]model.MACObservation
	// onChange mirrors health/conflicts/notices to the scan header. The
	// slices passed are never mutated afterwards.
	onChange func(*model.HealthReport, []model.Conflict, []model.Notice)

	cmu    sync.RWMutex // guards closed for trySend vs close
	closed bool
}

func newState(now func() time.Time, self model.SelfInfo, publish func(model.Host)) *state {
	return &state{
		ops: make(chan func(map[string]*model.Host), 64), now: now, self: self, publish: publish,
		obs: map[string][]model.MACObservation{},
	}
}

// run processes mutations until ops is closed.
func (s *state) run() {
	hosts := map[string]*model.Host{}
	for op := range s.ops {
		op(hosts)
	}
}

// trySend queues op unless the state has been closed; it reports whether op
// was accepted. Use it from goroutines not sequenced before close.
func (s *state) trySend(op func(map[string]*model.Host)) bool {
	s.cmu.RLock()
	defer s.cmu.RUnlock()
	if s.closed {
		return false
	}
	s.ops <- op
	return true
}

// update gets or creates the host for ip, applies fn, stamps UpdatedAt and
// publishes a copy. It blocks until the owner goroutine has accepted it.
func (s *state) update(ip string, fn func(h *model.Host)) {
	s.mutate(ip, func(h *model.Host) *model.Event { fn(h); return nil })
}

// mutate is update where fn may return an extra event that is published
// right after the host copy (nil for none). Host transitions are fed to the
// notify engine.
func (s *state) mutate(ip string, fn func(h *model.Host) *model.Event) {
	s.ops <- func(hosts map[string]*model.Host) {
		h, ok := hosts[ip]
		var prev model.Host
		if ok {
			prev = copyHost(*h)
		} else {
			now := s.now().UTC().Format(time.RFC3339)
			h = &model.Host{IP: ip, FirstSeen: now, Online: true, LastSeen: now}
			if ip == s.self.IP {
				h.IsSelf = true
				if h.MAC == "" {
					h.MAC = s.self.MAC
				}
			}
			hosts[ip] = h
		}
		s.fresh = false
		ev := fn(h)
		if o := s.obs[ip]; s.keepHist && len(o) > 0 && !slices.Equal(h.MACHistory, o) {
			h.MACHistory = slices.Clone(o)
		}
		h.Advice = advice.PrinterNoStatus(*h)
		h.UpdatedAt = s.now().UTC().Format(time.RFC3339)
		cur := copyHost(*h)
		s.publish(cur)
		if ev != nil && s.emit != nil {
			s.emit(*ev)
		}
		if s.notify != nil {
			if s.fresh {
				prev = model.Host{}
			}
			s.addNotices(hosts, s.notify.Observe(prev, cur))
		}
		s.fresh = false
	}
}

// touch stamps and publishes h after an in-op change (owner goroutine only).
func (s *state) touch(h *model.Host) {
	h.UpdatedAt = s.now().UTC().Format(time.RFC3339)
	s.publish(copyHost(*h))
}

// snapshot returns copies of all hosts (unordered).
func (s *state) snapshot() []model.Host {
	reply := make(chan []model.Host, 1)
	s.ops <- func(hosts map[string]*model.Host) {
		reply <- hostList(hosts)
	}
	return <-reply
}

func hostList(hosts map[string]*model.Host) []model.Host {
	out := make([]model.Host, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, copyHost(*h))
	}
	return out
}

func (s *state) close() {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	s.closed = true
	close(s.ops)
}

// copyHost deep-copies slice fields. Snmp, Printer and MDNS pointers are
// treated as immutable once assigned and are shared.
func copyHost(h model.Host) model.Host {
	h.FoundBy = slices.Clone(h.FoundBy)
	h.OpenPorts = slices.Clone(h.OpenPorts)
	h.Web = slices.Clone(h.Web)
	h.Errors = slices.Clone(h.Errors)
	h.Conflicts = slices.Clone(h.Conflicts)
	h.MACHistory = slices.Clone(h.MACHistory)
	return h
}

func addSource(h *model.Host, src string) {
	if !slices.Contains(h.FoundBy, src) {
		h.FoundBy = append(h.FoundBy, src)
	}
}
