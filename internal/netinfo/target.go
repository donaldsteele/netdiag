// Package netinfo enumerates local interfaces and parses scan targets.
package netinfo

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// MaxHosts is the hard cap on targets per scan (a /22).
const MaxHosts = 1024

// ErrRangeTooLarge is returned when a target expands to more than MaxHosts.
var ErrRangeTooLarge = errors.New("target range too large (max /22, 1024 hosts)")

// ParseTarget parses a CIDR, dash range, short dash range or single IPv4
// address into a sorted, de-duplicated address list.
func ParseTarget(s string) ([]netip.Addr, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty target")
	}
	var out []netip.Addr
	var err error
	switch {
	case strings.Contains(s, "/"):
		out, err = parseCIDR(s)
	case strings.Contains(s, "-"):
		out, err = parseRange(s)
	default:
		var a netip.Addr
		if a, err = parseV4(s); err == nil {
			out = []netip.Addr{a}
		}
	}
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, netip.Addr.Compare)
	return slices.Compact(out), nil
}

func parseV4(s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return a, fmt.Errorf("invalid IP %q", s)
	}
	if !a.Is4() {
		return a, fmt.Errorf("only IPv4 is supported: %q", s)
	}
	return a, nil
}

func parseCIDR(s string) ([]netip.Addr, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q", s)
	}
	if !p.Addr().Is4() {
		return nil, fmt.Errorf("only IPv4 is supported: %q", s)
	}
	p = p.Masked()
	if 32-p.Bits() > 10 {
		return nil, ErrRangeTooLarge
	}
	var out []netip.Addr
	for a := p.Addr(); p.Contains(a); a = a.Next() {
		out = append(out, a)
	}
	if p.Bits() <= 30 {
		out = out[1 : len(out)-1]
	}
	return out, nil
}

func parseRange(s string) ([]netip.Addr, error) {
	l, r, _ := strings.Cut(s, "-")
	start, err := parseV4(l)
	if err != nil {
		return nil, err
	}
	var end netip.Addr
	r = strings.TrimSpace(r)
	if strings.Contains(r, ".") {
		end, err = parseV4(r)
		if err != nil {
			return nil, err
		}
	} else {
		n, perr := strconv.Atoi(r)
		if perr != nil || n < 0 || n > 255 {
			return nil, fmt.Errorf("invalid range end %q", r)
		}
		b := start.As4()
		b[3] = byte(n)
		end = netip.AddrFrom4(b)
	}
	if end.Compare(start) < 0 {
		return nil, fmt.Errorf("range end %s is before start %s", end, start)
	}
	s4, e4 := start.As4(), end.As4()
	size := int(beU32(e4)) - int(beU32(s4)) + 1
	if size > MaxHosts {
		return nil, ErrRangeTooLarge
	}
	out := make([]netip.Addr, 0, size)
	for a := start; a.Compare(end) <= 0; a = a.Next() {
		out = append(out, a)
	}
	return out, nil
}

func beU32(b [4]byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
