package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/arp"
	"netdiag/internal/mcpserver"
	"netdiag/internal/model"
	"netdiag/internal/scan"
	"netdiag/internal/snmpprobe"
	"netdiag/internal/store"
)

// readEvents reads an SSE stream until the named event (or EOF) and returns
// everything read.
func readEvents(t *testing.T, url, until string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	stop := false
	for sc.Scan() {
		line := sc.Text()
		b.WriteString(line + "\n")
		if line == "event: "+until {
			stop = true
			continue
		}
		if stop && strings.HasPrefix(line, "data: ") {
			break
		}
	}
	return b.String()
}

// routerARP is an ARP table with the router and the printer at 10.0.0.10,
// so a scan can tell the network (and keep its problems in the ledger).
type routerARP struct{}

func (routerARP) Table(context.Context) ([]arp.Entry, error) {
	return []arp.Entry{{IP: "10.0.0.1", MAC: "00:11:22:33:44:01"}, {IP: "10.0.0.10", MAC: "00:07:4D:00:00:10"}}, nil
}

// coverSNMP is a printer at 10.0.0.10 whose cover is open: a problem every
// scan reports, so the ledger has something to save.
type coverSNMP struct{}

func (coverSNMP) Probe(_ context.Context, ip, _ string) (*snmpprobe.Result, error) {
	if ip != "10.0.0.10" {
		return nil, snmpprobe.ErrNoResponse
	}
	return &snmpprobe.Result{
		Snmp: &model.SnmpInfo{Version: "v2c", SysDescr: "Zebra ZD621", SysName: "Front Desk", HrPrinterStatus: "other"},
		Printer: &model.PrinterInfo{Model: "ZD621", Alerts: []model.Alert{
			{Severity: "critical", Code: "hrPrinterDetectedErrorState.doorOpen", Description: "Door or cover open"}}},
	}, nil
}

