package problems

// This file is what is wrong right now (v0.6 #1, §4.11): the Problems right
// now card (GET /api/problems) and MCP list_problems read NewView (Current
// with the ledger), and the team chat first-connect burst posts its
// Alerted items; the Problems tab and MCP filter=problems use the broader
// per-device HostIssues.

import (
	"fmt"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"netdiag/internal/advice"
	"netdiag/internal/model"
	"netdiag/internal/notify"
)

// Card severities: the team chat words (model.ChatSeverity*), so a card
// item and its chat alert read the same. The engine's "warn" is "warning".
const (
	SevCritical = model.ChatSeverityCritical
	SevWarning  = model.ChatSeverityWarning
	SevInfo     = model.ChatSeverityInfo
)

// Card titles of the problems that are not alert notices of their own.
const (
	TitleHealthWarn   = "Health check warning"
	TitleHealthNotRun = "Health check could not run"
	TitlePossible     = "Possible conflict"
)

// DeviceRef names the device a problem is about.
type DeviceRef struct {
	IP   string `json:"ip"`
	MAC  string `json:"mac,omitempty"`
	Name string `json:"name,omitempty"`
}

// Problem is one item of the Problems right now card.
type Problem struct {
	// Key is the alert key (notify.Key, ConflictKey or HealthKey): the key
	// a banner, the ledger and the team chat card of the same problem have.
	Key string `json:"key"`
	// Kind is the alert trigger (model.Trigger*).
	Kind     string `json:"kind"`
	Severity string `json:"severity"` // SevCritical, SevWarning or SevInfo
	Title    string `json:"title"`
	// Detail says which one: the check, the printer alert, the supply and
	// its level, or the addresses of a conflict.
	Detail string     `json:"detail,omitempty"`
	Device *DeviceRef `json:"device,omitempty"`
	// Check is the health check id of a health item.
	Check string `json:"check,omitempty"`
	// Since is when the problem began (RFC 3339), when netdiag knows.
	Since  string        `json:"since,omitempty"`
	Advice *model.Advice `json:"advice,omitempty"`

	// keys are the keys the problem may be open under in the ledger
	// (notify.HostKeys: the MAC key, then the "ip:" key).
	keys []string
	// alerted: the ledger holds the problem open under Key (NewView), so
	// netdiag alerted on it and its OK will close it (View.Alerted).
	alerted bool
}

// rank orders severities: critical first.
func rank(sev string) int {
	switch sev {
	case SevCritical:
		return 0
	case SevWarning:
		return 1
	}
	return 2
}

// cardSeverity is the card word for an engine severity.
func cardSeverity(sev string) string {
	switch sev {
	case "critical":
		return SevCritical
	case "warn":
		return SevWarning
	}
	return SevInfo
}

// supplyThreshold is the low-supply threshold of set: 15 when it is not
// 1..100, as in the page (app.js supplyThreshold).
func supplyThreshold(set model.Settings) int {
	if t := set.Alerts.SupplyThresholdPct; t >= 1 && t <= 100 {
		return t
	}
	return notify.DefaultSupplyThresholdPct
}

// offline mirrors the page's isOffline: a found device that live mode
// marked offline. This computer is never offline.
func offline(h model.Host) bool { return h.Alive && !h.Online && !h.IsSelf }

// lowSupply reports a supply that is low, empty, or at or below thr.
func lowSupply(s model.Supply, thr int) bool {
	return s.State == "low" || s.State == "empty" || (s.Percent >= 0 && s.Percent <= thr)
}

// checkDone reports whether a health check gave an answer. A check that
// could not run counts as not done, except Wi-Fi and DHCP details this
// computer doesn't share (anything but a timeout): the page's rule since
// v0.6-D27.
func checkDone(c model.HealthCheck) bool {
	if c.Status != model.HealthUnavailable {
		return true
	}
	return (c.ID == "wifi" || c.ID == "dhcp") && model.HealthReason(c) != model.ReasonTimeout
}

func deviceRef(h model.Host) *DeviceRef {
	return &DeviceRef{IP: h.IP, MAC: strings.ToUpper(strings.TrimSpace(h.MAC)), Name: notify.Name(h)}
}

func checkName(c model.HealthCheck) string {
	if c.Label != "" {
		return c.Label
	}
	return c.ID
}

// normMAC is a MAC as conflicts and the ignore list carry it.
func normMAC(m string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(m), "-", ":"))
}

