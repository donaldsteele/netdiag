package chat

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"netdiag/internal/bubbles"
)

// The ports a bubbles server listens on unless its admin changed them: the
// HTTPS API and the plain-HTTP onboarding listener that hands out the CA.
const (
	DefaultPort        = 28225
	DefaultOnboardPort = 28226
)

// Target is a chat server address netdiag may pair with.
type Target struct {
	Host        string // an IPv4 address or a name ending in .local
	Port        int    // the HTTPS port
	OnboardPort int    // the onboarding (CA download) port
}

// BaseURL is the HTTPS address of the server.
func (t Target) BaseURL() string { return "https://" + t.hostPort(t.Port) }

// OnboardingURL is the plain-HTTP address that serves the CA.
func (t Target) OnboardingURL() string { return "http://" + t.hostPort(t.OnboardPort) }

// Address is host:port as the page shows and sends it back.
func (t Target) Address() string { return t.hostPort(t.Port) }

// AdminPairingURL is the page of bubbles' admin area where the admin
// approves netdiag (Admin > Pairing).
func (t Target) AdminPairingURL() string { return t.BaseURL() + "/admin/pairing" }

func (t Target) hostPort(p int) string { return net.JoinHostPort(t.Host, strconv.Itoa(p)) }

// errTarget is the answer for an address netdiag will not pair with.
var errTarget = errors.New("Use the address of a chat server on the local network, for example 192.168.1.20 or office.local.")

// ParseTarget reads an address the person typed, or one discovery found:
// host, host:port or https://host:port. The port is the HTTPS port (28225
// when there is none). onboard is the onboarding port when discovery knows
// it (the TXT "onboard" key); otherwise it is 28226, or the HTTPS port + 1
// when a port was given, as bubbles' own two defaults are (v0.6-D35).
//
// Only local addresses are accepted (4.10 "Pairing targets"): private,
// link-local, shared (100.64.0.0/10) and loopback IPv4 addresses, and
// names ending in .local. A chat server on the internet is never paired
// with, so the token never leaves the local network.
func ParseTarget(address string, onboard int) (Target, error) {
	s := strings.TrimSpace(address)
	if i := strings.Index(s, "://"); i >= 0 {
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "https" && u.Scheme != "bubbles") || u.Host == "" {
			return Target{}, errTarget
		}
		s = u.Host
	}
	s = strings.TrimSuffix(s, "/")
	host, portText := s, ""
	if h, p, err := net.SplitHostPort(s); err == nil {
		host, portText = h, p
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if !LocalHost(host) {
		return Target{}, errTarget
	}
	t := Target{Host: host, Port: DefaultPort, OnboardPort: DefaultOnboardPort}
	if portText != "" {
		p, err := strconv.Atoi(portText)
		if err != nil || p < 1 || p > 65535 {
			return Target{}, errors.New("The port must be a number from 1 to 65535.")
		}
		t.Port, t.OnboardPort = p, p+1
		if p == 65535 {
			t.OnboardPort = DefaultOnboardPort
		}
	}
	if onboard > 0 && onboard <= 65535 {
		t.OnboardPort = onboard
	}
	return t, nil
}

// LocalHost reports whether host is one netdiag pairs with: an IPv4
// address that is private, link-local, shared (CGNAT) or loopback, or a
// .local name.
func LocalHost(host string) bool {
	if a, err := netip.ParseAddr(host); err == nil {
		return LocalAddr(a)
	}
	name, ok := strings.CutSuffix(host, ".local")
	if !ok || name == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// LocalAddr reports whether a is a local IPv4 address (see LocalHost).
func LocalAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.Is4() {
		return false
	}
	return a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLoopback() || cgnat.Contains(a)
}

// targetOf is the Target of a server netdiag already knows (bubbles.json,
// a connection string or the bubbles tray's config), or an error when its
// address is not a local one.
func targetOf(baseURL, onboardingURL string) (Target, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" {
		return Target{}, errTarget
	}
	onboard := 0
	if o, err := url.Parse(onboardingURL); err == nil && strings.EqualFold(o.Hostname(), u.Hostname()) {
		onboard, _ = strconv.Atoi(o.Port())
		if onboard == 0 && o.Scheme == "http" {
			onboard = 80
		}
	}
	return ParseTarget(u.Host, onboard)
}

// fingerprintGroups splits a fingerprint into the 16 groups of four the
// bubbles admin pages show (Admin > Overview).
func fingerprintGroups(fp string) []string {
	return strings.Fields(bubbles.FormatFingerprint(fp))
}
