package scan

import (
	"slices"
	"time"

	"netdiag/internal/model"
)

// state owns the authoritative host map. All mutations arrive over a channel
// and are applied by a single goroutine, so no locking is required.
type state struct {
	ops     chan func(map[string]*model.Host)
	now     func() time.Time
	publish func(model.Host)
	self    model.SelfInfo
	// emit publishes non-host events (presence) in order with host events.
	emit func(model.Event)
}

func newState(now func() time.Time, self model.SelfInfo, publish func(model.Host)) *state {
	return &state{ops: make(chan func(map[string]*model.Host), 64), now: now, self: self, publish: publish}
}

// run processes mutations until ops is closed.
func (s *state) run() {
	hosts := map[string]*model.Host{}
	for op := range s.ops {
		op(hosts)
	}
}

// update gets or creates the host for ip, applies fn, stamps UpdatedAt and
// publishes a copy. It blocks until the owner goroutine has accepted it.
func (s *state) update(ip string, fn func(h *model.Host)) {
	s.mutate(ip, func(h *model.Host) *model.Event { fn(h); return nil })
}

// mutate is update where fn may return an extra event that is published
// right after the host copy (nil for none).
func (s *state) mutate(ip string, fn func(h *model.Host) *model.Event) {
	s.ops <- func(hosts map[string]*model.Host) {
		h, ok := hosts[ip]
		if !ok {
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
		ev := fn(h)
		h.UpdatedAt = s.now().UTC().Format(time.RFC3339)
		s.publish(copyHost(*h))
		if ev != nil && s.emit != nil {
			s.emit(*ev)
		}
	}
}

// snapshot returns copies of all hosts (unordered).
func (s *state) snapshot() []model.Host {
	reply := make(chan []model.Host, 1)
	s.ops <- func(hosts map[string]*model.Host) {
		out := make([]model.Host, 0, len(hosts))
		for _, h := range hosts {
			out = append(out, copyHost(*h))
		}
		reply <- out
	}
	return <-reply
}

func (s *state) close() { close(s.ops) }

// copyHost deep-copies slice fields. Snmp and Printer pointers are treated as
// immutable once assigned and are shared.
func copyHost(h model.Host) model.Host {
	h.FoundBy = slices.Clone(h.FoundBy)
	h.OpenPorts = slices.Clone(h.OpenPorts)
	h.Web = slices.Clone(h.Web)
	h.Errors = slices.Clone(h.Errors)
	return h
}

func addSource(h *model.Host, src string) {
	if !slices.Contains(h.FoundBy, src) {
		h.FoundBy = append(h.FoundBy, src)
	}
}
