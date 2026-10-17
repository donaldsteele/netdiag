package mdns

import (
	"cmp"
	"errors"
	"maps"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"golang.org/x/net/dns/dnsmessage"

	"netdiag/internal/model"
)

// Limits that keep a hostile or broken responder from growing state.
const (
	maxTXTKeys     = 32
	maxTXTValueLen = 255
	maxInstances   = 512
	maxHosts       = 512
)

// PTR maps a service type (or subtype) to a service instance.
type PTR struct{ Service, Instance string }

// SRV locates a service instance on a target host.
type SRV struct {
	Instance, Target string
	Port             uint16
}

// TXT carries the key/value metadata of a service instance.
type TXT struct {
	Instance string
	KV       map[string]string
}

// A maps a hostname to an IPv4 address.
type A struct {
	Host string
	IP   netip.Addr
}

// Answers holds the DNS-SD relevant records of one response packet, taken
// from the answer and additional sections (authority is ignored).
type Answers struct {
	PTR []PTR
	SRV []SRV
	TXT []TXT
	A   []A
}

// ErrNotResponse is returned by Parse for packets that are queries.
var ErrNotResponse = errors.New("mdns: packet is not a response")

// Parse decodes one mDNS/DNS response packet. On a malformed packet it
// returns whatever was decoded before the error together with the error; it
// never panics.
func Parse(b []byte) (*Answers, error) {
	var p dnsmessage.Parser
	ans := &Answers{}
	hdr, err := p.Start(b)
	if err != nil {
		return ans, err
	}
	if !hdr.Response {
		return ans, ErrNotResponse
	}
	if err := p.SkipAllQuestions(); err != nil {
		return ans, err
	}
	if err := parseSection(&p, ans, p.AnswerHeader, p.SkipAnswer); err != nil {
		return ans, err
	}
	if err := p.SkipAllAuthorities(); err != nil {
		return ans, err
	}
	if err := parseSection(&p, ans, p.AdditionalHeader, p.SkipAdditional); err != nil {
		return ans, err
	}
	return ans, nil
}

func parseSection(p *dnsmessage.Parser, ans *Answers,
	next func() (dnsmessage.ResourceHeader, error), skip func() error) error {
	for {
		h, err := next()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			return nil
		}
		if err != nil {
			return err
		}
		name := h.Name.String()
		switch h.Type {
		case dnsmessage.TypePTR:
			r, err := p.PTRResource()
			if err != nil {
				return err
			}
			ans.PTR = append(ans.PTR, PTR{Service: name, Instance: r.PTR.String()})
		case dnsmessage.TypeSRV:
			r, err := p.SRVResource()
			if err != nil {
				return err
			}
			ans.SRV = append(ans.SRV, SRV{Instance: name, Target: r.Target.String(), Port: r.Port})
		case dnsmessage.TypeTXT:
			r, err := p.TXTResource()
			if err != nil {
				return err
			}
			ans.TXT = append(ans.TXT, TXT{Instance: name, KV: parseTXT(r.TXT)})
		case dnsmessage.TypeA:
			r, err := p.AResource()
			if err != nil {
				return err
			}
			ans.A = append(ans.A, A{Host: name, IP: netip.AddrFrom4(r.A)})
		default:
			if err := skip(); err != nil {
				return err
			}
		}
	}
}

// parseTXT turns DNS-SD TXT strings into a map. Keys are lower-cased, the
// first occurrence of a key wins, and boolean keys (no "=") map to "".
func parseTXT(ss []string) map[string]string {
	kv := map[string]string{}
	for _, s := range ss {
		if s == "" || len(kv) >= maxTXTKeys {
			continue
		}
		k, v, _ := strings.Cut(s, "=")
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if _, dup := kv[k]; dup {
			continue
		}
		if len(v) > maxTXTValueLen {
			v = v[:maxTXTValueLen]
		}
		kv[k] = v
	}
	return kv
}

