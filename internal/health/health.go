// Package health runs the local network health checks shown in the health
// panel: gateway, DHCP, DNS, web server, internet and Wi-Fi.
package health

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"netdiag/internal/arp"
	"netdiag/internal/discovery"
	"netdiag/internal/model"
	"netdiag/internal/portprobe"
)

const (
	// OverallTimeout caps a whole Run.
	OverallTimeout = 10 * time.Second
	// DefaultInternetURL is used when Config.InternetURL is empty.
	DefaultInternetURL = "http://connectivitycheck.gstatic.com/generate_204"

	wifiCacheTTL = 60 * time.Second
	maxBody      = 256 << 10

	// gatewayDialTimeout bounds each TCP try on the gateway when it does
	// not answer ping.
	gatewayDialTimeout = 800 * time.Millisecond
)

// ErrLocationOff is the Wi-Fi reader's error (wrapped) when Windows hides
// Wi-Fi details from apps because Location is off.
var ErrLocationOff = errors.New("Windows blocks Wi-Fi details until Location is on")

// gatewayPorts are tried, in order, when the gateway does not answer ping:
// most routers serve their admin page on one of them.
var gatewayPorts = []int{80, 443}

// Config is the settings subset the health checks need.
type Config struct {
	WebURL       string
	MustContain  string
	ExpectStatus int // 0 -> 200
	// TrustedCert is the SHA-256 fingerprint of a web server certificate
	// the user chose to trust (see model.SettingsWebServer).
	TrustedCert string
	InternetURL string
	// NoInternetNetworks are the networks (model.NetworkID) where the
	// "This network has no internet" switch is on (v0.6-D15). On one of
	// them, a name server that answers but cannot reach the internet is a
	// note, not a warning, and both checks say so in Data["noInternet"]
	// (see noInternet).
	NoInternetNetworks []string
}

// ConfigFor maps settings to the health check config.
func ConfigFor(s model.Settings) Config {
	return Config{
		WebURL:       s.WebServer.URL,
		MustContain:  s.WebServer.MustContain,
		ExpectStatus: s.WebServer.ExpectStatus,
		TrustedCert:  s.WebServer.TrustedCert,
		InternetURL:  s.InternetCheckURL,

		NoInternetNetworks: slices.Clone(s.NoInternetNetworks),
	}
}

// Input describes what to check.
type Input struct {
	Iface   string // interface name, e.g. "en0" (may be empty)
	IfaceIP string // interface IPv4 (used to find the adapter on Windows)
	Gateway string // the router of Iface's network, IPv4 (may be empty)
	// CIDR is Iface's network, its address prefix (e.g. 192.168.1.0/24).
	// With Gateway and the router's MAC it names the network (may be empty).
	CIDR   string
	Config Config
}

// Resolver is the DNS lookup surface used by the dns check.
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// ExecFunc runs a command and returns its combined stdout.
type ExecFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// Runner executes health checks. Zero fields get defaults; use by pointer.
type Runner struct {
	Pinger   discovery.Pinger
	HTTP     *http.Client
	Resolver Resolver
	Exec     ExecFunc
	Now      func() time.Time
	// WiFi and DHCP read the OS Wi-Fi association and DHCP lease; nil uses
	// the platform readers.
	WiFi func(ctx context.Context, iface string) (WiFiInfo, error)
	DHCP func(ctx context.Context, iface, ip string) (DHCPInfo, error)
	// dnsServers lists the DNS servers of the adapter with ip; askDNS
	// reports how one of them replies to the questions. Both are used only
	// after a name fails to resolve; nil uses the platform reader and a
	// direct UDP query.
	dnsServers func(ctx context.Context, iface, ip string) ([]string, error)
	askDNS     func(ctx context.Context, server string, qs []dnsmessage.Question) dnsResult
	// dial makes one TCP connection and refused tells a refused connection
	// (the host answered) from silence; the gateway check uses them when
	// ping gets no reply. nil uses portprobe.Dial and portprobe.Refused.
	dial    func(ctx context.Context, ip string, port int, timeout time.Duration) (net.Conn, error)
	refused func(error) bool
	// gatewayMAC reads the router's MAC from this computer's ARP cache
	// (the gateway check has just talked to it), or ""; nil uses arp.
	gatewayMAC func(ctx context.Context, ifaceIP, gateway string) string
	// roots verifies web server certificates; nil uses the system roots.
	roots *x509.CertPool

	mu       sync.Mutex
	wifiAt   time.Time
	wifiKey  string
	wifiInfo WiFiInfo
	wifiErr  error
}

