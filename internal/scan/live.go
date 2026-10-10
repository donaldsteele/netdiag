package scan

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"sync"
	"time"

	"netdiag/internal/arp"
	"netdiag/internal/discovery"
	"netdiag/internal/model"
)

// LiveSweepWorkers caps ICMP concurrency during live-mode cycles.
const LiveSweepWorkers = 64

// Presence check tuning: known hosts get one cheap check per live cycle.
const (
	PresenceWorkers     = 64
	PresencePingTimeout = time.Second
	PresenceTCPTimeout  = 800 * time.Millisecond
)

// live runs live mode after the initial scan: it announces the completed
// initial result, then every interval sweeps only the target addresses not
// yet known, fully probes each newly found host exactly once and gives every
// known host one cheap presence check (never a re-probe). Cycles never
// overlap. It returns when ctx is cancelled.
func (r *runner) live(ctx context.Context, targets []netip.Addr) {
	interval := r.d.LiveInterval
	if interval <= 0 {
		interval = time.Duration(r.req.LiveIntervalSec) * time.Second
	}
	if interval <= 0 {
		interval = model.DefaultLiveIntervalSec * time.Second
	}

	hosts := r.st.snapshot() // barrier: all prior host events are on the bus
	known := knownOf(hosts)
	r.s.mu.Lock()
	r.s.header.State = model.StateLive
	r.s.mu.Unlock()
	r.stage(model.StageLive)
	r.s.bus.Publish(model.Event{Type: model.EventSnapshot, Data: r.s.Snapshot()})

	status := model.LiveStatus{
		Unseen:      len(unknown(targets, known)),
		IntervalSec: int((interval + time.Second - 1) / time.Second),
	}
	status.Online, status.Offline = presenceCounts(hosts)
	r.publishLive(&status, interval, false)

	cpEvery := r.d.CheckpointInterval
	if cpEvery <= 0 {
		cpEvery = CheckpointInterval
	}
	lastCP := time.Now()

	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		found, unseen := r.liveCycle(ctx, targets)
		if ctx.Err() != nil {
			return
		}
		status.Cycle++
		status.NewHosts += found
		status.Unseen = unseen
		status.Online, status.Offline = presenceCounts(r.st.snapshot())
		r.publishLive(&status, interval, true)
		if time.Since(lastCP) >= cpEvery {
			r.checkpoint()
			lastCP = time.Now()
		}
		timer.Reset(interval) // scheduled after the cycle finished: no overlap
	}
}

func (r *runner) publishLive(st *model.LiveStatus, interval time.Duration, checked bool) {
	now := r.d.Now().UTC()
	if checked {
		st.LastCheckAt = now.Format(time.RFC3339)
	}
	st.NextCheckAt = now.Add(interval).Format(time.RFC3339)
	r.s.bus.Publish(model.Event{Type: model.EventLive, Data: *st})
}

// knownOf returns the IPs of every alive (discovered) host, online or not.
func knownOf(hosts []model.Host) map[string]bool {
	known := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		if h.Alive {
			known[h.IP] = true
		}
	}
	return known
}

// presenceCounts returns how many known hosts are online and offline. The
// self host always counts as online.
func presenceCounts(hosts []model.Host) (online, offline int) {
	for _, h := range hosts {
		switch {
		case !h.Alive:
		case h.Online || h.IsSelf:
			online++
		default:
			offline++
		}
	}
	return online, offline
}

func unknown(targets []netip.Addr, known map[string]bool) []netip.Addr {
	var out []netip.Addr
	for _, a := range targets {
		if !known[a.String()] {
			out = append(out, a)
		}
	}
	return out
}

