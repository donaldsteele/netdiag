package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"netdiag/internal/model"
)

// TestReasonOnEveryNonOKResult: every result that is not ok names its
// reason in Data["reason"] (the advice catalogue keys on it), and the
// reasons are the ones each path means.
func TestReasonOnEveryNonOKResult(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/503":
			w.WriteHeader(503)
		case "/away":
			http.Redirect(w, r, "http://elsewhere.example/", http.StatusFound)
		case "/slow":
			time.Sleep(2100 * time.Millisecond)
			w.Write([]byte("ok"))
		default:
			w.Write([]byte("hello"))
		}
	}))
	defer web.Close()
	ctx := context.Background()
	type result struct {
		name   string
		c      model.HealthCheck
		reason string
	}
	var got []result
	add := func(name string, c model.HealthCheck, reason string) { got = append(got, result{name, c, reason}) }

	// gateway
	r := newTestRunner(fakePinger{}, fakeResolver{}, "")
	add("gateway silent", r.checkGateway(ctx, Input{Gateway: "192.168.1.1"}), model.ReasonNoReply)
	add("gateway unknown", r.checkGateway(ctx, Input{Iface: "Wi-Fi"}), model.ReasonNoGateway)
	add("gateway none", r.checkGateway(ctx, Input{}), model.ReasonNoGateway)
	add("gateway invalid", r.checkGateway(ctx, Input{Gateway: "nope"}), model.ReasonNoGateway)
	r = newTestRunner(fakePinger{err: errors.New("no icmp")}, fakeResolver{}, "")
	add("gateway ping broken", r.checkGateway(ctx, Input{Gateway: "192.168.1.1"}), model.ReasonPingError)
	r = newTestRunner(fakePinger{}, fakeResolver{}, "")
	r.dial = func(context.Context, string, int, time.Duration) (net.Conn, error) { return nil, errRefused }
	tcp := r.checkGateway(ctx, Input{Gateway: "192.168.1.1"})
	add("gateway tcp only", tcp, model.ReasonTCPOnly)
	if tcp.Data["pingError"] != nil {
		t.Errorf("a router that ignores ping is marked pingError: %+v", tcp)
	}
	// Ping could not run here at all, and the router's web port answers:
	// still tcp_only, but not the router's doing.
	r = newTestRunner(fakePinger{err: errors.New("no icmp")}, fakeResolver{}, "")
	r.dial = func(context.Context, string, int, time.Duration) (net.Conn, error) { return nil, errRefused }
	noPing := r.checkGateway(ctx, Input{Gateway: "192.168.1.1"})
	add("gateway tcp only, ping broken", noPing, model.ReasonTCPOnly)
	if noPing.Data["pingError"] != true || !strings.Contains(noPing.Detail, "ping could not run on this computer") {
		t.Errorf("ping broken, web port answers: %+v", noPing)
	}
	slow := newTestRunner(fakePinger{ok: true}, fakeResolver{}, "")
	slow.Pinger = slowPinger{}
	add("gateway slow", slow.checkGateway(ctx, Input{Gateway: "192.168.1.1"}), model.ReasonSlow)

	// dhcp
	r = newTestRunner(fakePinger{}, fakeResolver{}, "")
	add("dhcp unreadable", r.checkDHCP(ctx, Input{}), model.ReasonError)
	r.DHCP = func(context.Context, string, string) (DHCPInfo, error) { return DHCPInfo{APIPA: true}, nil }
	add("dhcp apipa", r.checkDHCP(ctx, Input{}), model.ReasonAPIPA)
	r.DHCP = func(context.Context, string, string) (DHCPInfo, error) { return DHCPInfo{}, nil }
	add("dhcp static", r.checkDHCP(ctx, Input{}), model.ReasonNoLease)

	// dns
	dns := func(servers []string, serr error, reply dnsResult, res fakeResolver) *Runner {
		r := newTestRunner(fakePinger{}, res, "")
		r.dnsServers = func(context.Context, string, string) ([]string, error) { return servers, serr }
		r.askDNS = func(context.Context, string, []dnsmessage.Question) dnsResult { return reply }
		return r
	}
	failed := fakeResolver{err: errors.New("lookup failed")}
	gw := Input{Gateway: "192.168.1.1"}
	add("dns slow", dns([]string{"192.168.1.1"}, nil, dnsResolved, fakeResolver{err: &net.DNSError{Err: "i/o timeout", IsTimeout: true}}).checkDNS(ctx, gw), model.ReasonSlow)
	add("dns mismatch", dns([]string{"192.168.1.1"}, nil, dnsResolved, failed).checkDNS(ctx, gw), model.ReasonLocalMismatch)
	add("dns nxdomain", dns([]string{"192.168.1.1"}, nil, dnsNoSuchName, failed).checkDNS(ctx, gw), model.ReasonNXDomain)
	add("dns no internet", dns([]string{"192.168.1.1"}, nil, dnsReplied, failed).checkDNS(ctx, gw), model.ReasonNoInternet)
	add("dns no reply", dns([]string{"192.168.1.1"}, nil, dnsNoReply, failed).checkDNS(ctx, gw), model.ReasonNoReply)
	add("dns servers unreadable", dns(nil, errors.New("x"), dnsNoReply, failed).checkDNS(ctx, gw), model.ReasonError)
	add("dns no server", dns(nil, nil, dnsNoReply, failed).checkDNS(ctx, gw), model.ReasonNoServer)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	add("dns cancelled", dns(nil, nil, dnsNoReply, fakeResolver{err: context.Canceled}).checkDNS(cancelled, gw), model.ReasonTimeout)

	// webserver
	r = newTestRunner(fakePinger{}, fakeResolver{}, "")
	webIn := func(u, must string) Input { return Input{Config: Config{WebURL: u, MustContain: must}} }
	add("web not set", r.checkWeb(ctx, webIn("", "")), model.ReasonNotSet)
	add("web invalid", r.checkWeb(ctx, webIn("http://[::1", "")), model.ReasonInvalid)
	add("web status", r.checkWeb(ctx, webIn(web.URL+"/503", "")), model.ReasonStatus)
	add("web redirect", r.checkWeb(ctx, webIn(web.URL+"/away", "")), model.ReasonRedirect)
	add("web content", r.checkWeb(ctx, webIn(web.URL+"/", "missing")), model.ReasonContent)
	add("web request", r.checkWeb(ctx, webIn("http://127.0.0.1:1/", "")), model.ReasonRequest)
	add("web slow", r.checkWeb(ctx, webIn(web.URL+"/slow", "")), model.ReasonSlow)
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("x")) }))
	defer tlsSrv.Close()
	add("web tls", r.checkWeb(ctx, webIn(tlsSrv.URL+"/", "")), model.ReasonTLS)

	// internet
	internet := func(u string) model.HealthCheck { return r.checkInternet(ctx, Input{Config: Config{InternetURL: u}}) }
	add("internet invalid", internet("http://[::1"), model.ReasonInvalid)
	add("internet no response", internet("http://127.0.0.1:1/"), model.ReasonNoResponse)
	add("internet portal", internet(web.URL+"/away"), model.ReasonPortal)
	add("internet status", internet(web.URL+"/503"), model.ReasonStatus)

	// wifi
	wifi := func(w WiFiInfo, err error) model.HealthCheck {
		r := newTestRunner(fakePinger{}, fakeResolver{}, "")
		r.WiFi = func(context.Context, string) (WiFiInfo, error) { return w, err }
		return r.checkWiFi(ctx, Input{})
	}
	add("wifi unreadable", wifi(WiFiInfo{}, errors.New("wi-fi info not supported on this OS")), model.ReasonError)
	add("wifi location off", wifi(WiFiInfo{}, fmt.Errorf("%w: turn it on", ErrLocationOff)), model.ReasonHiddenLocation)
	add("wifi not connected", wifi(WiFiInfo{}, nil), model.ReasonNotConnected)
	add("wifi weak", wifi(WiFiInfo{Connected: true, SSID: "x", SignalPct: 32}, nil), model.ReasonWeak)

	for _, g := range got {
		if g.c.Status == model.HealthOK && g.reason != model.ReasonTCPOnly {
			t.Errorf("%s: status ok, want a result that is not ok: %+v", g.name, g.c)
		}
		if r := model.HealthReason(g.c); r != g.reason {
			t.Errorf("%s: reason %q, want %q (%s %q)", g.name, r, g.reason, g.c.Status, g.c.Detail)
		}
	}

	// A healthy result has no reason.
	r = newTestRunner(fakePinger{ok: true}, fakeResolver{}, "")
	rep := r.Run(ctx, Input{Gateway: "192.168.1.1", Config: Config{WebURL: web.URL + "/", InternetURL: web.URL + "/"}})
	for _, c := range rep.Checks {
		if c.Status == model.HealthOK && model.HealthReason(c) != "" {
			t.Errorf("%s ok with reason %q", c.ID, model.HealthReason(c))
		}
		if c.Status != model.HealthOK && model.HealthReason(c) == "" {
			t.Errorf("%s %s without a reason: %q", c.ID, c.Status, c.Detail)
		}
	}
}

