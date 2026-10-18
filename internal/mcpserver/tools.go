package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/notify"
	"netdiag/internal/scan"
	"netdiag/internal/service"
	"netdiag/internal/store"
)

// ToolNames is the complete, exact set of tools the server registers. The
// authority test pins the registered tools to this list.
var ToolNames = []string{
	"list_interfaces", "start_scan", "get_scan", "cancel_scan", "list_devices",
	"get_device", "get_events", "get_health", "run_health_check", "probe_host",
	"list_history", "compare_scans", "get_settings",
	"get_status", "list_problems", "get_runbook",
}

const (
	defaultWaitSec  = 30
	maxWaitSec      = 120
	maxDiffRows     = 200
	defaultHistory  = 20
	maxHistory      = 100
	progressMinGap  = 250 * time.Millisecond
	errNoStoreText  = "history unavailable: data store could not be opened"
	hintListHistory = "; call list_history to see saved scans"
	// healthChecksText names every check of a health report.
	healthChecksText = "gateway, DHCP, DNS, internet, Wi-Fi, web server"
)

func ptr[T any](v T) *T { return &v }

func readAnn(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, OpenWorldHint: ptr(false)}
}

func probeAnn(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: false, DestructiveHint: ptr(false), OpenWorldHint: ptr(true)}
}

type tools struct {
	svc  *service.Service
	opts Options
}

// mapErr converts service errors into actionable agent-facing errors.
func mapErr(err error) error {
	if errors.Is(err, service.ErrNoStore) {
		return errors.New(errNoStoreText)
	}
	return err
}

func (t *tools) resolve(id string) (model.ScanResult, error) {
	r, err := t.svc.ResolveScan(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return r, fmt.Errorf("scan %s not found%s", id, hintListHistory)
		}
		return r, mapErr(err)
	}
	return RedactScan(r), nil
}

// ---- inputs / outputs ----

type noInput struct{}

type listInterfacesOut struct {
	Interfaces      []netinfo.Iface `json:"interfaces"`
	SuggestedTarget string          `json:"suggestedTarget"`
}

type startScanIn struct {
	CIDR            string `json:"cidr,omitempty" jsonschema:"target network in CIDR form, e.g. 10.0.0.0/24 (max /22); defaults to the suggested target of the first interface"`
	Interface       string `json:"interface,omitempty" jsonschema:"network interface name to scan from"`
	Community       string `json:"community,omitempty" jsonschema:"SNMP v2c community string; defaults to the built-in default"`
	WaitSec         *int   `json:"waitSec,omitempty" jsonschema:"seconds to wait for the scan to finish before returning (0-120, default 30)"`
	Live            bool   `json:"live,omitempty" jsonschema:"keep watching for changes after the initial scan (HTTP endpoint only)"`
	LiveIntervalSec int    `json:"liveIntervalSec,omitempty" jsonschema:"seconds between live-mode cycles"`
}

type startScanOut struct {
	ScanSummary
	AlreadyRunning bool `json:"alreadyRunning"`
	// AlreadyWatching is true when a scan is watching the network: it was
	// left running and its summary is returned instead.
	AlreadyWatching bool `json:"alreadyWatching"`
	StillRunning    bool `json:"stillRunning"`
}

type scanIDIn struct {
	ScanID string `json:"scanId" jsonschema:"scan id from start_scan or list_history"`
}

type cancelOut struct {
	Cancelled bool   `json:"cancelled"`
	State     string `json:"state"`
	Note      string `json:"note,omitempty"`
}

// noteWatchingFromWindow is cancel_scan's answer for a live scan that was
// started from the netdiag window (D19), and noteFirstPassFromWindow for one
// in live mode that is still on its first scan (v0.6-D29): the window shows
// Cancel until it is watching.
const (
	noteWatchingFromWindow  = "That scan is watching the network from the netdiag window; ask the person to click Stop live there."
	noteFirstPassFromWindow = "That scan was started from the netdiag window to watch the network and is still on its first scan; ask the person to click Cancel there, or Stop live once it is watching."
)

