// Package notify is a pure, deterministic alert rule engine. It turns state
// transitions into Notices, honouring per-trigger toggles, a supply threshold,
// dedupe keys and a cooldown. Each fault it reports opens a problem under its
// key (see Key); the OK notice that later closes the problem carries the same
// key, so whatever showed the fault (a banner, a chat card) can show it fixed.
// It never performs I/O.
package notify

import (
	"fmt"
	"slices"
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
// HealthFailRuns runs in a row and fixed after it passed HealthOKRuns runs
// in a row (a run that could not check, "unavailable", counts neither way).
// An offline device is fixed after OnlineOKChecks successful presence checks
// in a row (OfflineAfterMisses missed checks took it offline), and a
// conflict after it was absent from ConflictOKRuns cycle analyses in a row.
// A printer alert closes as soon as the printer stops reporting it: the
// printer is authoritative.
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
	detail string // what the OK message names (a printer alert's or supply's description)
	seen   string // offline: the host's LastSeen when last observed
	good   int    // good observations in a row
	// folded holds the printer's general alerts (see general) that came
	// with this printer alert, by alertID: they are part of this problem.
	folded map[string]bool
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
	// at is the MAC each IP was last observed with (see belongs).
	at map[string]string
	// held is the storm guard (SetHold).
	held bool
}

