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