type listDevicesIn struct {
	ScanID     string `json:"scanId" jsonschema:"scan id"`
	Filter     string `json:"filter,omitempty" jsonschema:"one of all, printers, web, offline, conflicts, new, mdns, problems"`
	DeviceType string `json:"deviceType,omitempty" jsonschema:"only devices of this device type"`
	Query      string `json:"query,omitempty" jsonschema:"substring match on ip, mac, hostname, vendor, SNMP name, mDNS names or mDNS service types such as _ipp._tcp"`
	Limit      int    `json:"limit,omitempty" jsonschema:"page size (default 50, max 200)"`
	Cursor     string `json:"cursor,omitempty" jsonschema:"nextCursor from the previous page"`
}

type getDeviceIn struct {
	ScanID string `json:"scanId" jsonschema:"scan id"`
	IP     string `json:"ip,omitempty" jsonschema:"device IPv4 address (ip or mac is required)"`
	MAC    string `json:"mac,omitempty" jsonschema:"device MAC address (ip or mac is required)"`
}

type getEventsIn struct {
	ScanID   string `json:"scanId" jsonschema:"scan id of a scan started by this netdiag process"`
	SinceSeq int64  `json:"sinceSeq,omitempty" jsonschema:"return events after this sequence number (default 0)"`
	Limit    int    `json:"limit,omitempty" jsonschema:"max events (default 100, max 500)"`
}

type getEventsOut struct {
	ScanID   string               `json:"scanId"`
	Events   []service.AgentEvent `json:"events"`
	NextSeq  int64                `json:"nextSeq"`
	Dropped  bool                 `json:"dropped"`
	Finished bool                 `json:"finished"`
}

type healthOut struct {
	None   bool                `json:"none,omitempty"`
	Report *model.HealthReport `json:"report,omitempty"`
}

type probeHostIn struct {
	IP        string `json:"ip" jsonschema:"IPv4 address on a local interface subnet"`
	Community string `json:"community,omitempty" jsonschema:"SNMP v2c community string"`
}

type listHistoryIn struct {
	Limit int `json:"limit,omitempty" jsonschema:"max entries, newest first (default 20, max 100)"`
}

type listHistoryOut struct {
	Total   int                  `json:"total"`
	History []model.HistoryEntry `json:"history"`
}

type compareIn struct {
	BaseID    string `json:"baseId,omitempty" jsonschema:"baseline scan id; defaults to the previous scan of the same CIDR"`
	CurrentID string `json:"currentId" jsonschema:"scan id to compare against the baseline"`
}

type compareOut struct {
	Base             model.DiffScanRef      `json:"base"`
	Current          model.DiffScanRef      `json:"current"`
	Summary          model.DiffSummary      `json:"summary"`
	Added            []DeviceRow            `json:"added"`
	AddedTruncated   bool                   `json:"addedTruncated,omitempty"`
	Removed          []DeviceRow            `json:"removed"`
	RemovedTruncated bool                   `json:"removedTruncated,omitempty"`
	Changed          []model.DiffHostChange `json:"changed"`
	ChangedTruncated bool                   `json:"changedTruncated,omitempty"`
}

// ---- registration ----

