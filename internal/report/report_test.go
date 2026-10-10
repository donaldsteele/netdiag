package report

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"netdiag/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/report")

const goldenDir = "../../testdata/report"

// fixture returns a ScanResult with deliberately unsorted hosts, a printer
// with multi-line/quoted text, a self-host, a host with no RTT, and a web
// service with an error.
func fixture() model.ScanResult {
	return model.ScanResult{
		ID: "11111111-2222-3333-4444-555555555555",
		Request: model.ScanRequest{
			CIDR:      "192.168.2.0/24",
			Interface: "Wi-Fi",
			Community: "public",
			Ports:     model.DefaultPorts,
		},
		StartedAt:  "2026-10-09T10:00:00Z",
		FinishedAt: "2026-10-09T10:00:07Z",
		State:      "done",
		Self: model.SelfInfo{
			IP:        "192.168.2.50",
			MAC:       "AA:BB:CC:00:11:22",
			Interface: "Wi-Fi",
		},
		Gateway: "192.168.2.1",
		Hosts: []model.Host{
			{
				IP:        "192.168.10.5",
				Alive:     false,
				FoundBy:   []string{"arp"},
				MAC:       "DE:AD:BE:EF:00:01",
				UpdatedAt: "2026-10-09T10:00:05Z",
			},
			{
				IP:        "192.168.2.200",
				Alive:     true,
				Online:    true,
				LastSeen:  "2026-10-09T10:00:03Z",
				FoundBy:   []string{"icmp", "tcp"},
				RttMs:     12.34,
				MAC:       "00:50:56:AB:CD:EF",
				Vendor:    "VMware, Inc.",
				OpenPorts: []int{8080},
				Web: []model.WebService{
					{
						Port:   8080,
						Scheme: "http",
						URL:    "http://192.168.2.200:8080/",
						Error:  "connection reset",
					},
				},
				UpdatedAt: "2026-10-09T10:00:06Z",
			},
			{
				IP:            "192.168.2.10",
				Alive:         true,
				FoundBy:       []string{"icmp", "arp"},
				RttMs:         3.2,
				MAC:           "00:1B:A9:12:34:56",
				Vendor:        "Brother Industries",
				OpenPorts:     []int{80, 443, 9100},
				IsPrinter:     true,
				PrinterReason: "hrDeviceType=printer",
				Web: []model.WebService{
					{
						Port:   80,
						Scheme: "http",
						URL:    "http://192.168.2.10/",
						Status: 200,
						Title:  "Brother HL-L2350DW",
						Server: "debut/1.30",
					},
					{
						Port:   443,
						Scheme: "https",
						URL:    "https://192.168.2.10/",
						Status: 200,
						Title:  "Brother HL-L2350DW",
						Server: "debut/1.30",
						TLS: &model.TLSInfo{
							Subject:    "CN=BRW001BA9123456",
							Issuer:     "CN=BRW001BA9123456",
							NotAfter:   "2030-01-01T00:00:00Z",
							SelfSigned: true,
						},
					},
				},
				Snmp: &model.SnmpInfo{
					Version:                     "v2c",
					SysDescr:                    "Brother HL-L2350DW series",
					SysName:                     "BRW001BA9123456",
					HrDeviceStatus:              "running",
					HrPrinterStatus:             "idle",
					HrPrinterDetectedErrorState: []string{"lowToner"},
				},
				Printer: &model.PrinterInfo{
					Model:          "HL-L2350DW",
					Serial:         "E7Z123456",
					PageCount:      12345,
					ConsoleDisplay: []string{"READY"},
					Supplies: []model.Supply{
						{
							Description: "Black Toner",
							Type:        "toner",
							Color:       "black",
							Level:       150,
							Max:         1000,
							Percent:     15,
							Unit:        "impressions",
							State:       "low",
						},
						{
							Description: `Drum, "Long Life"`,
							Type:        "drum",
							Level:       -2,
							Max:         -2,
							Percent:     -1,
							State:       "unknown",
						},
					},
					InputTrays: []model.InputTray{
						{Name: "Tray 1", MediaName: "A4", Level: 100, Max: 250, Status: "ok"},
					},
					Alerts: []model.Alert{
						{
							Severity:    "warning",
							Code:        "toner-low",
							Description: "Toner low, \"replace soon\"\nsecond line",
						},
					},
					Covers: []model.Cover{{Description: "Front Cover", Status: "closed"}},
				},
				UpdatedAt: "2026-10-09T10:00:07Z",
			},
			{
				IP:        "192.168.2.1",
				Alive:     true,
				FoundBy:   []string{"icmp"},
				RttMs:     1.1,
				MAC:       "C0:FF:EE:00:00:01",
				OpenPorts: []int{80},
				Web: []model.WebService{
					{
						Port:   80,
						Scheme: "http",
						URL:    "http://192.168.2.1/",
						Status: 401,
						Server: "lighttpd",
					},
				},
				Errors:    []string{"snmp: timeout"},
				UpdatedAt: "2026-10-09T10:00:04Z",
			},
			{
				IP:        "192.168.2.50",
				IsSelf:    true,
				Alive:     true,
				FoundBy:   []string{"icmp"},
				MAC:       "AA:BB:CC:00:11:22",
				UpdatedAt: "2026-10-09T10:00:03Z",
			},
		},
		Stats: model.Stats{Scanned: 254, Alive: 4, Printers: 1, Web: 3},
	}
}

