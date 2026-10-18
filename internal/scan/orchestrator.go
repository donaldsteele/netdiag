// Package scan orchestrates the discovery pipeline:
// Resolve -> Sweep (icmp) -> Harvest (arp) -> Probe -> Classify -> Finish.
package scan

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"netdiag/internal/arp"
	"netdiag/internal/discovery"
	"netdiag/internal/ipp"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/notify"
	"netdiag/internal/oui"
	"netdiag/internal/portprobe"
	"netdiag/internal/report"
	"netdiag/internal/snmpprobe"
)

// DefaultProbeWorkers is the size of the shared per-host probe pool.
const DefaultProbeWorkers = 64

// DNSTimeout bounds the best-effort reverse DNS lookup per host.
const DNSTimeout = 500 * time.Millisecond

// progressInterval throttles progress events per stage.
const progressInterval = 100 * time.Millisecond

// ErrInvalidRequest wraps request validation failures (bad or oversized range).
var ErrInvalidRequest = errors.New("invalid scan request")

// ARPReader reads the OS neighbour table.
type ARPReader interface {
	Table(ctx context.Context) ([]arp.Entry, error)
}

// PortScanner returns the open TCP ports of ip among ports.
type PortScanner interface {
	ScanPorts(ctx context.Context, ip string, ports []int) []int
}

// WebProber fingerprints web services on the open ports of ip.
type WebProber interface {
	ProbeAll(ctx context.Context, ip string, open []int) []model.WebService
}

// SNMPProber queries a host via SNMP.
type SNMPProber interface {
	Probe(ctx context.Context, ip, community string) (*snmpprobe.Result, error)
}

// SNMPStatusPoller is optionally implemented by an SNMPProber; live mode then
// polls known printers' status objects every StatusPollInterval.
type SNMPStatusPoller interface {
	PollStatus(ctx context.Context, ip, community string, t snmpprobe.StatusTarget) (snmpprobe.Status, error)
}

// PortChecker reports whether a single TCP port of ip is reachable (used by
// live-mode presence checks).
type PortChecker interface {
	Reachable(ctx context.Context, ip string, port int, timeout time.Duration) bool
}

// Resolver does reverse DNS (net.Resolver satisfies it).
type Resolver interface {
	LookupAddr(ctx context.Context, addr string) ([]string, error)
}

// VendorLookup maps a MAC to a vendor name.
type VendorLookup interface {
	Lookup(mac string) string
}

// Environment reports the local machine's address and default gateway.
type Environment interface {
	Self(ifaceName string) (model.SelfInfo, error)
	Gateway() (string, error)
}

// Deps are the injected stage dependencies. Zero fields are filled by
// DefaultDeps values in New.
type Deps struct {
	Pinger       discovery.Pinger
	ARP          ARPReader
	Ports        PortScanner
	TCP          PortChecker
	Web          WebProber
	SNMP         SNMPProber
	DNS          Resolver
	Vendors      VendorLookup
	Env          Environment
	Sweep        discovery.Config // tuning only; Pinger and callbacks are overwritten
	ProbeWorkers int
	Now          func() time.Time
	// LiveInterval overrides the request's live interval when > 0 (tests).
	LiveInterval time.Duration
	// PrinterPoll overrides PrinterPollInterval when > 0 (tests).
	PrinterPoll time.Duration
	// StatusPoll overrides StatusPollInterval when > 0 (tests).
	StatusPoll time.Duration

	// v0.2 dependencies. Unlike the fields above, nil disables the feature
	// (New does not fill them); DefaultDeps wires the real implementations.
	Health   HealthChecker  // health checks at start and every live cycle
	MDNS     MDNSBrowser    // mDNS browse at start and every live cycle
	IPP      IPPClient      // IPP Get-Printer-Attributes when 631 is open
	Store    HistoryStore   // history auto-save on done and live checkpoints
	Settings SettingsSource // community, health, alert and conflict settings
	// CheckpointInterval overrides CheckpointInterval when > 0 (tests).
	CheckpointInterval time.Duration
	// LocalPrefixes lists the local interface subnets ProbeHost accepts. When
	// nil it is derived from netinfo.Interfaces (tests inject a fake).
	LocalPrefixes func() ([]netip.Prefix, error)

	// v0.6 dependencies. nil turns each off, and DefaultDeps leaves them
	// nil: `netdiag ui` passes the one ledger and profile store of the
	// process, so each file has one writer.
	Ledger   ProblemLedger // open-problem ledger, reconciled after the first pass
	Profiles ProfileStore  // network-profile.json
	// Wall is the wall clock the storm guard measures sleep with (tests);
	// nil uses time.Now without its monotonic reading, which keeps counting
	// while the computer sleeps on some systems and not on others.
	Wall func() time.Time
}