// liveCycle sweeps the still-unknown targets (never known hosts, even
// offline ones), reads the ARP table once (shared by new-device harvesting,
// MAC-change detection and ARP presence), presence-checks every known host
// and probes each new host once. Known hosts are never re-probed unless their
// MAC changed (a different device reusing the IP). It returns the number of
// new hosts and the number of target addresses still unknown afterwards.
func (r *runner) liveCycle(ctx context.Context, targets []netip.Addr) (found, unseen int) {
	hosts := r.st.snapshot()
	known := knownOf(hosts)
	cands := unknown(targets, known)

	var mu sync.Mutex
	isNew := map[string]bool{}
	var order []string
	mark := func(ip string) {
		mu.Lock()
		defer mu.Unlock()
		if !isNew[ip] {
			isNew[ip] = true
			order = append(order, ip)
		}
	}
	var pmu sync.Mutex
	probed := map[string]bool{}
	// Every host marked new in this cycle is either fully probed or, if the
	// cycle is cancelled first, flagged so its record is not silently left
	// half-done.
	defer func() {
		for _, ip := range order {
			if !probed[ip] {
				r.st.update(ip, func(h *model.Host) { h.Errors = append(h.Errors, "probe: cancelled") })
			}
		}
	}()

	// One mDNS round and a health re-run in parallel with the sweep. The
	// health goroutine is awaited before returning (it posts to the state).
	var bg sync.WaitGroup
	defer bg.Wait()
	r.startHealth(ctx, &bg)
	mdnsDone := r.startMDNS(ctx, MDNSRoundsLive)

	if len(cands) > 0 {
		cfg := r.d.Sweep
		cfg.Pinger = r.d.Pinger
		if cfg.Workers <= 0 || cfg.Workers > LiveSweepWorkers {
			cfg.Workers = LiveSweepWorkers
		}
		cfg.OnProgress = nil
		cfg.OnAlive = func(res discovery.Result) {
			ip := res.IP.String()
			rtt := float64(res.RTT.Microseconds()) / 1000
			mark(ip)
			r.st.update(ip, func(h *model.Host) {
				h.Alive = true
				h.IsNew = true
				addSource(h, "icmp")
				if rtt > 0 {
					h.RttMs = rtt
				}
				classify(h)
			})
		}
		_ = discovery.Sweep(ctx, cands, cfg) // errors repeat every cycle; ignore
	}
	recs := <-mdnsDone
	if ctx.Err() != nil {
		return 0, 0
	}

	// mDNS: new in-range IPs feed discovery (probed once below); known IPs
	// only get their mdns record updated, never a re-probe.
	byIP := make(map[string]model.Host, len(hosts))
	for _, h := range hosts {
		byIP[h.IP] = h
	}
	for _, rec := range recs {
		if !r.inRange[rec.IP] {
			continue
		}
		info := rec.Info
		if known[rec.IP] {
			if mdnsEqual(byIP[rec.IP].MDNS, info) {
				continue
			}
			r.st.update(rec.IP, func(h *model.Host) {
				h.MDNS = mergeMDNS(h.MDNS, info)
				classify(h)
			})
			continue
		}
		mark(rec.IP)
		r.st.update(rec.IP, func(h *model.Host) {
			h.Alive = true
			h.IsNew = true
			addSource(h, "mdns")
			h.MDNS = mergeMDNS(h.MDNS, info)
			classify(h)
		})
	}

	// ARP: one passive read per cycle, in-range entries only.
	entries, _ := r.arpTable(ctx)
	arpMAC := map[string]string{}
	reset := map[string]bool{}
	seen := map[string]string{}
	for _, e := range entries {
		if mac, ok := arp.NormalizeMAC(e.MAC); ok && r.inRange[e.IP] {
			seen[e.IP] = mac
		}
	}
	r.st.observeMACs(seen)
	for _, e := range entries {
		if !r.inRange[e.IP] {
			continue
		}
		mac, ok := arp.NormalizeMAC(e.MAC)
		if !ok {
			continue
		}
		arpMAC[e.IP] = mac
		old := byIP[e.IP]
		switch {
		case !known[e.IP]:
			vendor := r.d.Vendors.Lookup(mac)
			mark(e.IP)
			r.st.update(e.IP, func(h *model.Host) {
				h.Alive = true
				h.IsNew = true
				addSource(h, "arp")
				h.MAC = mac
				h.Vendor = vendor
			})
		case !old.IsSelf && old.MAC != "" && old.MAC != mac && !reset[e.IP]:
			vendor := r.d.Vendors.Lookup(mac)
			nowT := r.d.Now()
			if t, ok := r.reprobeAt[e.IP]; ok && nowT.Sub(t) < ReprobeCooldown {
				// Flapping: the IP was re-probed recently. Only record the
				// MAC; conflict analysis reports the flap.
				r.st.update(e.IP, func(h *model.Host) {
					h.MAC = mac
					h.Vendor = vendor
				})
				continue
			}
			// Same IP, different MAC: another device reusing the address
			// (e.g. DHCP). Start a fresh record and probe it once.
			r.reprobeAt[e.IP] = nowT
			reset[e.IP] = true
			now := nowT.UTC().Format(time.RFC3339)
			mark(e.IP)
			r.st.update(e.IP, func(h *model.Host) {
				*h = model.Host{
					IP: h.IP, Alive: true, IsNew: true, FoundBy: []string{"arp"},
					MAC: mac, Vendor: vendor, FirstSeen: now, Online: true, LastSeen: now,
				}
				r.st.fresh = true // runs on the state goroutine
			})
		}
	}

	r.checkPresence(ctx, hosts, reset, arpMAC)

	// Probe each new host exactly once, bounded by the probe pool size.
	sem := make(chan struct{}, r.d.ProbeWorkers)
	var wg sync.WaitGroup
	for _, ip := range order {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r.probe(ctx, ip)
			if ctx.Err() == nil {
				pmu.Lock()
				probed[ip] = true
				pmu.Unlock()
			}
		}()
	}
	wg.Wait()
	bg.Wait() // health report posted before the analysis
	if ctx.Err() == nil {
		r.st.analyze()
	}
	newCands := 0
	for _, a := range cands {
		if isNew[a.String()] {
			newCands++
		}
	}
	return len(order), len(cands) - newCands
}

