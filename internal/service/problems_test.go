package service

import (
	"context"
	"testing"

	"netdiag/internal/health"
	"netdiag/internal/model"
	"netdiag/internal/problems"
)

// okHealth answers every on-demand run with fine checks at a fixed time.
type okHealth struct{}

func (okHealth) Run(context.Context, health.Input) model.HealthReport {
	return model.HealthReport{CheckedAt: "2099-01-01T00:00:00Z", Checks: []model.HealthCheck{
		{ID: "gateway", Label: "Gateway", Status: model.HealthOK}, {ID: "dns", Label: "DNS", Status: model.HealthOK}}}
}

// Problems speaks for the newest saved scan when nothing ran in this
// process, takes the start of an open problem from the ledger, and lets a
// newer Check again stand in for the scan's health report (GET /api/health
// says the same).
func TestProblemsSavedScanLedgerAndHealthRun(t *testing.T) {
	s, st := newSvc(t, true, okHealth{})
	if v := s.Problems(); v.Source != problems.SourceNone {
		t.Fatalf("nothing yet: %+v", v)
	}
	const net = "n-0123456789ab"
	saved := model.ScanResult{ID: "h-1", Request: model.ScanRequest{CIDR: "10.9.0.0/24"}, State: "done",
		StartedAt: "2026-10-17T10:00:00Z", FinishedAt: "2026-10-17T10:01:00Z",
		Health: &model.HealthReport{CheckedAt: "2026-10-17T10:00:30Z", Network: net, Checks: []model.HealthCheck{
			{ID: "gateway", Label: "Gateway", Status: model.HealthOK},
			{ID: "dns", Label: "DNS", Status: model.HealthFail, Data: map[string]any{"reason": model.ReasonNoReply}}}}}
	if err := st.SaveScan(saved); err != nil {
		t.Fatal(err)
	}
	l, err := problems.OpenLedger(st)
	if err != nil {
		t.Fatal(err)
	}
	l.Apply(net, []model.Notice{{Trigger: model.TriggerHealthFailed, Key: "healthFailed||dns", Severity: "warn",
		Title: "Health check failed", At: "2026-10-17T09:50:00Z"}})
	s.cfg.Ledger = l

	v := s.Problems()
	if v.Source != problems.SourceSaved || v.EverythingFine || v.CheckedAt != saved.FinishedAt || len(v.Open) != 1 {
		t.Fatalf("saved: %+v", v)
	}
	if p := v.Open[0]; p.Key != "healthFailed||dns" || p.Severity != problems.SevWarning || p.Since != "2026-10-17T09:50:00Z" ||
		p.Advice == nil {
		t.Errorf("dns item: %+v", p)
	}

	s.RunHealth(context.Background(), "")
	v = s.Problems()
	if !v.EverythingFine || len(v.Open) != 0 || v.CheckedAt != "2099-01-01T00:00:00Z" {
		t.Errorf("after Check again: %+v", v)
	}
	if h, ok := s.LatestHealth(); !ok || h.CheckedAt != "2099-01-01T00:00:00Z" {
		t.Errorf("GET /api/health after Check again: %+v", h)
	}
}
