package service

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"netdiag/internal/health"
	"netdiag/internal/model"
	"netdiag/internal/problems"
	"netdiag/internal/scan"
)

// okHealth answers every on-demand run with fine checks of network at a
// fixed time.
type okHealth struct{ network, at string }

func (h okHealth) Run(context.Context, health.Input) model.HealthReport {
	at := h.at
	if at == "" {
		at = "2099-01-01T00:00:00Z"
	}
	return model.HealthReport{CheckedAt: at, Network: h.network, Checks: []model.HealthCheck{
		{ID: "gateway", Label: "Gateway", Status: model.HealthOK}, {ID: "dns", Label: "DNS", Status: model.HealthOK}}}
}

const testNet = "n-0123456789ab"

// savedDNSFail is a saved scan of testNet whose DNS check failed.
func savedDNSFail() model.ScanResult {
	return model.ScanResult{ID: "h-1", Request: model.ScanRequest{CIDR: "10.9.0.0/24"}, State: "done",
		StartedAt: "2026-10-17T10:00:00Z", FinishedAt: "2026-10-17T10:01:00Z",
		Health: &model.HealthReport{CheckedAt: "2026-10-17T10:00:30Z", Network: testNet, Checks: []model.HealthCheck{
			{ID: "gateway", Label: "Gateway", Status: model.HealthOK},
			{ID: "dns", Label: "DNS", Status: model.HealthFail, Data: map[string]any{"reason": model.ReasonNoReply}}}}}
}

// Problems speaks for the newest saved scan when nothing ran in this
// process, takes the start of an open problem from the ledger, and lets a
// newer Check again of the same network stand in for the scan's health
// report (GET /api/health says the same). A Check again of another network
// (another adapter, or this computer moved) never does.
func TestProblemsSavedScanLedgerAndHealthRun(t *testing.T) {
	s, st := newSvc(t, true, okHealth{network: testNet})
	if v := s.Problems(); v.Source != problems.SourceNone {
		t.Fatalf("nothing yet: %+v", v)
	}
	saved := savedDNSFail()
	if err := st.SaveScan(saved); err != nil {
		t.Fatal(err)
	}
	l, err := problems.OpenLedger(st)
	if err != nil {
		t.Fatal(err)
	}
	l.Apply(testNet, []model.Notice{{Trigger: model.TriggerHealthFailed, Key: "healthFailed||dns", Severity: "warn",
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
	if a := v.Alerted(); len(a) != 1 || a[0].Key != "healthFailed||dns" {
		t.Errorf("alerted: %+v", a)
	}

	s.RunHealth(context.Background(), "")
	v = s.Problems()
	if !v.EverythingFine || len(v.Open) != 0 || v.CheckedAt != "2099-01-01T00:00:00Z" {
		t.Errorf("after Check again: %+v", v)
	}
	if h, ok := s.LatestHealth(); !ok || h.CheckedAt != "2099-01-01T00:00:00Z" {
		t.Errorf("GET /api/health after Check again: %+v", h)
	}

	// Check again on another network: the scan's own report speaks again.
	s.cfg.Health = okHealth{network: "n-ffffffffffff", at: "2099-01-02T00:00:00Z"}
	s.RunHealth(context.Background(), "")
	v = s.Problems()
	if v.EverythingFine || len(v.Open) != 1 || v.CheckedAt != saved.FinishedAt {
		t.Errorf("Check again of another network: %+v", v)
	}
	if h, ok := s.LatestHealth(); !ok || h.CheckedAt != saved.Health.CheckedAt {
		t.Errorf("GET /api/health after another network's Check again: %+v", h)
	}
	// Nor one whose network netdiag could not tell.
	s.cfg.Health = okHealth{at: "2099-01-03T00:00:00Z"}
	s.RunHealth(context.Background(), "")
	if v := s.Problems(); v.EverythingFine {
		t.Errorf("Check again of an unknown network: %+v", v)
	}
}

// blockPinger answers nothing until its scan is cancelled.
type blockPinger struct{}

func (blockPinger) Ping(ctx context.Context, _ netip.Addr, _ time.Duration) (time.Duration, bool, error) {
	<-ctx.Done()
	return 0, false, ctx.Err()
}

// A scan cancelled during its first pass saw part of the network and ran
// no health checks: the card speaks for the newest saved scan instead.
func TestProblemsSkipsCancelledScan(t *testing.T) {
	s, st := newSvc(t, true, nil)
	if err := st.SaveScan(savedDNSFail()); err != nil {
		t.Fatal(err)
	}
	s.cfg.Registry = scan.NewRegistry(scan.New(scan.Deps{Pinger: blockPinger{}, ARP: fakeARP{}, Ports: fakePorts{}, Web: fakeWeb{},
		SNMP: fakeSNMP{}, DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{}}))
	sc, err := s.Start(model.ScanRequest{CIDR: "10.0.0.0/30"})
	if err != nil {
		t.Fatal(err)
	}
	if v := s.Problems(); v.Source != problems.SourceLive || v.EverythingFine {
		t.Errorf("first pass running: %+v", v)
	}
	sc.Cancel()
	select {
	case <-sc.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("scan did not stop")
	}
	if st := sc.State(); st != model.StateCancelled {
		t.Fatalf("state %s", st)
	}
	v := s.Problems()
	if v.Source != problems.SourceSaved || len(v.Open) != 1 || v.Open[0].Key != "healthFailed||dns" {
		t.Errorf("after a cancelled scan: %+v", v)
	}
}

// The saved path decodes only the newest readable scan, not the whole
// history (Store.LatestScan): a damaged newest file is passed over.
func TestProblemsSavedScanSkipsUnreadable(t *testing.T) {
	s, st := newSvc(t, true, nil)
	if err := st.SaveScan(savedDNSFail()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir(), "history", "20991231T000000Z-bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v := s.Problems(); v.Source != problems.SourceSaved || len(v.Open) != 1 {
		t.Errorf("saved behind an unreadable file: %+v", v)
	}
}

// The team chat burst takes the card's alerted items only while the card
// speaks for a live scan of the network the burst is for: never a saved
// scan's, nor a scan of another network.
func TestAlertedOnSpeaksForTheWatchingScan(t *testing.T) {
	s, st := newSvc(t, true, nil)
	if err := st.SaveScan(savedDNSFail()); err != nil {
		t.Fatal(err)
	}
	l, err := problems.OpenLedger(st)
	if err != nil {
		t.Fatal(err)
	}
	l.Apply(testNet, []model.Notice{{Trigger: model.TriggerHealthFailed, Key: "healthFailed||dns", Severity: "warn",
		Title: "Health check failed", At: "2026-10-17T09:50:00Z"}})
	s.cfg.Ledger = l
	if a, ok := s.alertedOn(testNet); ok || a != nil {
		t.Fatalf("a saved scan: %+v %v", a, ok)
	}
	s.cfg.Registry = scan.NewRegistry(scan.New(scan.Deps{Pinger: blockPinger{}, ARP: fakeARP{}, Ports: fakePorts{}, Web: fakeWeb{},
		SNMP: fakeSNMP{}, DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{}}))
	sc, err := s.Start(model.ScanRequest{CIDR: "10.0.0.0/30", Live: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { sc.Cancel(); <-sc.Done() }()
	// The running scan could not tell its network (no router in its ARP
	// table): it speaks for network "" only.
	if _, ok := s.alertedOn(testNet); ok {
		t.Error("spoke for another network's scan")
	}
	if a, ok := s.alertedOn(""); !ok || len(a) != 0 {
		t.Errorf("the watching scan: %+v %v", a, ok)
	}
}
