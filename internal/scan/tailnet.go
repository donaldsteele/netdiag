package scan

import (
	"context"
	"sync"
	"time"

	"netdiag/internal/model"
	"netdiag/internal/tailscale"
)

// TailnetReader reads this computer's Tailscale service (tailscale.Client).
type TailnetReader interface {
	Status(ctx context.Context) (tailscale.Status, error)
}

// tailnetReadCap bounds one reading of the Tailscale service.
const tailnetReadCap = 5 * time.Second

// startTailnet reads the tailnet in the background (when Deps.Tailnet is
// set) and posts the reading to the state goroutine, which keeps it on the
// scan result and feeds it to the alert engine (v0.6.1). A service that
// could not be read posts nothing, so one bad read never counts as
// Tailscale stopping. The caller must bg.Wait before closing the state.
func (r *runner) startTailnet(ctx context.Context, bg *sync.WaitGroup) {
	if r.d.Tailnet == nil {
		return
	}
	bg.Add(1)
	go func() {
		defer bg.Done()
		rctx, cancel := context.WithTimeout(ctx, tailnetReadCap)
		defer cancel()
		st, err := r.d.Tailnet.Status(rctx)
		if err != nil || ctx.Err() != nil {
			return
		}
		r.st.setTailnet(TailnetSnapshot(st, r.d.Now()))
	}()
}

// TailnetSnapshot is what a scan keeps of one tailnet reading.
func TailnetSnapshot(st tailscale.Status, now time.Time) *model.TailnetSnapshot {
	v := tailscale.ViewOf(st, nil, now)
	return &model.TailnetSnapshot{State: v.State, Suffix: v.Suffix, Self: v.Self, Devices: v.Devices, CheckedAt: v.CheckedAt}
}

// setTailnet stores a tailnet reading on the scan result and feeds it to
// the alert engine.
func (s *state) setTailnet(snap *model.TailnetSnapshot) {
	s.ops <- func(hosts map[string]*model.Host) {
		s.tailnet = snap
		if s.onTailnet != nil {
			s.onTailnet(snap)
		}
		if s.notify != nil {
			s.addNotices(hosts, s.notify.ObserveTailnet(snap))
		}
	}
}
