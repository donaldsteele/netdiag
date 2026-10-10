package scan

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"netdiag/internal/arp"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/snmpprobe"
)

// ErrNotLocal is returned by ProbeHost for addresses outside every local
// interface subnet.
var ErrNotLocal = errors.New("address is not on a local interface subnet")

// probePingTimeout bounds the single liveness ping in ProbeHost.
const probePingTimeout = time.Second

func defaultLocalPrefixes() ([]netip.Prefix, error) {
	list, err := netinfo.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []netip.Prefix
	for _, f := range list {
		if p, err := netip.ParsePrefix(f.CIDR); err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// ProbeHost runs the per-host probe pipeline (ports, web, IPP, SNMP, reverse
// DNS, vendor, device type) against a single local IPv4 address. It uses an
// isolated runner: no notifications, history, bus events, or interaction with
// any running Scan. If nothing responds the returned host has Alive=false.
func (o *Orchestrator) ProbeHost(ctx context.Context, ip, community string) (model.Host, error) {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is4() {
		return model.Host{}, fmt.Errorf("invalid IPv4 address %q", ip)
	}
	ip = addr.String()
	lp := o.deps.LocalPrefixes
	if lp == nil {
		lp = defaultLocalPrefixes
	}
	prefixes, err := lp()
	if err != nil {
		return model.Host{}, fmt.Errorf("list interfaces: %w", err)
	}
	local := false
	for _, p := range prefixes {
		if p.Contains(addr) {
			local = true
			break
		}
	}
	if !local {
		return model.Host{}, ErrNotLocal
	}
	if err := ctx.Err(); err != nil {
		return model.Host{}, err
	}
	if community == "" {
		community = model.DefaultCommunity
	}

	d := o.deps
	var self model.SelfInfo
	gw := ""
	if d.Env != nil {
		self, _ = d.Env.Self("")
		gw, _ = d.Env.Gateway()
	}
	r := &runner{
		d: d, self: self,
		req:       model.ScanRequest{Ports: model.DefaultPorts, Community: community},
		inRange:   map[string]bool{ip: true},
		queued:    map[string]bool{},
		lastProg:  map[string]time.Time{},
		reprobeAt: map[string]time.Time{},
		targets:   map[string]snmpprobe.StatusTarget{},
		gateway:   gw,
	}
	r.st = newState(d.Now, self, func(model.Host) {})
	r.st.gateway = gw
	done := make(chan struct{})
	go func() { r.st.run(); close(done) }()
	defer func() { r.st.close(); <-done }()

	// Seed: ARP MAC + vendor (best effort), then one ping for liveness.
	if d.ARP != nil {
		if entries, err := d.ARP.Table(ctx); err == nil {
			for _, e := range entries {
				mac, ok := arp.NormalizeMAC(e.MAC)
				if e.IP != ip || !ok {
					continue
				}
				vendor := ""
				if d.Vendors != nil {
					vendor = d.Vendors.Lookup(mac)
				}
				r.st.update(ip, func(h *model.Host) {
					h.Alive = true
					addSource(h, "arp")
					h.MAC = mac
					h.Vendor = vendor
					classify(h, r.gateway)
				})
				break
			}
		}
	}
	if d.Pinger != nil {
		if rtt, ok, _ := d.Pinger.Ping(ctx, addr, probePingTimeout); ok {
			ms := float64(rtt.Microseconds()) / 1000
			r.st.update(ip, func(h *model.Host) {
				h.Alive = true
				addSource(h, "icmp")
				if ms > 0 {
					h.RttMs = ms
				}
				classify(h, r.gateway)
			})
		}
	}
	if err := ctx.Err(); err != nil {
		return model.Host{}, err
	}

	r.probe(ctx, ip)
	if err := ctx.Err(); err != nil {
		return model.Host{}, err
	}

	// Anything that answered a probe counts as alive.
	r.st.update(ip, func(h *model.Host) {
		if len(h.OpenPorts) > 0 || h.Snmp != nil {
			h.Alive = true
		}
		classify(h, r.gateway)
	})
	for _, h := range r.st.snapshot() {
		if h.IP == ip {
			return h, nil
		}
	}
	return model.Host{IP: ip}, nil
}
