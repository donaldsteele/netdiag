package ipp

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"netdiag/internal/model"
)

var update = flag.Bool("update", false, "regenerate testdata/ipp fixtures")

const fixtureDir = "../../testdata/ipp"

func kw(ss ...string) []Value {
	out := make([]Value, len(ss))
	for i, s := range ss {
		out[i] = Value{Tag: TagKeyword, Str: s}
	}
	return out
}

func ints(tag byte, ns ...int32) []Value {
	out := make([]Value, len(ns))
	for i, n := range ns {
		out[i] = Value{Tag: tag, Int: n}
	}
	return out
}

func rawAttr(b []byte, tag byte, name string, val []byte) []byte {
	b = append(b, tag)
	b = binary.BigEndian.AppendUint16(b, uint16(len(name)))
	b = append(b, name...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(val)))
	return append(b, val...)
}

func opGroup() Group {
	return Group{Tag: TagOperation, Attrs: []Attribute{
		{Name: "attributes-charset", Values: []Value{{Tag: TagCharset, Str: "utf-8"}}},
		{Name: "attributes-natural-language", Values: []Value{{Tag: TagLanguage, Str: "en-us"}}},
	}}
}

// hpResponse is a real-shaped reply from a color laser that is stopped
// because a tray is empty, with one cyan cartridge low.
func hpResponse() []byte {
	m := &Message{Major: 1, Minor: 1, Code: 0x0000, RequestID: 1, Groups: []Group{
		opGroup(),
		{Tag: TagUnsupported, Attrs: []Attribute{
			{Name: "printer-impressions-completed", Values: []Value{{Tag: TagUnsupportedValue}}},
		}},
		{Tag: TagPrinter, Attrs: []Attribute{
			{Name: "printer-name", Values: []Value{{Tag: TagName, Str: "HP_Color_LaserJet_MFP_M479fdw"}}},
			{Name: "printer-info", Values: []Value{{Tag: TagTextWithLang, Str: "HP Color LaserJet MFP M479fdw"}}},
			{Name: "printer-location", Values: []Value{{Tag: TagText, Str: "Registration desk"}}},
			{Name: "printer-make-and-model", Values: []Value{{Tag: TagText, Str: "HP Color LaserJet MFP M479fdw"}}},
			{Name: "printer-device-id", Values: []Value{{Tag: TagText, Str: "MFG:HP;MDL:Color LaserJet MFP M479fdw;CMD:PJL,PCL,PDF,PWG;SN:CNB1234567;"}}},
			{Name: "printer-state", Values: ints(TagEnum, 5)},
			{Name: "printer-state-reasons", Values: kw("media-empty-error", "marker-supply-low-warning", "door-open", "cleaning-report")},
			{Name: "printer-state-message", Values: []Value{{Tag: TagText, Str: "Load paper in Tray 2"}}},
			{Name: "marker-names", Values: []Value{{Tag: TagName, Str: "Black Cartridge HP 415A"}, {Tag: TagName, Str: "Cyan Cartridge HP 415A"},
				{Tag: TagName, Str: "Magenta Cartridge HP 415A"}, {Tag: TagName, Str: "Yellow Cartridge HP 415A"}, {Tag: TagName, Str: "Imaging Drum"}}},
			{Name: "marker-levels", Values: ints(TagInteger, 80, 10, 0, -3, -2)},
			{Name: "marker-low-levels", Values: ints(TagInteger, 12, 12, 12, 12, 12)},
			{Name: "marker-high-levels", Values: ints(TagInteger, 100, 100, 100, 100, 100)},
			{Name: "marker-colors", Values: kw("#000000", "#00FFFF", "#FF00FF", "#FFFF00", "none")},
			{Name: "marker-types", Values: kw("toner-cartridge", "toner-cartridge", "toner-cartridge", "toner-cartridge", "opc")},
			{Name: "printer-input-tray", Values: []Value{
				{Tag: TagOctetString, Str: "type=sheetFeedManual;mediafeed=0;mediaxfeed=0;maxcapacity=100;level=-2;status=0;name=Tray 1;"},
				{Tag: TagOctetString, Str: "type=sheetFeedAutoRemovableTray;mediafeed=0;mediaxfeed=0;maxcapacity=250;level=0;status=19;name=Tray 2;"},
			}},
		}},
	}}
	b := Encode(m)
	b = b[:len(b)-1] // reopen the printer group to append a collection
	b = rawAttr(b, TagBeginCollection, "media-col-default", nil)
	b = rawAttr(b, TagMemberName, "", []byte("media-size"))
	b = rawAttr(b, TagBeginCollection, "", nil)
	b = rawAttr(b, TagMemberName, "", []byte("x-dimension"))
	b = rawAttr(b, TagInteger, "", []byte{0, 0, 0x52, 0x08})
	b = rawAttr(b, TagEndCollection, "", nil)
	b = rawAttr(b, TagEndCollection, "", nil)
	b = rawAttr(b, TagURI, "printer-uuid", []byte("urn:uuid:564e4333-4230-4e38-3532-40b034f1d0e1"))
	return append(b, TagEnd)
}

