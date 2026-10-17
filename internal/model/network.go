package model

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/netip"
	"strings"
)

// NetworkID names a network without its Wi-Fi name: "n-" and the first 12
// hex digits of the SHA-256 of "cidr|gateway|gatewayMAC", where cidr is
// this computer's network (its adapter's address prefix, such as
// 192.168.1.0/24), gateway the router's address and gatewayMAC the
// router's MAC. Two offices that both use 192.168.1.1 have different
// routers, so different ids.
//
// The "This network has no internet" switch (v0.6-D15) stores these ids in
// Settings.NoInternetNetworks, and health reports carry the id of the
// network they checked. The network fingerprint (v0.6 M6,
// network-profile.json) uses the same id.
func NetworkID(cidr, gateway, gatewayMAC string) string {
	if p, err := netip.ParsePrefix(strings.TrimSpace(cidr)); err == nil {
		cidr = p.Masked().String()
	}
	if a, err := netip.ParseAddr(strings.TrimSpace(gateway)); err == nil {
		gateway = a.String()
	}
	if hw, err := net.ParseMAC(strings.TrimSpace(gatewayMAC)); err == nil {
		gatewayMAC = strings.ToUpper(hw.String())
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(cidr) + "|" + strings.TrimSpace(gateway) + "|" + strings.TrimSpace(gatewayMAC)))
	return "n-" + hex.EncodeToString(sum[:])[:12]
}

// ValidNetworkID reports whether s has the form NetworkID returns.
func ValidNetworkID(s string) bool {
	if len(s) != 14 || !strings.HasPrefix(s, "n-") {
		return false
	}
	for _, c := range s[2:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// MaxNoInternetNetworks is how many networks the "This network has no
// internet" switch remembers; turning it on for one more forgets the
// oldest.
const MaxNoInternetNetworks = 20