func TestCSVHeaderOrder(t *testing.T) {
	want := []string{
		"ip", "mac", "vendor", "hostname", "alive", "found_by", "rtt_ms",
		"open_ports", "is_printer", "printer_reason", "model", "serial",
		"page_count", "supplies", "printer_status", "errors", "web_urls", "web_titles",
		"online", "last_seen",
	}
	if !reflect.DeepEqual(CSVColumns, want) {
		t.Fatalf("CSVColumns = %v\nwant %v", CSVColumns, want)
	}
}

func TestGoldenCSV(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, fixture()); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	compareGolden(t, filepath.Join(goldenDir, "golden.csv"), buf.Bytes())
}

func TestGoldenJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, fixture()); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	compareGolden(t, filepath.Join(goldenDir, "golden.json"), buf.Bytes())
}

func compareGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create)", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output differs from %s (run with -update to regenerate)\n--- got ---\n%s", path, got)
	}
}

func TestCSVRoundTripEscaping(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, fixture()); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v", err)
	}
	if len(recs) != 1+5 {
		t.Fatalf("got %d records, want 6 (header + 5 hosts)", len(recs))
	}
	for i, rec := range recs {
		if len(rec) != len(CSVColumns) {
			t.Fatalf("record %d has %d fields, want %d", i, len(rec), len(CSVColumns))
		}
	}
	col := map[string]int{}
	for i, name := range CSVColumns {
		col[name] = i
	}
	// Host rows are in numeric IP order: .1, .10, .50, .200, 10.5.
	printer := recs[2]
	if printer[col["ip"]] != "192.168.2.10" {
		t.Fatalf("row 2 ip = %q, want 192.168.2.10", printer[col["ip"]])
	}
	if got := printer[col["supplies"]]; got != `Black Toner=15%; Drum, "Long Life"=unknown` {
		t.Errorf("supplies = %q", got)
	}
	if got := printer[col["printer_status"]]; got != "idle" {
		t.Errorf("printer_status = %q", got)
	}
	if got := printer[col["page_count"]]; got != "12345" {
		t.Errorf("page_count = %q", got)
	}
	if got := printer[col["rtt_ms"]]; got != "3.2" {
		t.Errorf("rtt_ms = %q", got)
	}
	if got := printer[col["open_ports"]]; got != "80; 443; 9100" {
		t.Errorf("open_ports = %q", got)
	}
	if got := printer[col["web_titles"]]; got != "Brother HL-L2350DW; Brother HL-L2350DW" {
		t.Errorf("web_titles = %q", got)
	}
	if !strings.Contains(printer[col["model"]], "HL-L2350DW") {
		t.Errorf("model = %q", printer[col["model"]])
	}

	self := recs[3]
	if self[col["ip"]] != "192.168.2.50" || self[col["rtt_ms"]] != "" {
		t.Errorf("self row = %v; want empty rtt_ms for zero RTT", self)
	}
	last := recs[5]
	if last[col["ip"]] != "192.168.10.5" || last[col["alive"]] != "false" {
		t.Errorf("last row = %v", last)
	}
	ws := recs[4]
	if ws[col["web_urls"]] != "http://192.168.2.200:8080/" || ws[col["alive"]] != "true" {
		t.Errorf("web row = %v", ws)
	}
}

func TestEmptyResultCSVHeaderOnly(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteCSV(&buf, model.ScanResult{}); err != nil {
		t.Fatal(err)
	}
	want := strings.Join(CSVColumns, ",") + "\r\n"
	if buf.String() != want {
		t.Fatalf("empty CSV = %q, want %q", buf.String(), want)
	}
}

func TestJSONIsIndentedScanResult(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, fixture()); err != nil {
		t.Fatal(err)
	}
	var back model.ScanResult
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if back.ID != fixture().ID || len(back.Hosts) != 5 {
		t.Fatalf("round trip lost data: id=%q hosts=%d", back.ID, len(back.Hosts))
	}
	if !strings.Contains(buf.String(), "\n  \"id\"") {
		t.Error("JSON should be indented with two spaces")
	}
}

func TestSortHostsNumericAndStable(t *testing.T) {
	in := []model.Host{
		{IP: "10.0.0.2"},
		{IP: "not-an-ip"},
		{IP: "10.0.0.10"},
		{IP: "10.0.0.2", Vendor: "dup"},
		{IP: "9.255.255.255"},
	}
	orig := make([]model.Host, len(in))
	copy(orig, in)

	out := SortHosts(in)

	wantIPs := []string{"9.255.255.255", "10.0.0.2", "10.0.0.2", "10.0.0.10", "not-an-ip"}
	for i, h := range out {
		if h.IP != wantIPs[i] {
			t.Fatalf("out[%d].IP = %q, want %q", i, h.IP, wantIPs[i])
		}
	}
	if out[1].Vendor != "" || out[2].Vendor != "dup" {
		t.Errorf("duplicate IPs not kept in input order: %+v", out[1:3])
	}
	if !reflect.DeepEqual(in, orig) {
		t.Error("SortHosts modified its input slice")
	}
}

func TestWriteDoesNotMutateResult(t *testing.T) {
	res := fixture()
	first := res.Hosts[0].IP
	var buf bytes.Buffer
	if err := WriteCSV(&buf, res); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(&buf, res); err != nil {
		t.Fatal(err)
	}
	if res.Hosts[0].IP != first {
		t.Error("writers reordered the caller's Hosts slice")
	}
}
