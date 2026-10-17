package health

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"netdiag/internal/model"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "health", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseGetPacket(t *testing.T) {
	i := parseGetPacket(fixture(t, "ipconfig_getpacket.txt"))
	if !i.Enabled || i.Server != "192.168.1.1" || i.LeaseSeconds != 86400 || i.IP != "192.168.1.77" {
		t.Fatalf("%+v", i)
	}
	if parseGetPacket("").Enabled {
		t.Fatal("empty must be static")
	}
}

func TestParseSystemProfiler(t *testing.T) {
	w, err := parseSystemProfiler([]byte(fixture(t, "system_profiler_airport.json")), "en0")
	if err != nil || w.SSID != "EventNet" || w.Channel != 36 || w.Band != "5 GHz" || w.RSSI != -55 || w.SignalPct != 90 || w.TxRateMbps != 866 {
		t.Fatalf("%+v %v", w, err)
	}
	w, err = parseSystemProfiler([]byte(fixture(t, "system_profiler_redacted.json")), "")
	if err != nil || !w.Redacted || w.SSID != "" || !w.Connected {
		t.Fatalf("%+v %v", w, err)
	}
	if _, err = parseSystemProfiler([]byte("{nope"), ""); err == nil {
		t.Fatal("bad json must error")
	}
}

type fakePinger struct {
	ok  bool
	err error
}

func (f fakePinger) Ping(ctx context.Context, ip netip.Addr, to time.Duration) (time.Duration, bool, error) {
	return 3 * time.Millisecond, f.ok, f.err
}

var (
	errSilent  = errors.New("i/o timeout")
	errRefused = errors.New("connection refused")
)

type fakeResolver struct{ err error }

func (f fakeResolver) LookupHost(ctx context.Context, h string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []string{"1.2.3.4"}, nil
}

func newTestRunner(p fakePinger, res fakeResolver, execOut string) *Runner {
	r := &Runner{Pinger: p, Resolver: res, Now: func() time.Time { return time.Unix(1000, 0) },
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, errors.New("exec disabled in test")
		},
		WiFi: func(ctx context.Context, iface string) (WiFiInfo, error) {
			return WiFiInfo{}, errors.New("wi-fi disabled in test")
		},
		DHCP: func(ctx context.Context, iface, ip string) (DHCPInfo, error) {
			return DHCPInfo{}, errors.New("dhcp disabled in test")
		},
		dnsServers: func(ctx context.Context, iface, ip string) ([]string, error) {
			return nil, errors.New("dns servers disabled in test")
		},
		dial: func(ctx context.Context, ip string, port int, to time.Duration) (net.Conn, error) {
			return nil, errSilent
		},
		refused: func(err error) bool { return errors.Is(err, errRefused) }}
	_ = execOut
	r.defaults()
	return r
}

func byID(rep model.HealthReport, id string) model.HealthCheck {
	for _, c := range rep.Checks {
		if c.ID == id {
			return c
		}
	}
	return model.HealthCheck{}
}

func TestRunChecks(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/204" {
			w.WriteHeader(204)
			return
		}
		w.Write([]byte("<html>Event Portal</html>"))
	}))
	defer web.Close()
	r := newTestRunner(fakePinger{ok: true}, fakeResolver{}, "")
	rep := r.Run(context.Background(), Input{Gateway: "192.168.1.1",
		Config: Config{WebURL: web.URL, MustContain: "Event Portal", InternetURL: web.URL + "/204"}})
	if len(rep.Checks) != 6 || rep.CheckedAt == "" {
		t.Fatalf("%+v", rep)
	}
	for _, id := range []string{"gateway", "dns", "webserver", "internet"} {
		if c := byID(rep, id); c.Status != model.HealthOK {
			t.Errorf("%s: %+v", id, c)
		}
	}
	if c := byID(rep, "wifi"); c.Status != model.HealthUnavailable {
		t.Errorf("wifi: %+v", c)
	}
	// failures
	r = newTestRunner(fakePinger{ok: false}, fakeResolver{err: errors.New("nxdomain")}, "")
	rep = r.Run(context.Background(), Input{Gateway: "192.168.1.1",
		Config: Config{WebURL: web.URL, MustContain: "missing", ExpectStatus: 200, InternetURL: "http://127.0.0.1:1/"}})
	if byID(rep, "gateway").Status != model.HealthFail || byID(rep, "dns").Status != model.HealthFail ||
		byID(rep, "webserver").Status != model.HealthFail || byID(rep, "internet").Status != model.HealthInfo {
		t.Errorf("%+v", rep.Checks)
	}
	rep = r.Run(context.Background(), Input{Config: Config{ExpectStatus: 200}})
	if byID(rep, "gateway").Status != model.HealthUnavailable || byID(rep, "webserver").Status != model.HealthInfo {
		t.Errorf("%+v", rep.Checks)
	}
}

