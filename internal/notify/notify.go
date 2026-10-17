// Package notify is a pure, deterministic alert rule engine. It turns state
// transitions into Notices, honouring per-trigger toggles, a supply threshold,
// dedupe keys and a cooldown. Each fault it reports opens a problem under its
// key (see Key); the OK notice that later closes the problem carries the same
// key, so whatever showed the fault (a banner, a chat card) can show it fixed.
// It never performs I/O.
package notify

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"netdiag/internal/model"
)

// DefaultCooldown is the default dedupe cooldown.
const DefaultCooldown = 300 * time.Second

// DefaultSupplyThresholdPct is the default low-supply threshold.
const DefaultSupplyThresholdPct = 15

// Debounce counts (v0.6-D18). A health check is reported after it failed
// HealthFailRuns runs in a row and fixed after it did not fail HealthOKRuns
// runs in a row. An offline device is fixed after OnlineOKChecks successful
// presence checks in a row (OfflineAfterMisses missed checks took it
// offline), and a conflict after it was absent from ConflictOKRuns analyses
// in a row. A printer alert closes as soon as the printer stops reporting it:
// the printer is authoritative.
const (
	HealthFailRuns = 2
	HealthOKRuns   = 2
	OnlineOKChecks = 2
	ConflictOKRuns = 2
)

// SupplyHysteresisPct is how far above the threshold a low supply must rise
// before it counts as fixed, so a level that wobbles around the threshold
// does not open and close the problem on every poll.
const SupplyHysteresisPct = 5

// Config holds the alert settings the engine needs.
type Config struct {
	// Triggers maps trigger name (model.Trigger*) to enabled. Missing = disabled.
	Triggers           map[string]bool
	SupplyThresholdPct int
	Cooldown           time.Duration
}

// DefaultConfig mirrors the default settings (deviceOffline off).
func DefaultConfig() Config {
	return Config{
		Triggers: map[string]bool{
			model.TriggerPrinterOffline: true,
			model.TriggerDeviceOffline:  false,
			model.TriggerSupplyLow:      true,
			model.TriggerPrinterAlert:   true,
			model.TriggerNewDevice:      true,
			model.TriggerIPConflict:     true,
			model.TriggerHealthFailed:   true,
		},
		SupplyThresholdPct: DefaultSupplyThresholdPct,
		Cooldown:           DefaultCooldown,
	}
}

// OpenFault is a problem the engine reported and has not closed yet. The
// open-problem ledger (v0.6 F2b) keeps these across restarts.
type OpenFault struct {
	Key, Trigger, Severity, IP, MAC, Name, Title string
	OpenedAt                                     time.Time
}

// fault is an open problem plus its progress toward the OK notice.
type fault struct {
	OpenFault
	detail string // what the OK message names (a printer alert's description)
	seen   string // offline: the host's LastSeen when last observed
	good   int    // good observations in a row
}

// Engine produces Notices from transitions.
type Engine struct {
	mu   sync.Mutex
	cfg  Config
	now  func() time.Time
	seq  int
	last map[string]time.Time // cooldown: when each key last produced a notice
	// swept is when prune last ran.
	swept time.Time
	// open holds every problem reported and not yet fixed, by key. Only an
	// open problem can get an OK notice, so netdiag never announces a
	// recovery from a fault nobody was told about.
	open map[string]*fault
	// bad counts each health check's failed runs in a row (by check id).
	bad map[string]int
	// held is the storm guard (SetHold).
	held bool
}

// New creates an engine. A nil clock uses time.Now.
func New(cfg Config, clock func() time.Time) *Engine {
	if clock == nil {
		clock = time.Now
	}
	return &Engine{cfg: cfg, now: clock, last: map[string]time.Time{},
		open: map[string]*fault{}, bad: map[string]int{}}
}

// SetConfig replaces the config (dedupe history and open problems are kept).
func (e *Engine) SetConfig(cfg Config) {
	e.mu.Lock()
	e.cfg = cfg
	e.mu.Unlock()
}

// SetHold turns the storm guard's hold on or off. While it is on, new
// presence and health faults are dropped and leave no trace (no open
// problem, no cooldown, no failed run counted), so this computer losing the
// network does not report every device offline. OK notices still go out.
func (e *Engine) SetHold(on bool) {
	e.mu.Lock()
	e.held = on
	e.mu.Unlock()
}