func registerTools(s *mcp.Server, svc *service.Service, opts Options) {
	t := &tools{svc: svc, opts: opts}

	mcp.AddTool(s, &mcp.Tool{Name: "list_interfaces", Title: "List network interfaces",
		Description: "List usable network interfaces with the suggested scan target for each.",
		Annotations: readAnn("List network interfaces")}, t.listInterfaces)

	mcp.AddTool(s, &mcp.Tool{Name: "start_scan", Title: "Start a network scan",
		Description: "Start a network scan and wait up to waitSec (default 30) for it to finish. Returns a compact summary; if stillRunning is true, poll get_scan. If a scan is already running its summary is returned with alreadyRunning=true. It never replaces a scan that is watching the network: that scan keeps running and its summary is returned with alreadyWatching=true. Sends real network traffic.",
		Annotations: probeAnn("Start a network scan")}, t.startScan)

	mcp.AddTool(s, &mcp.Tool{Name: "get_scan", Title: "Get scan summary",
		Description: "Compact summary of a scan (live registry first, then saved history): stats, health, conflicts, printer alerts.",
		Annotations: readAnn("Get scan summary")}, t.getScan)

	mcp.AddTool(s, &mcp.Tool{Name: "cancel_scan", Title: "Cancel a running scan",
		Description: "Cancel a running scan started in this netdiag process. Already collected results are kept. A live-mode scan started from the netdiag window is not cancelled, even during its first scan (cancelled=false with a note): ask the person to click Stop live there.",
		Annotations: &mcp.ToolAnnotations{Title: "Cancel a running scan", DestructiveHint: ptr(false),
			OpenWorldHint: ptr(false), IdempotentHint: true}}, t.cancelScan)

	mcp.AddTool(s, &mcp.Tool{Name: "list_devices", Title: "List devices in a scan",
		Description: "Paginated compact device rows for a scan, with optional filter, deviceType and query. Use filter=problems to see what needs attention; it matches the Problems tab in the netdiag UI: devices that are offline or in a conflict (other than one that involves a MAC ignored in Settings), printers with alerts or a supply that is low, empty or at or below the supply threshold in Settings, and web pages that failed or returned an HTTP error.",
		Annotations: readAnn("List devices in a scan")}, t.listDevices)

	mcp.AddTool(s, &mcp.Tool{Name: "get_device", Title: "Get device details",
		Description: "Full details of one device (ports, web services, SNMP, printer supplies/alerts) plus conflicts touching it. Identify by ip or mac.",
		Annotations: readAnn("Get device details")}, t.getDevice)

	mcp.AddTool(s, &mcp.Tool{Name: "get_events", Title: "Get scan change events",
		Description: "Poll the change feed of a scan (new/offline/online hosts, printer changes, conflicts, health, alert notices, and fixed entries that close a notice with the same key). Pass the returned nextSeq as sinceSeq next time.",
		Annotations: readAnn("Get scan change events")}, t.getEvents)

	mcp.AddTool(s, &mcp.Tool{Name: "get_health", Title: "Get latest health report",
		Description: "Latest network health report (" + healthChecksText + " checks) from a scan or history; none=true if there is none yet.",
		Annotations: readAnn("Get latest health report")}, t.getHealth)

	mcp.AddTool(s, &mcp.Tool{Name: "run_health_check", Title: "Run a health check now",
		Description: "Run the network health checks (" + healthChecksText + ") now (takes up to about 10 seconds) on the network adapter of the running or most recent scan of this netdiag, or the default network adapter when there is none; the router is checked only if it is on that adapter's network. Sends real network traffic.",
		Annotations: probeAnn("Run a health check now")}, t.runHealth)

	mcp.AddTool(s, &mcp.Tool{Name: "probe_host", Title: "Probe a single host",
		Description: "Probe one host on a local subnet (ports, web, SNMP, IPP, vendor, device type) without starting a scan or saving anything. Sends real network traffic.",
		Annotations: probeAnn("Probe a single host")}, t.probeHost)

	mcp.AddTool(s, &mcp.Tool{Name: "list_history", Title: "List saved scans",
		Description: "List saved scans, newest first.",
		Annotations: readAnn("List saved scans")}, t.listHistory)

	mcp.AddTool(s, &mcp.Tool{Name: "compare_scans", Title: "Compare two scans",
		Description: "Diff two scans: devices added, removed and changed (rows capped at 200 each). baseId defaults to the previous scan of the same CIDR.",
		Annotations: readAnn("Compare two scans")}, t.compareScans)

	mcp.AddTool(s, &mcp.Tool{Name: "get_settings", Title: "Get settings",
		Description: "Read netdiag settings (read-only; the SNMP community and any password in the web server URL are redacted). Agents cannot change settings.",
		Annotations: readAnn("Get settings")}, t.getSettings)

	mcp.AddTool(s, &mcp.Tool{Name: "get_status", Title: "Get netdiag status",
		Description: "Start here. Whether the netdiag window is open and serves this connection (window.thisProcess), the scan watching the network (watching, with its scanId; null when nothing watches), the scan running now, and a count of the problems with when they were last checked. The note says in a sentence what this connection can and cannot see.",
		Annotations: readAnn("Get netdiag status")}, t.getStatus)

	mcp.AddTool(s, &mcp.Tool{Name: "list_problems", Title: "List problems right now",
		Description: "What is wrong on the local network right now, most urgent first, as the Problems right now card in the netdiag window shows it: failing health checks, offline printers, printer alerts, low supplies and conflicts (each follows its Alerts setting), with plain-words advice (advice.meaning, advice.steps and the advice.actions buttons), when it began, and which tool re-checks it. everythingFine is true only when a finished check that is not paused, with health results (unless health alerts are off), found nothing that is a warning or worse; " +
			"it is also false when nothing was checked yet (source none), while a scan is still on its first pass, while the checks are paused (held), and when the health checks did not run, so false with no warning means \"not checked\", not a problem. " +
			"Also lists the problems fixed in the last 24 hours.",
		Annotations: readAnn("List problems right now")}, t.listProblems)

	mcp.AddTool(s, &mcp.Tool{Name: "get_runbook", Title: "Get a runbook",
		Description: "netdiag's runbooks, the same as its prompts. Without a name it lists them; with a name (and its arguments) it returns the steps to follow. When the person describes a problem in their own words, pick the best match and follow it.",
		Annotations: readAnn("Get a runbook")}, t.getRunbook)
}

