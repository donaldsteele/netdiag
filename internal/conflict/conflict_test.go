package conflict

import (
	"testing"
	"time"

	"netdiag/internal/model"
)

var now = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func ob(macs ...string) []model.MACObservation {
	var o []model.MACObservation
	for _, m := range macs {
		o = append(o, model.MACObservation{MAC: m, At: "2026-01-01T00:00:00Z"})
	}
	return o
}

func kinds(cs []model.Conflict) map[string]int {
	m := map[string]int{}
	for _, c := range cs {
		m[c.Kind]++
	}
	return m
}

func TestFlapVsSingleChange(t *testing.T) {
	single := map[string][]model.MACObservation{"10.0.0.5": ob("AA:AA:AA:AA:AA:01", "AA:AA:AA:AA:AA:02")}
	if cs := Analyze(nil, single, "10.0.0.1", "10.0.0.2", nil, now); len(cs) != 0 {
		t.Fatalf("single change: %v", cs)
	}
	flap := map[string][]model.MACObservation{"10.0.0.5": ob("AA:AA:AA:AA:AA:01", "aa:aa:aa:aa:aa:02", "AA:AA:AA:AA:AA:01")}
	cs := Analyze(nil, flap, "10.0.0.1", "10.0.0.2", nil, now)
	if len(cs) != 1 || cs[0].Kind != model.ConflictMACFlap || cs[0].Severity != "fail" {
		t.Fatalf("flap: %v", cs)
	}
	again := Analyze(nil, flap, "10.0.0.1", "10.0.0.2", nil, now)
	if again[0].ID != cs[0].ID {
		t.Fatal("unstable id")
	}
	if cs := Analyze(nil, flap, "", "", []string{"aa:aa:aa:aa:aa:01"}, now); len(cs) != 0 {
		t.Fatalf("ignored: %v", cs)
	}
}

func TestStaleFlapIsNotConflict(t *testing.T) {
	// A,B,A alternations from an hour ago must not keep a flap alive.
	stale := map[string][]model.MACObservation{"10.0.0.5": {
		{MAC: "AA:AA:AA:AA:AA:01", At: "2025-12-31T23:00:00Z"},
		{MAC: "AA:AA:AA:AA:AA:02", At: "2025-12-31T23:01:00Z"},
		{MAC: "AA:AA:AA:AA:AA:01", At: "2025-12-31T23:02:00Z"},
	}}
	if cs := Analyze(nil, stale, "10.0.0.1", "10.0.0.2", nil, now); len(cs) != 0 {
		t.Fatalf("stale flap produced conflict: %v", cs)
	}
	// Same alternations inside the 10-minute window are still a flap.
	recent := map[string][]model.MACObservation{"10.0.0.5": {
		{MAC: "AA:AA:AA:AA:AA:01", At: "2025-12-31T23:55:00Z"},
		{MAC: "AA:AA:AA:AA:AA:02", At: "2025-12-31T23:56:00Z"},
		{MAC: "AA:AA:AA:AA:AA:01", At: "2025-12-31T23:57:00Z"},
	}}
	if cs := Analyze(nil, recent, "10.0.0.1", "10.0.0.2", nil, now); len(cs) != 1 || cs[0].Kind != model.ConflictMACFlap {
		t.Fatalf("recent flap missing: %v", cs)
	}
	// Stale history followed by a fresh single change is not a flap.
	mixed := map[string][]model.MACObservation{"10.0.0.5": {
		{MAC: "AA:AA:AA:AA:AA:01", At: "2025-12-31T23:00:00Z"},
		{MAC: "AA:AA:AA:AA:AA:02", At: "2025-12-31T23:01:00Z"},
		{MAC: "AA:AA:AA:AA:AA:01", At: "2025-12-31T23:02:00Z"},
		{MAC: "AA:AA:AA:AA:AA:03", At: "2026-01-01T00:00:00Z"},
	}}
	if cs := Analyze(nil, mixed, "10.0.0.1", "10.0.0.2", nil, now); len(cs) != 0 {
		t.Fatalf("stale history plus single change: %v", cs)
	}
}

