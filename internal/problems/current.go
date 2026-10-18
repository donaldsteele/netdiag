package problems

// This file is what is wrong right now (v0.6 #1, §4.11): the Problems right
// now card (GET /api/problems), MCP list_problems and the team chat
// first-connect burst use Current; the Problems tab and MCP
// filter=problems use the broader per-device HostIssues.

import (
	"fmt"
	"runtime"
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
	// (notify.HostKeys): Key first.
	keys []string
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
//     turns at one address).
//
// The severities are the one severity map (notify, v0.6-D3). The keys are
// the alert keys, without the ledger's: View swaps in the key a problem is
// open under and its start.
func Current(r model.ScanResult, set model.Settings) []Problem {
	tr := set.Alerts.Triggers
	thr := supplyThreshold(set)
	byIP := make(map[string]model.Host, len(r.Hosts))
	for _, h := range r.Hosts {
		byIP[h.IP] = h
	}
	x := advice.Ctx{GOOS: runtime.GOOS, Gateway: r.Gateway, Hosts: byIP}
	var out []Problem

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
			out = append(out, p)
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
				out = append(out, hostProblem(trig, h, "", notify.TriggerSeverity(trig), title, "", h.OfflineSince, x))
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
				out = append(out, it)
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
				out = append(out, it)
			}
		}
	}

	if tr.IPConflict {
		for _, c := range r.Conflicts {
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
			out = append(out, p)
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
// names its address), a printer with alerts or a supply that is low, empty
// or at or below the threshold in set, or a web page that failed or
// answered with an HTTP error. The page's isProblem (web/app.js
// hostIssues) mirrors it; testdata/problems/hosts.json holds the cases both
// test against.
func HostIssues(h model.Host, set model.Settings, conflicts []model.Conflict) []Issue {
	var out []Issue
	if offline(h) {
		out = append(out, Issue{Kind: IssueOffline})
	}
	for _, c := range conflicts {
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
	// CheckedAt is when the scan behind it last looked (RFC 3339).
	CheckedAt string `json:"checkedAt,omitempty"`
	Source    string `json:"source"`
	// Watching is true while a live-mode scan runs.
	Watching bool `json:"watching"`
	// EverythingFine: there is a scan to speak for, its health checks ran
	// (unless health alerts are off), and nothing is a warning or worse.
	EverythingFine bool        `json:"everythingFine"`
	Open           []Problem   `json:"open"`
	Fixed          []FixedItem `json:"fixed"`
}

// Input is what NewView needs.
type Input struct {
	Source    string
	Watching  bool
	CheckedAt string
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

// NewView builds the card's answer. An open problem the ledger has under
// one of its keys takes that key and its OpenedAt; the fixed list holds
// the network's problems fixed in the last FixedFor that are not open
// again, newest first.
func NewView(in Input) View {
	v := View{Source: in.Source, Watching: in.Watching, CheckedAt: in.CheckedAt, Open: []Problem{}, Fixed: []FixedItem{}}
	if in.Result == nil {
		v.Source = SourceNone
		return v
	}
	opened := map[string]Entry{}
	for _, e := range in.Open {
		if e.Network == in.Network {
			opened[e.Key] = e
		}
	}
	open := map[string]bool{}
	for _, p := range Current(*in.Result, in.Settings) {
		for _, k := range p.keys {
			if e, ok := opened[k]; ok {
				p.Key = k
				if e.OpenedAt != "" {
					p.Since = e.OpenedAt
				}
				break
			}
		}
		open[p.Key] = true
		v.Open = append(v.Open, p)
	}
	hasHealth := in.Result.Health != nil && len(in.Result.Health.Checks) > 0
	v.EverythingFine = EverythingFine(v.Open) && (hasHealth || !in.Settings.Alerts.Triggers.HealthFailed)

	for i := len(in.Fixed) - 1; i >= 0; i-- {
		f := in.Fixed[i]
		if f.Network != in.Network || open[f.Key] || olderThan(f.FixedAt, in.Now, FixedFor) {
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
