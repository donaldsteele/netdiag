// Package arp reads the OS ARP cache and parses it into IP/MAC entries.
package arp

import (
	"context"
	"net"
	"time"
)

// Entry is one resolved ARP table row.
type Entry struct {
	IP  string // dotted IPv4
	MAC string // normalized AA:BB:CC:DD:EE:FF
	// Iface is the IPv4 address of the Windows adapter the row belongs to
	// (the "Interface:" line of `arp -a`). Empty on darwin/other.
	Iface string
	// Type is "dynamic" or "static" on Windows. Empty on darwin/other.
	Type string
}

const readTimeout = 2 * time.Second

// Table reads the OS ARP cache (2s timeout) and returns valid entries,
// deduplicated to one row per IP (see Dedupe). The preferred interface is
// chosen by defaultIface; use TableFor when the scanned interface is known.
func Table(ctx context.Context) ([]Entry, error) {
	return TableFor(ctx, "")
}

// TableFor is Table with an explicit preferred interface: when an IP is
// listed under several Windows adapters, the row from the adapter whose
// address is ifaceIP wins. An empty ifaceIP falls back to defaultIface.
func TableFor(ctx context.Context, ifaceIP string) ([]Entry, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	entries, err := readTable(ctx)
	if err != nil {
		return nil, err
	}
	if ifaceIP == "" {
		ifaceIP = defaultIface(entries)
	}
	return Dedupe(entries, ifaceIP), nil
}

// Dedupe collapses entries that share an IP into one row per IP, keeping
// first-seen IP order. Windows lists an IP once per adapter, so duplicates
// are normal there. For each IP the candidate is chosen in order:
//
//  1. the entry whose Iface equals preferIfaceIP (when preferIfaceIP is set);
//  2. else the first entry with Type "dynamic";
//  3. else the first entry.
func Dedupe(entries []Entry, preferIfaceIP string) []Entry {
	var order []string
	groups := make(map[string][]Entry, len(entries))
	for _, e := range entries {
		if _, seen := groups[e.IP]; !seen {
			order = append(order, e.IP)
		}
		groups[e.IP] = append(groups[e.IP], e)
	}
	if len(order) == 0 {
		return nil
	}
	out := make([]Entry, 0, len(order))
	for _, ip := range order {
		out = append(out, pick(groups[ip], preferIfaceIP))
	}
	return out
}

// pick applies the Dedupe preference order to the candidates for one IP.
func pick(cands []Entry, preferIfaceIP string) Entry {
	if preferIfaceIP != "" {
		for _, c := range cands {
			if c.Iface == preferIfaceIP {
				return c
			}
		}
	}
	for _, c := range cands {
		if c.Type == "dynamic" {
			return c
		}
	}
	return cands[0]
}

// localIPv4s lists the host's IPv4 addresses. It is a variable so tests can
// substitute a fixed set.
var localIPv4s = func() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if ip4 := ipn.IP.To4(); ip4 != nil {
				out = append(out, ip4)
			}
		}
	}
	return out
}

// defaultIface returns the adapter IP (Entry.Iface) to prefer when the caller
// does not know the scanned interface: the first adapter (in table order)
// whose IP shares a /24 with a local IPv4 address, or "" if none does (or
// rows carry no adapter, as on darwin/other). Because an adapter's own
// address is local, this normally selects the first adapter; the /24 test
// guards against addresses that are no longer configured. Ties fall through
// to Dedupe's dynamic-then-first rule.
func defaultIface(entries []Entry) string {
	locals := localIPv4s()
	seen := make(map[string]bool)
	for _, e := range entries {
		if e.Iface == "" || seen[e.Iface] {
			continue
		}
		seen[e.Iface] = true
		blk := net.ParseIP(e.Iface).To4()
		if blk == nil {
			continue
		}
		for _, l := range locals {
			if l[0] == blk[0] && l[1] == blk[1] && l[2] == blk[2] {
				return e.Iface
			}
		}
	}
	return ""
}