// splitInstance splits "<label>.<_svc>.<_tcp|_udp>.local." into the instance
// label and the service type "_svc._tcp". ok is false when name is not a
// service instance name (e.g. it is a bare service type).
func splitInstance(name string) (label, service string, ok bool) {
	s := strings.TrimSuffix(name, ".")
	low := strings.ToLower(s)
	if !strings.HasSuffix(low, ".local") {
		return "", "", false
	}
	s, low = s[:len(s)-len(".local")], low[:len(low)-len(".local")]
	var proto string
	switch {
	case strings.HasSuffix(low, "._tcp"):
		proto = "._tcp"
	case strings.HasSuffix(low, "._udp"):
		proto = "._udp"
	default:
		return "", "", false
	}
	rest := s[:len(s)-len(proto)]
	j := strings.LastIndex(rest, "._")
	if j <= 0 {
		return "", "", false
	}
	return rest[:j], strings.ToLower(rest[j+1:]) + proto, true
}

// ServiceType normalizes "_ipp._tcp", "_ipp._tcp.local" or "_ipp._tcp.local."
// to "_ipp._tcp".
func ServiceType(s string) string {
	s = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
	return strings.TrimSuffix(s, ".local")
}

// PrinterServices are DNS-SD service types that identify a printer.
var PrinterServices = []string{"_ipp._tcp", "_ipps._tcp", "_printer._tcp", "_pdl-datastream._tcp"}

// IsPrinterService reports whether svc (any form accepted by ServiceType) is
// a printer service.
func IsPrinterService(svc string) bool {
	svc = ServiceType(svc)
	for _, p := range PrinterServices {
		if svc == p {
			return true
		}
	}
	return false
}

// PrinterService returns the first printer service in services (in
// PrinterServices order), or "" if there is none. Callers use it to build
// printerReason "mdns service <svc>".
func PrinterService(services []string) string {
	for _, p := range PrinterServices {
		for _, s := range services {
			if ServiceType(s) == p {
				return p
			}
		}
	}
	return ""
}

// Record is the aggregated mDNS knowledge about one IPv4 address.
type Record struct {
	IP   string         `json:"ip"`
	Info model.MDNSInfo `json:"info"`
}

type instance struct {
	label, service string
	target         string // SRV target host, as received
	port           uint16 // SRV port, set with target
	txt            map[string]string
	src            netip.Addr // source of the first packet mentioning it
}

// collector accumulates answers from many packets and resolves them to
// per-IP records.
type collector struct {
	inst  map[string]*instance // key: lower-cased instance name
	hosts map[string][]netip.Addr
	names map[string]string // lower-cased host -> display form
}

func newCollector() *collector {
	return &collector{inst: map[string]*instance{}, hosts: map[string][]netip.Addr{}, names: map[string]string{}}
}

func (c *collector) instance(name string, src netip.Addr) *instance {
	key := strings.ToLower(name)
	if in, ok := c.inst[key]; ok {
		return in
	}
	label, svc, ok := splitInstance(name)
	if !ok || len(c.inst) >= maxInstances {
		return nil
	}
	in := &instance{label: label, service: svc, src: src}
	c.inst[key] = in
	return in
}

// add merges the answers of one packet received from src.
func (c *collector) add(a *Answers, src netip.Addr) {
	if a == nil {
		return
	}
	src = src.Unmap()
	for _, p := range a.PTR {
		if ServiceType(p.Service) == "_services._dns-sd._udp" {
			continue
		}
		c.instance(p.Instance, src)
	}
	for _, s := range a.SRV {
		if in := c.instance(s.Instance, src); in != nil && in.target == "" {
			in.target, in.port = s.Target, s.Port
		}
	}
	for _, t := range a.TXT {
		in := c.instance(t.Instance, src)
		if in == nil {
			continue
		}
		if in.txt == nil {
			in.txt = map[string]string{}
		}
		for k, v := range t.KV {
			if _, dup := in.txt[k]; !dup && len(in.txt) < maxTXTKeys {
				in.txt[k] = v
			}
		}
	}
	for _, r := range a.A {
		ip := r.IP.Unmap()
		if !ip.Is4() || ip.IsUnspecified() {
			continue
		}
		key := strings.ToLower(r.Host)
		if _, ok := c.hosts[key]; !ok && len(c.hosts) >= maxHosts {
			continue
		}
		if !containsAddr(c.hosts[key], ip) {
			c.hosts[key] = append(c.hosts[key], ip)
		}
		if _, ok := c.names[key]; !ok {
			c.names[key] = r.Host
		}
	}
}

func containsAddr(list []netip.Addr, ip netip.Addr) bool {
	for _, a := range list {
		if a == ip {
			return true
		}
	}
	return false
}