// NewRunner returns a Runner with production defaults.
func NewRunner() *Runner {
	r := &Runner{}
	r.defaults()
	return r
}

func (r *Runner) defaults() {
	if r.Pinger == nil {
		r.Pinger = discovery.NewPinger()
	}
	if r.HTTP == nil {
		r.HTTP = &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	if r.Resolver == nil {
		r.Resolver = net.DefaultResolver
	}
	if r.Exec == nil {
		r.Exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		}
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.WiFi == nil {
		r.WiFi = func(ctx context.Context, iface string) (WiFiInfo, error) { return readWiFi(ctx, r, iface) }
	}
	if r.DHCP == nil {
		r.DHCP = func(ctx context.Context, iface, ip string) (DHCPInfo, error) { return readDHCP(ctx, r, iface, ip) }
	}
	if r.dnsServers == nil {
		r.dnsServers = readDNSServers
	}
	if r.askDNS == nil {
		r.askDNS = dnsReplies
	}
	if r.dial == nil {
		r.dial = portprobe.Dial
	}
	if r.refused == nil {
		r.refused = portprobe.Refused
	}
	if r.gatewayMAC == nil {
		r.gatewayMAC = arpGatewayMAC
	}
}

// checkMeta is the checks in the order Run runs them, each with its plain
// name (model.HealthLabel).
var checkMeta = func() []struct{ id, label string } {
	ids := []string{"gateway", "dhcp", "dns", "webserver", "internet", "wifi"}
	out := make([]struct{ id, label string }, len(ids))
	for i, id := range ids {
		out[i].id, out[i].label = id, model.HealthLabel(id)
	}
	return out
}()

// Label is the name a check's results carry ("Router" for the check
// gateway), or id itself for a check there is none of. Team chat names a
// failed check with it when it has only the problem's key.
func Label(id string) string {
	if l := model.HealthLabel(id); l != "" {
		return l
	}
	return id
}

// Run executes all checks concurrently under a 10s cap and returns the
// report. It never fails; problems are reported per check.
func (r *Runner) Run(ctx context.Context, in Input) model.HealthReport {
	r.mu.Lock()
	r.defaults()
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, OverallTimeout)
	defer cancel()

	fns := []func(context.Context, Input) model.HealthCheck{
		r.checkGateway, r.checkDHCP, r.checkDNS, r.checkWeb, r.checkInternet, r.checkWiFi,
	}
	type res struct {
		i int
		c model.HealthCheck
	}
	ch := make(chan res, len(fns))
	for i, fn := range fns {
		go func() {
			defer func() {
				if p := recover(); p != nil {
					ch <- res{i, why(mk(i, model.HealthUnavailable, fmt.Sprintf("check crashed: %v", p), 0), model.ReasonError)}
				}
			}()
			c := fn(ctx, in)
			c.ID, c.Label = checkMeta[i].id, checkMeta[i].label
			ch <- res{i, c}
		}()
	}
	out := make([]model.HealthCheck, len(fns))
	done := make([]bool, len(fns))
	for n := 0; n < len(fns); n++ {
		select {
		case x := <-ch:
			out[x.i], done[x.i] = x.c, true
		case <-ctx.Done():
			n = len(fns)
		}
	}
	for i := range out {
		if !done[i] {
			out[i] = why(mk(i, model.HealthUnavailable, "timed out", 0), model.ReasonTimeout)
		}
	}
	rep := model.HealthReport{CheckedAt: r.Now().UTC().Format(time.RFC3339), Checks: out}
	rep.Network = r.network(ctx, in)
	if rep.Network != "" && slices.Contains(in.Config.NoInternetNetworks, rep.Network) {
		noInternet(rep.Checks)
	}
	return rep
}

// network returns the id of the network checked (model.NetworkID), or ""
// when netdiag cannot tell: there is a router but its MAC is unknown, so
// two networks that use the same addresses could not be told apart.
// Without a router, the network is named by its address prefix alone.
func (r *Runner) network(ctx context.Context, in Input) string {
	if in.Gateway == "" {
		if in.CIDR == "" {
			return ""
		}
		return model.NetworkID(in.CIDR, "", "")
	}
	// The checks may have used up the run's time; the ARP cache is local
	// and quick, so its read gets a short deadline of its own.
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	mac := r.gatewayMAC(actx, in.IfaceIP, in.Gateway)
	if mac == "" {
		return ""
	}
	return model.NetworkID(in.CIDR, in.Gateway, mac)
}

