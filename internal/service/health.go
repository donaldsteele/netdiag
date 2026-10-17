package service

import (
	"context"
	"time"

	"netdiag/internal/health"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
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

// RunHealth runs an on-demand health check, capped at 10s. iface names
// the interface to check; empty means the network of the running or
// latest scan, else the default interface (see healthTarget).
func (s *Service) RunHealth(ctx context.Context, iface string) model.HealthReport {
	in := s.healthTarget(iface)
	if s.cfg.Store != nil {
		if set, err := s.cfg.Store.LoadSettings(); err == nil {
			in.Config = health.ConfigFor(set)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, healthRunCap)
	defer cancel()
	return s.cfg.Health.Run(ctx, in)
}

// healthTarget picks the interface and gateway an on-demand health check
// looks at, so it checks the same network as the scan on screen:
//
//   - the interface and gateway of the running scan, else of the latest
//     scan of this process, when iface is empty or names that scan's
//     interface, and the interface still has the address it had in the
//     scan (otherwise the computer has moved to another network);
//   - else the interface named iface, or the default-route interface;
//     with the default-route gateway.
func (s *Service) healthTarget(iface string) health.Input {
	list, lerr := s.cfg.Interfaces()
	find := func(name string) (netinfo.Iface, bool) {
		for _, f := range list {
			if f.Name == name {
				return f, true
			}
		}
		return netinfo.Iface{}, false
	}
	if self, gw, ok := s.latestScanNetwork(); ok && (iface == "" || iface == self.Interface) {
		f, found := find(self.Interface)
		if lerr != nil || found && f.IP == self.IP {
			if gw == "" && found && f.IsDefault {
				gw, _ = s.cfg.Gateway()
			}
			return health.Input{Iface: self.Interface, IfaceIP: self.IP, Gateway: gw}
		}
	}
	var in health.Input
	if lerr == nil && len(list) > 0 {
		f, ok := find(iface)
		if !ok || iface == "" {
			f = list[0]
			for _, c := range list {
				if c.IsDefault {
					f = c
					break
				}
			}
		}
		in.Iface, in.IfaceIP = f.Name, f.IP
	}
	if gw, err := s.cfg.Gateway(); err == nil {
		in.Gateway = gw
	}
	return in
}

// latestScanNetwork returns this computer's interface and the gateway seen
// by the running scan, else by the most recently started scan of this
// process.
func (s *Service) latestScanNetwork() (model.SelfInfo, string, bool) {
	if s.cfg.Registry == nil {
		return model.SelfInfo{}, "", false
	}
	if sc, ok := s.cfg.Registry.Running(); ok {
		if r := sc.Snapshot(); r.Self.Interface != "" {
			return r.Self, r.Gateway, true
		}
	}
	list := s.cfg.Registry.List()
	for i := len(list) - 1; i >= 0; i-- {
		if r := list[i].Snapshot(); r.Self.Interface != "" {
			return r.Self, r.Gateway, true
		}
	}
	return model.SelfInfo{}, "", false
}