// DefaultDeps returns the production dependencies, including health, mDNS,
// IPP and (when the data directory is usable) the settings/history store.
func DefaultDeps() Deps {
	return withV2Defaults(baseDeps())
}

// baseDeps returns the production v0.1 dependencies.
func baseDeps() Deps {
	return Deps{
		Pinger:       discovery.NewPinger(),
		ARP:          arpReader{},
		Ports:        portScanner{},
		TCP:          portChecker{},
		Web:          webProber{},
		SNMP:         snmpProber{},
		DNS:          net.DefaultResolver,
		Vendors:      vendorLookup{},
		Env:          osEnv{},
		ProbeWorkers: DefaultProbeWorkers,
		Now:          time.Now,
	}
}

type arpReader struct{}

func (arpReader) Table(ctx context.Context) ([]arp.Entry, error) { return arp.Table(ctx) }

// TableFor prefers the scanned interface's rows when Windows lists an IP
// under several adapters.
func (arpReader) TableFor(ctx context.Context, ifaceIP string) ([]arp.Entry, error) {
	return arp.TableFor(ctx, ifaceIP)
}

// ifaceARPReader is an optional ARPReader extension that accepts the scanned
// interface's IP.
type ifaceARPReader interface {
	TableFor(ctx context.Context, ifaceIP string) ([]arp.Entry, error)
}

// arpTable reads the ARP table, preferring the scanned interface when known.
func (r *runner) arpTable(ctx context.Context) ([]arp.Entry, error) {
	if fr, ok := r.d.ARP.(ifaceARPReader); ok && r.self.IP != "" {
		return fr.TableFor(ctx, r.self.IP)
	}
	return r.d.ARP.Table(ctx)
}

type portScanner struct{}

func (portScanner) ScanPorts(ctx context.Context, ip string, ports []int) []int {
	return portprobe.ScanPorts(ctx, ip, ports)
}

type portChecker struct{}

// Reachable dials ip:port once. A refused connection also proves the host
// is up (it answered with a reset).
func (portChecker) Reachable(ctx context.Context, ip string, port int, timeout time.Duration) bool {
	c, err := portprobe.Dial(ctx, ip, port, timeout)
	if err == nil {
		c.Close()
		return true
	}
	return ctx.Err() == nil && portprobe.Refused(err)
}

type webProber struct{}

func (webProber) ProbeAll(ctx context.Context, ip string, open []int) []model.WebService {
	return portprobe.ProbeAll(ctx, ip, open)
}

type snmpProber struct{}

func (snmpProber) Probe(ctx context.Context, ip, community string) (*snmpprobe.Result, error) {
	return snmpprobe.Probe(ctx, snmpprobe.GoSNMPDialer, ip, community)
}

func (snmpProber) PollStatus(ctx context.Context, ip, community string, t snmpprobe.StatusTarget) (snmpprobe.Status, error) {
	return snmpprobe.PollStatus(ctx, snmpprobe.GoSNMPDialer, ip, community, t)
}

type vendorLookup struct{}

func (vendorLookup) Lookup(mac string) string { return oui.Lookup(mac) }

type osEnv struct{}

func (osEnv) Self(name string) (model.SelfInfo, error) {
	list, err := netinfo.Interfaces()
	if err != nil {
		return model.SelfInfo{}, err
	}
	if len(list) == 0 {
		return model.SelfInfo{}, errors.New("no usable IPv4 interface")
	}
	f := list[0]
	for _, c := range list {
		if name != "" && c.Name == name {
			f = c
			break
		}
	}
	return model.SelfInfo{IP: f.IP, MAC: f.MAC, Interface: f.Name, CIDR: f.CIDR}, nil
}

func (osEnv) Gateway() (string, error) {
	r, err := netinfo.DefaultRoute()
	if err != nil {
		return "", err
	}
	if !r.Gateway.IsValid() {
		return "", errors.New("no default gateway")
	}
	return r.Gateway.String(), nil
}

