package service

import (
	"time"

	"netdiag/internal/model"
	"netdiag/internal/problems"
	"netdiag/internal/scan"
	"netdiag/internal/store"
)

// Problems is what is wrong right now (GET /api/problems; MCP list_problems
// builds on it in v0.6 M9): problems.Current over the scan the card speaks
// for, with the ledger's start of each open problem and its problems fixed
// in the last day. The scan is the running one (source live), else the
// newest one netdiag still holds (latest), else the newest readable saved
// scan (saved). A newer on-demand health run (Check again) stands in for
// the scan's own health report.
func (s *Service) Problems() problems.View {
	now := time.Now().UTC()
	in := problems.Input{Now: now, Settings: store.DefaultSettings()}
	if s.cfg.Store != nil {
		if set, err := s.cfg.Store.LoadSettings(); err == nil {
			in.Settings = set
		}
	}
	var sc *scan.Scan
	if s.cfg.Registry != nil {
		if run, ok := s.cfg.Registry.Running(); ok {
			sc, in.Source, in.Watching = run, problems.SourceLive, run.Request().Live
		} else if list := s.cfg.Registry.List(); len(list) > 0 {
			sc, in.Source = list[len(list)-1], problems.SourceLatest
		}
	}
	var r model.ScanResult
	switch {
	case sc != nil:
		r = model.RedactScan(sc.Snapshot())
		in.Network = sc.Network()
	case s.cfg.Store != nil:
		list, _ := s.cfg.Store.ListHistory()
		for _, e := range list {
			if e.Error != "" {
				continue
			}
			if res, err := s.cfg.Store.LoadScan(e.ID); err == nil {
				r, in.Source = model.RedactScan(res), problems.SourceSaved
				break
			}
		}
	}
	if in.Source == "" {
		in.Source = problems.SourceNone
		return problems.NewView(in)
	}
	if in.Network == "" && r.Health != nil {
		in.Network = r.Health.Network
	}
	if h := s.lastHealthRun(); h != nil && (r.Health == nil || h.CheckedAt > r.Health.CheckedAt) {
		r.Health = h
	}
	in.CheckedAt = r.FinishedAt
	if in.CheckedAt == "" {
		in.CheckedAt = r.StartedAt
	}
	if in.Source == problems.SourceLive {
		in.CheckedAt = now.Format(time.RFC3339)
	} else if r.Health != nil && r.Health.CheckedAt > in.CheckedAt {
		in.CheckedAt = r.Health.CheckedAt
	}
	in.Result = &r
	if s.cfg.Ledger != nil {
		in.Open, in.Fixed = s.cfg.Ledger.Snapshot()
	}
	return problems.NewView(in)
}
