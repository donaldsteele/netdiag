// Package conflict is a pure analyzer that detects IP/MAC/name conflicts.
package conflict

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"netdiag/internal/model"
)

const extenderHint = "Wi-Fi range extenders and mesh nodes proxy-ARP for other devices; if this is one, use Ignore this MAC."

// flapWindow is how far back MAC observations count toward a flap. Older
// alternations are stale and must not keep a conflict alive.
const flapWindow = 10 * time.Minute

// inFlapWindow reports whether an RFC3339 observation time falls inside the
// window ending at now. Unparseable times are excluded.
func inFlapWindow(at string, cutoff time.Time) bool {
	t, err := time.Parse(time.RFC3339, at)
	return err == nil && !t.Before(cutoff)
}

func normMAC(m string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(m), "-", ":"))
}

func normName(n string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(n)), ".")
}

func makeID(kind string, ips, macs []string, name string) string {
	a := append([]string(nil), ips...)
	b := append([]string(nil), macs...)
	sort.Strings(a)
	sort.Strings(b)
	h := sha1.Sum([]byte(kind + "|" + strings.Join(a, ",") + "|" + strings.Join(b, ",") + "|" + name))
	return "c-" + hex.EncodeToString(h[:])[:10]
}

// Analyze returns the current conflicts, sorted by ID.
func Analyze(hosts []model.Host, obs map[string][]model.MACObservation, gateway, self string, ignored []string, now time.Time) []model.Conflict {
	ign := map[string]bool{}
	for _, m := range ignored {
		ign[normMAC(m)] = true
	}
	// MACs belonging to gateway/self are excluded from multi-IP detection.
	excl := map[string]bool{}
	for _, h := range hosts {
		if h.IP == gateway || h.IP == self || h.IsSelf {
			if m := normMAC(h.MAC); m != "" {
				excl[m] = true
			}
		}
	}
	nowS := now.UTC().Format(time.RFC3339)
	var out []model.Conflict

	// 1. MAC flapping.
	ips := make([]string, 0, len(obs))
	for ip := range obs {
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	cutoff := now.Add(-flapWindow)
	for _, ip := range ips {
		var seq []string
		var first, last string
		for _, o := range obs[ip] {
			m := normMAC(o.MAC)
			if m == "" || ign[m] || !inFlapWindow(o.At, cutoff) {
				continue
			}
			if len(seq) == 0 || seq[len(seq)-1] != m {
				seq = append(seq, m)
			}
			if first == "" {
				first = o.At
			}
			last = o.At
		}
		if len(seq) < 3 {
			continue
		}
		count := map[string]int{}
		repeat := false
		for _, m := range seq {
			count[m]++
			if count[m] > 1 {
				repeat = true
			}
		}
		if !repeat {
			continue
		}
		macs := make([]string, 0, len(count))
		for m := range count {
			macs = append(macs, m)
		}
		sort.Strings(macs)
		out = append(out, model.Conflict{
			ID: makeID(model.ConflictMACFlap, []string{ip}, macs, ""), Kind: model.ConflictMACFlap,
			Severity: "fail", IPs: []string{ip}, MACs: macs, FirstSeen: orNow(first, nowS), LastSeen: orNow(last, nowS),
			Detail: fmt.Sprintf("%s keeps alternating between %d MAC addresses (%s); two devices likely share this IP.", ip, len(macs), strings.Join(macs, ", ")),
		})
	}

	// 2. Same MAC on multiple IPs.
	byMAC := map[string][]string{}
	for _, h := range hosts {
		m := normMAC(h.MAC)
		if m == "" || ign[m] || excl[m] || h.IsSelf || h.IP == gateway || h.IP == self {
			continue
		}
		byMAC[m] = appendUniq(byMAC[m], h.IP)
	}
	macKeys := make([]string, 0, len(byMAC))
	for m := range byMAC {
		macKeys = append(macKeys, m)
	}
	sort.Strings(macKeys)
	for _, m := range macKeys {
		l := byMAC[m]
		if len(l) < 2 {
			continue
		}
		sort.Strings(l)
		out = append(out, model.Conflict{
			ID: makeID(model.ConflictMACMultiIP, l, []string{m}, ""), Kind: model.ConflictMACMultiIP,
			Severity: "warn", IPs: l, MACs: []string{m}, FirstSeen: nowS, LastSeen: nowS,
			Detail: fmt.Sprintf("MAC %s answers for %d IPs (%s). %s", m, len(l), strings.Join(l, ", "), extenderHint),
		})
	}

	// 3. Duplicate names across different MACs.
	type grp struct {
		ips  []string
		macs []string
	}
	byName := map[string]*grp{}
	for _, h := range hosts {
		m := normMAC(h.MAC)
		if m == "" || ign[m] {
			continue
		}
		names := []string{normName(h.Hostname)}
		if h.Snmp != nil {
			names = append(names, normName(h.Snmp.SysName))
		}
		if h.MDNS != nil {
			names = append(names, normName(h.MDNS.Instance),
				strings.TrimSuffix(normName(h.MDNS.Hostname), ".local"))
		}
		for _, n := range names {
			if n == "" {
				continue
			}
			g := byName[n]
			if g == nil {
				g = &grp{}
				byName[n] = g
			}
			g.ips = appendUniq(g.ips, h.IP)
			g.macs = appendUniq(g.macs, m)
		}
	}
	nameKeys := make([]string, 0, len(byName))
	for n := range byName {
		nameKeys = append(nameKeys, n)
	}
	sort.Strings(nameKeys)
	for _, n := range nameKeys {
		g := byName[n]
		if len(g.macs) < 2 {
			continue
		}
		sort.Strings(g.ips)
		sort.Strings(g.macs)
		out = append(out, model.Conflict{
			ID: makeID(model.ConflictDuplicateName, g.ips, g.macs, n), Kind: model.ConflictDuplicateName,
			Severity: "warn", IPs: g.ips, MACs: g.macs, Name: n, FirstSeen: nowS, LastSeen: nowS,
			Detail: fmt.Sprintf("Name %q is used by %d different devices (%s).", n, len(g.macs), strings.Join(g.ips, ", ")),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func orNow(s, now string) string {
	if s == "" {
		return now
	}
	return s
}

func appendUniq(l []string, v string) []string {
	for _, x := range l {
		if x == v {
			return l
		}
	}
	return append(l, v)
}