// Orchestrator runs scans with a fixed set of dependencies.
type Orchestrator struct {
	deps Deps
}

// New returns an orchestrator; nil v0.1 fields of d fall back to the
// DefaultDeps values. Nil v0.2 fields (Health, MDNS, IPP, Store, Settings)
// stay nil and disable their feature.
func New(d Deps) *Orchestrator {
	def := baseDeps()
	if d.Pinger == nil {
		d.Pinger = def.Pinger
	}
	if d.ARP == nil {
		d.ARP = def.ARP
	}
	if d.Ports == nil {
		d.Ports = def.Ports
	}
	if d.TCP == nil {
		d.TCP = def.TCP
	}
	if d.Web == nil {
		d.Web = def.Web
	}
	if d.SNMP == nil {
		d.SNMP = def.SNMP
	}
	if d.DNS == nil {
		d.DNS = def.DNS
	}
	if d.Vendors == nil {
		d.Vendors = def.Vendors
	}
	if d.Env == nil {
		d.Env = def.Env
	}
	if d.ProbeWorkers <= 0 {
		d.ProbeWorkers = DefaultProbeWorkers
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Wall == nil {
		d.Wall = func() time.Time { return time.Now().Round(0) }
	}
	return &Orchestrator{deps: d}
}

// Scan is one running or finished scan.
type Scan struct {
	ID     string
	bus    *Bus
	cancel context.CancelFunc
	done   chan struct{}

	settings model.Settings // loaded at Start

	// header holds everything except Hosts/Stats while running. Its request
	// is redacted (model.ScanRequest.Redacted), so every snapshot,
	// checkpoint, saved result and done event hides the SNMP community; only
	// the runner keeps the real one, for SNMP.
	mu        sync.Mutex
	header    model.ScanResult
	result    *model.ScanResult
	apply     func(model.Settings) bool // set while the pipeline runs
	liveSince string                    // when the scan went live (RFC 3339); "" before
	network   string                    // the fingerprint id, set at the end of the first pass

	reconciled     chan struct{} // closed once the first pass is reconciled
	reconciledOnce sync.Once
}

// Request returns the scan's request with the SNMP community hidden. Its
// Origin says who started the scan.
func (s *Scan) Request() model.ScanRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.header.Request.Redacted()
}

// StartedAt is when the scan started (RFC 3339).
func (s *Scan) StartedAt() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.header.StartedAt
}

// LiveSince is when the scan finished its first pass and began watching the
// network (RFC 3339), or "" if it has not gone live.
func (s *Scan) LiveSince() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.liveSince
}

// Network is the id (model.NetworkID) of the network the scan watches,
// taken at the end of its first pass: "" before, and when netdiag cannot
// tell the network apart (the router's MAC is unknown). The notices of a
// scan with a ledger are all about this network (they wait for the end of
// the first pass, and stop when this computer is on another network), and
// the ledger keeps them under it, so the team chat router (v0.6 M11) passes
// it to Ledger.Chat and MarkChat.
func (s *Scan) Network() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.network
}

// LiveStatus is the latest live status of a live-mode scan (its last cycle,
// whether it checked, and the storm guard); ok is false until it went live.
func (s *Scan) LiveStatus() (model.LiveStatus, bool) { return s.bus.Live() }

// Reconciled is closed once the scan's first pass has been reconciled with
// the open-problem ledger (v0.6 X-1): the ledger then knows which problems
// are still open, and the notices the pass held back have been published.
// The team chat outbox holds what a previous run left until then (v0.6
// 4.9). A scan that stops before its first pass ends never closes it; Done
// is closed then.
func (s *Scan) Reconciled() <-chan struct{} { return s.reconciled }

// markReconciled closes Reconciled, once.
func (s *Scan) markReconciled() { s.reconciledOnce.Do(func() { close(s.reconciled) }) }

// ApplySettings applies changed settings (alert triggers, threshold,
// cooldown, health config, ignored conflict MACs) to a running scan and
// re-analyzes conflicts. It reports false if the scan is not running.
func (s *Scan) ApplySettings(set model.Settings) bool {
	s.mu.Lock()
	fn := s.apply
	s.mu.Unlock()
	if fn == nil {
		return false
	}
	return fn(set)
}

// Subscribe returns snapshot+live events; see Bus.Subscribe.
func (s *Scan) Subscribe() (<-chan model.Event, func()) { return s.bus.Subscribe() }