// ---- handlers ----

func (t *tools) listInterfaces(_ context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, listInterfacesOut, error) {
	list, err := t.svc.Interfaces()
	if err != nil {
		return nil, listInterfacesOut{}, fmt.Errorf("list interfaces: %w", err)
	}
	out := listInterfacesOut{Interfaces: list}
	if len(list) > 0 {
		out.SuggestedTarget = list[0].Suggested
	}
	return nil, out, nil
}

func (t *tools) defaultTarget(iface string) (string, error) {
	list, err := t.svc.Interfaces()
	if err != nil {
		return "", fmt.Errorf("list interfaces: %w", err)
	}
	for _, f := range list {
		if (iface == "" || f.Name == iface) && f.Suggested != "" {
			return f.Suggested, nil
		}
	}
	return "", errors.New("no usable network interface found; pass cidr explicitly")
}

func (t *tools) startScan(ctx context.Context, req *mcp.CallToolRequest, in startScanIn) (*mcp.CallToolResult, startScanOut, error) {
	if in.Live && t.opts.Transport == "stdio" {
		return nil, startScanOut{}, errors.New("live mode is only available over the HTTP endpoint of a running netdiag UI")
	}
	wait := defaultWaitSec
	if in.WaitSec != nil {
		wait = max(0, min(*in.WaitSec, maxWaitSec))
	}
	cidr := strings.TrimSpace(in.CIDR)
	if cidr == "" {
		var err error
		if cidr, err = t.defaultTarget(in.Interface); err != nil {
			return nil, startScanOut{}, err
		}
	}
	sc, err := t.svc.Start(model.ScanRequest{
		CIDR: cidr, Interface: in.Interface, Community: in.Community,
		Live: in.Live, LiveIntervalSec: in.LiveIntervalSec,
	}.WithOrigin(model.OriginMCP))
	if errors.Is(err, scan.ErrWatching) {
		// Never replaces a scan that is watching the network (F9).
		if run, ok := t.svc.Registry().Running(); ok {
			snap := RedactScan(run.Snapshot())
			return nil, startScanOut{ScanSummary: Summarize(snap, supplyThreshold(t.svc)), AlreadyRunning: true,
				AlreadyWatching: true, StillRunning: snap.State == model.StateRunning}, nil
		}
		return nil, startScanOut{}, errors.New("netdiag is watching this network; call get_scan or try again shortly")
	}
	if errors.Is(err, scan.ErrBusy) {
		if run, ok := t.svc.Registry().Running(); ok {
			snap := RedactScan(run.Snapshot())
			return nil, startScanOut{ScanSummary: Summarize(snap, supplyThreshold(t.svc)), AlreadyRunning: true,
				StillRunning: snap.State == model.StateRunning}, nil
		}
		return nil, startScanOut{}, errors.New("a scan is already running; call get_scan or try again shortly")
	}
	if err != nil {
		return nil, startScanOut{}, err
	}
	t.wait(ctx, req, sc, time.Duration(wait)*time.Second, in.Live)
	snap := RedactScan(sc.Snapshot())
	return nil, startScanOut{ScanSummary: Summarize(snap, supplyThreshold(t.svc)), StillRunning: snap.State == model.StateRunning}, nil
}

