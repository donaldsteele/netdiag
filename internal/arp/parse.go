package arp

import (
	"net"
	"regexp"
	"strings"
)

var (
	winRe = regexp.MustCompile(`^\s*(\d{1,3}(?:\.\d{1,3}){3})\s+([0-9a-fA-F]{1,2}(?:-[0-9a-fA-F]{1,2}){5})\s+\S+`)
	macRe = regexp.MustCompile(`\((\d{1,3}(?:\.\d{1,3}){3})\) at ([0-9a-fA-F]{1,2}(?::[0-9a-fA-F]{1,2}){5})\b`)
)

// ParseWindows parses `arp -a` output from Windows.
func ParseWindows(s string) []Entry {
	var out []Entry
	for _, line := range strings.Split(s, "\n") {
		if m := winRe.FindStringSubmatch(line); m != nil {
			out = appendEntry(out, m[1], m[2])
		}
	}
	return out
}

// ParseDarwin parses `arp -an` output from macOS (incomplete rows are skipped).
func ParseDarwin(s string) []Entry {
	var out []Entry
	for _, line := range strings.Split(s, "\n") {
		if m := macRe.FindStringSubmatch(line); m != nil {
			out = appendEntry(out, m[1], m[2])
		}
	}
	return out
}

func appendEntry(out []Entry, ip, mac string) []Entry {
	pip := net.ParseIP(ip).To4()
	if pip == nil || pip.IsMulticast() || pip.Equal(net.IPv4bcast) || pip.IsUnspecified() {
		return out
	}
	m, ok := NormalizeMAC(mac)
	if !ok {
		return out
	}
	return append(out, Entry{IP: pip.String(), MAC: m})
}

// NormalizeMAC converts "-" or ":" separated MACs (with possibly unpadded
// octets) into AA:BB:CC:DD:EE:FF. It returns false for all-zero, broadcast,
// and multicast addresses.
func NormalizeMAC(s string) (string, bool) {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == ':' })
	if len(parts) != 6 {
		return "", false
	}
	b := make([]byte, 6)
	for i, p := range parts {
		if len(p) == 1 {
			p = "0" + p
		}
		if len(p) != 2 {
			return "", false
		}
		hw, err := net.ParseMAC("00:00:00:00:00:" + p)
		if err != nil {
			return "", false
		}
		b[i] = hw[5]
	}
	allZero, allFF := true, true
	for _, v := range b {
		allZero = allZero && v == 0
		allFF = allFF && v == 0xff
	}
	if allZero || allFF || b[0]&1 == 1 {
		return "", false
	}
	return strings.ToUpper(net.HardwareAddr(b).String()), true
}
