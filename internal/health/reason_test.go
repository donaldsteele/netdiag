package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
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
	add("gateway tcp only", r.checkGateway(ctx, Input{Gateway: "192.168.1.1"}), model.ReasonTCPOnly)
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

// TestNoInternetSwitch: with "This network has no internet" on (v0.6-D15),
// a name server that answers but cannot reach the internet is a note, and
// both checks mark it; with it off, the DNS check stays a warning.
func TestNoInternetSwitch(t *testing.T) {
	ctx := context.Background()
	r := newTestRunner(fakePinger{}, fakeResolver{err: errors.New("lookup failed")}, "")
	r.dnsServers = func(context.Context, string, string) ([]string, error) { return []string{"192.168.1.1"}, nil }
	r.askDNS = func(context.Context, string, []dnsmessage.Question) dnsResult { return dnsReplied }
	off := r.checkDNS(ctx, Input{Gateway: "192.168.1.1"})
	if off.Status != model.HealthWarn || off.Data["noInternet"] != nil {
		t.Fatalf("switch off: %+v", off)
	}
	in := Input{Gateway: "192.168.1.1", Config: Config{NoInternet: true, InternetURL: "http://127.0.0.1:1/"}}
	on := r.checkDNS(ctx, in)
	if on.Status != model.HealthInfo || on.Data["noInternet"] != true || model.HealthReason(on) != model.ReasonNoInternet ||
		!strings.Contains(on.Detail, "DNS server 192.168.1.1 answers") {
		t.Fatalf("switch on: %+v", on)
	}
	// A name server that does not answer at all is still a failure.
	r.askDNS = func(context.Context, string, []dnsmessage.Question) dnsResult { return dnsNoReply }
	if c := r.checkDNS(ctx, in); c.Status != model.HealthFail {
		t.Fatalf("no reply with the switch on: %+v", c)
	}
	inet := r.checkInternet(ctx, in)
	if inet.Status != model.HealthInfo || inet.Data["noInternet"] != true || model.HealthReason(inet) != model.ReasonNoResponse {
		t.Fatalf("internet with the switch on: %+v", inet)
	}
	if got := ConfigFor(model.Settings{NoInternet: true}); !got.NoInternet {
		t.Fatal("ConfigFor drops NoInternet")
	}
}