type slowPinger struct{}

func (slowPinger) Ping(context.Context, netip.Addr, time.Duration) (time.Duration, bool, error) {
	return 180 * time.Millisecond, true, nil
}

// TestNoInternetSwitch: "This network has no internet" (v0.6-D15) is on
// for one network only. On it, a name server that answers but cannot reach
// the internet is a note, and both checks mark it. On another network with
// the same addresses (another router), on a network whose router MAC is
// unknown, or with the switch off, the DNS check stays a warning.
func TestNoInternetSwitch(t *testing.T) {
	ctx := context.Background()
	const routerA, routerB = "aa:bb:cc:00:00:01", "AA:BB:CC:00:00:02"
	here := model.NetworkID("192.168.1.0/24", "192.168.1.1", routerA)
	run := func(gw, mac string, reply dnsResult, nets ...string) model.HealthReport {
		t.Helper()
		r := newTestRunner(fakePinger{ok: true}, fakeResolver{err: errors.New("lookup failed")}, "")
		r.dnsServers = func(context.Context, string, string) ([]string, error) { return []string{"192.168.1.1"}, nil }
		r.askDNS = func(context.Context, string, []dnsmessage.Question) dnsResult { return reply }
		r.gatewayMAC = func(ctx context.Context, ifaceIP, gateway string) string {
			if ifaceIP != "192.168.1.20" || gateway != "192.168.1.1" || ctx.Err() != nil {
				t.Errorf("gatewayMAC(%q, %q), ctx %v", ifaceIP, gateway, ctx.Err())
			}
			return mac
		}
		return r.Run(ctx, Input{Iface: "wlan0", IfaceIP: "192.168.1.20", Gateway: gw, CIDR: "192.168.1.0/24",
			Config: Config{InternetURL: "http://127.0.0.1:1/", NoInternetNetworks: nets}})
	}
	on := run("192.168.1.1", routerA, dnsReplied, "n-000000000000", here)
	dns, inet := byID(on, "dns"), byID(on, "internet")
	if on.Network != here || dns.Status != model.HealthInfo || dns.Data["noInternet"] != true ||
		model.HealthReason(dns) != model.ReasonNoInternet || !strings.Contains(dns.Detail, "DNS server 192.168.1.1 answers") {
		t.Fatalf("switch on: %s %+v", on.Network, dns)
	}
	if inet.Status != model.HealthInfo || inet.Data["noInternet"] != true || model.HealthReason(inet) != model.ReasonNoResponse {
		t.Fatalf("internet with the switch on: %+v", inet)
	}
	if gw := byID(on, "gateway"); gw.Status != model.HealthOK || gw.Data["noInternet"] != nil {
		t.Fatalf("gateway: %+v", gw)
	}
	other := model.NetworkID("192.168.1.0/24", "192.168.1.1", routerB)
	for _, tc := range []struct {
		name    string
		rep     model.HealthReport
		network string
	}{
		{"switch off", run("192.168.1.1", routerA, dnsReplied), here},
		{"another router", run("192.168.1.1", routerB, dnsReplied, here), other},
		{"router MAC not known", run("192.168.1.1", "", dnsReplied, here), ""},
		{"switch on for another network", run("192.168.1.1", routerA, dnsReplied, other), here},
	} {
		if c := byID(tc.rep, "dns"); c.Status != model.HealthWarn || c.Data["noInternet"] != nil || byID(tc.rep, "internet").Data["noInternet"] != nil {
			t.Errorf("%s: %+v", tc.name, c)
		}
		if tc.rep.Network != tc.network {
			t.Errorf("%s: network %q, want %q", tc.name, tc.rep.Network, tc.network)
		}
	}
	// A name server that does not answer at all is still a failure.
	if c := byID(run("192.168.1.1", routerA, dnsNoReply, here), "dns"); c.Status != model.HealthFail || c.Data["noInternet"] != nil {
		t.Fatalf("no reply with the switch on: %+v", c)
	}
	// Without a router, the network is named by its address prefix.
	alone := model.NetworkID("192.168.1.0/24", "", "")
	if rep := run("", "", dnsReplied, alone); rep.Network != alone || byID(rep, "dns").Status != model.HealthInfo {
		t.Fatalf("no router: %s %+v", rep.Network, byID(rep, "dns"))
	}
	if got := ConfigFor(model.Settings{NoInternetNetworks: []string{here}}); !slices.Equal(got.NoInternetNetworks, []string{here}) {
		t.Fatal("ConfigFor drops NoInternetNetworks")
	}
}