// wait blocks until the scan finishes (or, in live mode, completes its
// initial pass), d elapses, or ctx is cancelled. It forwards progress
// notifications when the client supplied a progress token.
func (t *tools) wait(ctx context.Context, req *mcp.CallToolRequest, sc *scan.Scan, d time.Duration, live bool) {
	if d <= 0 {
		return
	}
	ch, cancel := sc.Subscribe()
	defer cancel()
	var token any
	if req != nil && req.Params != nil {
		token = req.Params.GetProgressToken()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	var last time.Time
	for {
		if live && sc.State() == model.StateLive {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-sc.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				ch = nil
				continue
			}
			if ev.Type != model.EventProgress || token == nil {
				continue
			}
			p, isP := ev.Data.(model.Progress)
			if !isP || time.Since(last) < progressMinGap {
				continue
			}
			last = time.Now()
			_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token,
				Message:       fmt.Sprintf("%s %d/%d", p.Stage, p.Done, p.Total),
				Progress:      float64(p.Done),
				Total:         float64(p.Total),
			})
		}
	}
}

func (t *tools) getScan(_ context.Context, _ *mcp.CallToolRequest, in scanIDIn) (*mcp.CallToolResult, ScanSummary, error) {
	r, err := t.resolve(in.ScanID)
	if err != nil {
		return nil, ScanSummary{}, err
	}
	return nil, Summarize(r, supplyThreshold(t.svc)), nil
}

func (t *tools) cancelScan(_ context.Context, _ *mcp.CallToolRequest, in scanIDIn) (*mcp.CallToolResult, cancelOut, error) {
	sc, ok := t.svc.Get(in.ScanID)
	if !ok {
		return nil, cancelOut{}, fmt.Errorf("scan %s is not a live scan in this process; nothing to cancel%s", in.ScanID, hintListHistory)
	}
	state := sc.State()
	cancelled := false
	if q := sc.Request(); (state == model.StateRunning || state == model.StateLive) && q.Live && q.Origin() != model.OriginMCP {
		// Watching started from the window stays on (D19), from its first
		// scan on (v0.6-D29).
		note := noteWatchingFromWindow
		if state == model.StateRunning {
			note = noteFirstPassFromWindow
		}
		return nil, cancelOut{State: state, Note: note}, nil
	}
	if state == model.StateRunning || state == model.StateLive {
		cancelled = t.svc.Cancel(in.ScanID)
		select {
		case <-sc.Done():
		case <-time.After(2 * time.Second):
		}
		state = sc.State()
	}
	return nil, cancelOut{Cancelled: cancelled, State: state}, nil
}

func (t *tools) listDevices(_ context.Context, _ *mcp.CallToolRequest, in listDevicesIn) (*mcp.CallToolResult, DevicePage, error) {
	r, err := t.resolve(in.ScanID)
	if err != nil {
		return nil, DevicePage{}, err
	}
	page, err := ListDevices(r, ListOptions{
		Filter: in.Filter, DeviceType: in.DeviceType, Query: in.Query,
		Limit: in.Limit, Cursor: in.Cursor, SupplyPct: supplyThreshold(t.svc), IgnoredMACs: ignoredMACs(t.svc),
	})
	return nil, page, err
}

// ignoredMACs are the conflict MACs the person ignored in Settings (none
// when settings cannot be read).
func ignoredMACs(svc *service.Service) []string {
	if svc != nil {
		if set, err := svc.LoadSettings(); err == nil {
			return set.IgnoredConflictMACs
		}
	}
	return nil
}

// supplyThreshold is the low-supply percentage from Settings
// (alerts.supplyThresholdPct), or the default when settings cannot be read
// or hold a value outside 1-100 (the UI falls back the same way).
func supplyThreshold(svc *service.Service) int {
	if svc != nil {
		if set, err := svc.LoadSettings(); err == nil {
			if t := set.Alerts.SupplyThresholdPct; t >= 1 && t <= 100 {
				return t
			}
		}
	}
	return notify.DefaultSupplyThresholdPct
}