// activeConflicts are the conflicts of cs that involve none of the MACs the
// person ignored (Settings, Ignore this MAC): the page drops such a conflict
// as soon as the MAC is ignored (app.js ignoreMac, conflictsFor), while a
// finished scan keeps the conflicts it found.
func activeConflicts(cs []model.Conflict, set model.Settings) []model.Conflict {
	if len(set.IgnoredConflictMACs) == 0 {
		return cs
	}
	ign := make(map[string]bool, len(set.IgnoredConflictMACs))
	for _, m := range set.IgnoredConflictMACs {
		ign[normMAC(m)] = true
	}
	var out []model.Conflict
	for _, c := range cs {
		if !slices.ContainsFunc(c.MACs, func(m string) bool { return ign[normMAC(m)] }) {
			out = append(out, c)
		}
	}
	return out
}

// Current lists what is wrong in r right now, most urgent first, for the
// Problems right now card. Each item follows its Alerts trigger in set:
//
//   - health checks (healthFailed): a failed check (critical for the
//     gateway, a warning for the others) and a check that could not run (a
//     warning); a check that warns is a note (info), "Also worth knowing";
//   - an offline printer (printerOffline, a warning) and any other offline
//     device (deviceOffline, off by default, a note). An offline device
//     shows only that: its printer readings are from before;
//   - each printer alert (printerAlert; the printer's own severity) and
//     each supply at or below the threshold (supplyLow, a warning);
//   - each conflict (ipConflict; critical only for two devices seen taking
//     turns at one address) that involves no MAC the person ignored.
//
// The severities are the one severity map (notify, v0.6-D3). The keys are
// the alert keys, each item's its own: a second device behind the same MAC
// (a range extender) gets its "ip:" key, as the engine opens it. NewView
// swaps in the key a problem is open under in the ledger and its start.
//
// Current is what is wrong, not what netdiag alerted on: it also lists what
// the alert engine never opens (a check that could not run, a supply the
// printer calls low above the threshold, a one-shot scan's failed check,
// which has no streak of two). Team chat posts only View.Alerted, whose
// cards an OK closes.
func Current(r model.ScanResult, set model.Settings) []Problem {
	tr := set.Alerts.Triggers
	thr := supplyThreshold(set)
	byIP := make(map[string]model.Host, len(r.Hosts))
	for _, h := range r.Hosts {
		byIP[h.IP] = h
	}
	x := advice.Ctx{GOOS: runtime.GOOS, Gateway: r.Gateway, Hosts: byIP}
	var out []Problem
	// add appends p under the first of its keys that no earlier item of
	// another device has (Engine.hostKey). An item whose key the same device
	// already has is the same problem again (a printer listing one alert
	// twice), and is left out.
	taken := map[string]string{} // key -> the device address of its item
	add := func(p Problem) {
		ip := ""
		if p.Device != nil {
			ip = p.Device.IP
		}
		for _, k := range p.keys {
			owner, ok := taken[k]
			if !ok {
				p.Key, taken[k] = k, ip
				out = append(out, p)
				return
			}
			if owner == ip {
				return
			}
		}
	}

	if tr.HealthFailed && r.Health != nil {
		for _, c := range r.Health.Checks {
			p := Problem{Key: notify.HealthKey(c.ID), Kind: model.TriggerHealthFailed, Check: c.ID, Detail: checkName(c), Advice: c.Advice}
			switch {
			case c.Status == model.HealthFail:
				p.Severity, p.Title = cardSeverity(notify.HealthSeverity(c.ID)), "Health check failed"
			case !checkDone(c):
				p.Severity, p.Title = SevWarning, TitleHealthNotRun
			case c.Status == model.HealthWarn:
				p.Severity, p.Title = SevInfo, TitleHealthWarn
			case c.Status == model.HealthUnavailable:
				p.Severity, p.Title = SevInfo, TitleHealthNotRun
			default:
				continue
			}
			if p.Advice == nil {
				p.Advice = advice.Health(c, x)
			}
			p.keys = []string{p.Key}
			add(p)
		}
	}

	for _, h := range r.Hosts {
		if h.IsSelf {
			continue
		}
		if offline(h) {
			trig, title := model.TriggerDeviceOffline, "Device offline"
			if h.IsPrinter {
				trig, title = model.TriggerPrinterOffline, "Printer offline"
			}
			if trig == model.TriggerPrinterOffline && tr.PrinterOffline || trig == model.TriggerDeviceOffline && tr.DeviceOffline {
				add(hostProblem(trig, h, "", notify.TriggerSeverity(trig), title, "", h.OfflineSince, x))
			}
			continue
		}
		p := h.Printer
		if !h.IsPrinter || p == nil {
			continue
		}
		if tr.PrinterAlert {
			for _, a := range notify.OwnAlerts(p.Alerts) {
				d := a.Description
				if d == "" {
					d = a.Code
				}
				it := hostProblem(model.TriggerPrinterAlert, h, notify.AlertPart(a), notify.AlertSeverity(a), "Printer alert", d, "", x)
				it.Advice = advice.Notice(model.Notice{Trigger: model.TriggerPrinterAlert, IP: h.IP, Code: a.Code, Detail: a.Description,
					Message: d}, &h, x)
				add(it)
			}
		}
		if tr.SupplyLow {
			parts := notify.SupplyParts(p.Supplies)
			for i, s := range p.Supplies {
				if !lowSupply(s, thr) {
					continue
				}
				d := fmt.Sprintf("%s at %d%%", s.Description, s.Percent)
				if s.Percent < 0 {
					d = fmt.Sprintf("%s is %s", s.Description, s.State)
				}
				it := hostProblem(model.TriggerSupplyLow, h, parts[i], notify.TriggerSeverity(model.TriggerSupplyLow), "Supply low", d, "", x)
				it.Advice = advice.Notice(model.Notice{Trigger: model.TriggerSupplyLow, IP: h.IP, Detail: s.Description, Message: d}, &h, x)
				add(it)
			}
		}
	}

	if tr.IPConflict {
		for _, c := range activeConflicts(r.Conflicts, set) {
			p := Problem{Key: notify.ConflictKey(c), Kind: model.TriggerIPConflict, Severity: cardSeverity(notify.ConflictSeverity(c)),
				Title: "Network conflict", Detail: strings.Join(c.IPs, ", "), Advice: c.Advice}
			if !advice.SwapSeen(c, x) {
				p.Title = TitlePossible
			}
			if len(c.IPs) > 0 {
				if h, ok := byIP[c.IPs[0]]; ok {
					p.Device = deviceRef(h)
				} else {
					p.Device = &DeviceRef{IP: c.IPs[0]}
				}
			}
			if p.Advice == nil {
				p.Advice = advice.Conflict(c, x)
			}
			p.keys = []string{p.Key}
			add(p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i].Severity) < rank(out[j].Severity) })
	return out
}

