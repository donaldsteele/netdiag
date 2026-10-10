package netinfo

import (
	"net/netip"
	"regexp"
	"sort"
)

// Iface describes a usable local IPv4 interface.
type Iface struct {
	Name      string `json:"name"`
	IP        string `json:"ip"`
	CIDR      string `json:"cidr"`
	MAC       string `json:"mac"`
	IsDefault bool   `json:"isDefault"`
	// Suggested is the default scan target for this interface (see SuggestTarget).
	Suggested string `json:"suggested"`
}

var virtualRe = regexp.MustCompile(`(?i)vEthernet|VMware|VirtualBox|docker|utun|awdl|llw|bridge|Hyper-V|Loopback|WSL|Tailscale`)

// Rank orders interfaces: default-route interface first, then RFC1918
// addresses, then the rest. Virtual interfaces are dropped unless that
// would leave nothing. IsDefault is set on the default interface.
func Rank(in []Iface, defaultIfaceName string) []Iface {
	all := make([]Iface, len(in))
	copy(all, in)
	var kept []Iface
	for i := range all {
		all[i].IsDefault = defaultIfaceName != "" && all[i].Name == defaultIfaceName
		if !virtualRe.MatchString(all[i].Name) || all[i].IsDefault {
			kept = append(kept, all[i])
		}
	}
	if len(kept) == 0 {
		kept = all
	}
	score := func(f Iface) int {
		s := 0
		if f.IsDefault {
			s += 2
		}
		if a, err := netip.ParseAddr(f.IP); err == nil && a.IsPrivate() {
			s++
		}
		return s
	}
	sort.SliceStable(kept, func(i, j int) bool { return score(kept[i]) > score(kept[j]) })
	return kept
}
