// Package devtype classifies discovered hosts into coarse device categories
// using simple, deterministic, token-based rules.
package devtype

import (
	"fmt"
	"strings"
	"unicode"

	"netdiag/internal/model"
)

// Type is a device category.
type Type string

// Device categories.
const (
	Gateway  Type = "gateway"
	Printer  Type = "printer"
	Computer Type = "computer"
	Phone    Type = "phone"
	TV       Type = "tv"
	NAS      Type = "nas"
	Camera   Type = "camera"
	Network  Type = "network"
	IoT      Type = "iot"
	Server   Type = "server"
	Self     Type = "self"
	Unknown  Type = "unknown"
)

var labels = map[Type]string{
	Gateway: "Gateway", Printer: "Printer", Computer: "Computer",
	Phone: "Phone/tablet", TV: "TV/streamer", NAS: "NAS/storage",
	Camera: "Camera", Network: "Network gear", IoT: "Smart home/IoT",
	Server: "Server/web device", Self: "This computer", Unknown: "Unknown",
}

// All returns every Type in display order.
func All() []Type {
	return []Type{Gateway, Printer, Computer, Phone, TV, NAS, Camera, Network, IoT, Server, Self, Unknown}
}

// Label returns the human-readable name for t ("Unknown" for unrecognised).
func Label(t Type) string {
	if l, ok := labels[t]; ok {
		return l
	}
	return "Unknown"
}

