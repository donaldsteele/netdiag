package mcpserver

import (
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"netdiag/internal/model"
	"netdiag/internal/notify"
	"netdiag/internal/report"
)

// Caps that keep tool output small regardless of network size.
const (
	maxSummaryConflicts = 20
	maxSummaryPrinters  = 20
	maxSummaryWarnings  = 10
	maxSummaryFailing   = 10
	maxPrinterAlerts    = 3
	maxPrinterSupplies  = 4
	maxDetailChars      = 100

	DefaultDeviceLimit = 50
	MaxDeviceLimit     = 200
)

// SummaryRequest is the request echo in a ScanSummary.
type SummaryRequest struct {
	CIDR      string `json:"cidr"`
	Interface string `json:"interface"`
	Live      bool   `json:"live"`
}

// SummaryStats are the headline counters.
type SummaryStats struct {
	Alive     int `json:"alive"`
	Printers  int `json:"printers"`
	Web       int `json:"web"`
	MDNS      int `json:"mdns"`
	Offline   int `json:"offline"`
	Conflicts int `json:"conflicts"`
	New       int `json:"new"`
}

// FailingCheck is a health check that is failing or warning.
type FailingCheck struct {
	Check  string `json:"check"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// SummaryHealth condenses a HealthReport.
type SummaryHealth struct {
	Summary string         `json:"summary"`
	Failing []FailingCheck `json:"failing"`
}

// SummaryConflict is a compact conflict.
type SummaryConflict struct {
	Kind     string   `json:"kind"`
	Severity string   `json:"severity"`
	IPs      []string `json:"ips"`
	MACs     []string `json:"macs"`
	Name     string   `json:"name,omitempty"`
	Detail   string   `json:"detail"`
}

// PrinterAlert is a printer with a problem.
type PrinterAlert struct {
	IP          string   `json:"ip"`
	Model       string   `json:"model"`
	State       string   `json:"state"`
	Alerts      []string `json:"alerts"`
	LowSupplies []string `json:"lowSupplies"`
}

// ScanSummary is the compact scan overview returned by tools.
type ScanSummary struct {
	ScanID             string            `json:"scanId"`
	State              string            `json:"state"`
	Request            SummaryRequest    `json:"request"`
	StartedAt          string            `json:"startedAt"`
	FinishedAt         string            `json:"finishedAt"`
	Stats              SummaryStats      `json:"stats"`
	Health             SummaryHealth     `json:"health"`
	Conflicts          []SummaryConflict `json:"conflicts"`
	ConflictsTruncated bool              `json:"conflictsTruncated,omitempty"`
	PrinterAlerts      []PrinterAlert    `json:"printerAlerts"`
	PrinterAlertsTrunc bool              `json:"printerAlertsTruncated,omitempty"`
	Warnings           []string          `json:"warnings"`
	NextStep           string            `json:"nextStep"`
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

func capStrings(in []string, n int) []string {
	out := make([]string, 0, min(len(in), n))
	for i, s := range in {
		if i >= n {
			break
		}
		out = append(out, clip(s, maxDetailChars))
	}
	return out
}

// supplyLabels lists the supplies of p that are low (see lowSupply), as
// "Black toner 8%", "Black toner empty" or, with no known level,
// "Black toner low".
func (fc filterCtx) supplyLabels(p *model.PrinterInfo) []string {
	out := []string{}
	for _, s := range p.Supplies {
		if !fc.lowSupply(s) {
			continue
		}
		switch {
		case s.State == "empty" && s.Percent <= 0:
			out = append(out, s.Description+" empty")
		case s.Percent < 0:
			out = append(out, s.Description+" "+s.State)
		default:
			out = append(out, fmt.Sprintf("%s %d%%", s.Description, s.Percent))
		}
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Summarize builds a bounded ScanSummary. supplyPct is the low-supply
// threshold (alerts.supplyThresholdPct; <= 0 means the default): the
// printer alerts are the printers list_devices filter=problems lists for
// their alerts or supplies, like the Problems tab.
func Summarize(r model.ScanResult, supplyPct int) ScanSummary {
	fc := newFilterCtx(r, supplyPct)
	s := ScanSummary{
		ScanID: r.ID, State: r.State,
		Request:    SummaryRequest{CIDR: r.Request.CIDR, Interface: r.Request.Interface, Live: r.Request.Live},
		StartedAt:  r.StartedAt,
		FinishedAt: r.FinishedAt,
		Stats: SummaryStats{
			Alive: r.Stats.Alive, Printers: r.Stats.Printers, Web: r.Stats.Web, MDNS: r.Stats.MDNS,
			Offline: r.Stats.Offline, Conflicts: r.Stats.Conflicts,
		},
		Health:        SummaryHealth{Summary: "no health data", Failing: []FailingCheck{}},
		Conflicts:     []SummaryConflict{},
		PrinterAlerts: []PrinterAlert{},
		Warnings:      capStrings(r.Warnings, maxSummaryWarnings),
	}
	for _, h := range r.Hosts {
		if h.IsNew {
			s.Stats.New++
		}
	}
	if r.Health != nil {
		fail, warn := 0, 0
		for _, c := range r.Health.Checks {
			if c.Status != model.HealthFail && c.Status != model.HealthWarn {
				continue
			}
			if c.Status == model.HealthFail {
				fail++
			} else {
				warn++
			}
			if len(s.Health.Failing) < maxSummaryFailing {
				s.Health.Failing = append(s.Health.Failing, FailingCheck{Check: c.ID, Status: c.Status, Detail: clip(c.Detail, maxDetailChars)})
			}
		}
		if fail == 0 && warn == 0 {
			s.Health.Summary = "all ok"
		} else {
			s.Health.Summary = fmt.Sprintf("%d fail, %d warn", fail, warn)
		}
	}
	for _, c := range r.Conflicts {
		if len(s.Conflicts) >= maxSummaryConflicts {
			s.ConflictsTruncated = true
			break
		}
		ips, macs := c.IPs, c.MACs
		if len(ips) > 4 {
			ips = ips[:4]
		}
		if len(macs) > 4 {
			macs = macs[:4]
		}
		s.Conflicts = append(s.Conflicts, SummaryConflict{
			Kind: c.Kind, Severity: c.Severity, IPs: nonNil(ips), MACs: nonNil(macs),
			Name: c.Name, Detail: clip(c.Detail, maxDetailChars),
		})
	}
	for _, h := range report.SortHosts(r.Hosts) {
		if !fc.printerProblem(h) {
			continue
		}
		if len(s.PrinterAlerts) >= maxSummaryPrinters {
			s.PrinterAlertsTrunc = true
			break
		}
		al := []string{}
		for _, a := range h.Printer.Alerts {
			al = append(al, a.Description)
		}
		s.PrinterAlerts = append(s.PrinterAlerts, PrinterAlert{
			IP: h.IP, Model: clip(h.Printer.Model, 40), State: h.Printer.State,
			Alerts:      capStrings(al, maxPrinterAlerts),
			LowSupplies: capStrings(fc.supplyLabels(h.Printer), maxPrinterSupplies),
		})
	}
	switch r.State {
	case model.StateRunning:
		s.NextStep = "Scan still running; call get_scan again in ~10s or get_events to follow progress"
	case model.StateLive:
		s.NextStep = "Live mode is watching for changes; call get_events to follow, or list_devices with filter=problems"
	case model.StateError:
		s.NextStep = "Scan failed; check warnings, then start a new scan"
	default:
		s.NextStep = "Use list_devices with filter=problems to see devices that need attention"
	}
	return s
}

// DeviceRow is one compact device line.
type DeviceRow struct {
	IP           string   `json:"ip"`
	MAC          string   `json:"mac"`
	Vendor       string   `json:"vendor"`
	Hostname     string   `json:"hostname"`
	DeviceType   string   `json:"deviceType"`
	Online       bool     `json:"online"`
	IsNew        bool     `json:"isNew"`
	IsPrinter    bool     `json:"isPrinter"`
	PrinterState string   `json:"printerState"`
	OpenPorts    []int    `json:"openPorts"`
	WebURLs      []string `json:"webUrls"`
	Conflict     bool     `json:"conflict"`
	LastSeen     string   `json:"lastSeen"`
}

// Row converts a host to a DeviceRow.
func Row(h model.Host) DeviceRow {
	row := DeviceRow{
		IP: h.IP, MAC: h.MAC, Vendor: h.Vendor, Hostname: h.Hostname, DeviceType: h.DeviceType,
		Online: h.Online, IsNew: h.IsNew, IsPrinter: h.IsPrinter,
		OpenPorts: []int{}, WebURLs: []string{}, Conflict: len(h.Conflicts) > 0, LastSeen: h.LastSeen,
	}
	if h.Printer != nil {
		row.PrinterState = h.Printer.State
	}
	row.OpenPorts = append(row.OpenPorts, h.OpenPorts...)
	for _, w := range h.Web {
		if w.URL != "" {
			row.WebURLs = append(row.WebURLs, w.URL)
		}
	}
	return row
}

// Filters accepted by ListDevices.
var deviceFilters = []string{"all", "printers", "web", "offline", "conflicts", "new", "mdns", "problems"}

// filterCtx holds the scan-wide facts some filters need.
type filterCtx struct {
	supplyPct   int             // low-supply threshold for "problems"
	conflictIPs map[string]bool // IPs named by a conflict of the scan
}

func newFilterCtx(r model.ScanResult, supplyPct int) filterCtx {
	if supplyPct <= 0 {
		supplyPct = notify.DefaultSupplyThresholdPct
	}
	fc := filterCtx{supplyPct: supplyPct, conflictIPs: map[string]bool{}}
	for _, c := range r.Conflicts {
		for _, ip := range c.IPs {
			fc.conflictIPs[ip] = true
		}
	}
	return fc
}

// hostOffline mirrors the UI's isOffline (and Stats.Offline): a found device
// that live mode marked offline. This computer is never offline.
func hostOffline(h model.Host) bool {
	return h.Alive && !h.Online && !h.IsSelf
}

func (fc filterCtx) hasConflict(h model.Host) bool {
	return len(h.Conflicts) > 0 || fc.conflictIPs[h.IP]
}

// isProblem mirrors the UI's Problems tab (web/app.js isProblem): offline, in
// a conflict, a printer with alerts or a supply that is low, empty or at or
// below the supply threshold, or a web page that failed or answered with an
// HTTP error.
func (fc filterCtx) isProblem(h model.Host) bool {
	if hostOffline(h) || fc.hasConflict(h) || fc.printerProblem(h) {
		return true
	}
	for _, w := range h.Web {
		if w.Error != "" || w.Status == 0 || w.Status >= 400 {
			return true
		}
	}
	return false
}

// printerProblem is the printer part of isProblem: a printer with alerts
// or a low supply. A printer's state alone (such as "stopped") does not
// count, as in the UI.
func (fc filterCtx) printerProblem(h model.Host) bool {
	p := h.Printer
	if !h.IsPrinter || p == nil {
		return false
	}
	if len(p.Alerts) > 0 {
		return true
	}
	for _, s := range p.Supplies {
		if fc.lowSupply(s) {
			return true
		}
	}
	return false
}

// lowSupply reports a supply that is low, empty, or at or below the supply
// threshold.
func (fc filterCtx) lowSupply(s model.Supply) bool {
	return s.State == "low" || s.State == "empty" || (s.Percent >= 0 && s.Percent <= fc.supplyPct)
}

func (fc filterCtx) match(filter string, h model.Host) bool {
	switch filter {
	case "", "all":
		return true
	case "printers":
		return h.IsPrinter
	case "web":
		return len(h.Web) > 0
	case "offline":
		return hostOffline(h)
	case "conflicts":
		return fc.hasConflict(h)
	case "new":
		return h.IsNew
	case "mdns":
		return h.MDNS != nil
	case "problems":
		return fc.isProblem(h)
	}
	return false
}

func matchQuery(q string, h model.Host) bool {
	if q == "" {
		return true
	}
	fields := []string{h.IP, h.MAC, h.Hostname, h.Vendor}
	if h.MDNS != nil {
		fields = append(fields, h.MDNS.Instance, h.MDNS.Hostname)
	}
	if h.Snmp != nil {
		fields = append(fields, h.Snmp.SysName)
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// DevicePage is one page of devices.
type DevicePage struct {
	ScanID     string      `json:"scanId"`
	Total      int         `json:"total"`
	Items      []DeviceRow `json:"items"`
	NextCursor string      `json:"nextCursor,omitempty"`
}

func decodeCursor(c string) (int, error) {
	if c == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || !strings.HasPrefix(string(b), "o:") {
		return 0, errors.New("invalid cursor")
	}
	n, err := strconv.Atoi(string(b[2:]))
	if err != nil || n < 0 {
		return 0, errors.New("invalid cursor")
	}
	return n, nil
}

func encodeCursor(off int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(off)))
}

// ListOptions selects and pages the devices returned by ListDevices.
type ListOptions struct {
	Filter     string // one of deviceFilters; "" means all
	DeviceType string // exact device type, "" for any
	Query      string // case-insensitive substring of ip, mac or a name
	Limit      int    // page size; <= 0 means DefaultDeviceLimit
	Cursor     string // nextCursor of the previous page
	// SupplyPct is the low-supply threshold used by filter=problems (the
	// alerts.supplyThresholdPct setting); <= 0 means the default.
	SupplyPct int
}

// ListDevices filters and paginates the hosts of r in stable IP order.
func ListDevices(r model.ScanResult, o ListOptions) (DevicePage, error) {
	filter := strings.ToLower(strings.TrimSpace(o.Filter))
	if filter != "" && !slices.Contains(deviceFilters, filter) {
		return DevicePage{}, fmt.Errorf("unknown filter %q; valid: %s", filter, strings.Join(deviceFilters, "|"))
	}
	off, err := decodeCursor(o.Cursor)
	if err != nil {
		return DevicePage{}, err
	}
	limit := o.Limit
	if limit <= 0 {
		limit = DefaultDeviceLimit
	}
	limit = min(limit, MaxDeviceLimit)
	q := strings.ToLower(strings.TrimSpace(o.Query))
	fc := newFilterCtx(r, o.SupplyPct)
	var matched []model.Host
	for _, h := range report.SortHosts(r.Hosts) {
		if !fc.match(filter, h) {
			continue
		}
		if o.DeviceType != "" && h.DeviceType != o.DeviceType {
			continue
		}
		if !matchQuery(q, h) {
			continue
		}
		matched = append(matched, h)
	}
	page := DevicePage{ScanID: r.ID, Total: len(matched), Items: []DeviceRow{}}
	if off > len(matched) {
		off = len(matched)
	}
	end := min(off+limit, len(matched))
	for _, h := range matched[off:end] {
		page.Items = append(page.Items, Row(h))
	}
	if end < len(matched) {
		page.NextCursor = encodeCursor(end)
	}
	return page, nil
}

// DeviceDetail is a full host plus the conflicts touching it.
type DeviceDetail struct {
	Host      model.Host       `json:"host"`
	Conflicts []model.Conflict `json:"conflicts"`
}

// Detail finds a host by ip, or by mac (case-insensitive) when ip is empty.
func Detail(r model.ScanResult, ip, mac string) (DeviceDetail, error) {
	ip, mac = strings.TrimSpace(ip), strings.TrimSpace(mac)
	if ip == "" && mac == "" {
		return DeviceDetail{}, errors.New("ip or mac is required")
	}
	for _, h := range r.Hosts {
		if (ip != "" && h.IP != ip) || (ip == "" && !strings.EqualFold(h.MAC, mac)) {
			continue
		}
		d := DeviceDetail{Host: h, Conflicts: []model.Conflict{}}
		for _, c := range r.Conflicts {
			touches := false
			for _, cip := range c.IPs {
				if cip == h.IP {
					touches = true
				}
			}
			for _, id := range h.Conflicts {
				if id == c.ID {
					touches = true
				}
			}
			if touches {
				d.Conflicts = append(d.Conflicts, c)
			}
		}
		return d, nil
	}
	return DeviceDetail{}, errors.New("device not found")
}
