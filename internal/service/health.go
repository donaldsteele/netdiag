package service

import (
	"context"
	"net/netip"
	"runtime"
	"time"

	"netdiag/internal/advice"
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
	rep := s.cfg.Health.Run(ctx, in)
	advice.Report(&rep, advice.Ctx{GOOS: runtime.GOOS, Gateway: in.Gateway})
	return rep
}

// healthTarget picks the interface and gateway an on-demand health check
// looks at, so it checks the same network as the scan on screen:
//
//   - the interface and gateway of the running scan, else of the latest
//     scan of this process, when iface is empty or names that scan's
//     interface, and the interface still has the address it had in the
//     scan (otherwise the computer has moved to another network);
//   - else the interface named iface, or the default-route interface.
//
// A gateway is used only on the default-route adapter or when it lies
// inside the interface's network: with two adapters, the default route's
// router may belong to the other one. Otherwise the gateway is left empty, and the Gateway check says no
// router is known for this adapter. When the interfaces cannot be listed,
// no adapter is named and the default route's router is used.
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
			if found {
				switch {
				case gw == "" && f.IsDefault:
					gw = s.gatewayOn(f)
				case gw != "" && !f.IsDefault && !onNetwork(gw, f):
					gw = ""
				}
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
		in.Iface, in.IfaceIP, in.Gateway = f.Name, f.IP, s.gatewayOn(f)
		return in
	}
	if gw, err := s.cfg.Gateway(); err == nil {
		in.Gateway = gw
	}
	return in
}

// gatewayOn returns the default route's gateway for f: always when f is
// the default-route adapter (the route goes out through it, whatever its
// first address or prefix is, e.g. a second static address or a /32 link),
// otherwise only when the gateway lies inside f's network. Else "".
func (s *Service) gatewayOn(f netinfo.Iface) string {
	if gw, err := s.cfg.Gateway(); err == nil && (f.IsDefault || onNetwork(gw, f)) {
		return gw
	}
	return ""
}

// onNetwork reports whether the address gw lies inside f's network
// (f.CIDR). An address or network that does not parse is not inside.
func onNetwork(gw string, f netinfo.Iface) bool {
	p, err := netip.ParsePrefix(f.CIDR)
	if err != nil {
		return false
	}
	a, err := netip.ParseAddr(gw)
	return err == nil && p.Contains(a)
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
