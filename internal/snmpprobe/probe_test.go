package snmpprobe

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netdiag/internal/model"
)

type fixture struct {
	Name     string `json:"name"`
	V2cFails bool   `json:"v2cFails"`
	Timeout  bool   `json:"timeout"`
	Varbinds []fvb  `json:"varbinds"`
}

type fvb struct {
	OID   string          `json:"oid"`
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

func (f fvb) pdu(t *testing.T) PDU {
	p := PDU{OID: f.OID}
	switch f.Type {
	case "int", "ticks":
		p.Kind = KindInt
		if f.Type == "ticks" {
			p.Kind = KindTicks
		}
		if err := json.Unmarshal(f.Value, &p.Int); err != nil {
			t.Fatal(err)
		}
	case "oid":
		p.Kind = KindOID
		_ = json.Unmarshal(f.Value, &p.Str)
	default:
		p.Kind = KindOctet
		var s string
		_ = json.Unmarshal(f.Value, &s)
		if h, ok := strings.CutPrefix(s, "hex:"); ok {
			b, err := hex.DecodeString(h)
			if err != nil {
				t.Fatal(err)
			}
			p.Bytes = b
		} else {
			p.Bytes = []byte(s)
		}
	}
	return p
}

type fakeClient struct {
	pdus    []PDU
	version string
	fx      *fixture
}

func (c *fakeClient) Get(ctx context.Context, oids []string) ([]PDU, error) {
	if c.fx.Timeout {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if c.fx.V2cFails && c.version == "v2c" {
		return nil, errors.New("timeout")
	}
	var out []PDU
	for _, o := range oids {
		found := false
		for _, p := range c.pdus {
			if p.OID == o {
				out = append(out, p)
				found = true
			}
		}
		if !found {
			out = append(out, PDU{OID: o, Kind: KindNull})
		}
	}
	return out, nil
}

func (c *fakeClient) Walk(ctx context.Context, root string) ([]PDU, error) {
	var out []PDU
	for _, p := range c.pdus {
		if strings.HasPrefix(p.OID, root+".") {
			out = append(out, p)
		}
	}
	return out, nil
}

func (c *fakeClient) Close() error { return nil }

func load(t *testing.T, name string) (Dialer, *[]string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "snmp", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx fixture
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	var pdus []PDU
	for _, v := range fx.Varbinds {
		pdus = append(pdus, v.pdu(t))
	}
	var versions []string
	return func(ctx context.Context, ip, community, version string) (Client, error) {
		versions = append(versions, version)
		return &fakeClient{pdus: pdus, version: version, fx: &fx}, nil
	}, &versions
}

func TestHP(t *testing.T) {
	d, vers := load(t, "hp")
	r, err := Probe(context.Background(), d, "10.0.0.5", "public")
	if err != nil {
		t.Fatal(err)
	}
	if len(*vers) != 1 || r.Snmp.Version != "v2c" {
		t.Fatalf("versions %v / %s", *vers, r.Snmp.Version)
	}
	if r.Snmp.SysUpTime != "4d 0h" || r.Snmp.HrDeviceStatus != "running" || r.Snmp.HrPrinterStatus != "idle" {
		t.Fatalf("snmp %+v", r.Snmp)
	}
	if got := r.Snmp.HrPrinterDetectedErrorState; len(got) != 1 || got[0] != "lowToner" {
		t.Fatalf("errstate %v", got)
	}
	p := r.Printer
	if p == nil || p.Serial != "VNB3K12345" || p.PageCount != 12345 || p.Model != "HP Color LaserJet Pro M454dw" {
		t.Fatalf("printer %+v", p)
	}
	if len(p.Supplies) != 4 {
		t.Fatalf("supplies %+v", p.Supplies)
	}
	want := []struct {
		color, state string
		pct          int
	}{{"black", "ok", 45}, {"cyan", "ok", -1}, {"magenta", "empty", 0}, {"yellow", "unknown", -1}}
	for i, w := range want {
		s := p.Supplies[i]
		if s.Color != w.color || s.State != w.state || s.Percent != w.pct {
			t.Errorf("supply %d: %+v", i, s)
		}
	}
	if len(p.InputTrays) != 1 || p.InputTrays[0].Name != "Tray 1" || p.InputTrays[0].Level != 100 {
		t.Errorf("trays %+v", p.InputTrays)
	}
	if len(p.Covers) != 1 || p.Covers[0].Status != "closed" {
		t.Errorf("covers %+v", p.Covers)
	}
	if len(p.Alerts) != 1 || p.Alerts[0].Severity != "warning" || p.Alerts[0].Code != "1101" {
		t.Errorf("alerts %+v", p.Alerts)
	}
	if len(p.ConsoleDisplay) != 1 || p.ConsoleDisplay[0] != "READY" {
		t.Errorf("console %v", p.ConsoleDisplay)
	}
}

func TestBrotherV1FallbackAndBinary(t *testing.T) {
	d, vers := load(t, "brother")
	r, err := Probe(context.Background(), d, "10.0.0.6", "public")
	if err != nil {
		t.Fatal(err)
	}
	if len(*vers) != 2 || r.Snmp.Version != "v1" {
		t.Fatalf("versions %v / %s", *vers, r.Snmp.Version)
	}
	if r.Snmp.SysLocation != "fffe01" {
		t.Errorf("binary location = %q", r.Snmp.SysLocation)
	}
	if r.Snmp.HrPrinterStatus != "printing" {
		t.Errorf("status %q", r.Snmp.HrPrinterStatus)
	}
	s := r.Printer.Supplies
	if len(s) != 2 || s[0].State != "low" || s[0].Percent != 12 || s[0].Color != "black" || s[1].Type != "fuser" {
		t.Errorf("supplies %+v", s)
	}
	if len(r.Printer.ConsoleDisplay) != 1 || r.Printer.ConsoleDisplay[0] != "PRINTING" {
		t.Errorf("console %v", r.Printer.ConsoleDisplay)
	}
}

func TestEpsonPrinterMIBOnly(t *testing.T) {
	d, _ := load(t, "epson")
	r, err := Probe(context.Background(), d, "10.0.0.7", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Printer == nil || r.Printer.Serial != "X4AB123456" {
		t.Fatalf("printer %+v", r.Printer)
	}
	s := r.Printer.Supplies
	if len(s) != 2 || s[0].Percent != 60 || s[0].Color != "black" || s[1].State != "low" || s[1].Color != "cyan" {
		t.Errorf("supplies %+v", s)
	}
	h := &model.Host{Snmp: r.Snmp, Printer: r.Printer}
	if ok, why := Classify(h); !ok || !strings.Contains(why, "Printer-MIB") {
		t.Errorf("classify %v %q", ok, why)
	}
}

func TestRouterNotPrinter(t *testing.T) {
	d, _ := load(t, "router")
	r, err := Probe(context.Background(), d, "10.0.0.1", "public")
	if err != nil {
		t.Fatal(err)
	}
	if r.Printer != nil || r.Snmp.HrPrinterStatus != "" || r.Snmp.SysName != "gw" {
		t.Fatalf("%+v %+v", r.Snmp, r.Printer)
	}
	h := &model.Host{Snmp: r.Snmp}
	Apply(h)
	if h.IsPrinter {
		t.Error("router flagged as printer")
	}
}

func TestTimeoutBoundedByDeadline(t *testing.T) {
	d, _ := load(t, "router")
	fxDial := func(ctx context.Context, ip, c, v string) (Client, error) {
		return &fakeClient{fx: &fixture{Timeout: true}, version: v}, nil
	}
	_ = d
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Probe(ctx, fxDial, "10.0.0.9", "public")
	if !errors.Is(err, ErrNoResponse) {
		t.Fatalf("err %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("did not honor deadline")
	}
}

func TestDialError(t *testing.T) {
	d := func(ctx context.Context, ip, c, v string) (Client, error) { return nil, errors.New("boom") }
	if _, err := Probe(context.Background(), d, "x", "public"); !errors.Is(err, ErrNoResponse) {
		t.Fatalf("err %v", err)
	}
}

func TestErrorStatesBitmask(t *testing.T) {
	got := errorStates([]byte{0xA0, 0x02}) // bits 0 (lowPaper), 2 (lowToner), 14 (overduePreventMaintenance)
	if strings.Join(got, ",") != "lowPaper,lowToner,overduePreventMaintenance" {
		t.Errorf("got %v", got)
	}
	if len(errorStates(nil)) != 0 {
		t.Error("nil should be empty")
	}
}

// Zebra ZD621 with the head open and paused: empty prtAlertTable, status only
// in HOST-RESOURCES.
func TestStatusAlerts(t *testing.T) {
	got := statusAlerts(&model.SnmpInfo{HrDeviceStatus: "down", HrPrinterDetectedErrorState: []string{"doorOpen", "offline", "lowPaper"}})
	want := []model.Alert{
		{Severity: "critical", Code: "hrPrinterDetectedErrorState.doorOpen", Description: "Door or cover open"},
		{Severity: "critical", Code: "hrPrinterDetectedErrorState.offline", Description: "Printer offline or paused"},
		{Severity: "warning", Code: "hrPrinterDetectedErrorState.lowPaper", Description: "Paper low"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("alert %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if a := statusAlerts(&model.SnmpInfo{HrDeviceStatus: "down"}); len(a) != 1 || a[0].Severity != "critical" {
		t.Errorf("down without error bits: %+v", a)
	}
	if a := statusAlerts(&model.SnmpInfo{HrDeviceStatus: "warning"}); len(a) != 1 || a[0].Severity != "warning" {
		t.Errorf("warning without error bits: %+v", a)
	}
	if a := statusAlerts(&model.SnmpInfo{HrDeviceStatus: "running"}); len(a) != 0 {
		t.Errorf("healthy printer: %+v", a)
	}
}

func TestSupplyPercent(t *testing.T) {
	cases := []struct {
		l, m  int64
		p     int
		state string
	}{{-2, 100, -1, "unknown"}, {-3, 100, -1, "ok"}, {0, 100, 0, "empty"}, {15, 100, 15, "low"}, {450, 1000, 45, "ok"}, {50, -2, -1, "unknown"}, {200, 100, 100, "ok"}}
	for _, c := range cases {
		p, s := supplyPercent(c.l, c.m)
		if p != c.p || s != c.state {
			t.Errorf("(%d,%d) = %d,%s", c.l, c.m, p, s)
		}
	}
}

func TestTextHexFallback(t *testing.T) {
	if got := text(PDU{Kind: KindOctet, Bytes: []byte{0xff, 0xfe}}); got != "fffe" {
		t.Error(got)
	}
	if got := text(PDU{Kind: KindOctet, Bytes: []byte("abc\x00\x00")}); got != "abc" {
		t.Error(got)
	}
}

func TestClassifyPorts(t *testing.T) {
	cases := []struct {
		ports []int
		ok    bool
		sub   string
	}{{[]int{80, 9100}, true, "9100"}, {[]int{631}, true, "631"}, {[]int{515}, true, "515"}, {[]int{80, 443}, false, ""}}
	for _, c := range cases {
		h := &model.Host{OpenPorts: c.ports}
		Apply(h)
		if h.IsPrinter != c.ok || !strings.Contains(h.PrinterReason, c.sub) {
			t.Errorf("%v -> %v %q", c.ports, h.IsPrinter, h.PrinterReason)
		}
	}
	h := &model.Host{Snmp: &model.SnmpInfo{HrPrinterStatus: "idle"}, OpenPorts: []int{9100}}
	if _, why := Classify(h); !strings.Contains(why, "hrDeviceType") {
		t.Errorf("first rule should win: %q", why)
	}
}

func TestPollStatusMatchesProbe(t *testing.T) {
	d, _ := load(t, "hp")
	r, err := Probe(context.Background(), d, "10.0.0.5", "public")
	if err != nil || r.Status == nil {
		t.Fatalf("probe: %v, status target %+v", err, r)
	}
	s, err := PollStatus(context.Background(), d, "10.0.0.5", "public", *r.Status)
	if err != nil {
		t.Fatal(err)
	}
	want := StatusOf(r.Snmp)
	if s.Key() != want.Key() || s.PrinterStatus != want.PrinterStatus || s.DeviceStatus != "running" {
		t.Fatalf("poll %+v, probe %+v", s, want)
	}
	bad := *r.Status
	bad.PrtIdx = "999"
	if _, err := PollStatus(context.Background(), d, "10.0.0.5", "public", bad); err == nil {
		t.Fatal("unknown row must fail so the caller re-probes")
	}
}

func TestStatusKeyIgnoresPrinterStatus(t *testing.T) {
	a := Status{DeviceStatus: "running", PrinterStatus: "idle", ErrorState: []string{"offline", "doorOpen"}}
	b := Status{DeviceStatus: "running", PrinterStatus: "printing", ErrorState: []string{"doorOpen", "offline"}}
	if a.Key() != b.Key() {
		t.Fatalf("%q != %q", a.Key(), b.Key())
	}
	if a.Key() == (Status{DeviceStatus: "running"}).Key() {
		t.Fatal("error bits must change the key")
	}
}
