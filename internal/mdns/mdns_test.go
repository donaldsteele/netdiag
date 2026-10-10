package mdns

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"netdiag/internal/model"
)

type pkt struct {
	b   []byte
	src *net.UDPAddr
}

// fakeTransport replays fixture packets in response to queries.
type fakeTransport struct {
	mu       sync.Mutex
	deadline time.Time
	writes   [][]byte
	dests    []net.Addr
	in       chan pkt
	closed   chan struct{}
	once     sync.Once
	respond  func(n int, q []byte) []pkt
}

func newFake(respond func(n int, q []byte) []pkt) *fakeTransport {
	return &fakeTransport{in: make(chan pkt, 64), closed: make(chan struct{}), respond: respond}
}

func (f *fakeTransport) WriteTo(p []byte, addr net.Addr) (int, error) {
	f.mu.Lock()
	f.writes = append(f.writes, append([]byte(nil), p...))
	f.dests = append(f.dests, addr)
	n := len(f.writes)
	f.mu.Unlock()
	if f.respond != nil {
		for _, r := range f.respond(n, p) {
			f.in <- r
		}
	}
	return len(p), nil
}

func (f *fakeTransport) ReadFrom(p []byte) (int, net.Addr, error) {
	f.mu.Lock()
	dl := f.deadline
	f.mu.Unlock()
	timer := time.NewTimer(time.Until(dl))
	defer timer.Stop()
	select {
	case r := <-f.in:
		return copy(p, r.b), r.src, nil
	case <-f.closed:
		return 0, nil, net.ErrClosed
	case <-timer.C:
		return 0, nil, os.ErrDeadlineExceeded
	}
}

func (f *fakeTransport) SetReadDeadline(t time.Time) error {
	f.mu.Lock()
	f.deadline = t
	f.mu.Unlock()
	return nil
}

func (f *fakeTransport) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

func src(a, b, c, d byte) *net.UDPAddr {
	return &net.UDPAddr{IP: net.IPv4(a, b, c, d), Port: 5353}
}

func questions(t *testing.T, q []byte) []dnsmessage.Question {
	t.Helper()
	var p dnsmessage.Parser
	if _, err := p.Start(q); err != nil {
		t.Fatal(err)
	}
	qs, err := p.AllQuestions()
	if err != nil {
		t.Fatal(err)
	}
	return qs
}

func fixtureResponder(t *testing.T) func(int, []byte) []pkt {
	return func(n int, q []byte) []pkt {
		var out []pkt
		if n == 1 {
			out = append(out,
				pkt{readHex(t, "brother_ipp.hex"), src(192, 168, 1, 50)},
				pkt{readHex(t, "nas_http.hex"), src(192, 168, 1, 20)},
				pkt{readHex(t, "cast_ptr.hex"), src(192, 168, 1, 77)},
				pkt{[]byte{0xde, 0xad}, src(192, 168, 1, 99)}, // garbage is ignored
			)
		}
		for _, qq := range questions(t, q) {
			if qq.Type == dnsmessage.TypeA && qq.Name.String() == "nas.local." {
				out = append(out, pkt{readHex(t, "nas_a.hex"), src(192, 168, 1, 20)})
			}
		}
		return out
	}
}

func TestParseFixture(t *testing.T) {
	a, err := Parse(readHex(t, "brother_ipp.hex"))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.PTR) != 2 || len(a.SRV) != 2 || len(a.TXT) != 1 || len(a.A) != 1 {
		t.Fatalf("counts: %+v", a)
	}
	if a.PTR[0].Instance != "Brother HL-L2350DW series._ipp._tcp.local." {
		t.Errorf("ptr: %q", a.PTR[0].Instance)
	}
	kv := a.TXT[0].KV
	if kv["ty"] != "Brother HL-L2350DW series" || kv["color"] != "F" || kv["note"] != "" || kv["rp"] != "ipp/print" {
		t.Errorf("txt: %v", kv)
	}
	if a.A[0].IP.String() != "192.168.1.50" || a.SRV[0].Port != 631 {
		t.Errorf("a/srv: %+v %+v", a.A, a.SRV)
	}
}

func TestParseMalformed(t *testing.T) {
	full := readHex(t, "brother_ipp.hex")
	for i := 0; i < len(full); i++ {
		a, _ := Parse(full[:i]) // must not panic
		if a == nil {
			t.Fatal("nil answers")
		}
	}
	q, _ := buildQuery(1, []string{"_ipp._tcp.local."}, nil, nil, false)
	if _, err := Parse(q); !errors.Is(err, ErrNotResponse) {
		t.Errorf("query parsed as response: %v", err)
	}
}