// Cancel requests cancellation. Safe to call multiple times.
func (s *Scan) Cancel() { s.cancel() }

// Done is closed once the scan has finished and the done event is published.
func (s *Scan) Done() <-chan struct{} { return s.done }

// Snapshot returns the final result if finished, otherwise the current
// in-progress view with State "running" (or "live" once the initial scan
// of a live-mode scan has completed).
func (s *Scan) Snapshot() model.ScanResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.result != nil {
		return *s.result
	}
	r := s.header
	r.Request.Ports = slices.Clone(r.Request.Ports)
	r.Warnings = slices.Clone(r.Warnings)
	r.Hosts = s.bus.Hosts()
	r.Stats = computeStats(r.Hosts, r.Stats.Scanned)
	r.Stats.Conflicts = len(r.Conflicts)
	return r
}

// State returns the current scan state (running, live, done, ...).
func (s *Scan) State() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.result != nil {
		return s.result.State
	}
	return s.header.State
}

// Wait blocks until the scan finishes and returns the final result.
func (s *Scan) Wait() model.ScanResult {
	<-s.done
	return s.Snapshot()
}

// MaxPorts is the most TCP ports one scan may check on each device.
const MaxPorts = 64

// validPorts returns the ports to probe: the defaults when none are given,
// otherwise the given ports in order with duplicates removed. Every port
// must be 1..65535 and there may be at most MaxPorts of them.
func validPorts(in []int) ([]int, error) {
	if len(in) == 0 {
		return slices.Clone(model.DefaultPorts), nil
	}
	seen := make(map[int]bool, min(len(in), MaxPorts+1))
	out := make([]int, 0, min(len(in), MaxPorts))
	for _, p := range in {
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("%w: %d is not a port number (use 1 to 65535)", ErrInvalidRequest, p)
		}
		if seen[p] {
			continue
		}
		if len(out) == MaxPorts {
			return nil, fmt.Errorf("%w: too many ports (a scan can check at most %d)", ErrInvalidRequest, MaxPorts)
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, nil
}

// Validate normalises req (default community/ports) and expands its targets.
// Errors wrap ErrInvalidRequest.
func Validate(req model.ScanRequest) (model.ScanRequest, []netip.Addr, error) {
	req.CIDR = strings.TrimSpace(req.CIDR)
	if req.CIDR == "" {
		return req, nil, fmt.Errorf("%w: cidr is required", ErrInvalidRequest)
	}
	targets, err := netinfo.ParseTarget(req.CIDR)
	if err != nil {
		return req, nil, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if len(targets) == 0 {
		return req, nil, fmt.Errorf("%w: no targets", ErrInvalidRequest)
	}
	if req.Community == "" {
		req.Community = model.DefaultCommunity
	}
	if req.Ports, err = validPorts(req.Ports); err != nil {
		return req, nil, err
	}
	if req.Live {
		if req.LiveIntervalSec == 0 {
			req.LiveIntervalSec = model.DefaultLiveIntervalSec
		}
		req.LiveIntervalSec = min(max(req.LiveIntervalSec, model.MinLiveIntervalSec), model.MaxLiveIntervalSec)
	}
	return req, targets, nil
}

// Start validates req and launches the pipeline in the background. The scan
// runs until finished or ctx/Cancel cancels it.
func (o *Orchestrator) Start(ctx context.Context, req model.ScanRequest) (*Scan, error) {
	set := loadSettings(o.deps.Settings)
	if strings.TrimSpace(req.Community) == "" && set.Community != "" {
		req.Community = set.Community
	}
	req, targets, err := Validate(req)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Scan{ID: newID(), bus: NewBus(), cancel: cancel, done: make(chan struct{}), settings: set, reconciled: make(chan struct{})}
	s.header = model.ScanResult{
		ID:            s.ID,
		Request:       req.Redacted(),
		StartedAt:     o.deps.Now().UTC().Format(time.RFC3339),
		State:         model.StateRunning,
		Stats:         model.Stats{Scanned: len(targets)},
		SchemaVersion: model.HistorySchemaVersion,
	}
	go o.run(ctx, s, req, targets)
	return s, nil
}

// Run is a synchronous Start + Wait.
func (o *Orchestrator) Run(ctx context.Context, req model.ScanRequest) (model.ScanResult, error) {
	s, err := o.Start(ctx, req)
	if err != nil {
		return model.ScanResult{}, err
	}
	return s.Wait(), nil
}

type runner struct {
	d       Deps
	s       *Scan
	self    model.SelfInfo
	req     model.ScanRequest
	st      *state
	inRange map[string]bool

	jobs     chan string
	qmu      sync.Mutex
	queued   map[string]bool
	probeOn  bool // probe stage announced; guarded by qmu
	probeTot int  // guarded by qmu
	probeDn  int  // guarded by qmu

	pmu      sync.Mutex
	lastProg map[string]time.Time

	gateway   string
	notify    *notify.Engine // the state's alert engine (safe for concurrent use)
	smu       sync.Mutex
	settings  model.Settings       // guarded by smu
	reprobeAt map[string]time.Time // live goroutine only
	polledAt  time.Time            // last live printer poll; live goroutine only
	tmu       sync.Mutex
	targets   map[string]snmpprobe.StatusTarget // printer status rows; guarded by tmu
	cpWG      sync.WaitGroup                    // in-flight live checkpoints

	// Run and live goroutine only: the router's MAC from the first pass's
	// ARP read, the network fingerprint taken at the end of the first pass,
	// whether this computer's address was inside the target then, and the
	// storm guard (guard.go).
	gatewayMAC  string
	fp          Fingerprint
	selfInRange bool
	guard       stormGuard
	// stopped is set when the storm guard found this computer on another
	// network (guard.go leave): the printer status loop polls no more.
	stopped atomic.Bool
}

func (o *Orchestrator) run(ctx context.Context, s *Scan, req model.ScanRequest, targets []netip.Addr) {
	defer s.cancel()
	d := o.deps
	r := &runner{
		d: d, s: s, req: req,
		inRange:   make(map[string]bool, len(targets)),
		jobs:      make(chan string, len(targets)),
		queued:    map[string]bool{},
		lastProg:  map[string]time.Time{},
		settings:  s.settings,
		reprobeAt: map[string]time.Time{},
		targets:   map[string]snmpprobe.StatusTarget{},
	}
	for _, a := range targets {
		r.inRange[a.String()] = true
	}

	// Resolve: self + gateway (best effort).
	var warnings []string
	self, err := d.Env.Self(req.Interface)
	if err != nil {
		warnings = append(warnings, "self: "+err.Error())
	}
	if self.MAC != "" {
		if m, ok := arp.NormalizeMAC(self.MAC); ok {
			self.MAC = m
		}
	}
	gw, err := d.Env.Gateway()
	if err != nil {
		warnings = append(warnings, "gateway: "+err.Error())
	}
	s.mu.Lock()
	s.header.Self = self
	s.header.Gateway = gw
	s.header.Warnings = warnings
	s.mu.Unlock()
	r.self = self
	r.gateway = gw

	r.st = newState(d.Now, self, func(h model.Host) {
		s.bus.Publish(model.Event{Type: model.EventHost, Data: h})
	})
	r.st.emit = s.bus.Publish
	r.st.gateway = gw
	r.st.vendor = d.Vendors.Lookup
	r.st.ignored = slices.Clone(s.settings.IgnoredConflictMACs)
	r.st.keepHist = req.Live
	r.notify = notify.New(notifyConfig(s.settings), d.Now)
	r.st.notify = r.notify
	if d.Ledger != nil {
		// The first pass reports nothing until it is reconciled with the
		// ledger (finishFirstPass), so a restart repeats no problem.
		r.notify.Defer()
		r.st.ledger = d.Ledger
	}
	r.selfInRange = r.inRange[self.IP]
	r.st.onChange = func(h *model.HealthReport, c []model.Conflict, n []model.Notice) {
		s.mu.Lock()
		s.header.Health, s.header.Conflicts, s.header.Notices = h, c, n
		s.mu.Unlock()
	}
	stateDone := make(chan struct{})
	go func() { r.st.run(); close(stateDone) }()
	s.mu.Lock()
	s.apply = r.applySettings
	s.mu.Unlock()

	// Health and mDNS run in parallel with the sweep; results are posted to
	// the state goroutine.
	var bg sync.WaitGroup
	r.startHealth(ctx, &bg)
	mdnsDone := r.startMDNS(ctx, MDNSRoundsScan)

	// Probe pool runs concurrently with the sweep (pipeline, not barrier).
	var wg sync.WaitGroup
	for range d.ProbeWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip := range r.jobs {
				if ctx.Err() == nil {
					r.probe(ctx, ip)
				}
				r.qmu.Lock()
				r.probeDn++
				r.qmu.Unlock()
				r.probeProgress()
			}
		}()
	}

	// Sweep.
	r.stage(model.StageICMP)
	cfg := d.Sweep
	cfg.Pinger = d.Pinger
	cfg.OnAlive = func(res discovery.Result) {
		ip := res.IP.String()
		rtt := float64(res.RTT.Microseconds()) / 1000
		r.st.update(ip, func(h *model.Host) {
			h.Alive = true
			addSource(h, "icmp")
			if rtt > 0 {
				h.RttMs = rtt
			}
			classify(h, r.gateway)
		})
		r.enqueue(ip)
	}
	cfg.OnProgress = func(done, total int) { r.progress(model.StageICMP, done, total) }
	if err := discovery.Sweep(ctx, targets, cfg); err != nil && ctx.Err() == nil {
		r.warn("icmp: " + err.Error())
	}
	r.forceProgress(ctx, model.StageICMP, len(targets), len(targets))

	// Harvest.
	if ctx.Err() == nil {
		r.stage(model.StageARP)
		r.harvest(ctx)
	}

	// mDNS: merge records; in-range IPs not yet known are probed like
	// ARP-found hosts.
	if recs := <-mdnsDone; ctx.Err() == nil {
		for _, rec := range recs {
			if !r.inRange[rec.IP] {
				continue
			}
			info := rec.Info
			r.st.update(rec.IP, func(h *model.Host) {
				h.Alive = true
				addSource(h, "mdns")
				h.MDNS = mergeMDNS(h.MDNS, info)
				classify(h, r.gateway)
			})
			r.enqueue(rec.IP)
		}
	}

	// Probe stage: announce, then close the queue and drain.
	if ctx.Err() == nil {
		r.stage(model.StageProbe)
	}
	r.qmu.Lock()
	r.probeOn = true
	r.qmu.Unlock()
	r.probeProgress()
	close(r.jobs)
	wg.Wait()
	bg.Wait() // health
	r.st.analyze()
	initialDone := ctx.Err() == nil
	r.finishFirstPass(initialDone)

	// Live: keep watching for new hosts until cancelled.
	if initialDone && req.Live {
		r.live(ctx, targets)
	}

	// Finish.
	s.mu.Lock()
	s.apply = nil
	s.mu.Unlock()
	hosts := report.SortHosts(r.st.snapshot())
	r.st.close()
	<-stateDone
	r.cpWG.Wait() // a late checkpoint must not overwrite the final save

	s.mu.Lock()
	res := s.header
	s.mu.Unlock()
	res.Warnings = slices.Clone(res.Warnings)
	res.Hosts = hosts
	res.Stats = computeStats(hosts, len(targets))
	res.Stats.Conflicts = len(res.Conflicts)
	res.FinishedAt = d.Now().UTC().Format(time.RFC3339)
	res.State = model.StateDone
	if !initialDone {
		res.State = model.StateCancelled
	}
	if d.Store != nil && res.State == model.StateDone {
		if err := d.Store.SaveScan(res); err != nil {
			res.Warnings = append(res.Warnings, "history: "+err.Error())
		}
	}
	s.mu.Lock()
	s.result = &res
	s.mu.Unlock()
	s.bus.Publish(model.Event{Type: model.EventDone, Data: res})
	close(s.done)
}

