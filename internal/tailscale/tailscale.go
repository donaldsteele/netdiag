// Package tailscale reads this computer's Tailscale state from the local
// Tailscale service (its LocalAPI): whether it is running, the tailnet, every
// device on it with its addresses, OS, online state and the path traffic takes
// to it (direct or relayed through a DERP server), and it can ping one device
// through Tailscale. It is read-only: netdiag never changes Tailscale's
// settings.
//
// It talks to the service over local IPC only and starts no process: a named
// pipe on Windows (netdiag starts no child processes there), and on macOS the
// service's Unix socket or the Tailscale app's local port and token. A
// computer without Tailscale reports StateNotInstalled.
package tailscale

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"
)

// States of the Tailscale service on this computer.
const (
	StateNotInstalled = "not-installed" // no Tailscale service found
	StateStopped      = "stopped"       // installed, but not running or disconnected
	StateNeedsLogin   = "needs-login"   // needs someone to sign in
	StateRunning      = "running"       // connected to a tailnet
)

// Paths traffic can take to a device.
const (
	PathDirect  = "direct"  // straight to the device (CurAddr)
	PathRelayed = "relayed" // through a Tailscale DERP relay
	PathIdle    = "idle"    // online, but no traffic lately, so no path yet
	PathOffline = "offline" // the device is offline
)