func TestMultiIP(t *testing.T) {
	hosts := []model.Host{
		{IP: "10.0.0.1", MAC: "AA:00:00:00:00:01"}, // gateway
		{IP: "10.0.0.9", MAC: "AA:00:00:00:00:01"},
		{IP: "10.0.0.2", MAC: "AA:00:00:00:00:02", IsSelf: true},
		{IP: "10.0.0.3", MAC: "AA:00:00:00:00:03"},
		{IP: "10.0.0.4", MAC: "AA:00:00:00:00:03"},
		{IP: "10.0.0.7", MAC: "AA:00:00:00:00:07"},
	}
	cs := Analyze(hosts, nil, "10.0.0.1", "10.0.0.2", nil, now)
	if len(cs) != 1 || cs[0].Kind != model.ConflictMACMultiIP || cs[0].Severity != "warn" || len(cs[0].IPs) != 2 {
		t.Fatalf("got %v", cs)
	}
	if cs[0].Detail == "" {
		t.Fatal("no hint")
	}
	if cs := Analyze(hosts, nil, "10.0.0.1", "10.0.0.2", []string{"aa:00:00:00:00:03"}, now); len(cs) != 0 {
		t.Fatalf("ignored: %v", cs)
	}
}

func TestDuplicateNames(t *testing.T) {
	hosts := []model.Host{
		{IP: "10.0.0.3", MAC: "AA:00:00:00:00:03", Hostname: "Printer1"},
		{IP: "10.0.0.4", MAC: "AA:00:00:00:00:04", Hostname: "printer1."},
		{IP: "10.0.0.5", MAC: "AA:00:00:00:00:05", Snmp: &model.SnmpInfo{SysName: "sw"}},
		{IP: "10.0.0.6", MAC: "AA:00:00:00:00:06", Snmp: &model.SnmpInfo{SysName: "SW"}},
		// same MAC, same name on two IPs: no duplicate_name
		{IP: "10.0.0.7", MAC: "AA:00:00:00:00:07", Hostname: "same"},
		{IP: "10.0.0.8", MAC: "AA:00:00:00:00:07", Hostname: "same", Snmp: &model.SnmpInfo{SysName: "same"}},
	}
	k := kinds(Analyze(hosts, nil, "", "", nil, now))
	if k[model.ConflictDuplicateName] != 2 {
		t.Fatalf("got %v", k)
	}
	if k[model.ConflictMACMultiIP] != 1 {
		t.Fatalf("multi %v", k)
	}
}

func TestDuplicateMDNSNames(t *testing.T) {
	hosts := []model.Host{
		// cloned printers advertising the same Bonjour instance name
		{IP: "10.0.0.3", MAC: "AA:00:00:00:00:03", MDNS: &model.MDNSInfo{Instance: "Reg Printer"}},
		{IP: "10.0.0.4", MAC: "AA:00:00:00:00:04", MDNS: &model.MDNSInfo{Instance: "reg printer"}},
		// same mDNS hostname on two devices, with and without trailing dot
		{IP: "10.0.0.5", MAC: "AA:00:00:00:00:05", MDNS: &model.MDNSInfo{Hostname: "BRN01.local."}},
		{IP: "10.0.0.6", MAC: "AA:00:00:00:00:06", MDNS: &model.MDNSInfo{Hostname: "brn01.local"}},
		// one device whose rDNS and mDNS names agree: no conflict
		{IP: "10.0.0.7", MAC: "AA:00:00:00:00:07", Hostname: "epson7.local", MDNS: &model.MDNSInfo{Instance: "EPSON7", Hostname: "epson7.local."}},
	}
	cs := Analyze(hosts, nil, "", "", nil, now)
	if k := kinds(cs); k[model.ConflictDuplicateName] != 2 || len(cs) != 2 {
		t.Fatalf("got %v", cs)
	}
}
