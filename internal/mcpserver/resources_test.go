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
	rl, err := e.cs.ListResources(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rl.Resources {
		got = append(got, r.URI)
	}
	for _, w := range []string{URIInterfaces, URIHealth, URIHistory, URISettings} {
		if !slices.Contains(got, w) {
			t.Errorf("resource %s missing from %v", w, got)
		}
	}
	tl, err := e.cs.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var tg []string
	for _, r := range tl.ResourceTemplates {
		tg = append(tg, r.URITemplate)
	}
	for _, w := range []string{TmplScan, TmplDevices, TmplDevice} {
		if !slices.Contains(tg, w) {
			t.Errorf("template %s missing from %v", w, tg)
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
