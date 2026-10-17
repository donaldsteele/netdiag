package scan

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"netdiag/internal/devtype"
	"netdiag/internal/health"
	"netdiag/internal/ipp"
	"netdiag/internal/mdns"
	"netdiag/internal/model"
	"netdiag/internal/notify"
	"netdiag/internal/snmpprobe"
	"netdiag/internal/store"
)

// CheckpointInterval is how often a live scan is saved to history.
const CheckpointInterval = 5 * time.Minute

// ReprobeCooldown caps the full re-probe after a MAC change to once per IP
// per window, so a flapping IP never causes a probe storm.
const ReprobeCooldown = 10 * time.Minute

// PrinterPollInterval is how often live mode re-reads known printers' SNMP
// and IPP status (at most one poll per printer per interval).
const PrinterPollInterval = 60 * time.Second

// StatusPollInterval is the default (Settings: printerStatusPollSec) for how
// often live mode reads just the status objects
// (device status, error bits) of known SNMP printers; a change triggers an
// immediate full printer poll.
const StatusPollInterval = 5 * time.Second

// MaxNotices is the number of notices kept on a scan result.
const MaxNotices = 100

// ReplayNotices is the number of recent notices replayed to new subscribers.
const ReplayNotices = 20

// MaxMACHistory is the number of MAC observations kept per IP.
const MaxMACHistory = 8

// HealthChecker runs the health checks (*health.Runner satisfies it).
type HealthChecker interface {
	Run(ctx context.Context, in health.Input) model.HealthReport
}

// MDNSBrowser browses mDNS on an interface with at most rounds query rounds.
type MDNSBrowser interface {
	Browse(ctx context.Context, ifaceName string, rounds int) ([]mdns.Record, error)
}

// IPPClient queries a printer via IPP Get-Printer-Attributes (*ipp.Client
// satisfies it).
type IPPClient interface {
	GetPrinterAttributes(ctx context.Context, ip string, port int) (*ipp.Result, error)
}

// HistoryStore persists scan results (*store.Store satisfies it).
type HistoryStore interface {
	SaveScan(r model.ScanResult) error
}

// SettingsSource provides the persisted settings (*store.Store satisfies it).
type SettingsSource interface {
	LoadSettings() (model.Settings, error)
}

// mdnsBrowser is the production MDNSBrowser.
type mdnsBrowser struct{}

func (mdnsBrowser) Browse(ctx context.Context, ifaceName string, rounds int) ([]mdns.Record, error) {
	b := &mdns.Browser{Rounds: rounds}
	return b.Browse(ctx, ifaceName, nil, mdns.DefaultWindow)
}

// withV2Defaults adds the production v0.2 dependencies (health, mDNS, IPP and,
// if the data directory is usable, the store) to d.
func withV2Defaults(d Deps) Deps {
	d.Health = health.NewRunner()
	d.MDNS = mdnsBrowser{}
	d.IPP = &ipp.Client{}
	if st, err := store.Open(""); err == nil {
		d.Store = st
		d.Settings = st
	}
	return d
}

// loadSettings returns the persisted settings, or defaults.
func loadSettings(src SettingsSource) model.Settings {
	if src != nil {
		if s, err := src.LoadSettings(); err == nil {
			return s
		}
	}
	return store.DefaultSettings()
}

// notifyConfig maps settings to the alert engine config.
func notifyConfig(s model.Settings) notify.Config {
	t := s.Alerts.Triggers
	cfg := notify.Config{
		Triggers: map[string]bool{
			model.TriggerPrinterOffline: t.PrinterOffline,
			model.TriggerDeviceOffline:  t.DeviceOffline,
			model.TriggerSupplyLow:      t.SupplyLow,
			model.TriggerPrinterAlert:   t.PrinterAlert,
			model.TriggerNewDevice:      t.NewDevice,
			model.TriggerIPConflict:     t.IPConflict,
			model.TriggerHealthFailed:   t.HealthFailed,
		},
		SupplyThresholdPct: s.Alerts.SupplyThresholdPct,
		Cooldown:           time.Duration(s.Alerts.CooldownSec) * time.Second,
	}
	if cfg.SupplyThresholdPct <= 0 {
		cfg.SupplyThresholdPct = notify.DefaultSupplyThresholdPct
	}
	if s.Alerts.CooldownSec <= 0 {
		cfg.Cooldown = notify.DefaultCooldown
	}
	return cfg
}

// classify applies the v0.1 printer rules plus IPP data and mDNS printer
// services.
func classify(h *model.Host, gateway string) {
	snmpprobe.Apply(h)
	if h.IsPrinter && h.Printer != nil && h.Printer.Source == ipp.SourceIPP &&
		(h.Snmp == nil || h.Snmp.HrPrinterStatus == "") {
		h.PrinterReason = "ipp: Get-Printer-Attributes answered"
	}
	if !h.IsPrinter && h.MDNS != nil {
		if svc := mdns.PrinterService(h.MDNS.Services); svc != "" {
			h.IsPrinter, h.PrinterReason = true, "mdns service "+svc
		}
	}
	t, why := devtype.Classify(*h, gateway)
	h.DeviceType, h.DeviceTypeReason = string(t), why
}

// mergeMDNS returns a new MDNSInfo combining old (may be nil) and add.
func mergeMDNS(old *model.MDNSInfo, add model.MDNSInfo) *model.MDNSInfo {
	out := model.MDNSInfo{}
	if old != nil {
		out = *old
		out.Services = slices.Clone(old.Services)
		out.TXT = maps.Clone(old.TXT)
	}
	if add.Instance != "" {
		out.Instance = add.Instance
	}
	if add.Hostname != "" {
		out.Hostname = add.Hostname
	}
	for _, s := range add.Services {
		if !slices.Contains(out.Services, s) {
			out.Services = append(out.Services, s)
		}
	}
	slices.Sort(out.Services)
	if len(add.TXT) > 0 {
		if out.TXT == nil {
			out.TXT = map[string]string{}
		}
		maps.Copy(out.TXT, add.TXT)
	}
	return &out
}