func (r *runner) warn(msg string) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.header.Warnings = append(r.s.header.Warnings, msg)
}

func (r *runner) stage(name string) {
	r.s.bus.Publish(model.Event{Type: model.EventStage, Data: model.StageInfo{Stage: name}})
}

// progress publishes at most one event per stage per progressInterval, plus
// the completing one.
func (r *runner) progress(stage string, done, total int) {
	r.pmu.Lock()
	now := time.Now()
	if done < total && now.Sub(r.lastProg[stage]) < progressInterval {
		r.pmu.Unlock()
		return
	}
	r.lastProg[stage] = now
	r.pmu.Unlock()
	r.s.bus.Publish(model.Event{Type: model.EventProgress, Data: model.Progress{Stage: stage, Done: done, Total: total}})
}

func (r *runner) forceProgress(ctx context.Context, stage string, done, total int) {
	if ctx.Err() != nil {
		return
	}
	r.s.bus.Publish(model.Event{Type: model.EventProgress, Data: model.Progress{Stage: stage, Done: done, Total: total}})
}

func (r *runner) enqueue(ip string) {
	r.qmu.Lock()
	defer r.qmu.Unlock()
	if r.queued[ip] {
		return
	}
	r.queued[ip] = true
	r.probeTot++
	r.jobs <- ip // buffer == len(targets), never blocks
}

