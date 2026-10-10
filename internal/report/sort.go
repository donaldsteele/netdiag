// Package report exports a ScanResult as JSON or CSV.
package report

import (
	"encoding/binary"
	"net"
	"sort"

	"netdiag/internal/model"
)

// SortHosts returns a copy of hosts ordered by numeric IPv4 address. Entries
// whose IP does not parse as IPv4 sort after all valid addresses, ordered by
// string. The input slice is never modified.
func SortHosts(hosts []model.Host) []model.Host {
	out := make([]model.Host, len(hosts))
	copy(out, hosts)
	sort.SliceStable(out, func(i, j int) bool {
		return ipLess(out[i].IP, out[j].IP)
	})
	return out
}

func ipKey(s string) (uint32, bool) {
	ip := net.ParseIP(s).To4()
	if ip == nil {
		return 0, false
	}
	return binary.BigEndian.Uint32(ip), true
}

func ipLess(a, b string) bool {
	ka, okA := ipKey(a)
	kb, okB := ipKey(b)
	switch {
	case okA && okB:
		return ka < kb
	case okA != okB:
		return okA
	default:
		return a < b
	}
}