func notFoundResponse() []byte {
	return Encode(&Message{Major: 1, Minor: 1, Code: StatusNotFound, RequestID: 1, Groups: []Group{opGroup()}})
}

var fixtures = map[string]func() []byte{
	"hp_color_stopped.hex": hpResponse,
	"not_found.hex":        notFoundResponse,
}

func TestUpdateFixtures(t *testing.T) {
	if !*update {
		t.Skip("run with -update to regenerate")
	}
	for name, build := range fixtures {
		h := hex.EncodeToString(build())
		var sb strings.Builder
		for i := 0; i < len(h); i += 64 {
			sb.WriteString(h[i:min(i+64, len(h))])
			sb.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(fixtureDir, name), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readHex(tb testing.TB, name string) []byte {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		tb.Fatal(err)
	}
	b, err := hex.DecodeString(strings.Join(strings.Fields(string(raw)), ""))
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

func TestDecodeFixture(t *testing.T) {
	m, err := Decode(readHex(t, "hp_color_stopped.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Code != 0 || m.RequestID != 1 || len(m.Groups) != 3 {
		t.Fatalf("header/groups: %+v", m)
	}
	if a := m.Attr(TagPrinter, "marker-names"); a == nil || len(a.Values) != 5 {
		t.Fatalf("marker-names: %+v", a)
	}
	if a := m.Attr(TagPrinter, "media-col-default"); a == nil || len(a.Values) != 0 {
		t.Errorf("collection: %+v", a)
	}
	if a := m.Attr(TagPrinter, "printer-uuid"); a == nil || !strings.HasPrefix(a.Values[0].Str, "urn:uuid:") {
		t.Errorf("attribute after collection lost: %+v", a)
	}
	if a := m.Attr(TagPrinter, "printer-info"); a == nil || a.Values[0].Str != "HP Color LaserJet MFP M479fdw" {
		t.Errorf("textWithLanguage: %+v", a)
	}
}

func TestRequestRoundTrip(t *testing.T) {
	req := NewRequest("ipp://10.0.0.5:631/ipp/print", 7)
	got, err := Decode(Encode(req))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, req) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, req)
	}
}

func TestDecodeMalformed(t *testing.T) {
	full := readHex(t, "hp_color_stopped.hex")
	for i := 0; i < len(full); i++ {
		m, err := Decode(full[:i])
		if m == nil || err == nil {
			t.Fatalf("prefix %d: m=%v err=%v", i, m, err)
		}
		Map(m) // must not panic
	}
	bad := [][]byte{
		{1, 1, 0, 0, 0, 0, 0, 1, 0x21, 0, 1, 'x', 0, 0, 3},          // value before group
		{1, 1, 0, 0, 0, 0, 0, 1, 4, 0x21, 0, 1, 'x', 0, 2, 0, 0, 3}, // bad int length
		{1, 1, 0, 0, 0, 0, 0, 1, 4, 0x37, 0, 0, 0, 0, 3},            // unbalanced end
		{1, 1, 0, 0, 0, 0, 0, 1, 4, 0x44, 0, 0, 0, 1, 'x', 3},       // extra value w/o attr
	}
	for i, b := range bad {
		if _, err := Decode(b); err == nil {
			t.Errorf("case %d: want error", i)
		}
	}
}

func TestMap(t *testing.T) {
	m, _ := Decode(readHex(t, "hp_color_stopped.hex"))
	r := Map(m)
	p := r.Printer
	if r.Source != "ipp" || r.State != "stopped" || r.Name != "HP_Color_LaserJet_MFP_M479fdw" || r.Location != "Registration desk" {
		t.Errorf("result: %+v", r)
	}
	if !reflect.DeepEqual(r.StateReasons, []string{"media-empty-error", "marker-supply-low-warning", "door-open", "cleaning-report"}) {
		t.Errorf("reasons: %v", r.StateReasons)
	}
	if p.Model != "HP Color LaserJet MFP M479fdw" || p.Serial != "CNB1234567" || p.PageCount != 0 {
		t.Errorf("model/serial/pages: %q %q %d", p.Model, p.Serial, p.PageCount)
	}
	if !reflect.DeepEqual(p.ConsoleDisplay, []string{"Load paper in Tray 2"}) {
		t.Errorf("console: %v", p.ConsoleDisplay)
	}
	wantSup := []model.Supply{
		{Description: "Black Cartridge HP 415A", Type: "tonerCartridge", Color: "black", Level: 80, Max: 100, Percent: 80, Unit: "percent", State: "ok"},
		{Description: "Cyan Cartridge HP 415A", Type: "tonerCartridge", Color: "cyan", Level: 10, Max: 100, Percent: 10, Unit: "percent", State: "low"},
		{Description: "Magenta Cartridge HP 415A", Type: "tonerCartridge", Color: "magenta", Level: 0, Max: 100, Percent: 0, Unit: "percent", State: "empty"},
		{Description: "Yellow Cartridge HP 415A", Type: "tonerCartridge", Color: "yellow", Level: -3, Max: 100, Percent: -1, Unit: "percent", State: "ok"},
		{Description: "Imaging Drum", Type: "opc", Color: "", Level: -2, Max: 100, Percent: -1, Unit: "percent", State: "unknown"},
	}
	if !reflect.DeepEqual(p.Supplies, wantSup) {
		t.Errorf("supplies:\n got %+v\nwant %+v", p.Supplies, wantSup)
	}
	wantAlerts := []model.Alert{
		{Severity: "critical", Code: "media-empty-error", Description: "Media empty"},
		{Severity: "warning", Code: "marker-supply-low-warning", Description: "Marker supply low"},
		{Severity: "critical", Code: "door-open", Description: "Door open"},
		{Severity: "other", Code: "cleaning-report", Description: "Cleaning"},
	}
	if !reflect.DeepEqual(p.Alerts, wantAlerts) {
		t.Errorf("alerts: %+v", p.Alerts)
	}
	if !reflect.DeepEqual(p.Covers, []model.Cover{{Description: "Door", Status: "open"}}) {
		t.Errorf("covers: %+v", p.Covers)
	}
	wantTrays := []model.InputTray{
		{Name: "Tray 1", Level: -2, Max: 100, Status: "ok"},
		{Name: "Tray 2", Level: 0, Max: 250, Status: "status 19"},
	}
	if !reflect.DeepEqual(p.InputTrays, wantTrays) {
		t.Errorf("trays: %+v", p.InputTrays)
	}
}

func TestMapEmpty(t *testing.T) {
	r := Map(&Message{})
	if r.Printer == nil || r.Printer.Supplies == nil || r.Printer.Alerts == nil || r.State != "" || r.StateReasons == nil {
		t.Fatalf("empty map: %+v", r)
	}
	// Model falls back to the IEEE 1284 device ID.
	m := &Message{Groups: []Group{{Tag: TagPrinter, Attrs: []Attribute{
		{Name: "printer-device-id", Values: []Value{{Tag: TagText, Str: "MANUFACTURER:Brother;MODEL:HL-L2350DW;"}}},
		{Name: "printer-state-reasons", Values: kw("none")},
		{Name: "printer-state", Values: ints(TagEnum, 4)},
	}}}}
	r = Map(m)
	if r.Printer.Model != "Brother HL-L2350DW" || len(r.StateReasons) != 0 || r.State != "processing" {
		t.Errorf("fallback: %+v %+v", r, r.Printer)
	}
}

func serve(t *testing.T, h http.HandlerFunc) (ip string, port int) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	host, p, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ = strconv.Atoi(p)
	return host, port
}