func (r *runner) probeProgress() {
	r.qmu.Lock()
	on, done, total := r.probeOn, r.probeDn, r.probeTot
	r.qmu.Unlock()
	if on {
		r.progress(model.StageProbe, done, total)
	}
}

func (r *runner) harvest(ctx context.Context) {
	entries, err := r.arpTable(ctx)
	if err != nil && ctx.Err() == nil {
		r.warn("arp: " + err.Error())
	}
	seen := map[string]string{}
	for _, e := range entries {
		if mac, ok := arp.NormalizeMAC(e.MAC); ok && r.inRange[e.IP] {
			seen[e.IP] = mac
		}
	}
	r.st.observeMACs(seen)
	r.gatewayMAC = gatewayMAC(entries, r.gateway)
	for _, e := range entries {
		if !r.inRange[e.IP] {
			continue
		}
		mac, ok := arp.NormalizeMAC(e.MAC)
		if !ok {
			continue
		}
		vendor := r.d.Vendors.Lookup(mac)
		r.st.update(e.IP, func(h *model.Host) {
			h.Alive = true
			addSource(h, "arp")
			h.MAC = mac
			h.Vendor = vendor
			classify(h, r.gateway)
		})
		r.enqueue(e.IP)
	}
	// Self is rarely in the ARP table; attach its vendor if we know its MAC.
	if self := r.self; self.IP != "" && self.MAC != "" && r.inRange[self.IP] {
		r.qmu.Lock()
		known := r.queued[self.IP]
		r.qmu.Unlock()
		if known {
			vendor := r.d.Vendors.Lookup(self.MAC)
			r.st.update(self.IP, func(h *model.Host) {
				if h.Vendor == "" {
					h.Vendor = vendor
				}
				classify(h, r.gateway)
			})
		}
	}
	r.forceProgress(ctx, model.StageARP, 1, 1)
}

