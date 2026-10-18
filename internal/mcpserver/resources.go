package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/service"
	"netdiag/internal/store"
)

const (
	resMIME        = "application/json"
	resDeviceLimit = 200
	uriPrefix      = "netdiag://"
)

// Resource URIs and templates. resources/list shows the seven static
// resources; individual scans and runbooks are exposed through the templates
// (the SDK only enumerates statically registered resources; scan ids are
// discoverable via the list_history tool and the netdiag://history resource,
// runbook names via netdiag://runbooks).
const (
	URIInterfaces = "netdiag://interfaces"
	URIHealth     = "netdiag://health"
	URIHistory    = "netdiag://history"
	URISettings   = "netdiag://settings"
	URIStatus     = "netdiag://status"
	URIProblems   = "netdiag://problems"
	URIRunbooks   = "netdiag://runbooks"

	TmplScan    = "netdiag://scans/{scanId}"
	TmplDevices = "netdiag://scans/{scanId}/devices"
	TmplDevice  = "netdiag://scans/{scanId}/devices/{ip}"
	TmplRunbook = "netdiag://runbooks/{name}"
)

func registerResources(s *mcp.Server, svc *service.Service, opts Options) {
	t := &tools{svc: svc, opts: opts}
	static := func(uri, name, title, desc string, fn func(ctx context.Context) (any, error)) {
		s.AddResource(&mcp.Resource{URI: uri, Name: name, Title: title, Description: desc, MIMEType: resMIME},
			func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				v, err := fn(ctx)
				if err != nil {
					return nil, err
				}
				return jsonResource(req.Params.URI, v)
			})
	}
	static(URIInterfaces, "interfaces", "Network interfaces", "Usable network interfaces with suggested scan targets (same as list_interfaces).",
		func(ctx context.Context) (any, error) {
			_, out, err := t.listInterfaces(ctx, nil, noInput{})
			return out, err
		})
	static(URIHealth, "health", "Latest health report", "Latest network health report (same as get_health).",
		func(ctx context.Context) (any, error) {
			_, out, err := t.getHealth(ctx, nil, noInput{})
			return out, err
		})
	static(URIHistory, "history", "Saved scans", "Saved scans, newest first (same as list_history with defaults).",
		func(ctx context.Context) (any, error) {
			_, out, err := t.listHistory(ctx, nil, listHistoryIn{})
			return out, err
		})
	static(URISettings, "settings", "Settings (read-only)", "netdiag settings with the SNMP community and any web server URL password redacted (same as get_settings).",
		func(ctx context.Context) (any, error) {
			_, out, err := t.getSettings(ctx, nil, noInput{})
			return out, err
		})
	static(URIStatus, "status", "netdiag status", "The netdiag window, the scan watching the network, a count of the problems and team chat (same as get_status).",
		func(ctx context.Context) (any, error) {
			return t.status(ctx), nil
		})
	static(URIProblems, "problems", "Problems right now", "What is wrong right now, with advice, and what was fixed in the last 24 hours (same as list_problems with defaults).",
		func(ctx context.Context) (any, error) {
			_, out, err := t.listProblems(ctx, nil, listProblemsIn{})
			return out, err
		})
	static(URIRunbooks, "runbooks", "Runbooks", "Every runbook (the same as the prompts) with its arguments (same as get_runbook without a name).",
		func(context.Context) (any, error) {
			return getRunbookOut{Runbooks: runbookIndex()}, nil
		})
	s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: TmplRunbook, Name: "runbook", Title: "Runbook",
		Description: "One runbook's steps (same as get_runbook with that name). A required argument it has no value for shows as <name>, for example <ip>.",
		MIMEType:    resMIME},
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return readRunbookResource(svc, req.Params.URI)
		})

	tmpl := func(tp, name, title, desc string) {
		s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: tp, Name: name, Title: title, Description: desc, MIMEType: resMIME},
			func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return readScanResource(svc, req.Params.URI)
			})
	}
	tmpl(TmplScan, "scan", "Scan summary", "Compact summary of a scan (same as get_scan).")
	tmpl(TmplDevices, "scan-devices", "Scan devices", "First 200 device rows of a scan (same as list_devices with filter=all, limit=200).")
	tmpl(TmplDevice, "scan-device", "Device detail", "Full details of one device in a scan (same as get_device by ip).")
}

func jsonResource(uri string, v any) (*mcp.ReadResourceResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: resMIME, Text: string(b)}}}, nil
}

// parseScanURI splits netdiag://scans/<id>[/devices[/<ip>]].
func parseScanURI(uri string) (scanID string, devices bool, ip string, ok bool) {
	rest, found := strings.CutPrefix(uri, uriPrefix+"scans/")
	if !found {
		return "", false, "", false
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	parts := strings.Split(rest, "/")
	dec := func(p string) (string, bool) {
		v, err := url.PathUnescape(p)
		return v, err == nil && v != ""
	}
	switch {
	case len(parts) == 1:
		id, good := dec(parts[0])
		return id, false, "", good
	case len(parts) == 2 && parts[1] == "devices":
		id, good := dec(parts[0])
		return id, true, "", good
	case len(parts) == 3 && parts[1] == "devices":
		id, g1 := dec(parts[0])
		addr, g2 := dec(parts[2])
		return id, true, addr, g1 && g2
	}
	return "", false, "", false
}

// readRunbookResource reads netdiag://runbooks/<name>: the runbook's text
// with every required argument shown as a placeholder (<ip>), since a
// resource read carries no arguments.
func readRunbookResource(svc *service.Service, uri string) (*mcp.ReadResourceResult, error) {
	rest, found := strings.CutPrefix(uri, URIRunbooks+"/")
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	name, err := url.PathUnescape(rest)
	if !found || err != nil {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	rb, ok := findRunbook(name)
	if !ok {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	a := map[string]string{}
	for _, ar := range rb.Args {
		if ar.Required {
			a[ar.Name] = "<" + ar.Name + ">"
		}
	}
	txt, err := runbookText(rb, svc, a)
	if err != nil {
		return nil, err
	}
	return jsonResource(uri, RunbookDoc{Name: rb.Name, Title: rb.Title, Text: txt})
}

func readScanResource(svc *service.Service, uri string) (*mcp.ReadResourceResult, error) {
	id, devices, ip, ok := parseScanURI(uri)
	if !ok {
		return nil, mcp.ResourceNotFoundError(uri)
	}
	r, err := svc.ResolveScan(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, service.ErrNoStore) {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return nil, err
	}
	r = RedactScan(r)
	switch {
	case !devices:
		return jsonResource(uri, Summarize(r, supplyThreshold(svc)))
	case ip == "":
		page, err := ListDevices(r, ListOptions{Filter: "all", Limit: resDeviceLimit})
		if err != nil {
			return nil, err
		}
		return jsonResource(uri, page)
	default:
		d, err := Detail(r, ip, "")
		if err != nil {
			return nil, mcp.ResourceNotFoundError(uri)
		}
		return jsonResource(uri, d)
	}
}