// arpGatewayMAC finds gateway in the ARP cache of the adapter with ifaceIP.
func arpGatewayMAC(ctx context.Context, ifaceIP, gateway string) string {
	list, err := arp.TableFor(ctx, ifaceIP)
	if err != nil {
		return ""
	}
	for _, e := range list {
		if e.IP == gateway {
			return e.MAC
		}
	}
	return ""
}

// noInternet applies the "This network has no internet" switch (v0.6-D15)
// to the checks of a network it is on for. A name server that answers but
// cannot reach the internet becomes a note; that result, and an internet
// result that is not ok, carry Data["noInternet"], so their advice says
// the network is set up without internet. A name server that does not
// answer, or says the name does not exist, keeps its status.
func noInternet(checks []model.HealthCheck) {
	for i := range checks {
		c := &checks[i]
		switch {
		case c.ID == "dns" && model.HealthReason(*c) == model.ReasonNoInternet:
			c.Status = model.HealthInfo
		case c.ID == "internet" && c.Status != model.HealthOK && model.HealthReason(*c) != "":
		default:
			continue
		}
		c.Data["noInternet"] = true
	}
}

func mk(i int, status, detail string, ms float64) model.HealthCheck {
	return model.HealthCheck{ID: checkMeta[i].id, Label: checkMeta[i].label, Status: status, Detail: detail, LatencyMs: ms}
}