// gatewayMAC returns the router's MAC from an ARP read, in or out of the
// target, or "" when the read does not have it.
func gatewayMAC(entries []arp.Entry, gateway string) string {
	if gateway == "" {
		return ""
	}
	for _, e := range entries {
		if e.IP == gateway {
			if mac, ok := arp.NormalizeMAC(e.MAC); ok {
				return mac
			}
		}
	}
	return ""
}

// finishFirstPass takes the network fingerprint and reconciles the alert
// engine with the open-problem ledger, once, right after the first pass's
// analysis (v0.6 X-1), for live and one-shot scans alike. A problem the
// ledger has for this network is checked against what the pass found when
// the pass covered it: one on a device in the target, a conflict when the
// target covers this computer's whole network, and every health check. The
// others are kept open as they are. A cancelled pass checks nothing.
func (r *runner) finishFirstPass(complete bool) {
	r.fp = NewFingerprint(r.self.CIDR, r.gateway, r.gatewayMAC)
	r.s.mu.Lock()
	r.s.network = r.fp.ID // before the reconcile lets the first notices out
	r.s.mu.Unlock()
	var check, keep []notify.OpenFault
	if r.d.Ledger != nil && r.fp.ID != "" {
		whole := r.coversNetwork()
		for _, f := range r.d.Ledger.OpenFor(r.fp.ID) {
			switch {
			case f.Trigger == model.TriggerHealthFailed,
				f.Trigger == model.TriggerIPConflict && whole,
				f.Trigger != model.TriggerIPConflict && r.inRange[f.IP]:
				check = append(check, f)
			default:
				keep = append(keep, f)
			}
		}
	}
	r.st.reconcile(r.fp.ID, check, keep, complete, r.s.markReconciled)
	if complete {
		r.saveProfile(r.fp)
	}
}