// Device is one device on the tailnet (or this computer, as Status.Self).
type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`              // host name
	DNSName  string    `json:"dnsName,omitempty"` // MagicDNS name, no trailing dot
	OS       string    `json:"os,omitempty"`
	IPs      []string  `json:"ips"`
	Online   bool      `json:"online"`
	Active   bool      `json:"active"`             // traffic in the last few minutes
	LastSeen time.Time `json:"lastSeen,omitzero"`  // when it was last online
	Path     string    `json:"path"`               // PathDirect, PathRelayed, PathIdle or PathOffline
	Endpoint string    `json:"endpoint,omitempty"` // the direct address, for PathDirect
	Relay    string    `json:"relay,omitempty"`    // the DERP region code, for example "nyc"
	// KeyExpiry is when the device's key expires; zero when it never does.
	KeyExpiry      time.Time `json:"keyExpiry,omitzero"`
	ExitNode       bool      `json:"exitNode,omitempty"`       // this computer sends its internet traffic through it
	ExitNodeOption bool      `json:"exitNodeOption,omitempty"` // it offers itself as an exit node
	SubnetRoutes   []string  `json:"subnetRoutes,omitempty"`   // networks it routes for the tailnet
	Tags           []string  `json:"tags,omitempty"`
}

// Status is the Tailscale state of this computer and its tailnet.
type Status struct {
	State    string   `json:"state"`
	Version  string   `json:"version,omitempty"`
	Tailnet  string   `json:"tailnet,omitempty"` // the tailnet's name
	MagicDNS bool     `json:"magicDNS"`
	Suffix   string   `json:"suffix,omitempty"` // MagicDNS suffix, for example tail1234.ts.net
	Self     *Device  `json:"self,omitempty"`
	Devices  []Device `json:"devices"` // the other devices, online first, then by name
	// Health are Tailscale's own warnings, in its words.
	Health []string `json:"health"`
	// CheckedAt is when this was read.
	CheckedAt time.Time `json:"checkedAt"`
}

// PingResult is the answer of a Tailscale ping to one device.
type PingResult struct {
	IP       string  `json:"ip"`
	Name     string  `json:"name,omitempty"`
	OK       bool    `json:"ok"`
	Error    string  `json:"error,omitempty"`
	Latency  float64 `json:"latencyMs,omitempty"` // milliseconds
	Path     string  `json:"path,omitempty"`      // PathDirect or PathRelayed
	Endpoint string  `json:"endpoint,omitempty"`  // the direct address
	Relay    string  `json:"relay,omitempty"`     // the DERP region code
}

// ErrNotInstalled means no Tailscale service answers on this computer.
var ErrNotInstalled = errors.New("Tailscale isn't running on this computer")

// dialer opens a connection to the local Tailscale service. Each platform
// supplies one (localapi_windows.go, localapi_darwin.go); token, when not
// empty, is sent as the password of HTTP basic auth.
type dialer func(ctx context.Context) (conn io.ReadWriteCloser, token string, err error)

// Client reads the local Tailscale service. The zero value uses this
// platform's connection.
type Client struct {
	dial dialer // nil: platformDial
	now  func() time.Time
}

// New returns a Client for this computer's Tailscale service.
func New() *Client { return &Client{} }

const (
	statusTimeout = 4 * time.Second
	pingTimeout   = 10 * time.Second
	maxBody       = 8 << 20
)

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// call sends one LocalAPI request and decodes the JSON answer into out.
func (c *Client) call(ctx context.Context, method, path string, out any) error {
	d := c.dial
	if d == nil {
		d = platformDial
	}
	conn, token, err := d(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// The connection has no deadlines of its own (a Windows pipe opened for
	// blocking I/O), so a context end closes it to unblock the read.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	req, err := http.NewRequest(method, "http://local-tailscaled.sock"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Connection", "close")
	if token != "" {
		req.SetBasicAuth("", token)
	}
	if err := req.Write(conn); err != nil {
		return fmt.Errorf("tailscale: %w", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("tailscale: %w", ctx.Err())
		}
		return fmt.Errorf("tailscale: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("tailscale: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(string(b))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("tailscale: %s: %s", resp.Status, msg)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("tailscale: unexpected answer: %w", err)
	}
	return nil
}

// Status reads the Tailscale state. A computer without a running Tailscale
// service gets Status{State: StateNotInstalled} and no error.
func (c *Client) Status(ctx context.Context) (Status, error) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	var raw rawStatus
	err := c.call(ctx, http.MethodGet, "/localapi/v0/status", &raw)
	if errors.Is(err, ErrNotInstalled) {
		return Status{State: StateNotInstalled, Devices: []Device{}, Health: []string{}, CheckedAt: c.clock()}, nil
	}
	if err != nil {
		return Status{}, err
	}
	st := raw.convert()
	st.CheckedAt = c.clock()
	return st, nil
}

// Ping pings one tailnet device (a Tailscale address) through Tailscale and
// reports the latency and the path. It is aimed at one device and bounded by
// pingTimeout; callers rate-limit it.
func (c *Client) Ping(ctx context.Context, ip string) (PingResult, error) {
	a, err := netip.ParseAddr(ip)
	if err != nil || !isTailscaleAddr(a) {
		return PingResult{}, fmt.Errorf("%q is not a Tailscale address (100.64.0.0/10 or fd7a:115c:a1e0::/48)", ip)
	}
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	var raw rawPing
	q := url.Values{"ip": {a.String()}, "type": {"disco"}}
	if err := c.call(ctx, http.MethodPost, "/localapi/v0/ping?"+q.Encode(), &raw); err != nil {
		return PingResult{}, err
	}
	res := PingResult{IP: a.String(), Name: raw.NodeName, Error: raw.Err}
	if raw.Err == "" {
		res.OK = true
		res.Latency = raw.LatencySeconds * 1000
		switch {
		case raw.Endpoint != "":
			res.Path, res.Endpoint = PathDirect, raw.Endpoint
		case raw.DERPRegionCode != "":
			res.Path, res.Relay = PathRelayed, raw.DERPRegionCode
		case raw.PeerRelay != "":
			res.Path, res.Relay = PathRelayed, raw.PeerRelay
		}
	}
	return res, nil
}

var (
	tsV4 = netip.MustParsePrefix("100.64.0.0/10")
	tsV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

func isTailscaleAddr(a netip.Addr) bool {
	a = a.Unmap()
	return tsV4.Contains(a) || tsV6.Contains(a)
}

// rawStatus is the part of the LocalAPI status netdiag reads.
type rawStatus struct {
	Version        string             `json:"Version"`
	BackendState   string             `json:"BackendState"`
	MagicDNSSuffix string             `json:"MagicDNSSuffix"`
	Health         []string           `json:"Health"`
	Self           *rawPeer           `json:"Self"`
	Peer           map[string]rawPeer `json:"Peer"`
	CurrentTailnet *struct {
		Name            string `json:"Name"`
		MagicDNSSuffix  string `json:"MagicDNSSuffix"`
		MagicDNSEnabled bool   `json:"MagicDNSEnabled"`
	} `json:"CurrentTailnet"`
}

type rawPeer struct {
	ID             string     `json:"ID"`
	HostName       string     `json:"HostName"`
	DNSName        string     `json:"DNSName"`
	OS             string     `json:"OS"`
	TailscaleIPs   []string   `json:"TailscaleIPs"`
	Online         bool       `json:"Online"`
	Active         bool       `json:"Active"`
	LastSeen       time.Time  `json:"LastSeen"`
	Relay          string     `json:"Relay"`
	CurAddr        string     `json:"CurAddr"`
	KeyExpiry      *time.Time `json:"KeyExpiry"`
	ExitNode       bool       `json:"ExitNode"`
	ExitNodeOption bool       `json:"ExitNodeOption"`
	PrimaryRoutes  []string   `json:"PrimaryRoutes"`
	Tags           []string   `json:"Tags"`
}

type rawPing struct {
	Err            string  `json:"Err"`
	LatencySeconds float64 `json:"LatencySeconds"`
	Endpoint       string  `json:"Endpoint"`
	DERPRegionCode string  `json:"DERPRegionCode"`
	PeerRelay      string  `json:"PeerRelay"`
	NodeName       string  `json:"NodeName"`
}

func (r rawStatus) convert() Status {
	st := Status{Version: r.Version, Suffix: r.MagicDNSSuffix, Devices: []Device{}, Health: []string{}}
	switch r.BackendState {
	case "Running":
		st.State = StateRunning
	case "NeedsLogin", "NeedsMachineAuth":
		st.State = StateNeedsLogin
	default: // Stopped, Starting, NoState
		st.State = StateStopped
	}
	if r.CurrentTailnet != nil {
		st.Tailnet, st.MagicDNS = r.CurrentTailnet.Name, r.CurrentTailnet.MagicDNSEnabled
		if st.Suffix == "" {
			st.Suffix = r.CurrentTailnet.MagicDNSSuffix
		}
	}
	for _, h := range r.Health {
		if h = strings.TrimSpace(h); h != "" {
			st.Health = append(st.Health, h)
		}
	}
	if r.Self != nil {
		d := r.Self.device()
		d.Online, d.Path = st.State == StateRunning, ""
		st.Self = &d
	}
	for _, p := range r.Peer {
		st.Devices = append(st.Devices, p.device())
	}
	sort.Slice(st.Devices, func(i, j int) bool {
		a, b := st.Devices[i], st.Devices[j]
		if a.Online != b.Online {
			return a.Online
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return st
}

func (p rawPeer) device() Device {
	d := Device{
		ID: p.ID, Name: p.HostName, DNSName: strings.TrimSuffix(p.DNSName, "."), OS: p.OS, IPs: []string{},
		Online: p.Online, Active: p.Active, Relay: p.Relay, ExitNode: p.ExitNode, ExitNodeOption: p.ExitNodeOption,
		SubnetRoutes: p.PrimaryRoutes, Tags: p.Tags,
	}
	for _, ip := range p.TailscaleIPs {
		if a, err := netip.ParseAddr(ip); err == nil {
			d.IPs = append(d.IPs, a.Unmap().String())
		}
	}
	if !p.LastSeen.IsZero() && p.LastSeen.Year() > 1 {
		d.LastSeen = p.LastSeen.UTC()
	}
	if p.KeyExpiry != nil && !p.KeyExpiry.IsZero() {
		d.KeyExpiry = p.KeyExpiry.UTC()
	}
	switch {
	case !p.Online:
		d.Path, d.Relay = PathOffline, ""
	case p.CurAddr != "":
		d.Path, d.Endpoint = PathDirect, p.CurAddr
	case p.Active && p.Relay != "":
		d.Path = PathRelayed
	default:
		d.Path = PathIdle
	}
	if d.Path != PathRelayed {
		d.Relay = ""
	}
	if d.Name == "" {
		d.Name, _, _ = strings.Cut(d.DNSName, ".")
	}
	return d
}
