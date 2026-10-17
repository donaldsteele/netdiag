// Package health runs the local network health checks shown in the health
// panel: gateway, DHCP, DNS, web server, internet and Wi-Fi.
package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"netdiag/internal/discovery"
	"netdiag/internal/model"
)

const (
	// OverallTimeout caps a whole Run.
	OverallTimeout = 10 * time.Second
	// DefaultInternetURL is used when Config.InternetURL is empty.
	DefaultInternetURL = "http://connectivitycheck.gstatic.com/generate_204"

	wifiCacheTTL = 60 * time.Second
	maxBody      = 256 << 10
)

// Config is the settings subset the health checks need.
type Config struct {
	WebURL       string
	MustContain  string
	ExpectStatus int // 0 -> 200
	InternetURL  string
}

// Input describes what to check.
type Input struct {
	Iface   string // interface name, e.g. "en0" (may be empty)
	IfaceIP string // interface IPv4 (used to find the adapter on Windows)
	Gateway string // default gateway IPv4 (may be empty)
	Config  Config
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
}

var checkMeta = []struct{ id, label string }{
	{"gateway", "Gateway reachable"},
	{"dhcp", "DHCP lease"},
	{"dns", "DNS resolving"},
	{"webserver", "Web server"},
	{"internet", "Internet reachability"},
	{"wifi", "Wi-Fi"},
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
					ch <- res{i, mk(i, model.HealthUnavailable, fmt.Sprintf("check crashed: %v", p), 0)}
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
			out[i] = mk(i, model.HealthUnavailable, "timed out", 0)
		}
	}
	return model.HealthReport{CheckedAt: r.Now().UTC().Format(time.RFC3339), Checks: out}
}

func mk(i int, status, detail string, ms float64) model.HealthCheck {
	return model.HealthCheck{ID: checkMeta[i].id, Label: checkMeta[i].label, Status: status, Detail: detail, LatencyMs: ms}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func (r *Runner) checkGateway(ctx context.Context, in Input) model.HealthCheck {
	if in.Gateway == "" {
		return mk(0, model.HealthUnavailable, "no default gateway detected", 0)
	}
	ip, err := netip.ParseAddr(in.Gateway)
	if err != nil {
		return mk(0, model.HealthUnavailable, "invalid gateway address "+in.Gateway, 0)
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
			}
			return c
		}
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr != nil {
		return mk(0, model.HealthUnavailable, "ping failed locally: "+lastErr.Error(), 0)
	}
	return mk(0, model.HealthFail, in.Gateway+" did not reply to ping (it may block ICMP)", 0)
}

func (r *Runner) checkDHCP(ctx context.Context, in Input) model.HealthCheck {
	info, err := r.DHCP(ctx, in.Iface, in.IfaceIP)
	if err != nil {
		return mk(1, model.HealthUnavailable, err.Error(), 0)
	}
	c := mk(1, model.HealthInfo, "", 0)
	c.Data = map[string]any{"enabled": info.Enabled, "apipa": info.APIPA}
	switch {
	case info.APIPA:
		c.Status = model.HealthFail
		c.Detail = "self-assigned 169.254.x.x address: no DHCP server answered"
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
		return mk(2, model.HealthFail, fmt.Sprintf("could not resolve %s: %s", host, msg), 0)
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
		return mk(2, model.HealthWarn, fmt.Sprintf("DNS is slow: %s did not resolve within 3 s, but DNS server %s resolved it when asked again", host, server), 0)
	case reply == dnsResolved:
		return mk(2, model.HealthWarn, fmt.Sprintf("this computer could not resolve %s (%s), but DNS server %s resolved it when asked directly: another DNS setting, such as a VPN, may be in the way", host, msg, server), 0)
	case reply == dnsNoSuchName:
		return mk(2, model.HealthWarn, fmt.Sprintf("DNS server %s answers, but says %s does not exist: check the internet check address in Settings, or a DNS filter may block it", server, host), 0)
	case server != "":
		return mk(2, model.HealthWarn, fmt.Sprintf("could not resolve %s, but DNS server %s answers: no internet connection, or the DNS server cannot reach the internet (%s)", host, server, msg), 0)
	case len(asked) > 0:
		return mk(2, model.HealthFail, fmt.Sprintf("could not resolve %s: no reply from DNS server %s (%s)", host, strings.Join(asked, ", "), msg), 0)
	case aerr != nil:
		return mk(2, model.HealthFail, fmt.Sprintf("could not resolve %s: %s", host, msg), 0)
	}
	return mk(2, model.HealthFail, fmt.Sprintf("could not resolve %s: no DNS server is set on this network adapter (%s)", host, msg), 0)
}

func (r *Runner) checkWeb(ctx context.Context, in Input) model.HealthCheck {
	cfg := in.Config
	if strings.TrimSpace(cfg.WebURL) == "" {
		return mk(3, model.HealthInfo, "no web server configured (set one in Settings)", 0)
	}
	want := cfg.ExpectStatus
	if want == 0 {
		want = 200
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.WebURL, nil)
	if err != nil {
		return mk(3, model.HealthFail, "invalid URL: "+err.Error(), 0)
	}
	t0 := time.Now()
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return mk(3, model.HealthFail, "request failed: "+err.Error(), 0)
	}
	defer resp.Body.Close()
	var body []byte
	if cfg.MustContain != "" {
		body, _ = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	}
	d := time.Since(t0)
	c := mk(3, model.HealthOK, fmt.Sprintf("HTTP %d in %.0f ms", resp.StatusCode, ms(d)), ms(d))
	c.Data = map[string]any{"url": cfg.WebURL, "status": resp.StatusCode}
	switch {
	case resp.StatusCode != want:
		c.Status = model.HealthFail
		c.Detail = fmt.Sprintf("HTTP %d (expected %d)", resp.StatusCode, want)
	case cfg.MustContain != "" && !strings.Contains(string(body), cfg.MustContain):
		c.Status = model.HealthFail
		c.Detail = fmt.Sprintf("HTTP %d but page does not contain %q", resp.StatusCode, cfg.MustContain)
	case d > 2*time.Second:
		c.Status = model.HealthWarn
		c.Detail += " (slow)"
	}
	return c
}

func (r *Runner) checkInternet(ctx context.Context, in Input) model.HealthCheck {
	u := in.Config.InternetURL
	if strings.TrimSpace(u) == "" {
		u = DefaultInternetURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return mk(4, model.HealthInfo, "invalid internet check URL", 0)
	}
	t0 := time.Now()
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return mk(4, model.HealthInfo, "internet check got no response (local checks are unaffected)", 0)
	}
	resp.Body.Close()
	d := time.Since(t0)
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return mk(4, model.HealthOK, fmt.Sprintf("internet reachable (HTTP %d, %.0f ms)", resp.StatusCode, ms(d)), ms(d))
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return mk(4, model.HealthInfo, fmt.Sprintf("redirected (HTTP %d): possible captive portal", resp.StatusCode), ms(d))
	default:
		return mk(4, model.HealthInfo, fmt.Sprintf("internet check returned HTTP %d", resp.StatusCode), ms(d))
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
		return mk(5, model.HealthUnavailable, err.Error(), 0)
	}
	if !w.Connected {
		return mk(5, model.HealthInfo, "not connected to Wi-Fi (wired or Wi-Fi off)", 0)
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