// checkPresence gives each known host (except self and hosts reset this
// cycle) one cheap check: an ICMP echo for ICMP-found hosts (with a TCP
// fallback if it has open ports), else a TCP connect to its lowest open port,
// else an ARP table match on IP and MAC. Results are applied by the state
// goroutine; nothing is applied if ctx is cancelled (a cancelled check is not
// a miss).
func (r *runner) checkPresence(ctx context.Context, hosts []model.Host, skip map[string]bool, arpMAC map[string]string) {
	var mu sync.Mutex
	ok := map[string]bool{}
	var checked []string
	var pings []netip.Addr
	tcpPort := map[string]int{}
	for _, h := range hosts {
		if !h.Alive || h.IsSelf || skip[h.IP] {
			continue
		}
		port := 0
		if len(h.OpenPorts) > 0 {
			port = slices.Min(h.OpenPorts)
		}
		if slices.Contains(h.FoundBy, "icmp") {
			if a, err := netip.ParseAddr(h.IP); err == nil {
				pings = append(pings, a)
			}
		} else if port == 0 {
			ok[h.IP] = arpMAC[h.IP] != "" && arpMAC[h.IP] == h.MAC
		}
		if port > 0 {
			tcpPort[h.IP] = port
		}
		checked = append(checked, h.IP)
	}
	if len(checked) == 0 || ctx.Err() != nil {
		return
	}

	if len(pings) > 0 {
		cfg := discovery.Config{
			Pinger:  r.d.Pinger,
			Workers: PresenceWorkers,
			Timeout: PresencePingTimeout,
			Retries: -1,
			Rate:    r.d.Sweep.Rate, // same pacing as the sweep
			OnAlive: func(res discovery.Result) {
				mu.Lock()
				ok[res.IP.String()] = true
				mu.Unlock()
			},
		}
		_ = discovery.Sweep(ctx, pings, cfg)
	}

	// TCP: hosts without ICMP, and ICMP hosts that did not answer.
	var tcp []string
	for _, ip := range checked {
		if tcpPort[ip] > 0 && !ok[ip] {
			tcp = append(tcp, ip)
		}
	}
	sem := make(chan struct{}, PresenceWorkers)
	var wg sync.WaitGroup
	for _, ip := range tcp {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if r.d.TCP.Reachable(ctx, ip, tcpPort[ip], PresenceTCPTimeout) {
				mu.Lock()
				ok[ip] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}

	now := r.d.Now().UTC().Format(time.RFC3339)
	for _, ip := range checked {
		r.st.mutate(ip, presenceUpdate(ok[ip], now))
	}
}

// presenceUpdate applies one presence check result to a host and returns a
// presence event only when the host changes between online and offline. A
// single miss never flips a host offline.
func presenceUpdate(seen bool, now string) func(h *model.Host) *model.Event {
	return func(h *model.Host) *model.Event {
		if seen {
			h.LastSeen = now
			h.MissedChecks = 0
			if h.Online {
				return nil
			}
			h.Online = true
			h.OfflineSince = ""
			return &model.Event{Type: model.EventPresence, Data: model.Presence{IP: h.IP, Online: true, At: now, Reason: "back online"}}
		}
		h.MissedChecks++
		if !h.Online || h.MissedChecks < model.OfflineAfterMisses {
			return nil
		}
		h.Online = false
		h.OfflineSince = now
		reason := fmt.Sprintf("no response to %d checks", h.MissedChecks)
		return &model.Event{Type: model.EventPresence, Data: model.Presence{IP: h.IP, Online: false, At: now, Reason: reason}}
	}
}