// hostProblem is a problem about device h under trigger and key part.
func hostProblem(trigger string, h model.Host, part, sev, title, detail, since string, x advice.Ctx) Problem {
	keys := notify.HostKeys(trigger, h, part)
	p := Problem{Key: keys[0], Kind: trigger, Severity: cardSeverity(sev), Title: title, Detail: detail,
		Device: deviceRef(h), Since: since, keys: keys}
	p.Advice = advice.Notice(model.Notice{Trigger: trigger, IP: h.IP, Message: detail}, &h, x)
	return p
}

// EverythingFine reports whether ps has no warning or critical item.
func EverythingFine(ps []Problem) bool {
	for _, p := range ps {
		if p.Severity != SevInfo {
			return false
		}
	}
	return true
}

// Issue kinds of HostIssues.
const (
	IssueOffline  = "offline"
	IssueConflict = "conflict"
	IssueAlert    = "printerAlert"
	IssueSupply   = "supplyLow"
	IssueWeb      = "web"
)

// Issue is one reason a device is on the Problems tab.
type Issue struct {
	Kind   string `json:"kind"` // Issue*
	Detail string `json:"detail,omitempty"`
}

// HostIssues lists why device h is on the Problems tab, broader than the
// card and regardless of the Alerts triggers: it is offline (phones
// included), in one of conflicts (one whose id the device lists, or that
// names its address, and that involves no MAC ignored in set), a printer
// with alerts or a supply that is low, empty or at or below the threshold
// in set, or a web page that failed or answered with an HTTP error. The
// page's isProblem (web/app.js hostIssues) mirrors it;
// testdata/problems/hosts.json holds the cases both test against.
func HostIssues(h model.Host, set model.Settings, conflicts []model.Conflict) []Issue {
	var out []Issue
	if offline(h) {
		out = append(out, Issue{Kind: IssueOffline})
	}
	for _, c := range activeConflicts(conflicts, set) {
		if containsStr(h.Conflicts, c.ID) || containsStr(c.IPs, h.IP) {
			out = append(out, Issue{Kind: IssueConflict, Detail: c.Kind})
			break
		}
	}
	if p := h.Printer; h.IsPrinter && p != nil {
		for _, a := range p.Alerts {
			out = append(out, Issue{Kind: IssueAlert, Detail: a.Description})
		}
		thr := supplyThreshold(set)
		for _, s := range p.Supplies {
			if lowSupply(s, thr) {
				out = append(out, Issue{Kind: IssueSupply, Detail: s.Description})
			}
		}
	}
	for _, w := range h.Web {
		if w.Error != "" || w.Status == 0 || w.Status >= 400 {
			out = append(out, Issue{Kind: IssueWeb, Detail: w.URL})
		}
	}
	return out
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Sources of a View: the running scan, the newest scan netdiag still holds,
// a saved scan from history, or nothing yet.
const (
	SourceLive   = "live"
	SourceLatest = "latest"
	SourceSaved  = "saved"
	SourceNone   = "none"
)

// FixedItem is a problem fixed in the last FixedFor.
type FixedItem struct {
	Key        string     `json:"key"`
	Kind       string     `json:"kind"`
	Title      string     `json:"title"`
	Device     *DeviceRef `json:"device,omitempty"`
	OpenedAt   string     `json:"openedAt,omitempty"`
	FixedAt    string     `json:"fixedAt"`
	DownForSec int64      `json:"downForSec,omitempty"`
}

// View is GET /api/problems.
type View struct {
	// CheckedAt is when the scan behind it last looked (RFC 3339): the
	// newest of its end (or start, while it runs), the end of its first
	// pass, its last live cycle that checked and its health report.
	CheckedAt string `json:"checkedAt,omitempty"`
	Source    string `json:"source"`
	// Watching is true while a live-mode scan runs.
	Watching bool `json:"watching"`
	// Held is the storm guard of the running live scan (model.LiveStatus):
	// its checks are paused (HeldReason link or gateway) or its alerts
	// stopped (network_changed), so what the card lists is from before.
	Held       bool   `json:"held,omitempty"`
	HeldReason string `json:"heldReason,omitempty"`
	HeldSince  string `json:"heldSince,omitempty"`
	// EverythingFine: there is a scan to speak for that has finished its
	// first pass and is not held, its health checks ran (unless health
	// alerts are off), and nothing is a warning or worse.
	EverythingFine bool        `json:"everythingFine"`
	Open           []Problem   `json:"open"`
	Fixed          []FixedItem `json:"fixed"`
}

// Alerted lists the open items the ledger holds open, under the ledger's
// key, most urgent first: the problems netdiag alerted on, which their OK
// notice closes. The team chat first-connect burst (v0.6 §4.9, M11) posts
// these, never Current's items, so every card it posts is closed by an OK.
func (v View) Alerted() []Problem {
	var out []Problem
	for _, p := range v.Open {
		if p.alerted {
			out = append(out, p)
		}
	}
	return out
}

// Input is what NewView needs.
type Input struct {
	Source    string
	Watching  bool
	CheckedAt string
	// Held, HeldReason and HeldSince are the running live scan's storm
	// guard (model.LiveStatus).
	Held       bool
	HeldReason string
	HeldSince  string
	// Result is the scan to speak for (nil for SourceNone).
	Result   *model.ScanResult
	Settings model.Settings
	// Network is the scan's network (model.NetworkID; "" when unknown):
	// the ledger's problems of that network give the start of an open
	// problem and the fixed list.
	Network string
	// Open and Fixed are the ledger's (Ledger.Snapshot); nil without one.
	Open  []Entry
	Fixed []Fixed
	Now   time.Time
}

// hostKind reports whether trigger is about one device, whose ledger
// entry must be that device's (owns).
func hostKind(trigger string) bool {
	switch trigger {
	case model.TriggerPrinterOffline, model.TriggerDeviceOffline, model.TriggerPrinterAlert, model.TriggerSupplyLow:
		return true
	}
	return false
}

// owns reports whether the ledger's open problem e, found under one of p's
// keys, is p (the engine's belongs): several devices can answer with one MAC
// (a range extender), so a device's problem is p's when it opened at p's
// address, or when its device moved there (another MAC answers at its old
// address now).
func owns(e Entry, p Problem, byIP map[string]model.Host) bool {
	if !hostKind(p.Kind) || p.Device == nil || e.IP == "" || e.IP == p.Device.IP {
		return true
	}
	h, ok := byIP[e.IP]
	return ok && h.MAC != "" && e.MAC != "" && normMAC(h.MAC) != normMAC(e.MAC)
}

// NewView builds the card's answer. An open problem the ledger holds under
// one of its keys, for its device, takes that key and its OpenedAt (and is
// Alerted); the others keep Current's key unless an alerted item has it,
// then they take their next one. The fixed list holds the network's
// problems fixed in the last FixedFor that are not open again, newest
// first.
func NewView(in Input) View {
	v := View{Source: in.Source, Watching: in.Watching, CheckedAt: in.CheckedAt, Held: in.Held, HeldReason: in.HeldReason,
		HeldSince: in.HeldSince, Open: []Problem{}, Fixed: []FixedItem{}}
	if in.Result == nil {
		v.Source, v.Held, v.HeldReason, v.HeldSince = SourceNone, false, "", ""
		return v
	}
	opened := map[string]Entry{}
	for _, e := range in.Open {
		if e.Network == in.Network {
			opened[e.Key] = e
		}
	}
	byIP := make(map[string]model.Host, len(in.Result.Hosts))
	for _, h := range in.Result.Hosts {
		byIP[h.IP] = h
	}
	ps := Current(*in.Result, in.Settings)
	used := map[string]bool{}
	for i := range ps {
		p := &ps[i]
		for _, k := range p.keys {
			if e, ok := opened[k]; ok && !used[k] && owns(e, *p, byIP) {
				p.Key, p.alerted, used[k] = k, true, true
				if e.OpenedAt != "" {
					p.Since = e.OpenedAt
				}
				break
			}
		}
	}
	for i := range ps {
		p := &ps[i]
		if p.alerted {
			continue
		}
		if used[p.Key] {
			for _, k := range p.keys {
				if !used[k] {
					p.Key = k
					break
				}
			}
		}
		used[p.Key] = true
	}
	// open: the device addresses ("" for none) of the open items under each
	// of their keys, so a fixed entry under any key of an open problem is
	// not listed as fixed too.
	open := map[string][]string{}
	for _, p := range ps {
		ip := ""
		if p.Device != nil && hostKind(p.Kind) {
			ip = p.Device.IP
		}
		for _, k := range p.keys {
			open[k] = append(open[k], ip)
		}
		v.Open = append(v.Open, p)
	}
	reopened := func(f Fixed) bool {
		for _, ip := range open[f.Key] {
			if ip == "" || f.IP == "" || ip == f.IP {
				return true
			}
		}
		return false
	}
	hasHealth := in.Result.Health != nil && len(in.Result.Health.Checks) > 0
	// While the first pass still sweeps, the devices it has not reached yet
	// are unknown; while the storm guard holds, the list is from before.
	v.EverythingFine = EverythingFine(v.Open) && (hasHealth || !in.Settings.Alerts.Triggers.HealthFailed) &&
		in.Result.State != model.StateRunning && !in.Held

	for i := len(in.Fixed) - 1; i >= 0; i-- {
		f := in.Fixed[i]
		if f.Network != in.Network || reopened(f) || olderThan(f.FixedAt, in.Now, FixedFor) {
			continue
		}
		kind, _, _ := strings.Cut(f.Key, "|")
		it := FixedItem{Key: f.Key, Kind: kind, Title: f.Title, OpenedAt: f.OpenedAt, FixedAt: f.FixedAt}
		if f.IP != "" {
			it.Device = &DeviceRef{IP: f.IP, Name: f.Name}
		}
		from, err1 := time.Parse(time.RFC3339, f.OpenedAt)
		to, err2 := time.Parse(time.RFC3339, f.FixedAt)
		if err1 == nil && err2 == nil && !to.Before(from) {
			it.DownForSec = int64(to.Sub(from) / time.Second)
		}
		v.Fixed = append(v.Fixed, it)
	}
	sort.SliceStable(v.Fixed, func(i, j int) bool { return v.Fixed[i].FixedAt > v.Fixed[j].FixedAt })
	return v
}