// why sets c's Data["reason"] (model.Reason*), which the advice catalogue
// keys on, and returns c.
func why(c model.HealthCheck, reason string) model.HealthCheck {
	if c.Data == nil {
		c.Data = map[string]any{}
	}
	c.Data["reason"] = reason
	return c
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func (r *Runner) checkGateway(ctx context.Context, in Input) model.HealthCheck {
	if in.Gateway == "" {
		if in.Iface != "" {
			return why(mk(0, model.HealthUnavailable, "no router is known for this network adapter ("+in.Iface+")", 0), model.ReasonNoGateway)
		}
		return why(mk(0, model.HealthUnavailable, "no default gateway detected", 0), model.ReasonNoGateway)
	}
	ip, err := netip.ParseAddr(in.Gateway)
	if err != nil {
		return why(mk(0, model.HealthUnavailable, "invalid gateway address "+in.Gateway, 0), model.ReasonNoGateway)
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		rtt, ok, perr := r.Pinger.Ping(ctx, ip, 1500*time.Millisecond)
		if perr != nil {
			lastErr = perr
			continue
		}
		if ok {
			c := mk(0, model.HealthOK, fmt.Sprintf("%s replied in %.0f ms", in.Gateway, ms(rtt)), ms(rtt))
			if rtt > 100*time.Millisecond {
				c.Status = model.HealthWarn
				c.Detail += " (slow)"
				c = why(c, model.ReasonSlow)
			}
			return c
		}
		if ctx.Err() != nil {
			break
		}
	}
	// Many routers ignore ping. A TCP connection to their admin page ports
	// proves the router is there just as well, even when it is refused.
	if port, d, ok := r.gatewayTCP(ctx, in.Gateway); ok {
		why := "it does not reply to ping"
		if lastErr != nil {
			why = "ping could not run on this computer"
		}
		c := mk(0, model.HealthOK, fmt.Sprintf("the router %s answers on port %d (%s)", in.Gateway, port, why), ms(d))
		c.Data = map[string]any{"method": "tcp", "port": port, "reason": model.ReasonTCPOnly}
		if lastErr != nil {
			// Not the router's doing: ping could not run here at all.
			c.Data["pingError"] = true
		}
		return c
	}
	if lastErr != nil {
		return why(mk(0, model.HealthUnavailable, "ping failed locally: "+lastErr.Error(), 0), model.ReasonPingError)
	}
	return why(mk(0, model.HealthFail, in.Gateway+" did not reply to ping or on ports 80 and 443", 0), model.ReasonNoReply)
}

// gatewayTCP tries a TCP connection to gw on each of gatewayPorts in turn
// and returns the first port that answers (connected or refused) and how
// long the answer took.
func (r *Runner) gatewayTCP(ctx context.Context, gw string) (int, time.Duration, bool) {
	for _, port := range gatewayPorts {
		if ctx.Err() != nil {
			break
		}
		t0 := time.Now()
		conn, err := r.dial(ctx, gw, port, gatewayDialTimeout)
		d := time.Since(t0)
		if err == nil {
			conn.Close()
			return port, d, true
		}
		if r.refused(err) {
			return port, d, true
		}
	}
	return 0, 0, false
}

func (r *Runner) checkDHCP(ctx context.Context, in Input) model.HealthCheck {
	info, err := r.DHCP(ctx, in.Iface, in.IfaceIP)
	if err != nil {
		return why(mk(1, model.HealthUnavailable, err.Error(), 0), model.ReasonError)
	}
	c := mk(1, model.HealthInfo, "", 0)
	c.Data = map[string]any{"enabled": info.Enabled, "apipa": info.APIPA}
	switch {
	case info.APIPA:
		c.Status = model.HealthFail
		c.Detail = "self-assigned 169.254.x.x address: no DHCP server answered"
		c.Data["reason"] = model.ReasonAPIPA
	case info.Enabled:
		c.Status = model.HealthOK
		c.Detail = "address obtained by DHCP"
		if info.Server != "" {
			c.Detail += " from " + info.Server
			c.Data["server"] = info.Server
		}
		if info.LeaseExpires != "" {
			c.Detail += ", lease expires " + info.LeaseExpires
			c.Data["leaseExpires"] = info.LeaseExpires
		} else if info.LeaseSeconds > 0 {
			c.Detail += fmt.Sprintf(", lease %s", (time.Duration(info.LeaseSeconds) * time.Second).String())
		}
	default:
		c.Detail = "static address (no DHCP lease)"
		c.Data["reason"] = model.ReasonNoLease
	}
	return c
}

func (r *Runner) checkDNS(ctx context.Context, in Input) model.HealthCheck {
	host := "connectivitycheck.gstatic.com"
	if u := hostOf(in.Config.InternetURL); u != "" && net.ParseIP(u) == nil {
		host = u
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	t0 := time.Now()
	addrs, err := r.Resolver.LookupHost(lctx, host)
	d := time.Since(t0)
	if err == nil && len(addrs) > 0 {
		return mk(2, model.HealthOK, fmt.Sprintf("%s resolved to %s in %.0f ms", host, addrs[0], ms(d)), ms(d))
	}
	msg := "no answer"
	if err != nil {
		msg = err.Error()
	}
	if ctx.Err() != nil {
		return why(mk(2, model.HealthFail, fmt.Sprintf("could not resolve %s: %s", host, msg), 0), model.ReasonTimeout)
	}
	var de *net.DNSError
	timedOut := lctx.Err() == context.DeadlineExceeded || errors.As(err, &de) && de.IsTimeout
	// Without internet an internet name cannot resolve, so ask this
	// computer's DNS servers directly whether they answer at all. That is
	// a warning, not a failure: either there is no internet, or the DNS
	// server cannot reach it, and only the second is a DNS fault.
	server, reply, asked, aerr := r.localDNSAnswer(ctx, in, host)
	switch {
	case reply == dnsResolved && timedOut:
		return why(mk(2, model.HealthWarn, fmt.Sprintf("DNS is slow: %s did not resolve within 3 s, but DNS server %s resolved it when asked again", host, server), 0), model.ReasonSlow)
	case reply == dnsResolved:
		return why(mk(2, model.HealthWarn, fmt.Sprintf("this computer could not resolve %s (%s), but DNS server %s resolved it when asked directly: another DNS setting, such as a VPN, may be in the way", host, msg, server), 0), model.ReasonLocalMismatch)
	case reply == dnsNoSuchName:
		return why(mk(2, model.HealthWarn, fmt.Sprintf("DNS server %s answers, but says %s does not exist: check the internet check address in Settings, or a DNS filter may block it", server, host), 0), model.ReasonNXDomain)
	case server != "":
		// A note instead on a network with no internet on purpose
		// (v0.6-D15; see noInternet).
		return why(mk(2, model.HealthWarn, fmt.Sprintf("could not resolve %s, but DNS server %s answers: no internet connection, or the DNS server cannot reach the internet (%s)", host, server, msg), 0), model.ReasonNoInternet)
	case len(asked) > 0:
		return why(mk(2, model.HealthFail, fmt.Sprintf("could not resolve %s: no reply from DNS server %s (%s)", host, strings.Join(asked, ", "), msg), 0), model.ReasonNoReply)
	case aerr != nil:
		return why(mk(2, model.HealthFail, fmt.Sprintf("could not resolve %s: %s", host, msg), 0), model.ReasonError)
	}
	return why(mk(2, model.HealthFail, fmt.Sprintf("could not resolve %s: no DNS server is set on this network adapter (%s)", host, msg), 0), model.ReasonNoServer)
}

func (r *Runner) checkInternet(ctx context.Context, in Input) model.HealthCheck {
	u := in.Config.InternetURL
	if strings.TrimSpace(u) == "" {
		u = DefaultInternetURL
	}
	// The check is never more than a note; on a network with no internet
	// on purpose its results say so (v0.6-D15; see noInternet).
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return why(mk(4, model.HealthInfo, "invalid internet check URL", 0), model.ReasonInvalid)
	}
	t0 := time.Now()
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return why(mk(4, model.HealthInfo, "internet check got no response (local checks are unaffected)", 0), model.ReasonNoResponse)
	}
	resp.Body.Close()
	d := time.Since(t0)
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return mk(4, model.HealthOK, fmt.Sprintf("internet reachable (HTTP %d, %.0f ms)", resp.StatusCode, ms(d)), ms(d))
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return why(mk(4, model.HealthInfo, fmt.Sprintf("redirected (HTTP %d): possible captive portal", resp.StatusCode), ms(d)), model.ReasonPortal)
	default:
		c := why(mk(4, model.HealthInfo, fmt.Sprintf("internet check returned HTTP %d", resp.StatusCode), ms(d)), model.ReasonStatus)
		c.Data["status"] = resp.StatusCode
		return c
	}
}

