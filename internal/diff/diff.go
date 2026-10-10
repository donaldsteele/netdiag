// Package diff compares two scan results and exports the comparison.
package diff

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"netdiag/internal/model"
)

// SupplyThreshold is the minimum percent-point change reported for a supply.
const SupplyThreshold = 5

// Compare diffs cur against base. Hosts are matched by MAC when both sides
// have one, otherwise by IP.
func Compare(base, cur model.ScanResult) model.Diff {
	d := model.Diff{
		Base:    ref(base),
		Current: ref(cur),
		Added:   []model.Host{},
		Removed: []model.Host{},
		Changed: []model.DiffHostChange{},
	}
	bUsed := make([]bool, len(base.Hosts))
	cUsed := make([]bool, len(cur.Hosts))
	type pair struct{ b, c int }
	var pairs []pair

	// Pass 1: MAC.
	bByMAC := map[string]int{}
	for i, h := range base.Hosts {
		if m := normMAC(h.MAC); m != "" {
			if _, ok := bByMAC[m]; !ok {
				bByMAC[m] = i
			}
		}
	}
	for j, h := range cur.Hosts {
		m := normMAC(h.MAC)
		if m == "" {
			continue
		}
		if i, ok := bByMAC[m]; ok && !bUsed[i] {
			bUsed[i], cUsed[j] = true, true
			pairs = append(pairs, pair{i, j})
		}
	}
	// Pass 2: IP for the leftovers.
	bByIP := map[string]int{}
	for i, h := range base.Hosts {
		if !bUsed[i] {
			if _, ok := bByIP[h.IP]; !ok {
				bByIP[h.IP] = i
			}
		}
	}
	for j, h := range cur.Hosts {
		if cUsed[j] {
			continue
		}
		if i, ok := bByIP[h.IP]; ok && !bUsed[i] {
			bUsed[i], cUsed[j] = true, true
			pairs = append(pairs, pair{i, j})
		}
	}

	for i, h := range base.Hosts {
		if !bUsed[i] {
			d.Removed = append(d.Removed, h)
		}
	}
	for j, h := range cur.Hosts {
		if !cUsed[j] {
			d.Added = append(d.Added, h)
		}
	}
	for _, p := range pairs {
		b, c := base.Hosts[p.b], cur.Hosts[p.c]
		if ch := hostChanges(b, c); len(ch) > 0 {
			d.Changed = append(d.Changed, model.DiffHostChange{IP: c.IP, MAC: c.MAC, Vendor: c.Vendor, Changes: ch})
		}
	}
	sort.SliceStable(d.Added, func(i, j int) bool { return ipLess(d.Added[i].IP, d.Added[j].IP) })
	sort.SliceStable(d.Removed, func(i, j int) bool { return ipLess(d.Removed[i].IP, d.Removed[j].IP) })
	sort.SliceStable(d.Changed, func(i, j int) bool { return ipLess(d.Changed[i].IP, d.Changed[j].IP) })
	d.Summary = model.DiffSummary{Added: len(d.Added), Removed: len(d.Removed), Changed: len(d.Changed)}
	return d
}

func ref(s model.ScanResult) model.DiffScanRef {
	return model.DiffScanRef{ID: s.ID, CIDR: s.Request.CIDR, StartedAt: s.StartedAt, FinishedAt: s.FinishedAt}
}

func normMAC(m string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(m), "-", ":"))
}

func ipLess(a, b string) bool {
	pa, pb := net.ParseIP(a), net.ParseIP(b)
	if pa == nil || pb == nil {
		return a < b
	}
	return string(pa.To16()) < string(pb.To16())
}

func hostChanges(b, c model.Host) []model.DiffFieldChange {
	var out []model.DiffFieldChange
	add := func(f, from, to string) {
		out = append(out, model.DiffFieldChange{Field: f, From: from, To: to})
	}
	if b.IP != c.IP {
		add("ip", b.IP, c.IP)
	}
	if normMAC(b.MAC) != normMAC(c.MAC) {
		add("mac", b.MAC, c.MAC)
	}
	if bp, cp := portsStr(b.OpenPorts), portsStr(c.OpenPorts); bp != cp {
		add("openPorts", bp, cp)
	}
	if b.Online != c.Online {
		add("online", strconv.FormatBool(b.Online), strconv.FormatBool(c.Online))
	}
	if bs, cs := printerState(b), printerState(c); bs != cs {
		add("printer.state", bs, cs)
	}
	out = append(out, supplyChanges(b, c)...)
	out = append(out, webChanges(b, c)...)
	return out
}

func portsStr(p []int) string {
	s := append([]int(nil), p...)
	sort.Ints(s)
	parts := make([]string, len(s))
	for i, v := range s {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

// printerState returns the printer state. It reads PrinterInfo's "state" JSON
// field when present (added in v0.2) and falls back to the SNMP hrPrinterStatus.
func printerState(h model.Host) string {
	if h.Printer != nil {
		if raw, err := json.Marshal(h.Printer); err == nil {
			var m struct {
				State string `json:"state"`
			}
			if json.Unmarshal(raw, &m) == nil && m.State != "" {
				return m.State
			}
		}
	}
	if h.Snmp != nil {
		return h.Snmp.HrPrinterStatus
	}
	return ""
}

func supplyChanges(b, c model.Host) []model.DiffFieldChange {
	if b.Printer == nil || c.Printer == nil {
		return nil
	}
	bm := map[string]int{}
	for _, s := range b.Printer.Supplies {
		if _, ok := bm[s.Description]; !ok {
			bm[s.Description] = s.Percent
		}
	}
	var out []model.DiffFieldChange
	seen := map[string]bool{}
	for _, s := range c.Printer.Supplies {
		if seen[s.Description] {
			continue
		}
		seen[s.Description] = true
		from, ok := bm[s.Description]
		if !ok {
			continue
		}
		delta := s.Percent - from
		if delta < 0 {
			delta = -delta
		}
		if delta >= SupplyThreshold {
			out = append(out, model.DiffFieldChange{Field: "supply:" + s.Description, From: fmt.Sprintf("%d%%", from), To: fmt.Sprintf("%d%%", s.Percent)})
		}
	}
	return out
}

func webChanges(b, c model.Host) []model.DiffFieldChange {
	bm := map[int]int{}
	cm := map[int]int{}
	ports := map[int]bool{}
	for _, w := range b.Web {
		bm[w.Port] = w.Status
		ports[w.Port] = true
	}
	for _, w := range c.Web {
		cm[w.Port] = w.Status
		ports[w.Port] = true
	}
	keys := make([]int, 0, len(ports))
	for p := range ports {
		keys = append(keys, p)
	}
	sort.Ints(keys)
	str := func(s int, ok bool) string {
		if !ok {
			return ""
		}
		return strconv.Itoa(s)
	}
	var out []model.DiffFieldChange
	for _, p := range keys {
		bs, bok := bm[p]
		cs, cok := cm[p]
		if bok && cok && bs == cs {
			continue
		}
		out = append(out, model.DiffFieldChange{Field: "web:" + strconv.Itoa(p), From: str(bs, bok), To: str(cs, cok)})
	}
	return out
}
