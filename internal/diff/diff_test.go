package diff

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"netdiag/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files")

func load(t *testing.T, name string) model.ScanResult {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "diff", name))
	if err != nil {
		t.Fatal(err)
	}
	var s model.ScanResult
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("..", "..", "testdata", "diff", name)
	if *update {
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	norm := func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")) }
	if !bytes.Equal(norm(want), norm(got)) {
		t.Errorf("%s mismatch; run with -update.\n got:\n%s", name, got)
	}
}

func TestGolden(t *testing.T) {
	d := Compare(load(t, "base.json"), load(t, "current.json"))
	var jb, cb bytes.Buffer
	if err := WriteJSON(&jb, d); err != nil {
		t.Fatal(err)
	}
	if err := WriteCSV(&cb, d); err != nil {
		t.Fatal(err)
	}
	golden(t, "expected.json", jb.Bytes())
	golden(t, "expected.csv", cb.Bytes())
}

func TestSummary(t *testing.T) {
	d := Compare(load(t, "base.json"), load(t, "current.json"))
	if d.Summary != (model.DiffSummary{Added: 1, Removed: 1, Changed: 4}) {
		t.Errorf("summary = %+v", d.Summary)
	}
	if d.Base.ID != "s1" || d.Current.CIDR != "192.168.1.0/24" {
		t.Errorf("refs = %+v %+v", d.Base, d.Current)
	}
}

func TestMACMoveIsSingleIPChange(t *testing.T) {
	d := Compare(load(t, "base.json"), load(t, "current.json"))
	for _, c := range d.Changed {
		if c.MAC == "AA:AA:AA:00:00:50" {
			if len(c.Changes) != 1 || c.Changes[0].Field != "ip" {
				t.Errorf("changes = %+v", c.Changes)
			}
			return
		}
	}
	t.Error("mover not found")
}

func TestIdentical(t *testing.T) {
	b := load(t, "base.json")
	d := Compare(b, b)
	if d.Summary != (model.DiffSummary{}) {
		t.Errorf("summary = %+v", d.Summary)
	}
}

func TestDeviceTypeChange(t *testing.T) {
	d := Compare(load(t, "base.json"), load(t, "current.json"))
	for _, c := range d.Changed {
		if c.IP == "192.168.1.20" {
			for _, f := range c.Changes {
				if f.Field == "deviceType" && f.From == "unknown" && f.To == "printer" {
					return
				}
			}
		}
	}
	t.Error("deviceType unknown->printer change missing")
}

func TestOldBaseNoSpuriousDeviceType(t *testing.T) {
	base := load(t, "base.json")
	for i := range base.Hosts {
		base.Hosts[i].DeviceType = ""
	}
	d := Compare(base, load(t, "current.json"))
	for _, c := range d.Changed {
		for _, f := range c.Changes {
			if f.Field == "deviceType" {
				t.Errorf("spurious deviceType change: %+v", c)
			}
		}
	}
}

// TestPortsOnlyComparedWhenBothScansProbedThem: a newer scan that also
// probes the remote access ports must not show them as newly opened on
// every device.
func TestPortsOnlyComparedWhenBothScansProbedThem(t *testing.T) {
	b := model.ScanResult{Request: model.ScanRequest{Ports: []int{80, 443}},
		Hosts: []model.Host{{IP: "10.0.0.5", MAC: "00:11:22:33:44:55", OpenPorts: []int{80}}}}
	c := model.ScanResult{Request: model.ScanRequest{Ports: []int{80, 443, 22, 3389}},
		Hosts: []model.Host{{IP: "10.0.0.5", MAC: "00:11:22:33:44:55", OpenPorts: []int{80, 3389}}}}
	if d := Compare(b, c); len(d.Changed) != 0 {
		t.Fatalf("new probe port reported as a change: %+v", d.Changed)
	}
	c.Hosts[0].OpenPorts = []int{443, 3389}
	d := Compare(b, c)
	if len(d.Changed) != 1 || d.Changed[0].Changes[0].Field != "openPorts" || d.Changed[0].Changes[0].To != "443" {
		t.Fatalf("real change on a common port: %+v", d.Changed)
	}
	// Scans without a recorded port list compare every port, as before.
	b.Request.Ports, c.Request.Ports = nil, nil
	if d := Compare(b, c); len(d.Changed) != 1 || d.Changed[0].Changes[0].To != "443,3389" {
		t.Fatalf("no port lists: %+v", d.Changed)
	}
}

// TestNoSpuriousTypeFromNewPorts: a device typed "computer" only because of
// Remote Desktop, which the older scan never checked, is not a type change.
func TestNoSpuriousTypeFromNewPorts(t *testing.T) {
	b := model.ScanResult{Request: model.ScanRequest{Ports: []int{80, 443, 8080, 8443, 9100, 631, 515}},
		Hosts: []model.Host{{IP: "10.0.0.5", MAC: "00:11:22:33:44:55", DeviceType: "unknown"}}}
	c := model.ScanResult{Request: model.ScanRequest{Ports: model.DefaultPorts},
		Hosts: []model.Host{{IP: "10.0.0.5", MAC: "00:11:22:33:44:55", OpenPorts: []int{3389}, DeviceType: "computer", DeviceTypeReason: "remote desktop port 3389"}}}
	if d := Compare(b, c); len(d.Changed) != 0 {
		t.Fatalf("spurious change: %+v", d.Changed)
	}
	// A real type change on common signals is still reported.
	c.Hosts[0].IsPrinter, c.Hosts[0].DeviceType = true, "printer"
	if d := Compare(b, c); len(d.Changed) != 1 || d.Changed[0].Changes[0].Field != "deviceType" {
		t.Fatalf("real change lost: %+v", d.Changed)
	}
}