func (r *Runner) checkWiFi(ctx context.Context, in Input) model.HealthCheck {
	key := in.Iface + "|" + in.IfaceIP
	r.mu.Lock()
	var w WiFiInfo
	var err error
	if r.wifiKey == key && !r.wifiAt.IsZero() && r.Now().Sub(r.wifiAt) < wifiCacheTTL {
		w, err = r.wifiInfo, r.wifiErr
		r.mu.Unlock()
	} else {
		r.mu.Unlock()
		w, err = r.WiFi(ctx, in.Iface)
		if ctx.Err() == nil {
			r.mu.Lock()
			r.wifiKey, r.wifiAt, r.wifiInfo, r.wifiErr = key, r.Now(), w, err
			r.mu.Unlock()
		}
	}
	if err != nil {
		reason := model.ReasonError
		if errors.Is(err, ErrLocationOff) {
			reason = model.ReasonHiddenLocation
		}
		return why(mk(5, model.HealthUnavailable, err.Error(), 0), reason)
	}
	if !w.Connected {
		return why(mk(5, model.HealthInfo, "not connected to Wi-Fi (wired or Wi-Fi off)", 0), model.ReasonNotConnected)
	}
	c := mk(5, model.HealthOK, "", 0)
	ssid := w.SSID
	if ssid == "" {
		ssid = "(name hidden by OS)"
	}
	parts := []string{ssid}
	data := map[string]any{"ssid": w.SSID}
	if w.Redacted {
		data["redacted"] = true
	}
	if w.BSSID != "" {
		data["bssid"] = w.BSSID
	}
	if w.SignalPct > 0 || w.RSSI != 0 {
		parts = append(parts, fmt.Sprintf("signal %d%%", w.SignalPct))
		data["signalPct"] = w.SignalPct
		data["rssi"] = w.RSSI
		if w.SignalPct < 40 {
			c.Status = model.HealthWarn
			data["reason"] = model.ReasonWeak
		}
	}
	if w.Channel > 0 {
		s := fmt.Sprintf("channel %d", w.Channel)
		if w.Band != "" {
			s += " (" + w.Band + ")"
		}
		parts = append(parts, s)
		data["channel"] = w.Channel
	}
	if w.Band != "" {
		data["band"] = w.Band
	}
	if w.TxRateMbps > 0 {
		parts = append(parts, fmt.Sprintf("%.0f Mbps", w.TxRateMbps))
		data["txRateMbps"] = w.TxRateMbps
	}
	c.Detail = strings.Join(parts, ", ")
	c.Data = data
	return c
}

func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return u.Hostname()
}
