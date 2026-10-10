package model

import (
	"encoding/json"
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
	if len(DefaultPorts) != 7 || DefaultCommunity != "public" {
		t.Fatal("bad defaults")
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