// F11 (the s3cr3t-x grep test): the SNMP community, whether sent with a scan
// or taken from the settings, must appear nowhere netdiag hands it out:
// scan JSON, the event stream, exports, comparisons, history (API and the
// files on disk), the open-problem ledger and network-profile.json, and MCP
// tools and resources. GET /api/settings is the one exception, because the
// Settings dialog edits the community.
func TestSecretCommunityAppearsNowhere(t *testing.T) {
	const secret = "s3cr3t-x"
	srv, u, st := startStoreWith(t, func(d *scan.Deps) { d.ARP, d.SNMP = routerARP{}, coverSNMP{} })
	noLeak := func(what, body string) {
		t.Helper()
		if strings.Contains(body, secret) {
			t.Errorf("%s leaks the community: %s", what, body)
		}
	}

	// The settings community is the secret too, and Agent access is on.
	_, b := doj(t, "GET", u+"/api/settings", "", "")
	var set map[string]any
	if err := json.Unmarshal([]byte(b), &set); err != nil {
		t.Fatal(err)
	}
	set["community"] = secret
	set["mcp"] = map[string]any{"enabled": true}
	doc, _ := json.Marshal(set)
	if r, b := doj(t, "PUT", u+"/api/settings", string(doc), "application/json"); r.StatusCode != 200 {
		t.Fatalf("PUT settings: %d %s", r.StatusCode, b)
	}

	r, b := doj(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/28","community":"`+secret+`","live":true,"liveIntervalSec":5}`, "application/json")
	if r.StatusCode != 202 {
		t.Fatalf("start live: %d %s", r.StatusCode, b)
	}
	live := idOf(t, b)
	stream := readEvents(t, u+"/api/scans/"+live+"/events", "live")
	if !strings.Contains(stream, "event: snapshot") || !strings.Contains(stream, `"community":"***"`) {
		t.Fatalf("live stream has no redacted snapshot: %s", stream)
	}
	noLeak("live event stream", stream)
	_, b = doj(t, "GET", u+"/api/scans/"+live, "", "")
	if !strings.Contains(b, `"community":"***"`) {
		t.Fatalf("GET scan not redacted: %s", b)
	}
	noLeak("GET /api/scans/{id}", b)
	for _, f := range []string{"json", "csv"} {
		r, b := doj(t, "GET", u+"/api/scans/"+live+"/export?format="+f, "", "")
		if r.StatusCode != 200 {
			t.Fatalf("export %s: %d %s", f, r.StatusCode, b)
		}
		noLeak("export "+f, b)
	}

	// A second scan, with no community of its own (so the settings one is
	// used), replaces the live one (Restart watching); both are saved to
	// history.
	r, b = doj(t, "POST", u+"/api/scans", `{"cidr":"10.0.0.0/28","replaceLive":true}`, "application/json")
	if r.StatusCode != 202 {
		t.Fatalf("start: %d %s", r.StatusCode, b)
	}
	cur := idOf(t, b)
	waitState(t, u, live, "done")
	waitState(t, u, cur, "done")
	stream = readEvents(t, u+"/api/scans/"+cur+"/events", "done")
	if !strings.Contains(stream, "event: done") || !strings.Contains(stream, `"community":"***"`) {
		t.Fatalf("no redacted done event: %s", stream)
	}
	noLeak("done event", stream)

	_, b = doj(t, "GET", u+"/api/history", "", "")
	noLeak("GET /api/history", b)
	for _, id := range []string{live, cur} {
		r, b := doj(t, "GET", u+"/api/history/"+id, "", "")
		if r.StatusCode != 200 || !strings.Contains(b, `"community":"***"`) {
			t.Fatalf("history %s: %d %s", id, r.StatusCode, b)
		}
		noLeak("GET /api/history/{id}", b)
	}
	_, b = doj(t, "GET", u+"/api/compare?base="+live+"&current="+cur, "", "")
	noLeak("GET /api/compare", b)
	for _, f := range []string{"json", "csv"} {
		r, b := doj(t, "GET", u+"/api/compare/export?base="+live+"&current="+cur+"&format="+f, "", "")
		if r.StatusCode != 200 {
			t.Fatalf("compare export %s: %d %s", f, r.StatusCode, b)
		}
		noLeak("compare export "+f, b)
	}

	// A file saved by an older version still holds the community; reading it
	// through the API hides it, and the file is rewritten without it.
	old := `{"id":"old1","schemaVersion":2,"startedAt":"2020-01-01T10:00:00Z","state":"done","request":{"cidr":"10.0.0.0/28","community":"` + secret + `"}}`
	if err := os.WriteFile(filepath.Join(st.Dir(), "history", "20200101T100000Z-old1.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	_, b = doj(t, "GET", u+"/api/history/old1", "", "")
	if !strings.Contains(b, `"community":"***"`) {
		t.Fatalf("old history file not redacted: %s", b)
	}
	noLeak("old history file via API", b)

	// MCP: a scan started by an agent with the community, then every tool
	// and resource that returns scans, history or settings.
	ctx := context.Background()
	cs := mcpConnect(t, u)
	mcpOut := func(what string, v any, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("MCP %s: %v", what, err)
		}
		out, _ := json.Marshal(v)
		if res, ok := v.(*mcp.CallToolResult); ok && res.IsError {
			t.Fatalf("MCP %s: tool error %s", what, out)
		}
		noLeak("MCP "+what, string(out))
	}
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		mcpOut(name, res, err)
		return res
	}
	started := call("start_scan", map[string]any{"cidr": "10.0.0.0/28", "community": secret, "waitSec": 20})
	var sum struct {
		ScanID string `json:"scanId"`
	}
	sb, _ := json.Marshal(started.StructuredContent)
	if err := json.Unmarshal(sb, &sum); err != nil || sum.ScanID == "" {
		t.Fatalf("start_scan = %s", sb)
	}
	waitState(t, u, sum.ScanID, "done")
	for _, id := range []string{live, cur, sum.ScanID} {
		call("get_scan", map[string]any{"scanId": id})
		call("list_devices", map[string]any{"scanId": id})
		call("get_device", map[string]any{"scanId": id, "ip": "10.0.0.10"})
		call("get_events", map[string]any{"scanId": id})
	}
	call("get_scan", map[string]any{"scanId": "old1"})
	call("list_history", nil)
	call("compare_scans", map[string]any{"baseId": live, "currentId": cur})
	call("get_settings", nil)

	list, err := cs.ListResources(ctx, &mcp.ListResourcesParams{})
	mcpOut("resources/list", list, err)
	uris := []string{mcpserver.URISettings, mcpserver.URIHistory, mcpserver.URIHealth}
	for _, id := range []string{live, cur, sum.ScanID, "old1"} {
		uris = append(uris, "netdiag://scans/"+id, "netdiag://scans/"+id+"/devices", "netdiag://scans/"+id+"/devices/10.0.0.10")
	}
	for _, uri := range uris {
		res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if err != nil && strings.HasSuffix(uri, "/10.0.0.10") && strings.Contains(uri, "old1") {
			continue // the old file has no hosts
		}
		mcpOut(uri, res, err)
	}

	// Finally, nothing written to disk by a scan holds it: the history files
	// (the old one rewritten on read) and every other file in the data dir
	// except settings.json, which the Settings dialog edits. The ledger is
	// saved first, as Quit saves it: it has the printer's cover alert, and
	// network-profile.json the network the scans saw.
	if err := srv.svc.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{store.FileOpenProblems, store.FileNetworkProfile} {
		data, err := os.ReadFile(filepath.Join(st.Dir(), name))
		if err != nil || len(data) == 0 {
			t.Fatalf("%s not written: %v", name, err)
		}
		if name == store.FileOpenProblems && !strings.Contains(string(data), `"trigger": "printerAlert"`) {
			t.Fatalf("%s has no cover alert: %s", name, data)
		}
	}
	err = filepath.WalkDir(st.Dir(), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() == "settings.json" {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		noLeak("file "+filepath.Base(p), string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(st.Dir(), "history", "*.json"))
	if len(files) != 4 {
		t.Fatalf("history files = %v", files)
	}
}
