package service

import (
	"context"
	"time"

	"netdiag/internal/health"
	"netdiag/internal/model"
)

const healthRunCap = 10 * time.Second

// LatestHealth returns the most relevant health report: running scan, then
// live scans, then history.
func (s *Service) LatestHealth() (*model.HealthReport, bool) {
	var best *model.HealthReport
	consider := func(h *model.HealthReport) {
		if h != nil && (best == nil || h.CheckedAt > best.CheckedAt) {
			best = h
		}
	}
	if sc, ok := s.cfg.Registry.Running(); ok {
		consider(sc.Snapshot().Health)
	}
	if best == nil {
		for _, sc := range s.cfg.Registry.List() {
			consider(sc.Snapshot().Health)
		}
	}
	if best == nil && s.cfg.Store != nil {
		list, _ := s.cfg.Store.ListHistory()
		for _, e := range list {
			if res, err := s.cfg.Store.LoadScan(e.ID); err == nil && res.Health != nil {
				best = res.Health
				break
			}
		}
	}
	return best, best != nil
}

// RunHealth runs an on-demand health check, capped at 10s.
func (s *Service) RunHealth(ctx context.Context, iface string) model.HealthReport {
	in := health.Input{}
	if list, err := s.cfg.Interfaces(); err == nil && len(list) > 0 {
		f := list[0]
		for _, c := range list {
			if iface != "" && c.Name == iface {
				f = c
				break
			}
		}
		in.Iface, in.IfaceIP = f.Name, f.IP
	}
	if gw, err := s.cfg.Gateway(); err == nil {
		in.Gateway = gw
	}
	if s.cfg.Store != nil {
		if set, err := s.cfg.Store.LoadSettings(); err == nil {
			in.Config = health.ConfigFor(set)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, healthRunCap)
	defer cancel()
	return s.cfg.Health.Run(ctx, in)
}