// mdnsEqual reports whether merging add into old would change nothing.
func mdnsEqual(old *model.MDNSInfo, add model.MDNSInfo) bool {
	if old == nil {
		return false
	}
	m := mergeMDNS(old, add)
	return m.Instance == old.Instance && m.Hostname == old.Hostname &&
		slices.Equal(m.Services, old.Services) && maps.Equal(m.TXT, old.TXT)
}

// mergePrinter combines SNMP printer data (may be nil) with an IPP result:
// SNMP fields win where present, IPP fills the gaps. It returns a new value
// (printer pointers are treated as immutable once published).
func mergePrinter(snmp *model.PrinterInfo, res *ipp.Result) *model.PrinterInfo {
	var ip model.PrinterInfo
	if res.Printer != nil {
		ip = *res.Printer
	}
	if ip.State == "" {
		ip.State = res.State
	}
	if len(ip.StateReasons) == 0 {
		ip.StateReasons = slices.Clone(res.StateReasons)
	}
	if snmp == nil {
		ip.Source = ipp.SourceIPP
		return &ip
	}
	out := *snmp
	if out.Model == "" {
		out.Model = ip.Model
	}
	if out.Serial == "" {
		out.Serial = ip.Serial
	}
	if out.PageCount == 0 {
		out.PageCount = ip.PageCount
	}
	if len(out.ConsoleDisplay) == 0 {
		out.ConsoleDisplay = ip.ConsoleDisplay
	}
	if len(out.Supplies) == 0 {
		out.Supplies = ip.Supplies
	}
	if len(out.InputTrays) == 0 {
		out.InputTrays = ip.InputTrays
	}
	if len(out.Alerts) == 0 {
		out.Alerts = ip.Alerts
	}
	if len(out.Covers) == 0 {
		out.Covers = ip.Covers
	}
	if out.State == "" {
		out.State = ip.State
	}
	if len(out.StateReasons) == 0 {
		out.StateReasons = ip.StateReasons
	}
	out.Source = "snmp+ipp"
	return &out
}

// withSNMPSource returns p (a fresh SNMP printer) tagged with source "snmp".
func withSNMPSource(p *model.PrinterInfo) *model.PrinterInfo {
	if p == nil || p.Source != "" {
		return p
	}
	c := *p
	c.Source = "snmp"
	return &c
}

// analysisHosts returns copies of hosts for conflict analysis; a host with no
// hostname uses its mDNS hostname so duplicate mDNS names are detected.
func analysisHosts(hosts []model.Host) []model.Host {
	out := make([]model.Host, len(hosts))
	for i, h := range hosts {
		if h.Hostname == "" && h.MDNS != nil {
			h.Hostname = strings.TrimSuffix(h.MDNS.Hostname, ".")
		}
		out[i] = h
	}
	return out
}

// MDNSRoundsScan and MDNSRoundsLive are the mDNS query rounds per initial
// scan and per live cycle (Locked Decision 3).
const (
	MDNSRoundsScan = 2
	MDNSRoundsLive = 1
)

// healthInput builds the health check input from the scan context and the
// current settings.
func (r *runner) healthInput() health.Input {
	r.smu.Lock()
	cfg := health.ConfigFor(r.settings)
	r.smu.Unlock()
	return health.Input{Iface: r.self.Interface, IfaceIP: r.self.IP, Gateway: r.gateway, Config: cfg}
}

// startHealth runs the health checks in the background (if enabled) and
// posts the report to the state goroutine. The caller must bg.Wait before
// closing the state.
func (r *runner) startHealth(ctx context.Context, bg *sync.WaitGroup) {
	if r.d.Health == nil {
		return
	}
	in := r.healthInput()
	bg.Add(1)
	go func() {
		defer bg.Done()
		rep := r.d.Health.Run(ctx, in)
		if ctx.Err() == nil {
			r.st.setHealth(rep)
		}
	}()
}

// startMDNS browses mDNS in the background; the channel yields the records
// (nil when disabled or failed) exactly once. Only the initial scan
// (rounds == MDNSRoundsScan) reports problems as warnings, so live cycles do
// not repeat them.
func (r *runner) startMDNS(ctx context.Context, rounds int) <-chan []mdns.Record {
	ch := make(chan []mdns.Record, 1)
	if r.d.MDNS == nil {
		ch <- nil
		return ch
	}
	go func() {
		recs, err := r.d.MDNS.Browse(ctx, r.self.Interface, rounds)
		if rounds == MDNSRoundsScan && ctx.Err() == nil {
			switch {
			case err != nil:
				r.warn("mdns: " + err.Error())
			case len(recs) == 0:
				r.warn("mdns: no responses")
			}
		}
		ch <- recs
	}()
	return ch
}

// applySettings stores set for later health runs and forwards it to the
// state goroutine.
func (r *runner) applySettings(set model.Settings) bool {
	r.smu.Lock()
	r.settings = set
	r.smu.Unlock()
	return r.st.applySettings(set)
}

// checkpoint saves the current live snapshot to history asynchronously; a
// failure becomes a warning.
func (r *runner) checkpoint() {
	if r.d.Store == nil {
		return
	}
	snap := r.s.Snapshot()
	r.cpWG.Add(1)
	go func() {
		defer r.cpWG.Done()
		if err := r.d.Store.SaveScan(snap); err != nil {
			r.warn("history checkpoint: " + err.Error())
		}
	}()
}