func (t *tools) getDevice(_ context.Context, _ *mcp.CallToolRequest, in getDeviceIn) (*mcp.CallToolResult, DeviceDetail, error) {
	r, err := t.resolve(in.ScanID)
	if err != nil {
		return nil, DeviceDetail{}, err
	}
	d, err := Detail(r, in.IP, in.MAC)
	if err != nil {
		return nil, d, fmt.Errorf("%w in scan %s; call list_devices to see available devices", err, in.ScanID)
	}
	return nil, d, nil
}

func (t *tools) getEvents(_ context.Context, _ *mcp.CallToolRequest, in getEventsIn) (*mcp.CallToolResult, getEventsOut, error) {
	l, err := t.svc.Events(in.ScanID)
	if err != nil {
		return nil, getEventsOut{}, fmt.Errorf("scan %s has no event log (events exist only for scans started in this netdiag process)%s", in.ScanID, hintListHistory)
	}
	ev, next, dropped := l.Since(in.SinceSeq, in.Limit)
	return nil, getEventsOut{ScanID: in.ScanID, Events: ev, NextSeq: next, Dropped: dropped, Finished: l.Finished()}, nil
}

func (t *tools) getHealth(_ context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, healthOut, error) {
	h, ok := t.svc.LatestHealth()
	if !ok {
		return nil, healthOut{None: true}, nil
	}
	return nil, healthOut{Report: h}, nil
}

func (t *tools) runHealth(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, healthOut, error) {
	h := t.svc.RunHealth(ctx, "")
	return nil, healthOut{Report: &h}, nil
}

func (t *tools) probeHost(ctx context.Context, _ *mcp.CallToolRequest, in probeHostIn) (*mcp.CallToolResult, DeviceDetail, error) {
	h, err := t.svc.ProbeHost(ctx, strings.TrimSpace(in.IP), in.Community)
	if err != nil {
		return nil, DeviceDetail{}, err
	}
	return nil, DeviceDetail{Host: h, Conflicts: []model.Conflict{}}, nil
}

func (t *tools) listHistory(_ context.Context, _ *mcp.CallToolRequest, in listHistoryIn) (*mcp.CallToolResult, listHistoryOut, error) {
	list, err := t.svc.ListHistory()
	if err != nil {
		return nil, listHistoryOut{}, mapErr(err)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultHistory
	}
	limit = min(limit, maxHistory)
	out := listHistoryOut{Total: len(list), History: list}
	if len(list) > limit {
		out.History = list[:limit]
	}
	return nil, out, nil
}

func (t *tools) compareScans(_ context.Context, _ *mcp.CallToolRequest, in compareIn) (*mcp.CallToolResult, compareOut, error) {
	d, err := t.svc.Compare(in.BaseID, in.CurrentID)
	if err != nil {
		var nf *service.NotFoundError
		if errors.As(err, &nf) {
			return nil, compareOut{}, fmt.Errorf("%s%s", nf.Msg, hintListHistory)
		}
		return nil, compareOut{}, mapErr(err)
	}
	out := compareOut{Base: d.Base, Current: d.Current, Summary: d.Summary,
		Added: []DeviceRow{}, Removed: []DeviceRow{}, Changed: []model.DiffHostChange{}}
	for i, h := range d.Added {
		if i >= maxDiffRows {
			out.AddedTruncated = true
			break
		}
		out.Added = append(out.Added, Row(h))
	}
	for i, h := range d.Removed {
		if i >= maxDiffRows {
			out.RemovedTruncated = true
			break
		}
		out.Removed = append(out.Removed, Row(h))
	}
	for i, c := range d.Changed {
		if i >= maxDiffRows {
			out.ChangedTruncated = true
			break
		}
		out.Changed = append(out.Changed, c)
	}
	return nil, out, nil
}

func (t *tools) getSettings(_ context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, model.Settings, error) {
	set, err := t.svc.LoadSettings()
	if err != nil {
		return nil, model.Settings{}, mapErr(err)
	}
	return nil, RedactSettings(set), nil
}
