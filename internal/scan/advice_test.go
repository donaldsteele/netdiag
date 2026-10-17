package scan

import (
	"context"
	"testing"

	"netdiag/internal/arp"
	"netdiag/internal/model"
)

// failing is a gateway failure as the health checks report it.
func failing() model.HealthReport {
	rep := healthReport(model.HealthFail)
	rep.Checks[0].Detail = "10.0.0.1 did not reply to ping or on ports 80 and 443"
	rep.Checks[0].Data = map[string]any{"reason": model.ReasonNoReply}
	return rep
}

// TestHealthAndNoticeAdvice: health reports carry their advice, and the
// healthFailed notice carries the check's advice.
func TestHealthAndNoticeAdvice(t *testing.T) {
	ok := healthReport(model.HealthOK)
	d := presenceDeps()
	d.Health = &fakeHealth{reports: []model.HealthReport{ok, failing()}}
	_, res := runCycles(t, d, "10.0.0.0/28", 4)
	if res.Health == nil || res.Health.Checks[0].Advice == nil ||
		res.Health.Checks[0].Advice.Meaning != "This computer can't reach your router." ||
		res.Health.Checks[0].Advice.Technical != "10.0.0.1 did not reply to ping or on ports 80 and 443" {
		t.Fatalf("health = %+v", res.Health)
	}
	if a := res.Health.Checks[1].Advice; a != nil {
		t.Fatalf("ok check has advice %+v", a)
	}
	n := notices(res, model.TriggerHealthFailed)
	if len(n) != 1 || n[0].Advice == nil || n[0].Advice.Meaning != "This computer can't reach your router." {
		t.Fatalf("notices = %+v", n)
	}
}

// TestConflictAdvice: a mac_flap conflict and its notice carry the advice
// for a swap that was seen, with Show device on the address.
func TestConflictAdvice(t *testing.T) {
	sp := &scriptPinger{sweepAlive: map[string]bool{}, script: map[string][]bool{}, presence: map[string]int{}}
	a := []arp.Entry{{IP: "10.0.0.5", MAC: "00:11:22:33:44:0A"}}
	b := []arp.Entry{{IP: "10.0.0.5", MAC: "00:11:22:33:44:0B"}}
	d := presenceDeps()
	d.Pinger = sp
	d.ARP = &scriptARP{tables: [][]arp.Entry{a, a, b, a, b, a, b}}
	d.Ports = fakePorts{"10.0.0.5": {80}}
	d.TCP = fakePorts{"10.0.0.5": {80}}
	_, res := runCycles(t, d, "10.0.0.0/28", 6)
	var flap *model.Conflict
	for i, c := range res.Conflicts {
		if c.Kind == model.ConflictMACFlap {
			flap = &res.Conflicts[i]
		}
	}
	want := "Two devices are taking turns using 10.0.0.5, so both work badly."
	if flap == nil || flap.Advice == nil || flap.Advice.Meaning != want || len(flap.Advice.Actions) != 1 ||
		flap.Advice.Actions[0].Kind != model.ActionShowDevice || flap.Advice.Actions[0].Target != "10.0.0.5" {
		t.Fatalf("conflict = %+v", flap)
	}
	n := notices(res, model.TriggerIPConflict)
	if len(n) != 1 || n[0].Advice == nil || n[0].Advice.Meaning != want {
		t.Fatalf("notices = %+v", n)
	}
}

// TestPrinterWithoutStatusAdvice: a printer found only by its print port
// gets the "doesn't report its status" advice; one with SNMP does not.
func TestPrinterWithoutStatusAdvice(t *testing.T) {
	res, err := New(fakeDeps()).Run(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	raw := hostByIP(t, res, "10.0.0.11")
	if !raw.IsPrinter || raw.Advice == nil || raw.Advice.Meaning != "This printer doesn't report its status, so netdiag can only see whether it answers." {
		t.Fatalf("print-port printer = %+v", raw)
	}
	if h := hostByIP(t, res, "10.0.0.10"); h.Advice != nil {
		t.Fatalf("SNMP printer has advice %+v", h.Advice)
	}
	if h := hostByIP(t, res, "10.0.0.1"); h.Advice != nil {
		t.Fatalf("router has advice %+v", h.Advice)
	}
}