func TestGetPrinterAttributes(t *testing.T) {
	fixture := readHex(t, "hp_color_stopped.hex")
	var hits atomic.Int32
	ip, port := serve(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/ipp/print" || r.Header.Get("Content-Type") != "application/ipp" {
			t.Errorf("request: %s %s %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		req, err := Decode(body)
		if err != nil || req.Code != OpGetPrinterAttributes {
			t.Errorf("decode request: %v %+v", err, req)
		}
		if a := req.Attr(TagOperation, "printer-uri"); a == nil || !strings.HasPrefix(a.Values[0].Str, "ipp://") {
			t.Errorf("printer-uri: %+v", a)
		}
		if a := req.Attr(TagOperation, "requested-attributes"); a == nil || len(a.Values) != len(RequestedAttributes) {
			t.Errorf("requested-attributes: %+v", a)
		}
		w.Header().Set("Content-Type", "application/ipp")
		_, _ = w.Write(fixture)
	})
	r, err := GetPrinterAttributes(context.Background(), ip, port)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "stopped" || len(r.Printer.Supplies) != 5 || len(r.Errors) != 0 || hits.Load() != 1 {
		t.Errorf("result: %+v hits=%d", r, hits.Load())
	}
}

func TestPathFallback(t *testing.T) {
	fixture := readHex(t, "hp_color_stopped.hex")
	var mu sync.Mutex
	var paths []string
	ip, port := serve(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/ipp/print" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(fixture)
	})
	if _, err := GetPrinterAttributes(context.Background(), ip, port); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(paths, []string{"/ipp/print", "/"}) {
		t.Errorf("paths: %v", paths)
	}
}