// Open returns the open problems, sorted by key.
func (e *Engine) Open() []OpenFault {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]OpenFault, 0, len(e.open))
	for _, f := range e.open {
		out = append(out, f.OpenFault)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// holdable reports whether the storm guard drops a trigger's faults.
func holdable(trigger string) bool {
	switch trigger {
	case model.TriggerPrinterOffline, model.TriggerDeviceOffline, model.TriggerHealthFailed:
		return true
	}
	return false
}

// emit appends the fault notice n unless its trigger is off, the storm guard
// holds it, its problem is already open or its key is inside the cooldown.
// A reported fault opens its problem (new devices have nothing to fix).
func (e *Engine) emit(out []model.Notice, n model.Notice, detail string) []model.Notice {
	if !e.cfg.Triggers[n.Trigger] || (e.held && holdable(n.Trigger)) {
		return out
	}
	if _, ok := e.open[n.Key]; ok {
		return out
	}
	now := e.now()
	e.prune(now)
	if t, ok := e.last[n.Key]; ok && now.Sub(t) < e.cfg.Cooldown {
		return out
	}
	e.last[n.Key] = now
	n.ID, n.At = e.next(now)
	if n.Trigger != model.TriggerNewDevice {
		e.open[n.Key] = &fault{detail: detail, OpenFault: OpenFault{
			Key: n.Key, Trigger: n.Trigger, Severity: n.Severity, IP: n.IP, MAC: n.MAC,
			Name: n.Name, Title: n.Title, OpenedAt: now}}
	}
	return append(out, n)
}

// emitOK closes the open problem f with an OK notice under the same key. It
// bypasses the trigger switches and the cooldown, so a problem someone was
// told about always closes, even after its trigger was switched off, and it
// forgets the key's cooldown, so a relapse is reported straight away.
func (e *Engine) emitOK(out []model.Notice, f *fault, title, msg string) []model.Notice {
	now := e.now()
	delete(e.open, f.Key)
	delete(e.last, f.Key)
	n := model.Notice{Trigger: f.Trigger, Severity: sevInfo, IP: f.IP, MAC: f.MAC, Name: f.Name,
		Title: title, Message: msg, Key: f.Key, OK: true,
		OpenedAt: f.OpenedAt.UTC().Format(time.RFC3339)}
	n.ID, n.At = e.next(now)
	return append(out, n)
}

func (e *Engine) next(now time.Time) (id, at string) {
	e.seq++
	return fmt.Sprintf("n-%d", e.seq), now.UTC().Format(time.RFC3339)
}

// prune drops cooldown entries older than the cooldown: they can no longer
// hold back a notice. It sweeps at most once per cooldown period, so the
// cost stays small and the map only holds keys from the last two periods
// instead of every key ever seen (a long live scan on a busy network sees
// many devices come and go).
func (e *Engine) prune(now time.Time) {
	if now.Sub(e.swept) < e.cfg.Cooldown {
		return
	}
	e.swept = now
	for k, t := range e.last {
		if now.Sub(t) >= e.cfg.Cooldown {
			delete(e.last, k)
		}
	}
}

// openFor returns the open problem of one of triggers on h with key part
// part, under its MAC key or the IP key it may have opened under before the
// MAC was known (nil when there is none).
func (e *Engine) openFor(h model.Host, part string, triggers ...string) *fault {
	for _, t := range triggers {
		for _, id := range hostIDs(h) {
			if f := e.open[keyFor(t, id, part)]; f != nil {
				return f
			}
		}
	}
	return nil
}

// openOf returns the open problems of trigger whose key starts with one of
// prefixes, sorted by key.
func (e *Engine) openOf(trigger string, prefixes ...string) []*fault {
	var out []*fault
	for k, f := range e.open {
		if f.Trigger != trigger {
			continue
		}
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				out = append(out, f)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func label(h model.Host) string {
	switch {
	case h.Hostname != "":
		return h.Hostname + " (" + h.IP + ")"
	case h.Printer != nil && h.Printer.Model != "":
		return h.Printer.Model + " (" + h.IP + ")"
	}
	return h.IP
}

// bestName is the best name netdiag has for h: its mDNS instance, SNMP
// system name, host name or printer model, in that order ("" for none).
func bestName(h model.Host) string {
	switch {
	case h.MDNS != nil && h.MDNS.Instance != "":
		return h.MDNS.Instance
	case h.Snmp != nil && h.Snmp.SysName != "":
		return h.Snmp.SysName
	case h.Hostname != "":
		return h.Hostname
	case h.Printer != nil && h.Printer.Model != "":
		return h.Printer.Model
	}
	return ""
}

// hostNotice is a fault notice about h; emit fills in its id and time.
func hostNotice(trigger string, h model.Host, part string, ctx sevCtx, title, msg string) model.Notice {
	return model.Notice{Trigger: trigger, Severity: severityFor(trigger, ctx), IP: h.IP,
		MAC: strings.ToUpper(h.MAC), Name: bestName(h), Title: title, Message: msg,
		Key: Key(trigger, h, part)}
}

// Observe compares a host's previous and current state. A zero prev (empty IP)
// means the host was not known before.
func (e *Engine) Observe(prev, cur model.Host) []model.Notice {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []model.Notice
	l := label(cur)

	if prev.IP == "" {
		if cur.IsNew {
			out = e.emit(out, hostNotice(model.TriggerNewDevice, cur, "", sevCtx{}, "New device",
				fmt.Sprintf("%s appeared on the network.", l)), "")
		}
		return out
	}

	out = e.presence(out, prev, cur, l)
	if cur.Printer != nil {
		out = e.supplies(out, prev.Printer, cur, l)
		out = e.printerAlerts(out, prev.Printer, cur, l)
	}
	return out
}

// presence reports a device going offline and closes that problem once the
// device answered OnlineOKChecks presence checks in a row. Each successful
// check stamps a new LastSeen, so a changed LastSeen counts as one good
// check; a miss or going offline again starts the count over.
func (e *Engine) presence(out []model.Notice, prev, cur model.Host, l string) []model.Notice {
	offline := []string{model.TriggerPrinterOffline, model.TriggerDeviceOffline}
	if prev.Online && !cur.Online && e.openFor(cur, "", offline...) == nil {
		if cur.IsPrinter {
			out = e.emit(out, hostNotice(model.TriggerPrinterOffline, cur, "", sevCtx{}, "Printer offline",
				fmt.Sprintf("Printer %s stopped responding.", l)), "")
		} else {
			out = e.emit(out, hostNotice(model.TriggerDeviceOffline, cur, "", sevCtx{}, "Device offline",
				fmt.Sprintf("%s stopped responding.", l)), "")
		}
	}
	f := e.openFor(cur, "", offline...)
	switch {
	case f == nil:
	case !cur.Online || cur.MissedChecks > 0:
		f.good, f.seen = 0, cur.LastSeen
	case cur.LastSeen != f.seen:
		f.good++
		f.seen = cur.LastSeen
		if f.good >= OnlineOKChecks {
			if f.Trigger == model.TriggerPrinterOffline {
				out = e.emitOK(out, f, "Printer back online", fmt.Sprintf("Printer %s answers again.", l))
			} else {
				out = e.emitOK(out, f, "Device back online", fmt.Sprintf("%s answers again.", l))
			}
		}
	}
	return out
}

// supplies reports a supply crossing down to the threshold and closes that
// problem once the supply is more than SupplyHysteresisPct above it.
func (e *Engine) supplies(out []model.Notice, pp *model.PrinterInfo, cur model.Host, l string) []model.Notice {
	thr := e.cfg.SupplyThresholdPct
	for _, cs := range cur.Printer.Supplies {
		part := supplyPart(cs.Description)
		if f := e.openFor(cur, part, model.TriggerSupplyLow); f != nil {
			if cs.Percent > thr+SupplyHysteresisPct {
				out = e.emitOK(out, f, "Supply OK again",
					fmt.Sprintf("%s: %s at %d%%.", l, cs.Description, cs.Percent))
			}
			continue
		}
		if pp == nil {
			continue
		}
		for _, ps := range pp.Supplies {
			if ps.Description != cs.Description {
				continue
			}
			if ps.Percent >= 0 && cs.Percent >= 0 && ps.Percent > thr && cs.Percent <= thr {
				out = e.emit(out, hostNotice(model.TriggerSupplyLow, cur, part, sevCtx{}, "Supply low",
					fmt.Sprintf("%s: %s at %d%% (threshold %d%%).", l, cs.Description, cs.Percent, thr)), "")
			}
			break
		}
	}
	return out
}

// printerAlerts reports each alert the printer newly raises under its own key
// and closes each one as soon as the printer stops reporting it.
func (e *Engine) printerAlerts(out []model.Notice, pp *model.PrinterInfo, cur model.Host, l string) []model.Notice {
	was := map[string]bool{}
	if pp != nil {
		for _, a := range pp.Alerts {
			was[a.Code+"|"+a.Description] = true
		}
	}
	now := map[string]bool{} // keys of the alerts the printer reports now
	for _, a := range cur.Printer.Alerts {
		part := alertPart(a)
		for _, id := range hostIDs(cur) {
			now[keyFor(model.TriggerPrinterAlert, id, part)] = true
		}
		if was[a.Code+"|"+a.Description] || e.openFor(cur, part, model.TriggerPrinterAlert) != nil {
			continue
		}
		out = e.emit(out, hostNotice(model.TriggerPrinterAlert, cur, part, sevCtx{printer: a.Severity},
			"Printer alert", fmt.Sprintf("%s: %s", l, a.Description)), a.Description)
	}
	var prefixes []string
	for _, id := range hostIDs(cur) {
		prefixes = append(prefixes, model.TriggerPrinterAlert+"|"+id+"|")
	}
	for _, f := range e.openOf(model.TriggerPrinterAlert, prefixes...) {
		if now[f.Key] {
			continue
		}
		msg := fmt.Sprintf("%s: the printer alert is cleared.", l)
		if f.detail != "" {
			msg = fmt.Sprintf("%s: cleared %s.", l, f.detail)
		}
		out = e.emitOK(out, f, "Printer alert cleared", msg)
	}
	return out
}

// ObserveConflicts takes the conflicts of one analysis. A conflict whose key
// (ConflictKey) is not open is reported; an open one closes after its key was
// absent from ConflictOKRuns analyses in a row. A conflict whose id changed
// under the same key is the same problem: no OK and no new notice.
func (e *Engine) ObserveConflicts(cur []model.Conflict) []model.Notice {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []model.Notice
	present := make(map[string]bool, len(cur))
	for _, c := range cur {
		k := ConflictKey(c)
		present[k] = true
		if f := e.open[k]; f != nil {
			f.good = 0
			continue
		}
		mac := ""
		if c.Kind == model.ConflictMACMultiIP {
			mac = strings.ToUpper(first(c.MACs))
		}
		// The analyzer marks a mac_flap "fail" only when the MACs were seen
		// taking turns (a swap), not on a single change.
		ctx := sevCtx{kind: c.Kind, swap: c.Kind == model.ConflictMACFlap && c.Severity == "fail"}
		out = e.emit(out, model.Notice{Trigger: model.TriggerIPConflict, Severity: severityFor(model.TriggerIPConflict, ctx),
			IP: first(c.IPs), MAC: mac, Name: c.Name, Title: "Network conflict", Message: c.Detail, Key: k}, "")
	}
	for _, f := range e.openOf(model.TriggerIPConflict, model.TriggerIPConflict+"|") {
		if present[f.Key] {
			continue
		}
		if f.good++; f.good < ConflictOKRuns {
			continue
		}
		msg := "The conflict is gone."
		if f.IP != "" {
			msg = fmt.Sprintf("The conflict at %s is gone.", f.IP)
		}
		out = e.emitOK(out, f, "Network conflict cleared", msg)
	}
	return out
}

// ObserveHealth takes one health report. A check is reported after it failed
// HealthFailRuns runs in a row, so one bad run never reaches anyone and a
// one-shot scan never raises one, and closed after it did not fail
// HealthOKRuns runs in a row. Checks missing from rep keep their state.
func (e *Engine) ObserveHealth(rep model.HealthReport) []model.Notice {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []model.Notice
	for _, c := range rep.Checks {
		name := c.Label
		if name == "" {
			name = c.ID
		}
		key := HealthKey(c.ID)
		f := e.open[key]
		if c.Status == model.HealthFail {
			switch {
			case f != nil:
				f.good = 0
			case e.held:
				// A failed run while the storm guard holds does not count.
			default:
				e.bad[c.ID]++
				if e.bad[c.ID] >= HealthFailRuns {
					out = e.emit(out, model.Notice{Trigger: model.TriggerHealthFailed,
						Severity: severityFor(model.TriggerHealthFailed, sevCtx{check: c.ID}),
						Title:    "Health check failed", Message: fmt.Sprintf("%s check failed.", name), Key: key}, "")
				}
			}
			continue
		}
		delete(e.bad, c.ID)
		if f == nil {
			continue
		}
		if f.good++; f.good >= HealthOKRuns {
			out = e.emitOK(out, f, "Health check OK again", fmt.Sprintf("%s check is OK again.", name))
		}
	}
	return out
}