// A router that ignores ping is still reachable when it answers, or
// refuses, a TCP connection on port 80 or 443.
func TestGatewayIgnoresPing(t *testing.T) {
	cases := []struct {
		name    string
		pinger  fakePinger
		answers map[int]error // port -> dial result; missing = silent
		status  string
		detail  string
		port    int
	}{
		{"port 80 open", fakePinger{}, map[int]error{80: nil}, model.HealthOK,
			"the router 192.168.1.1 answers on port 80 (it does not reply to ping)", 80},
		{"port 80 refused", fakePinger{}, map[int]error{80: errRefused}, model.HealthOK,
			"the router 192.168.1.1 answers on port 80 (it does not reply to ping)", 80},
		{"only 443", fakePinger{}, map[int]error{443: nil}, model.HealthOK,
			"the router 192.168.1.1 answers on port 443 (it does not reply to ping)", 443},
		{"silent", fakePinger{}, nil, model.HealthFail,
			"192.168.1.1 did not reply to ping or on ports 80 and 443", 0},
		{"ping broken, tcp ok", fakePinger{err: errors.New("no icmp")}, map[int]error{80: nil}, model.HealthOK,
			"the router 192.168.1.1 answers on port 80 (ping could not run on this computer)", 80},
		{"ping broken, tcp silent", fakePinger{err: errors.New("no icmp")}, nil, model.HealthUnavailable,
			"ping failed locally: no icmp", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRunner(tc.pinger, fakeResolver{}, "")
			var tried []int
			r.dial = func(ctx context.Context, ip string, port int, to time.Duration) (net.Conn, error) {
				if ip != "192.168.1.1" || to != gatewayDialTimeout {
					t.Errorf("dial %s timeout %v", ip, to)
				}
				tried = append(tried, port)
				err, ok := tc.answers[port]
				if !ok {
					return nil, errSilent
				}
				if err != nil {
					return nil, err
				}
				a, b := net.Pipe()
				b.Close()
				return a, nil
			}
			c := r.checkGateway(context.Background(), Input{Gateway: "192.168.1.1"})
			if c.Status != tc.status || c.Detail != tc.detail {
				t.Fatalf("got %s %q", c.Status, c.Detail)
			}
			if tc.port != 0 && (c.Data["port"] != tc.port || c.Data["method"] != "tcp") {
				t.Fatalf("data %+v", c.Data)
			}
			if tc.port == 80 && len(tried) != 1 || tc.port != 80 && len(tried) != 2 {
				t.Fatalf("ports tried %v", tried)
			}
		})
	}
	// A reply to ping never needs the TCP fallback.
	r := newTestRunner(fakePinger{ok: true}, fakeResolver{}, "")
	r.dial = func(context.Context, string, int, time.Duration) (net.Conn, error) {
		t.Fatal("dialled although ping replied")
		return nil, errSilent
	}
	if c := r.checkGateway(context.Background(), Input{Gateway: "192.168.1.1"}); c.Status != model.HealthOK {
		t.Fatalf("%+v", c)
	}
	// No router known: for the adapter checked, or at all.
	if c := r.checkGateway(context.Background(), Input{Iface: "Ethernet 2", IfaceIP: "10.0.0.30"}); c.Status != model.HealthUnavailable ||
		c.Detail != "no router is known for this network adapter (Ethernet 2)" {
		t.Fatalf("no router for the adapter: %+v", c)
	}
	if c := r.checkGateway(context.Background(), Input{}); c.Status != model.HealthUnavailable || c.Detail != "no default gateway detected" {
		t.Fatalf("no router: %+v", c)
	}
}

// The real dial and refusal check: a closed loopback port counts as an
// answer, so the check reports the gateway reachable.
func TestGatewayTCPRealDial(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	open := l.Addr().(*net.TCPAddr).Port
	defer l.Close()
	c, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := c.Addr().(*net.TCPAddr).Port
	c.Close()
	r := &Runner{}
	r.defaults()
	old := gatewayPorts
	defer func() { gatewayPorts = old }()
	for _, ports := range [][]int{{open}, {closed}} {
		gatewayPorts = ports
		if port, _, ok := r.gatewayTCP(context.Background(), "127.0.0.1"); !ok || port != ports[0] {
			t.Errorf("ports %v: got %d %v", ports, port, ok)
		}
	}
}

func TestWiFiCache(t *testing.T) {
	r := newTestRunner(fakePinger{}, fakeResolver{}, "")
	calls := 0
	r.WiFi = func(ctx context.Context, iface string) (WiFiInfo, error) {
		calls++
		return WiFiInfo{}, errors.New("x")
	}
	for i := 0; i < 3; i++ {
		r.checkWiFi(context.Background(), Input{Iface: "en0"})
	}
	if calls != 1 {
		t.Fatalf("expected cached wifi, reader calls=%d", calls)
	}
}