func TestIPPErrors(t *testing.T) {
	nf := readHex(t, "not_found.hex")
	cases := []struct {
		name     string
		h        http.HandlerFunc
		wantHits int32
	}{
		{"ipp-not-found", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(nf) }, 2},
		{"ipp-server-error", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(Encode(&Message{Major: 1, Minor: 1, Code: 0x0500, RequestID: 1, Groups: []Group{opGroup()}}))
		}, 1},
		{"http-500", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }, 1},
		{"not-printer", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(Encode(&Message{Major: 1, Minor: 1, RequestID: 1, Groups: []Group{opGroup()}}))
		}, 1},
		{"garbage", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>hello</html>")) }, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var hits atomic.Int32
			ip, port := serve(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); c.h(w, r) })
			r, err := GetPrinterAttributes(context.Background(), ip, port)
			if err == nil || r != nil {
				t.Fatalf("want error, got %+v", r)
			}
			if hits.Load() != c.wantHits {
				t.Errorf("hits=%d want %d", hits.Load(), c.wantHits)
			}
		})
	}
}

func TestTimeout(t *testing.T) {
	release := make(chan struct{})
	ip, port := serve(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	t.Cleanup(func() { close(release) })
	c := Client{Timeout: 200 * time.Millisecond}
	start := time.Now()
	_, err := c.GetPrinterAttributes(context.Background(), ip, port)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestBodyCap(t *testing.T) {
	m := &Message{Major: 1, Minor: 1, RequestID: 1, Groups: []Group{opGroup(), {Tag: TagPrinter, Attrs: []Attribute{
		{Name: "printer-make-and-model", Values: []Value{{Tag: TagText, Str: "Big Printer"}}},
	}}}}
	pad := strings.Repeat("x", 1000)
	for i := 0; i < 80; i++ {
		m.Groups[1].Attrs = append(m.Groups[1].Attrs, Attribute{Name: "x-pad-" + strconv.Itoa(i), Values: []Value{{Tag: TagOctetString, Str: pad}}})
	}
	body := Encode(m)
	if len(body) <= MaxBody {
		t.Fatal("fixture too small")
	}
	ip, port := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.Copy(w, bytes.NewReader(body)) })
	r, err := GetPrinterAttributes(context.Background(), ip, port)
	if err != nil {
		t.Fatal(err)
	}
	if r.Printer.Model != "Big Printer" || len(r.Errors) == 0 || !strings.Contains(r.Errors[0], "64 KB") {
		t.Errorf("result: model=%q errors=%v", r.Printer.Model, r.Errors)
	}
}

func FuzzDecode(f *testing.F) {
	matches, _ := filepath.Glob(filepath.Join(fixtureDir, "*.hex"))
	for _, m := range matches {
		f.Add(readHex(f, filepath.Base(m)))
	}
	f.Add(Encode(NewRequest("ipp://127.0.0.1:631/ipp/print", 1)))
	f.Add([]byte{1, 1, 0, 0, 0, 0, 0, 1, 4, 0x7f, 0, 0, 0, 4, 0, 0, 0, 0, 3})
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := Decode(b)
		if m == nil {
			t.Fatal("nil message")
		}
		r := Map(m)
		if r == nil || r.Printer == nil {
			t.Fatal("nil result")
		}
		if err == nil {
			// A cleanly decoded message must survive a re-encode/decode.
			if _, err := Decode(Encode(m)); err != nil {
				t.Fatalf("re-decode: %v", err)
			}
		}
	})
}
