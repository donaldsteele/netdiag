// Package remote opens a remote desktop or screen sharing session to a
// device, using the programs this computer already has: Remote Desktop
// Connection on Windows, Microsoft's Windows App and Screen Sharing on macOS.
// It only starts them when the person asks; it never logs in.
package remote

import (
	"errors"
	"fmt"
	"net/netip"

	"netdiag/internal/model"
)

// Kind is a remote access service.
type Kind string

const (
	RDP Kind = "rdp" // Windows Remote Desktop
	VNC Kind = "vnc" // VNC screen sharing
	SSH Kind = "ssh" // SSH: shown for the IT helper, never opened
)

// Port returns the TCP port of a kind, or 0 for an unknown kind.
func Port(k Kind) int {
	switch k {
	case RDP:
		return model.PortRDP
	case VNC:
		return model.PortVNC
	case SSH:
		return model.PortSSH
	}
	return 0
}

// Caps says which kinds this computer can open, and with which program.
type Caps struct {
	Platform string `json:"platform"` // "windows", "darwin" or other GOOS
	RDP      bool   `json:"rdp"`
	RDPApp   string `json:"rdpApp,omitempty"`
	VNC      bool   `json:"vnc"`
	VNCApp   string `json:"vncApp,omitempty"`
}

// ErrNoApp means this computer has no program that opens the session.
var ErrNoApp = errors.New("no program on this computer can open this")

// ErrNotOpenable means the kind is never opened (SSH) or is unknown.
var ErrNotOpenable = errors.New("this kind of connection is not opened by netdiag")

// Opener starts a session. The zero value of System uses the platform.
type Opener interface {
	Caps() Caps
	Open(k Kind, ip string, port int) error
}

// System is the Opener for this computer.
type System struct{}

// Caps reports what this computer can open.
func (System) Caps() Caps { return platformCaps() }

// Open starts the session after checking its arguments. ip must be a plain
// IPv4 or IPv6 address (no name), which keeps it safe to pass on.
func (System) Open(k Kind, ip string, port int) error {
	a, err := netip.ParseAddr(ip)
	if err != nil || a.Zone() != "" {
		return fmt.Errorf("not an IP address: %q", ip)
	}
	if !Local(a) {
		return fmt.Errorf("%s is not on a local network", ip)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("bad port %d", port)
	}
	switch k {
	case RDP, VNC:
		return platformOpen(k, a, port)
	}
	return ErrNotOpenable
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// Local reports whether a is a private, link-local or carrier-grade NAT
// address: sessions are only opened to devices on a local network.
func Local(a netip.Addr) bool {
	return a.IsPrivate() || a.IsLinkLocalUnicast() || cgnat.Contains(a)
}

// hostPort formats the address for a client: the bare IP on the default
// port, ip:port (or [ip]:port) otherwise.
func hostPort(a netip.Addr, port, def int) string {
	if port == def {
		return a.String()
	}
	return netip.AddrPortFrom(a, uint16(port)).String()
}

// RDPFile returns a Remote Desktop connection file for ip. It asks for the
// user name and password when it opens and sets nothing else.
func RDPFile(ip string, port int) ([]byte, error) {
	a, err := netip.ParseAddr(ip)
	if err != nil || a.Zone() != "" {
		return nil, fmt.Errorf("not an IP address: %q", ip)
	}
	return []byte("full address:s:" + hostPort(a, port, model.PortRDP) + "\r\n" +
		"prompt for credentials:i:1\r\n" +
		"screen mode id:i:2\r\n"), nil
}
