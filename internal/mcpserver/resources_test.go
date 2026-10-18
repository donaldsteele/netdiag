package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/model"
)

func (e *env) read(t *testing.T, uri string) (string, error) {
	t.Helper()
	res, err := e.cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return "", err
	}
	if len(res.Contents) != 1 || res.Contents[0].MIMEType != "application/json" {
		t.Fatalf("%s: bad contents %+v", uri, res.Contents)
	}
	return res.Contents[0].Text, nil
}

func TestListResourcesAndTemplates(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	var s startScanOut // a finished, saved scan must not be listed
	e.ok(t, "start_scan", map[string]any{"waitSec": 20}, &s)
	if s.ScanID == "" || s.StillRunning {
		t.Fatalf("start_scan = %+v", s)
	}
	rl, err := e.cs.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rl.Resources {
		got = append(got, r.URI)
	}
	for _, w := range []string{URIInterfaces, URIHealth, URIHistory, URISettings, URIStatus, URIProblems, URIRunbooks} {
		if !slices.Contains(got, w) {
			t.Errorf("resource %s missing from %v", w, got)
		}
	}
	// docs/mcp.md says resources/list shows only the seven fixed resources.
	if len(got) != 7 {
		t.Errorf("resources/list = %v, want only the seven fixed resources", got)
	}
	tl, err := e.cs.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var tg []string
	for _, r := range tl.ResourceTemplates {
		tg = append(tg, r.URITemplate)
	}
	for _, w := range []string{TmplScan, TmplDevices, TmplDevice, TmplRunbook} {
		if !slices.Contains(tg, w) {
			t.Errorf("template %s missing from %v", w, tg)
		}
	}
	if len(tg) != 4 {
		t.Errorf("templates = %v, want four", tg)
	}
}

// TestRunbookResources: netdiag://runbooks is get_runbook's index, and
// netdiag://runbooks/{name} the runbook's text (the prompt's), with a
// placeholder for a required argument; status and problems are the tools'.
func TestRunbookResources(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	txt, err := e.read(t, URIRunbooks)
	if err != nil {
		t.Fatal(err)
	}
	var idx getRunbookOut
	if err := json.Unmarshal([]byte(txt), &idx); err != nil || len(idx.Runbooks) != len(PromptNames) {
		t.Fatalf("index = %s err=%v", txt, err)
	}
	txt, err = e.read(t, "netdiag://runbooks/explain_problems")
	if err != nil {
		t.Fatal(err)
	}
	var doc RunbookDoc
	if err := json.Unmarshal([]byte(txt), &doc); err != nil || doc.Name != "explain_problems" || doc.Text != promptText(t, e, "explain_problems", nil) {
		t.Fatalf("runbook resource = %+v err=%v", doc, err)
	}
	txt, err = e.read(t, "netdiag://runbooks/diagnose_printer")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(txt), &doc); err != nil || !strings.Contains(doc.Text, "Diagnose the printer at <ip>") {
		t.Fatalf("placeholder missing: %s", txt)
	}
	if _, err := e.read(t, "netdiag://runbooks/nope"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "not found") {
		t.Fatalf("unknown runbook: %v", err)
	}
	for _, uri := range []string{URIStatus, URIProblems} {
		txt, err := e.read(t, uri)
		if err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(txt), &v); err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
		if _, ok := v["source"]; uri == URIProblems && !ok {
			t.Errorf("%s has no source: %s", uri, txt)
		}
		if _, ok := v["window"]; uri == URIStatus && !ok {
			t.Errorf("%s has no window: %s", uri, txt)
		}
	}
}

func TestReadResources(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	set, err := e.st.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	set.Community = commSecret
	if _, err := e.st.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	var s startScanOut
	e.ok(t, "start_scan", map[string]any{"waitSec": 20, "community": commSecret}, &s)
	if s.ScanID == "" {
		t.Fatal("no scan id")
	}
	var page DevicePage
	e.ok(t, "list_devices", map[string]any{"scanId": s.ScanID}, &page)
	if len(page.Items) == 0 {
		t.Fatal("no devices")
	}
	ip := page.Items[0].IP

	base := "netdiag://scans/" + s.ScanID
	for _, uri := range []string{URIInterfaces, URIHealth, URIHistory, URISettings, base, base + "/devices", base + "/devices/" + ip} {
		txt, err := e.read(t, uri)
		if err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(txt), &v); err != nil {
			t.Fatalf("%s: not JSON: %v", uri, err)
		}
		if strings.Contains(txt, commSecret) {
			t.Errorf("%s leaks community", uri)
		}
	}
	var sum ScanSummary
	txt, _ := e.read(t, base)
	if err := json.Unmarshal([]byte(txt), &sum); err != nil || sum.ScanID != s.ScanID || sum.State != model.StateDone {
		t.Errorf("scan resource = %+v err=%v", sum, err)
	}
	var dp DevicePage
	txt, _ = e.read(t, base+"/devices")
	if err := json.Unmarshal([]byte(txt), &dp); err != nil || len(dp.Items) != len(page.Items) {
		t.Errorf("devices resource = %+v err=%v", dp, err)
	}
	var dd DeviceDetail
	txt, _ = e.read(t, base+"/devices/"+ip)
	if err := json.Unmarshal([]byte(txt), &dd); err != nil || dd.Host.IP != ip {
		t.Errorf("device resource = %+v err=%v", dd.Host.IP, err)
	}
}

func TestReadResourceNotFound(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	var s startScanOut
	e.ok(t, "start_scan", map[string]any{"waitSec": 20}, &s)
	for _, uri := range []string{
		"netdiag://scans/does-not-exist",
		"netdiag://scans/does-not-exist/devices",
		"netdiag://scans/" + s.ScanID + "/devices/10.9.9.9",
		"netdiag://nothing",
	} {
		_, err := e.read(t, uri)
		if err == nil {
			t.Errorf("%s: expected error", uri)
		} else if !strings.Contains(strings.ToLower(err.Error()), "not found") {
			t.Errorf("%s: error %q should be resource-not-found", uri, err)
		}
	}
}

func TestParseScanURI(t *testing.T) {
	id, dev, ip, ok := parseScanURI("netdiag://scans/abc-1/devices/10.0.0.10")
	if !ok || id != "abc-1" || !dev || ip != "10.0.0.10" {
		t.Fatalf("got %q %v %q %v", id, dev, ip, ok)
	}
	for _, bad := range []string{"netdiag://scans/", "netdiag://scans/a/b", "netdiag://scans/a/devices/b/c", "netdiag://x"} {
		if _, _, _, ok := parseScanURI(bad); ok {
			t.Errorf("%s should not parse", bad)
		}
	}
}
