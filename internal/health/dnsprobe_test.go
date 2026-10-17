package health

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"netdiag/internal/model"
)

// fakeDNS is a UDP DNS server on 127.0.0.1. reply builds the answer to a
// query; returning nil sends nothing.
func fakeDNS(t *testing.T, reply func(q dnsmessage.Message) *dnsmessage.Message) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var q dnsmessage.Message
			if q.Unpack(buf[:n]) != nil {
				continue
			}
			if m := reply(q); m != nil {
				b, _ := m.Pack()
				pc.WriteTo(b, from)
			}
		}
	}()
	return pc.LocalAddr().String()
}

func rcodeReply(rc dnsmessage.RCode) func(dnsmessage.Message) *dnsmessage.Message {
	return func(q dnsmessage.Message) *dnsmessage.Message {
		return &dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID, Response: true, RCode: rc}, Questions: q.Questions}
	}
}

func testQuestions() []dnsmessage.Question {
	return []dnsmessage.Question{{Name: dnsmessage.MustNewName("example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}
}

func TestDNSRepliesAnyRcode(t *testing.T) {
	for rc, want := range map[dnsmessage.RCode]dnsResult{
		dnsmessage.RCodeSuccess:       dnsReplied, // no address in the answer
		dnsmessage.RCodeNameError:     dnsNoSuchName,
		dnsmessage.RCodeServerFailure: dnsReplied,
		dnsmessage.RCodeRefused:       dnsReplied,
	} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if got := dnsReplies(ctx, fakeDNS(t, rcodeReply(rc)), testQuestions()); got != want {
			t.Errorf("rcode %v: got %v, want %v", rc, got, want)
		}
		cancel()
	}
	// "No such name" for the gateway's reverse lookup only is a plain reply.
	ptr := []dnsmessage.Question{{Name: dnsmessage.MustNewName("1.1.168.192.in-addr.arpa."), Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got := dnsReplies(ctx, fakeDNS(t, rcodeReply(dnsmessage.RCodeNameError)), ptr); got != dnsReplied {
		t.Errorf("PTR no such name: got %v, want dnsReplied", got)
	}
}

// TestDNSRepliesResolved: an address for the A question means the name
// resolves after all, even if a PTR reply came first.
func TestDNSRepliesResolved(t *testing.T) {
	srv := fakeDNS(t, func(q dnsmessage.Message) *dnsmessage.Message {
		m := rcodeReply(dnsmessage.RCodeNameError)(q)
		if q.Questions[0].Type == dnsmessage.TypeA {
			m.Header.RCode = dnsmessage.RCodeSuccess
			m.Answers = []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{Name: q.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
				Body:   &dnsmessage.AResource{A: [4]byte{192, 0, 2, 7}},
			}}
		}
		return m
	})
	qs := []dnsmessage.Question{{Name: dnsmessage.MustNewName("1.1.168.192.in-addr.arpa."), Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET}}
	qs = append(qs, testQuestions()...)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got := dnsReplies(ctx, srv, qs); got != dnsResolved {
		t.Fatalf("got %v, want dnsResolved", got)
	}
}

func TestDNSRepliesIgnoresNonAnswers(t *testing.T) {
	cases := map[string]func(dnsmessage.Message) *dnsmessage.Message{
		"silent": func(dnsmessage.Message) *dnsmessage.Message { return nil },
		"wrong id": func(q dnsmessage.Message) *dnsmessage.Message {
			return &dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID + 1, Response: true}}
		},
		"not response": func(q dnsmessage.Message) *dnsmessage.Message {
			return &dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID}}
		},
	}
	for name, reply := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		if dnsReplies(ctx, fakeDNS(t, reply), testQuestions()) != dnsNoReply {
			t.Errorf("%s: counted as a reply", name)
		}
		cancel()
	}
	// No server at all on the port.
	pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
	closed := pc.LocalAddr().String()
	pc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if dnsReplies(ctx, closed, testQuestions()) != dnsNoReply {
		t.Error("closed port: counted as a reply")
	}
}