func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// hasSeq reports whether seq appears as consecutive tokens in toks.
func hasSeq(toks, seq []string) bool {
	if len(seq) == 0 {
		return false
	}
	for i := 0; i+len(seq) <= len(toks); i++ {
		ok := true
		for j := range seq {
			if toks[i+j] != seq[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// matchKW matches a keyword against tokens. A trailing "-" means the
// keyword must be the first token ("desktop-"); a leading "-" means it must
// be the last token ("-pc"). Otherwise it is a consecutive token sequence
// (hyphens inside a keyword act as separators).
func matchKW(toks []string, kw string) bool {
	switch {
	case strings.HasSuffix(kw, "-"):
		return len(toks) > 1 && toks[0] == strings.TrimSuffix(kw, "-")
	case strings.HasPrefix(kw, "-"):
		return len(toks) > 1 && toks[len(toks)-1] == strings.TrimPrefix(kw, "-")
	}
	return hasSeq(toks, tokens(kw))
}

// firstMatch returns the first keyword in kws matching any token list.
func firstMatch(lists [][]string, kws []string) (string, bool) {
	for _, kw := range kws {
		for _, t := range lists {
			if matchKW(t, kw) {
				return kw, true
			}
		}
	}
	return "", false
}

func hostNames(h model.Host) [][]string {
	out := [][]string{tokens(h.Hostname)}
	if h.MDNS != nil {
		out = append(out, tokens(h.MDNS.Hostname), tokens(h.MDNS.Instance))
	}
	return out
}

func mdnsName(svc string) string {
	svc = strings.ToLower(svc)
	for _, sep := range []string{"._tcp", "._udp"} {
		if i := strings.Index(svc, sep); i >= 0 {
			svc = svc[:i]
		}
	}
	return svc
}

func mdnsHas(set map[string]bool, names []string) string {
	for _, n := range names {
		if set[n] {
			return n
		}
	}
	return ""
}

func nasSignal(h model.Host) bool {
	lists := append([][]string{tokens(h.Vendor)}, hostNames(h)...)
	if h.MDNS != nil {
		for k, v := range h.MDNS.TXT {
			if strings.Contains(strings.ToLower(k), "model") {
				lists = append(lists, tokens(v))
			}
		}
	}
	if h.Printer != nil {
		lists = append(lists, tokens(h.Printer.Model))
	}
	_, ok := firstMatch(lists, nasKeywords)
	return ok
}

// macModel returns the Mac model identifier in m's TXT records ("Mac14,3"),
// or "". A Mac mini, Mac Studio or Mac Pro has no "macbook" or "imac" in its
// name, and with File Sharing off it announces only AirPlay and Companion,
// which alone read as a TV or a phone. Each service's own TXT is read first,
// then the merged TXT.
func macModel(m *model.MDNSInfo) string {
	txts := make([]map[string]string, 0, len(m.Details)+1)
	for _, d := range m.Details {
		txts = append(txts, d.TXT)
	}
	for _, txt := range append(txts, m.TXT) {
		for _, k := range macModelKeys {
			if v := strings.TrimSpace(txt[k]); macModelID.MatchString(v) {
				return v
			}
		}
	}
	return ""
}

// Classify returns the device type and the reason (winning signal).
//
// Priority: self, gateway, printer flag, mDNS, SNMP, vendor, hostname, web
// port, unknown. Within mDNS the group order is
// printer > camera > phone > storage (nas/computer) > Mac model > tv >
// companion > iot. A Mac model identifier in a TXT record (macModel) is a
// computer. The tv group is skipped when the hostname indicates a phone or
// computer (e.g. a MacBook advertising _airplay), and the companion group
// (_companion-link, which Macs, Apple TVs and HomePods announce too) when it
// indicates a computer.
func Classify(h model.Host, gateway string) (Type, string) {
	if h.IsSelf {
		return Self, "this computer"
	}
	if gateway != "" && h.IP == gateway {
		return Gateway, "default gateway"
	}
	if h.IsPrinter {
		if h.PrinterReason != "" {
			return Printer, "printer: " + h.PrinterReason
		}
		return Printer, "printer"
	}

	hostLists := hostNames(h)

	if h.MDNS != nil && len(h.MDNS.Services) > 0 {
		_, hostPhone := firstMatch(hostLists, phoneHostKeywords)
		_, hostComputer := firstMatch(hostLists, computerHostKeywords)
		set := map[string]bool{}
		for _, s := range h.MDNS.Services {
			set[mdnsName(s)] = true
		}
		for _, g := range []mdnsGroup{mdnsPrinter, mdnsCamera, mdnsPhone} {
			if n := mdnsHas(set, g.services); n != "" {
				return g.t, "mdns " + n
			}
		}
		if n := mdnsHas(set, storageServices); n != "" {
			if nasSignal(h) {
				return NAS, "mdns " + n + " + nas signal"
			}
			return Computer, "mdns " + n
		}
		if m := macModel(h.MDNS); m != "" {
			return Computer, "mdns model " + m
		}
		if !hostPhone && !hostComputer {
			if n := mdnsHas(set, mdnsTV.services); n != "" {
				return TV, "mdns " + n
			}
		}
		if !hostComputer {
			if n := mdnsHas(set, mdnsCompanion.services); n != "" {
				return Phone, "mdns " + n
			}
		}
		if n := mdnsHas(set, mdnsIoT.services); n != "" {
			return IoT, "mdns " + n
		}
	}

	if s := h.Snmp; s != nil {
		lists := [][]string{tokens(s.SysDescr), tokens(s.SysObjectID), tokens(s.SysName)}
		if kw, ok := firstMatch(lists, snmpNetworkKeywords); ok {
			return Network, "snmp sysDescr " + kw
		}
		if kw, ok := firstMatch(lists, nasKeywords); ok {
			return NAS, "snmp sysDescr " + kw
		}
	}

	if h.Vendor != "" {
		vt := [][]string{tokens(h.Vendor)}
		if _, ok := firstMatch(vt, phoneVendors); ok {
			if kw, ok := firstMatch(hostLists, phoneHostKeywords); ok {
				return Phone, "vendor " + h.Vendor + " + hostname " + kw
			}
		}
		for _, r := range vendorRules {
			if kw, ok := firstMatch(vt, r.keywords); ok {
				return r.t, "vendor " + kw
			}
		}
	}

	for _, r := range hostRules {
		if kw, ok := firstMatch(hostLists, r.keywords); ok {
			return r.t, "hostname " + strings.Trim(kw, "-")
		}
	}

	for _, p := range webPorts {
		for _, op := range h.OpenPorts {
			if op == p {
				return Server, fmt.Sprintf("open port %d", p)
			}
		}
		for _, w := range h.Web {
			if w.Port == p {
				return Server, fmt.Sprintf("open port %d", p)
			}
		}
	}
	for _, op := range h.OpenPorts {
		switch op {
		case model.PortRDP:
			return Computer, "remote desktop port 3389"
		case model.PortVNC:
			return Computer, "screen sharing port 5900"
		}
	}
	return Unknown, "no identifying signals"
}