// New creates an engine. A nil clock uses time.Now.
func New(cfg Config, clock func() time.Time) *Engine {
	if clock == nil {
		clock = time.Now
	}
	return &Engine{cfg: cfg, now: clock, last: map[string]time.Time{},
		open: map[string]*fault{}, bad: map[string]int{}, at: map[string]string{}}
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
// network does not report every device offline. Turning it on also forgets
// the failed runs counted before it, so after the hold a check needs its
// full HealthFailRuns again. OK notices still go out.
func (e *Engine) SetHold(on bool) {
	e.mu.Lock()
	e.held = on
	if on {
		clear(e.bad)
	}
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

// belongs reports whether the open problem f, found under one of h's keys,
// is h's. Several devices can answer with one MAC (a range extender or mesh
// node proxy-ARPs for the devices behind it), so a problem is h's when it
// opened at h's IP, or when its device moved to h's IP: another MAC answers
// at its old IP now.
func (e *Engine) belongs(f *fault, h model.Host) bool {
	if f.IP == h.IP {
		return true
	}
	m, ok := e.at[f.IP]
	return ok && m != f.MAC
}

// hostKey is the key a new problem of trigger on h opens under: Key, or h's
// IP key while another device behind the same MAC has Key open.
func (e *Engine) hostKey(trigger string, h model.Host, part string) string {
	k := Key(trigger, h, part)
	if f := e.open[k]; f != nil && !e.belongs(f, h) {
		return keyFor(trigger, "ip:"+h.IP, part)
	}
	return k
}

// openFor returns h's open problem of one of triggers with key part part,
// under its MAC key or under the IP key it opened under while the MAC was
// unknown or taken (see hostKey); nil when there is none.
func (e *Engine) openFor(h model.Host, part string, triggers ...string) *fault {
	for _, t := range triggers {
		for _, id := range hostIDs(h) {
			if f := e.open[keyFor(t, id, part)]; f != nil && e.belongs(f, h) {
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

// hostOpen returns h's open problems of trigger, sorted by key.
func (e *Engine) hostOpen(trigger string, h model.Host) []*fault {
	var prefixes []string
	for _, id := range hostIDs(h) {
		prefixes = append(prefixes, trigger+"|"+id+"|")
	}
	var out []*fault
	for _, f := range e.openOf(trigger, prefixes...) {
		if e.belongs(f, h) {
			out = append(out, f)
		}
	}
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
func (e *Engine) hostNotice(trigger string, h model.Host, part string, ctx sevCtx, title, msg string) model.Notice {
	return model.Notice{Trigger: trigger, Severity: severityFor(trigger, ctx), IP: h.IP,
		MAC: macOf(h), Name: bestName(h), Title: title, Message: msg,
		Key: e.hostKey(trigger, h, part)}
}

// Observe compares a host's previous and current state. A zero prev (empty IP)
// means the host was not known before.
func (e *Engine) Observe(prev, cur model.Host) []model.Notice {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []model.Notice
	l := label(cur)
	if m := macOf(cur); m != "" {
		e.at[cur.IP] = m
	}

	if prev.IP == "" {
		if cur.IsNew {
			out = e.emit(out, e.hostNotice(model.TriggerNewDevice, cur, devicePart(cur), sevCtx{}, "New device",
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
			out = e.emit(out, e.hostNotice(model.TriggerPrinterOffline, cur, "", sevCtx{}, "Printer offline",
				fmt.Sprintf("Printer %s stopped responding.", l)), "")
		} else {
			out = e.emit(out, e.hostNotice(model.TriggerDeviceOffline, cur, "", sevCtx{}, "Device offline",
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
// problem once the supply is more than SupplyHysteresisPct above it (or at
// 100%, for a threshold that leaves no room), once it reports some left
// without a level (a new cartridge that only says "ok"), or once a printer
// that still lists its supplies no longer lists it (a cartridge swapped for
// another kind). A poll that lists no supplies says nothing about them.
func (e *Engine) supplies(out []model.Notice, pp *model.PrinterInfo, cur model.Host, l string) []model.Notice {
	thr := e.cfg.SupplyThresholdPct
	okAt := min(thr+SupplyHysteresisPct+1, 100)
	before := map[string]model.Supply{}
	if pp != nil {
		for i, part := range supplyParts(pp.Supplies) {
			before[part] = pp.Supplies[i]
		}
	}
	listed := map[string]bool{} // keys of the supplies the printer lists now
	for i, part := range supplyParts(cur.Printer.Supplies) {
		cs := cur.Printer.Supplies[i]
		for _, id := range hostIDs(cur) {
			listed[keyFor(model.TriggerSupplyLow, id, part)] = true
		}
		if f := e.openFor(cur, part, model.TriggerSupplyLow); f != nil {
			switch {
			case cs.Percent >= okAt:
				out = e.emitOK(out, f, "Supply OK again",
					fmt.Sprintf("%s: %s at %d%%.", l, cs.Description, cs.Percent))
			case cs.Percent < 0 && cs.State == "ok":
				out = e.emitOK(out, f, "Supply OK again",
					fmt.Sprintf("%s: %s is no longer low.", l, cs.Description))
			}
			continue
		}
		ps, ok := before[part]
		if ok && ps.Percent >= 0 && cs.Percent >= 0 && ps.Percent > thr && cs.Percent <= thr {
			out = e.emit(out, e.hostNotice(model.TriggerSupplyLow, cur, part, sevCtx{}, "Supply low",
				fmt.Sprintf("%s: %s at %d%% (threshold %d%%).", l, cs.Description, cs.Percent, thr)), cs.Description)
		}
	}
	if len(cur.Printer.Supplies) == 0 {
		return out
	}
	for _, f := range e.hostOpen(model.TriggerSupplyLow, cur) {
		if listed[f.Key] {
			continue
		}
		msg := fmt.Sprintf("%s no longer reports the low supply.", l)
		if f.detail != "" {
			msg = fmt.Sprintf("%s no longer reports %s.", l, f.detail)
		}
		out = e.emitOK(out, f, "Supply OK again", msg)
	}
	return out
}

// general reports whether a is a printer's general "offline or paused"
// alert. A printer that stops for a specific reason (a cover open, a paper
// jam) raises it too, so next to such an error it is part of that problem,
// not one of its own (v0.6-D22): opening a Zebra's cover is one alert.
func general(a model.Alert) bool {
	switch a.Code {
	case "hrPrinterDetectedErrorState.offline", "paused":
		return true
	}
	return false
}

// stops reports whether a is a specific error that stops the printer.
func stops(a model.Alert) bool {
	return !general(a) && (a.Severity == "critical" || a.Severity == "error")
}

func alertID(a model.Alert) string { return a.Code + "|" + a.Description }

// printerAlerts reports each alert the printer newly raises under its own key
// and closes each one as soon as the printer stops reporting it. A general
// alert (see general) that comes with a specific error is not reported: the
// specific error's problem takes it in and stays open until both are gone,
// since a Zebra clears its cover alert a moment before its offline flag.
func (e *Engine) printerAlerts(out []model.Notice, pp *model.PrinterInfo, cur model.Host, l string) []model.Notice {
	was := map[string]bool{}
	if pp != nil {
		for _, a := range pp.Alerts {
			was[alertID(a)] = true
		}
	}
	alerts := cur.Printer.Alerts
	stopped := slices.ContainsFunc(alerts, stops)
	folded := map[string]bool{} // general alerts an open problem took in
	for _, f := range e.hostOpen(model.TriggerPrinterAlert, cur) {
		for id := range f.folded {
			folded[id] = true
		}
	}
	for _, a := range alerts {
		part := alertPart(a)
		if was[alertID(a)] || e.openFor(cur, part, model.TriggerPrinterAlert) != nil {
			continue
		}
		if general(a) && (stopped || folded[alertID(a)]) {
			continue
		}
		out = e.emit(out, e.hostNotice(model.TriggerPrinterAlert, cur, part, sevCtx{printer: a.Severity},
			"Printer alert", fmt.Sprintf("%s: %s", l, a.Description)), a.Description)
	}
	// The open problem of each specific error the printer reports takes in
	// the general alerts that have no problem of their own.
	reported := map[string]bool{}
	for _, a := range alerts {
		f := e.openFor(cur, alertPart(a), model.TriggerPrinterAlert)
		if f == nil {
			continue
		}
		reported[f.Key] = true
		if !stops(a) {
			continue
		}
		for _, g := range alerts {
			if general(g) && e.openFor(cur, alertPart(g), model.TriggerPrinterAlert) == nil {
				if f.folded == nil {
					f.folded = map[string]bool{}
				}
				f.folded[alertID(g)] = true
			}
		}
	}
	for _, f := range e.hostOpen(model.TriggerPrinterAlert, cur) {
		lingers := !stopped && slices.ContainsFunc(alerts, func(a model.Alert) bool { return f.folded[alertID(a)] })
		if reported[f.Key] || lingers {
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
// absent from ConflictOKRuns cycle analyses in a row. cycle is false for a
// second look at the same evidence (the analysis after a settings save): it
// reports conflicts but counts nothing toward an OK, so saving a setting
// cannot close a conflict early. A conflict whose id changed under the same
// key is the same problem: no OK and no new notice.
func (e *Engine) ObserveConflicts(cur []model.Conflict, cycle bool) []model.Notice {
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
	if !cycle {
		return out
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
// one-shot scan never raises one, and closed after it passed (ok, warn or
// info) HealthOKRuns runs in a row. A run that could not check
// ("unavailable": no gateway known, a local error, a timeout) says nothing
// either way and leaves both counts as they are, as do checks missing from
// rep.
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
		switch c.Status {
		case model.HealthUnavailable:
			continue
		case model.HealthFail:
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
