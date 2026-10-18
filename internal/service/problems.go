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
// in the last day. The scan is the running one (source live, with its storm
// guard), else the newest finished one netdiag still holds (latest), else
// the newest readable saved scan (saved), else the last on-demand health run
// (Check again) alone (health), else nothing (none). A newer on-demand
// health run of the same network stands in for the scan's health report
// (standsIn).
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
		} else {
			// A cancelled or failed scan saw part of the network at most and
			// ran no health checks: the card speaks for the newest finished
			// one, else for a saved scan.
			list := s.cfg.Registry.List()
			for i := len(list) - 1; i >= 0; i-- {
				if list[i].State() == model.StateDone {
					sc, in.Source = list[i], problems.SourceLatest
					break
				}
			}
		}
	}
	var r model.ScanResult
	var looked []string // when the scan looked, besides its start or end
	switch {
	case sc != nil:
		r = model.RedactScan(sc.Snapshot())
		in.Network = sc.Network()
		looked = append(looked, sc.LiveSince())
		if st, ok := sc.LiveStatus(); ok && in.Source == problems.SourceLive {
			// LastCheckAt moves only when a cycle checked: not while the
			// storm guard has stopped the checks.
			looked = append(looked, st.LastCheckAt)
			in.Held, in.HeldReason, in.HeldSince = st.Held, st.HeldReason, st.HeldSince
		}
	case s.cfg.Store != nil:
		if res, ok := s.cfg.Store.LatestScan(); ok {
			r, in.Source = model.RedactScan(res), problems.SourceSaved
		}
	}
	if in.Source == "" {
		in.Source = problems.SourceNone
		// No scan, but Check again ran: the card speaks for the health
		// checks (their problems; never everything fine, since no device
		// was looked at), so it does not say netdiag looked at nothing.
		h := s.lastHealthRun()
		if h == nil {
			return problems.NewView(in)
		}
		in.Source, in.Network, in.CheckedAt = problems.SourceHealth, h.Network, h.CheckedAt
		in.Result = &model.ScanResult{State: model.StateDone, Health: h}
		s.ledgerInto(&in)
		return problems.NewView(in)
	}
	if in.Network == "" && r.Health != nil {
		in.Network = r.Health.Network
	}
	if h := s.lastHealthRun(); standsIn(h, r.Health, in.Network, in.Source == problems.SourceLive) {
		r.Health = h
	}
	in.CheckedAt = r.FinishedAt
	if in.CheckedAt == "" {
		in.CheckedAt = r.StartedAt
	}
	if r.Health != nil {
		looked = append(looked, r.Health.CheckedAt)
	}
	for _, t := range looked {
		if t > in.CheckedAt { // RFC 3339 in UTC: the text sorts as the time
			in.CheckedAt = t
		}
	}
	in.Result = &r
	s.ledgerInto(&in)
	return problems.NewView(in)
}

// ledgerInto fills in's open and fixed problems from the ledger.
func (s *Service) ledgerInto(in *problems.Input) {
	switch {
	case s.cfg.Ledger != nil:
		in.Open, in.Fixed = s.cfg.Ledger.Snapshot()
	case s.cfg.Store != nil:
		// Not the window's netdiag (`netdiag mcp` over stdio): the window's
		// ledger is the file's only writer, so read what it last saved. An
		// unreadable file leaves out only the starts and the fixed list.
		in.Open, in.Fixed, _ = problems.ReadLedger(s.cfg.Store)
	}
}

// standsIn reports whether run, the report of the last Check again, may
// stand in for a scan's own health report own: it looked at the scan's
// network (never one netdiag could not tell, nor another network: another
// adapter, or after this computer moved), it is newer, and it does not fill
// in for a running scan whose own checks have not answered yet.
func standsIn(run, own *model.HealthReport, network string, running bool) bool {
	if run == nil || run.Network == "" || run.Network != network {
		return false
	}
	if own == nil {
		return !running
	}
	return run.CheckedAt > own.CheckedAt
}