func TestBrowseFixtures(t *testing.T) {
	f := newFake(fixtureResponder(t))
	b := Browser{Dial: func(string) (Transport, error) { return f, nil }}
	recs, err := b.Browse(context.Background(), "eth0", nil, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	want := []Record{
		{IP: "192.168.1.20", Info: model.MDNSInfo{Instance: "Office NAS", Hostname: "nas.local", Services: []string{"_http._tcp"}}},
		{IP: "192.168.1.50", Info: model.MDNSInfo{Instance: "Brother HL-L2350DW series", Hostname: "BRN3C2AF4.local",
			Services: []string{"_ipp._tcp", "_pdl-datastream._tcp"}}},
		{IP: "192.168.1.77", Info: model.MDNSInfo{Instance: "Living Room TV", Services: []string{"_googlecast._tcp"}}},
	}
	if len(recs) != len(want) {
		t.Fatalf("got %d records: %+v", len(recs), recs)
	}
	for i := range want {
		got := recs[i]
		got.Info.TXT = nil
		if !reflect.DeepEqual(got, want[i]) {
			t.Errorf("record %d:\n got %+v\nwant %+v", i, got, want[i])
		}
	}
	if recs[1].Info.TXT["ty"] != "Brother HL-L2350DW series" || recs[1].Info.TXT["adminurl"] == "" {
		t.Errorf("printer txt: %v", recs[1].Info.TXT)
	}
	if PrinterService(recs[1].Info.Services) != "_ipp._tcp" || PrinterService(recs[0].Info.Services) != "" {
		t.Error("PrinterService")
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 2 {
		t.Fatalf("rounds = %d, want 2", len(f.writes))
	}
	for _, d := range f.dests {
		if d.String() != "224.0.0.251:5353" {
			t.Errorf("dest %v", d)
		}
	}
	q1 := questions(t, f.writes[0])
	if len(q1) != len(DefaultServices) {
		t.Errorf("round1 questions %d", len(q1))
	}
	for _, q := range q1 {
		if q.Class&qu == 0 || q.Type != dnsmessage.TypePTR {
			t.Errorf("round1 question not QU PTR: %v", q)
		}
	}
	var qm, aNas int
	for _, q := range questions(t, f.writes[1]) {
		if q.Type == dnsmessage.TypePTR && q.Class&qu == 0 {
			qm++
		}
		if q.Type == dnsmessage.TypeA && q.Name.String() == "nas.local." {
			aNas++
		}
	}
	if qm != len(DefaultServices) || aNas != 1 {
		t.Errorf("round2: qm=%d aNas=%d", qm, aNas)
	}
}

func TestBrowseOneRoundAndServices(t *testing.T) {
	f := newFake(fixtureResponder(t))
	b := Browser{Dial: func(string) (Transport, error) { return f, nil }, Rounds: 1}
	recs, err := b.Browse(context.Background(), "", []string{"_ipp._tcp.local.", "_http._tcp"}, 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	n := len(f.writes)
	q := questions(t, f.writes[0])
	f.mu.Unlock()
	if n != 1 || len(q) != 2 || q[0].Name.String() != "_ipp._tcp.local." {
		t.Fatalf("writes=%d questions=%v", n, q)
	}
	// Without the round-2 A follow-up the NAS falls back to its source IP.
	if len(recs) != 3 || recs[0].IP != "192.168.1.20" || recs[0].Info.Hostname != "nas.local" {
		t.Errorf("recs: %+v", recs)
	}
}

func TestBrowseNoResponses(t *testing.T) {
	f := newFake(nil)
	b := Browser{Dial: func(string) (Transport, error) { return f, nil }}
	recs, err := b.Browse(context.Background(), "", nil, 100*time.Millisecond)
	if err != nil || len(recs) != 0 {
		t.Fatalf("recs=%v err=%v", recs, err)
	}
}

func TestBrowseCancel(t *testing.T) {
	f := newFake(nil)
	b := Browser{Dial: func(string) (Transport, error) { return f, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	if _, err := b.Browse(ctx, "", nil, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("cancel took %v", d)
	}
	select {
	case <-f.closed:
	default:
		t.Error("transport not closed")
	}
}

func TestBrowseDialError(t *testing.T) {
	b := Browser{Dial: func(string) (Transport, error) { return nil, errors.New("boom") }}
	if _, err := b.Browse(context.Background(), "", nil, time.Second); err == nil {
		t.Fatal("want error")
	}
}

func TestSplitInstance(t *testing.T) {
	cases := []struct {
		in, label, svc string
		ok             bool
	}{
		{"Brother HL._ipp._tcp.local.", "Brother HL", "_ipp._tcp", true},
		{"My.Dotted.Name._HTTP._TCP.local", "My.Dotted.Name", "_http._tcp", true},
		{"x._sleep-proxy._udp.local.", "x", "_sleep-proxy._udp", true},
		{"_ipp._tcp.local.", "", "", false},
		{"host.local.", "", "", false},
		{"a._ipp._tcp.example.com.", "", "", false},
	}
	for _, c := range cases {
		l, s, ok := splitInstance(c.in)
		if l != c.label || s != c.svc || ok != c.ok {
			t.Errorf("%q: got %q %q %v", c.in, l, s, ok)
		}
	}
	if !IsPrinterService("_IPPS._tcp.local.") || IsPrinterService("_http._tcp") {
		t.Error("IsPrinterService")
	}
}

func TestDialUDPEphemeral(t *testing.T) {
	tr, err := DialUDP("")
	if err != nil {
		t.Skipf("udp unavailable: %v", err)
	}
	defer tr.Close()
	c, ok := tr.(*net.UDPConn)
	if !ok {
		t.Fatalf("type %T", tr)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	if port == 0 || port == 5353 {
		t.Errorf("port %d", port)
	}
	if _, err := DialUDP("no-such-interface-xyz"); err == nil {
		t.Error("want error for unknown interface")
	}
}

func FuzzParse(f *testing.F) {
	matches, _ := filepath.Glob(filepath.Join(fixtureDir, "*.hex"))
	for _, m := range matches {
		f.Add(readHex(f, filepath.Base(m)))
	}
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0x84, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0xc0, 0x0c})
	f.Fuzz(func(t *testing.T, b []byte) {
		a, _ := Parse(b)
		c := newCollector()
		c.add(a, netipFrom(192, 168, 1, 1))
		c.pending(maxFollowUps)
		_ = c.records()
	})
}

func netipFrom(a, b, c, d byte) netip.Addr { return netip.AddrFrom4([4]byte{a, b, c, d}) }