// pending returns follow-up questions for round 2: SRV/TXT names for
// instances seen only via PTR, and A names for SRV targets without an
// address. At most limit names in total.
func (c *collector) pending(limit int) (srv []string, a []string) {
	keys := make([]string, 0, len(c.inst))
	for k := range c.inst {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	seenA := map[string]bool{}
	for _, k := range keys {
		if len(srv)+len(a) >= limit {
			break
		}
		in := c.inst[k]
		if in.target == "" {
			srv = append(srv, in.label+"."+in.service+".local.")
			continue
		}
		tk := strings.ToLower(in.target)
		if len(c.hosts[tk]) == 0 && !seenA[tk] {
			seenA[tk] = true
			a = append(a, in.target)
		}
	}
	return srv, a
}

func serviceRank(svc string) int {
	if IsPrinterService(svc) {
		return 0
	}
	return 1
}

// records resolves the collected data into per-IPv4 records sorted by IP.
// An instance is attributed to the IPv4 addresses of its SRV target; when
// those are unknown, to the source address of the packet that announced it.
// Printer service instances take precedence for Instance/Hostname/TXT.
// Details lists every instance with its own port and TXT keys, sorted by
// service type, then instance label.
func (c *collector) records() []Record {
	ins := make([]*instance, 0, len(c.inst))
	for _, in := range c.inst {
		ins = append(ins, in)
	}
	sort.Slice(ins, func(i, j int) bool {
		a, b := ins[i], ins[j]
		if ra, rb := serviceRank(a.service), serviceRank(b.service); ra != rb {
			return ra < rb
		}
		if a.service != b.service {
			return a.service < b.service
		}
		return a.label < b.label
	})
	byIP := map[netip.Addr]*model.MDNSInfo{}
	get := func(ip netip.Addr) *model.MDNSInfo {
		if m, ok := byIP[ip]; ok {
			return m
		}
		m := &model.MDNSInfo{Services: []string{}}
		byIP[ip] = m
		return m
	}
	for _, in := range ins {
		var ips []netip.Addr
		if in.target != "" {
			ips = c.hosts[strings.ToLower(in.target)]
		}
		if len(ips) == 0 && in.src.Is4() && !in.src.IsUnspecified() {
			ips = []netip.Addr{in.src}
		}
		for _, ip := range ips {
			m := get(ip)
			if m.Instance == "" {
				m.Instance = in.label
			}
			if m.Hostname == "" && in.target != "" {
				m.Hostname = strings.TrimSuffix(in.target, ".")
			}
			if !containsStr(m.Services, in.service) {
				m.Services = append(m.Services, in.service)
			}
			for k, v := range in.txt {
				if m.TXT == nil {
					m.TXT = map[string]string{}
				}
				if _, dup := m.TXT[k]; !dup {
					m.TXT[k] = v
				}
			}
			d := model.MDNSService{Type: in.service, Instance: in.label, Port: int(in.port)}
			if len(in.txt) > 0 {
				d.TXT = maps.Clone(in.txt)
			}
			m.Details = append(m.Details, d)
		}
	}
	hostKeys := make([]string, 0, len(c.hosts))
	for k := range c.hosts {
		hostKeys = append(hostKeys, k)
	}
	sort.Strings(hostKeys)
	for _, k := range hostKeys {
		for _, ip := range c.hosts[k] {
			m := get(ip)
			if m.Hostname == "" {
				m.Hostname = strings.TrimSuffix(c.names[k], ".")
			}
		}
	}
	keys := make([]netip.Addr, 0, len(byIP))
	for ip := range byIP {
		keys = append(keys, ip)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Less(keys[j]) })
	out := make([]Record, 0, len(keys))
	for _, ip := range keys {
		m := byIP[ip]
		sort.Strings(m.Services)
		SortDetails(m.Details)
		out = append(out, Record{IP: ip.String(), Info: *m})
	}
	return out
}

// SortDetails sorts service details by type, then instance label: the order
// model.MDNSInfo.Details promises.
func SortDetails(ds []model.MDNSService) {
	slices.SortFunc(ds, func(a, b model.MDNSService) int {
		return cmp.Or(cmp.Compare(a.Type, b.Type), cmp.Compare(a.Instance, b.Instance))
	})
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
