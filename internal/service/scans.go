package service

import (
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/scan"
	"netdiag/internal/store"
)

// Interfaces lists usable interfaces with suggested targets filled in.
func (s *Service) Interfaces() ([]netinfo.Iface, error) {
	list, err := s.cfg.Interfaces()
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []netinfo.Iface{}
	}
	for i := range list {
		list[i].Suggested, _ = netinfo.SuggestTarget(list[i])
	}
	return list, nil
}

// Start begins a scan bound to the service base context.
func (s *Service) Start(req model.ScanRequest) (*scan.Scan, error) {
	sc, err := s.cfg.Registry.Start(s.cfg.BaseCtx, req)
	if err != nil {
		return nil, err
	}
	s.attachLog(sc, false)
	return sc, nil
}

// Get returns a live scan by id.
func (s *Service) Get(id string) (*scan.Scan, bool) { return s.cfg.Registry.Get(id) }

// Cancel cancels a live scan; false if unknown.
func (s *Service) Cancel(id string) bool { return s.cfg.Registry.Cancel(id) }

// ResolveScan finds a scan by id in the live registry, then in history. The
// result never holds the SNMP community: scan snapshots and saved scans are
// redacted where they are made, and model.RedactScan is applied again here
// so every caller of the service gets the same guarantee (F11).
func (s *Service) ResolveScan(id string) (model.ScanResult, error) {
	if sc, ok := s.cfg.Registry.Get(id); ok {
		return model.RedactScan(sc.Snapshot()), nil
	}
	if s.cfg.Store == nil {
		return model.ScanResult{}, store.ErrNotFound
	}
	r, err := s.cfg.Store.LoadScan(id)
	return model.RedactScan(r), err
}