// TestLocalDNSAnswerAsksAllAtOnce: a silent first server must not hide a
// second one that answers.
func TestLocalDNSAnswerAsksAllAtOnce(t *testing.T) {
	silent := fakeDNS(t, func(dnsmessage.Message) *dnsmessage.Message { return nil })
	var mu sync.Mutex
	var names []string
	answers := fakeDNS(t, func(q dnsmessage.Message) *dnsmessage.Message {
		mu.Lock()
		for _, x := range q.Questions {
			names = append(names, x.Type.String()+" "+x.Name.String())
		}
		mu.Unlock()
		return rcodeReply(dnsmessage.RCodeServerFailure)(q)
	})
	r := newTestRunner(fakePinger{}, fakeResolver{}, "")
	r.dnsServers = func(context.Context, string, string) ([]string, error) { return []string{silent, answers}, nil }
	t0 := time.Now()
	server, reply, asked, err := r.localDNSAnswer(context.Background(), Input{Gateway: "192.168.1.1"}, "connectivitycheck.gstatic.com")
	if server != answers || reply != dnsReplied || len(asked) != 2 || err != nil {
		t.Fatalf("server %q reply %v asked %v err %v", server, reply, asked, err)
	}
	if d := time.Since(t0); d > time.Second {
		t.Fatalf("took %v: waited for the silent server", d)
	}
	// Both questions were sent before the first reply; wait for the second.
	for i := 0; i < 50; i++ {
		mu.Lock()
		n := len(names)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	slices.Sort(names)
	if want := []string{"TypeA connectivitycheck.gstatic.com.", "TypePTR 1.1.168.192.in-addr.arpa."}; !slices.Equal(names, want) {
		t.Fatalf("questions %v want %v", names, want)
	}
}

// TestLocalDNSAnswerKeepsBestReply: a quick server failure from one server
// must not hide another server that resolves the name a little later.
func TestLocalDNSAnswerKeepsBestReply(t *testing.T) {
	fail := fakeDNS(t, rcodeReply(dnsmessage.RCodeServerFailure))
	good := fakeDNS(t, func(q dnsmessage.Message) *dnsmessage.Message {
		time.Sleep(30 * time.Millisecond)
		m := rcodeReply(dnsmessage.RCodeSuccess)(q)
		if q.Questions[0].Type == dnsmessage.TypeA {
			m.Answers = []dnsmessage.Resource{{
				Header: dnsmessage.ResourceHeader{Name: q.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
				Body:   &dnsmessage.AResource{A: [4]byte{192, 0, 2, 7}},
			}}
		}
		return m
	})
	r := newTestRunner(fakePinger{}, fakeResolver{}, "")
	r.dnsServers = func(context.Context, string, string) ([]string, error) { return []string{fail, good, fail}, nil }
	server, reply, asked, err := r.localDNSAnswer(context.Background(), Input{Gateway: "192.168.1.1"}, "example.com")
	if server != good || reply != dnsResolved || err != nil {
		t.Fatalf("server %q reply %v err %v, want %q resolved", server, reply, err, good)
	}
	if len(asked) != 2 {
		t.Fatalf("asked %v: duplicates not removed", asked)
	}
}

// TestPTROnlyToLocalServers: public DNS servers are asked only the name
// that failed (as the system resolver just did), never about the gateway.
func TestPTROnlyToLocalServers(t *testing.T) {
	var mu sync.Mutex
	types := map[string][]string{}
	r := newTestRunner(fakePinger{}, fakeResolver{}, "")
	r.dnsServers = func(context.Context, string, string) ([]string, error) {
		return []string{"8.8.8.8", "192.168.1.1", "fe80::1%13", "fd00::53"}, nil
	}
	r.askDNS = func(_ context.Context, s string, qs []dnsmessage.Question) dnsResult {
		mu.Lock()
		defer mu.Unlock()
		for _, q := range qs {
			types[s] = append(types[s], q.Type.String())
		}
		return dnsNoReply
	}
	r.localDNSAnswer(context.Background(), Input{Gateway: "192.168.1.1"}, "connectivitycheck.gstatic.com")
	want := map[string][]string{
		"8.8.8.8":     {"TypeA"},
		"192.168.1.1": {"TypeA", "TypePTR"},
		"fe80::1%13":  {"TypeA", "TypePTR"},
		"fd00::53":    {"TypeA", "TypePTR"},
	}
	for s, w := range want {
		if !slices.Equal(types[s], w) {
			t.Errorf("%s asked %v, want %v", s, types[s], w)
		}
	}
}

// TestDNSWithoutInternet: when the internet name does not resolve, a DNS
// server that still answers makes the check a warning, not a failure.
func TestDNSWithoutInternet(t *testing.T) {
	cases := []struct {
		name    string
		servers []string
		err     error
		answers map[string]dnsResult
		want    string
		detail  string
	}{
		{"a server answers", []string{"8.8.8.8", "192.168.1.1"}, nil, map[string]dnsResult{"192.168.1.1": dnsReplied}, model.HealthWarn, "DNS server 192.168.1.1 answers: no internet connection"},
		{"the server says the name does not exist", []string{"192.168.1.1"}, nil, map[string]dnsResult{"192.168.1.1": dnsNoSuchName}, model.HealthWarn, "says connectivitycheck.gstatic.com does not exist"},
		{"the name resolves when asked directly", []string{"192.168.1.1"}, nil, map[string]dnsResult{"192.168.1.1": dnsResolved}, model.HealthWarn, "resolved it when asked directly"},
		{"no server answers", []string{"8.8.8.8", "8.8.4.4"}, nil, nil, model.HealthFail, "no reply from DNS server 8.8.8.8, 8.8.4.4"},
		{"no servers configured", nil, nil, nil, model.HealthFail, "no DNS server is set on this network adapter"},
		{"servers unreadable", nil, errors.New("x"), nil, model.HealthFail, "could not resolve connectivitycheck.gstatic.com: lookup failed"},
		{"unusable addresses skipped", []string{"fec0:0:0:ffff::1%1", "0.0.0.0", "bogus"}, nil, map[string]dnsResult{"fec0:0:0:ffff::1%1": dnsReplied, "0.0.0.0": dnsReplied, "bogus": dnsReplied}, model.HealthFail, "no DNS server is set"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRunner(fakePinger{}, fakeResolver{err: errors.New("lookup failed")}, "")
			r.dnsServers = func(context.Context, string, string) ([]string, error) { return tc.servers, tc.err }
			r.askDNS = func(_ context.Context, s string, _ []dnsmessage.Question) dnsResult { return tc.answers[s] }
			c := r.checkDNS(context.Background(), Input{Gateway: "192.168.1.1", IfaceIP: "192.168.1.20"})
			if c.Status != tc.want || !strings.Contains(c.Detail, tc.detail) || tc.err != nil && strings.Contains(c.Detail, "no DNS server is set") {
				t.Fatalf("got %s %q, want %s containing %q", c.Status, c.Detail, tc.want, tc.detail)
			}
		})
	}
	// A lookup that timed out but resolves when asked again is slow DNS.
	r := newTestRunner(fakePinger{}, fakeResolver{err: &net.DNSError{Err: "i/o timeout", IsTimeout: true}}, "")
	r.dnsServers = func(context.Context, string, string) ([]string, error) { return []string{"192.168.1.1"}, nil }
	r.askDNS = func(context.Context, string, []dnsmessage.Question) dnsResult { return dnsResolved }
	if c := r.checkDNS(context.Background(), Input{Gateway: "192.168.1.1"}); c.Status != model.HealthWarn || !strings.Contains(c.Detail, "DNS is slow") {
		t.Fatalf("timeout: %+v", c)
	}
	// A name with a label over 63 bytes cannot be asked about.
	r = newTestRunner(fakePinger{}, fakeResolver{err: errors.New("lookup failed")}, "")
	r.dnsServers = func(context.Context, string, string) ([]string, error) { return []string{"192.168.1.1"}, nil }
	r.askDNS = func(context.Context, string, []dnsmessage.Question) dnsResult {
		t.Error("asked a server about an invalid name")
		return dnsReplied
	}
	long := "http://" + strings.Repeat("a", 64) + ".example/"
	if c := r.checkDNS(context.Background(), Input{Gateway: "192.168.1.1", Config: Config{InternetURL: long}}); c.Status != model.HealthFail || strings.Contains(c.Detail, "no DNS server is set") {
		t.Fatalf("long label: %+v", c)
	}
	// A caller that has given up gets the plain failure, without probing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = newTestRunner(fakePinger{}, fakeResolver{err: context.Canceled}, "")
	r.dnsServers = func(context.Context, string, string) ([]string, error) {
		t.Error("probed after the caller gave up")
		return nil, nil
	}
	if c := r.checkDNS(ctx, Input{Gateway: "192.168.1.1"}); c.Status != model.HealthFail {
		t.Fatalf("cancelled: %+v", c)
	}
	// A name that resolves never asks the servers.
	r = newTestRunner(fakePinger{}, fakeResolver{}, "")
	r.dnsServers = func(context.Context, string, string) ([]string, error) {
		t.Fatal("DNS servers read although the name resolved")
		return nil, nil
	}
	if c := r.checkDNS(context.Background(), Input{Gateway: "192.168.1.1"}); c.Status != model.HealthOK {
		t.Fatalf("%+v", c)
	}
}

func TestParseResolvConf(t *testing.T) {
	in := "# macOS\ndomain lan\nnameserver 192.168.1.1\nnameserver fe80::1%en0\n  nameserver 8.8.8.8\nsearch lan\n"
	if got, want := parseResolvConf(strings.NewReader(in)), []string{"192.168.1.1", "fe80::1%en0", "8.8.8.8"}; !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestUsableDNSServer(t *testing.T) {
	for s, want := range map[string]bool{"192.168.1.1": true, "fe80::1%en0": true, "127.0.0.1:5353": true,
		"": false, "0.0.0.0": false, "::": false, "fec0:0:0:ffff::1": false, "fec0:0:0:ffff::1%1": false, "bogus": false} {
		if usableDNSServer(s) != want {
			t.Errorf("usableDNSServer(%q) = %v", s, !want)
		}
	}
}

func TestLocalDNSServer(t *testing.T) {
	for s, want := range map[string]bool{"192.168.1.1": true, "10.0.0.53": true, "127.0.0.1:5353": true, "fe80::1%13": true, "fd00::53": true,
		"8.8.8.8": false, "2001:4860:4860::8888": false, "100.100.100.100": false, "bogus": false} {
		if localDNSServer(s) != want {
			t.Errorf("localDNSServer(%q) = %v", s, !want)
		}
	}
}

func TestReverseName(t *testing.T) {
	if got := reverseName("192.168.68.1"); got != "1.68.168.192.in-addr.arpa." {
		t.Fatal(got)
	}
	if reverseName("") != "" || reverseName("fe80::1") != "" {
		t.Fatal("non-IPv4 should give empty")
	}
}