// coversNetwork reports whether the target is this computer's whole network
// (or more), so a conflict the analysis did not find is really gone.
func (r *runner) coversNetwork() bool {
	q, err := netip.ParsePrefix(r.self.CIDR)
	if err != nil {
		return false
	}
	p, err := netip.ParsePrefix(strings.TrimSpace(r.req.CIDR))
	return err == nil && p.Bits() <= q.Bits() && p.Masked().Contains(q.Masked().Addr())
}

// saveProfile records the network in network-profile.json; a failure
// becomes a warning.
func (r *runner) saveProfile(fp Fingerprint) {
	if r.d.Profiles == nil || fp.ID == "" {
		return
	}
	if err := r.d.Profiles.Seen(fp, r.d.Now()); err != nil {
		r.warn("network profile: " + err.Error())
	}
}

// probe runs the per-host job: TCP then web, with SNMP and reverse DNS in
// parallel. Each sub-step publishes an updated host.
func (r *runner) probe(ctx context.Context, ip string) {
	var ippRes *ipp.Result
	var ippErr error
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		open := r.d.Ports.ScanPorts(ctx, ip, r.req.Ports)
		r.st.update(ip, func(h *model.Host) {
			h.OpenPorts = slices.Clone(open)
			if len(open) > 0 {
				addSource(h, "tcp")
			}
			classify(h, r.gateway)
		})
		if len(open) == 0 || ctx.Err() != nil {
			return
		}
		if r.d.IPP != nil && slices.Contains(open, ippPort) {
			ippRes, ippErr = r.d.IPP.GetPrinterAttributes(ctx, ip, ippPort)
		}
		web := r.d.Web.ProbeAll(ctx, ip, open)
		if len(web) == 0 {
			return
		}
		r.st.update(ip, func(h *model.Host) { h.Web = web; classify(h, r.gateway) })
	}()
	go func() {
		defer wg.Done()
		res, err := r.d.SNMP.Probe(ctx, ip, r.req.Community)
		if err != nil && (errors.Is(err, snmpprobe.ErrNoResponse) || ctx.Err() != nil) {
			return // no agent: normal for most hosts
		}
		r.st.update(ip, func(h *model.Host) {
			if err != nil {
				h.Errors = append(h.Errors, "snmp: "+err.Error())
			}
			if res != nil {
				r.setTarget(ip, res.Status)
				h.Snmp = res.Snmp
				h.Printer = withSNMPSource(res.Printer)
				h.Errors = append(h.Errors, res.Errors...)
			}
			classify(h, r.gateway)
		})
	}()
	go func() {
		defer wg.Done()
		dctx, cancel := context.WithTimeout(ctx, DNSTimeout)
		defer cancel()
		names, err := r.d.DNS.LookupAddr(dctx, ip)
		if err != nil || len(names) == 0 {
			return
		}
		name := strings.TrimSuffix(names[0], ".")
		if name == "" {
			return
		}
		r.st.update(ip, func(h *model.Host) { h.Hostname = name; classify(h, r.gateway) })
	}()
	wg.Wait()
	ippErrMsg := ""
	if ippErr != nil && ctx.Err() == nil && !errors.Is(ippErr, ipp.ErrNotPrinter) {
		ippErrMsg = "ipp: " + ippErr.Error()
	}
	r.st.update(ip, func(h *model.Host) {
		if ippRes != nil && (ippRes.Printer != nil || ippRes.State != "") {
			h.Printer = mergePrinter(h.Printer, ippRes)
		}
		if ippRes != nil {
			h.Errors = append(h.Errors, ippRes.Errors...)
		}
		if ippErrMsg != "" {
			h.Errors = append(h.Errors, ippErrMsg)
		}
		classify(h, r.gateway)
	})
}

// ippPort is the IPP port; IPP is queried at most once per probed host and
// only when it is open.
const ippPort = 631

func computeStats(hosts []model.Host, scanned int) model.Stats {
	st := model.Stats{Scanned: scanned}
	for _, h := range hosts {
		if h.Alive {
			st.Alive++
			if !h.Online && !h.IsSelf {
				st.Offline++
			}
		}
		if h.IsPrinter {
			st.Printers++
		}
		if h.MDNS != nil {
			st.MDNS++
		}
		for _, w := range h.Web {
			if w.Error == "" && w.Status > 0 {
				st.Web++
				break
			}
		}
	}
	return st
}

// newID returns a random RFC 4122 version-4 UUID.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
