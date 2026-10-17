package model

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestHostJSONKeys(t *testing.T) {
	b, err := json.Marshal(Host{IP: "1.2.3.4", FoundBy: []string{"icmp"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, k := range []string{`"ip"`, `"isSelf"`, `"foundBy"`, `"rttMs"`, `"openPorts"`, `"isPrinter"`, `"printerReason"`, `"updatedAt"`, `"snmp":null`, `"printer":null`} {
		if !strings.Contains(s, k) {
			t.Errorf("missing %s in %s", k, s)
		}
	}
}

func TestDefaults(t *testing.T) {
	want := []int{80, 443, 8080, 8443, 9100, 631, 515, PortSSH, PortRDP, PortVNC}
	if !slices.Equal(DefaultPorts, want) || DefaultCommunity != "public" {
		t.Fatalf("bad defaults: %v", DefaultPorts)
	}
	for p, remote := range map[int]bool{22: true, 3389: true, 5900: true, 80: false, 9100: false} {
		if IsRemotePort(p) != remote {
			t.Errorf("IsRemotePort(%d) = %v", p, !remote)
		}
	}
}

// TestV01FieldNamesStable proves every v0.1 JSON field name still serializes.
func TestV01FieldNamesStable(t *testing.T) {
	res := ScanResult{Hosts: []Host{{Web: []WebService{{TLS: &TLSInfo{}}}, Snmp: &SnmpInfo{}, Printer: &PrinterInfo{
		Supplies: []Supply{{}}, InputTrays: []InputTray{{}}, Alerts: []Alert{{}}, Covers: []Cover{{}}}}}}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	keys := []string{"id", "request", "cidr", "interface", "community", "ports", "startedAt", "finishedAt", "state", "self", "mac", "gateway", "hosts", "stats",
		"scanned", "alive", "printers", "web", "ip", "isSelf", "foundBy", "rttMs", "vendor", "hostname", "openPorts", "snmp", "isPrinter", "printerReason", "printer", "errors", "updatedAt", "online",
		"port", "scheme", "url", "status", "title", "server", "tls", "error", "subject", "issuer", "notAfter", "selfSigned", "expired",
		"version", "sysDescr", "sysObjectID", "sysName", "sysLocation", "sysContact", "sysUpTime", "hrDeviceDescr", "hrDeviceStatus", "hrPrinterStatus", "hrPrinterDetectedErrorState",
		"model", "serial", "pageCount", "consoleDisplay", "supplies", "inputTrays", "alerts", "covers",
		"description", "type", "color", "level", "max", "percent", "unit", "name", "mediaName", "severity", "code",
		"mdns", "health", "schemaVersion"}
	for _, k := range keys {
		if !strings.Contains(s, `"`+k+`":`) {
			t.Errorf("missing %q in %s", k, s)
		}
	}
	var back ScanResult
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(back)
	if string(b2) != s {
		t.Errorf("round trip differs")
	}
}

// NetworkID names a network by its address prefix, router and router MAC,
// the same way however they are written; another router is another
// network, and ValidNetworkID accepts exactly its form.
func TestNetworkID(t *testing.T) {
	id := NetworkID("192.168.1.0/24", "192.168.1.1", "AA:BB:CC:DD:EE:01")
	if !ValidNetworkID(id) || len(id) != 14 || !strings.HasPrefix(id, "n-") {
		t.Fatalf("id %q", id)
	}
	for _, same := range []string{
		NetworkID(" 192.168.1.20/24 ", "192.168.1.1", "aa:bb:cc:dd:ee:01"),
		NetworkID("192.168.1.0/24", "192.168.1.1", "aa-bb-cc-dd-ee-01"),
	} {
		if same != id {
			t.Errorf("%q, want %q", same, id)
		}
	}
	for _, other := range []string{
		NetworkID("192.168.1.0/24", "192.168.1.1", "AA:BB:CC:DD:EE:02"),
		NetworkID("192.168.1.0/24", "192.168.1.254", "AA:BB:CC:DD:EE:01"),
		NetworkID("192.168.0.0/16", "192.168.1.1", "AA:BB:CC:DD:EE:01"),
		NetworkID("192.168.1.0/24", "", ""),
	} {
		if other == id {
			t.Errorf("another network has the same id %q", id)
		}
	}
	for _, bad := range []string{"", "n-", "n-0123456789a", "n-0123456789abc", "n-0123456789AB", "x-0123456789ab", "n-01234567892g"} {
		if ValidNetworkID(bad) {
			t.Errorf("ValidNetworkID(%q)", bad)
		}
	}
}
