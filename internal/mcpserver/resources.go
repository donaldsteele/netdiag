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

// Resource URIs and templates. resources/list shows the four static
// resources; individual scans are exposed through the templates (the SDK only
// enumerates statically registered resources, and scan ids are discoverable
// via the list_history tool and the netdiag://history resource).
const (
	URIInterfaces = "netdiag://interfaces"
	URIHealth     = "netdiag://health"
	URIHistory    = "netdiag://history"
	URISettings   = "netdiag://settings"

	TmplScan    = "netdiag://scans/{scanId}"
	TmplDevices = "netdiag://scans/{scanId}/devices"
	TmplDevice  = "netdiag://scans/{scanId}/devices/{ip}"
)

func registerResources(s *mcp.Server, svc *service.Service) {
	t := &tools{svc: svc}
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
	static(URISettings, "settings", "Settings (read-only)", "netdiag settings with the SNMP community redacted (same as get_settings).",
		func(ctx context.Context) (any, error) {
			_, out, err := t.getSettings(ctx, nil, noInput{})
			return out, err
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
		return jsonResource(uri, Summarize(r))
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
