package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"netdiag/web"
)

func TestStaticAssetsServed(t *testing.T) {
	s := New(Config{Static: web.FS()})
	if err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for _, p := range []string{"/", "/app.js", "/styles.css", "/vendor/alpine.min.js"} {
		req := httptest.NewRequest("GET", p, nil)
		req.Host = strings.TrimPrefix(s.URL(), "http://")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", p, rec.Code)
		}
		b, _ := io.ReadAll(rec.Body)
		if len(b) == 0 {
			t.Errorf("%s: empty body", p)
		}
		if p == "/" && !strings.Contains(string(b), "vendor/alpine.min.js") {
			t.Errorf("index missing alpine script tag")
		}
	}
}

func TestNoExternalReferences(t *testing.T) {
	re := regexp.MustCompile(`(?i)(src|href)\s*=\s*["']?(https?:)?//`)
	b, err := readAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if re.Match(b) {
		t.Errorf("index.html references external resource")
	}
	js, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`https?://`).Match(js) {
		t.Errorf("app.js contains absolute URL")
	}
}

func readAsset(name string) ([]byte, error) {
	f, err := web.FS().Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func TestLiveModeUI(t *testing.T) {
	html, err := readAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`type="checkbox" id="live-mode"`, `x-model="live"`, "Stop live", ">NEW<", `aria-live="polite"`, "liveText()"} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html missing %q", want)
		}
	}
	for _, want := range []string{"{id: 'new', label: 'New'}", "localStorage", "'New device: '", "on('live'", "on('snapshot'"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js missing %q", want)
		}
	}
}

func TestOfflineUI(t *testing.T) {
	html, err := readAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{">OFFLINE<", "offline: isOffline(h)", "offlineFor(h)"} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html missing %q", want)
		}
	}
	for _, want := range []string{"{id: 'offline', label: 'Offline'}", "on('presence'", "'Device went offline: '",
		"'Device back online: '", "' offline'", "if (this.isOffline(h)) return true;"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js missing %q", want)
		}
	}
}

func TestV02UI(t *testing.T) {
	html, err := readAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`id="health"`, `aria-labelledby="health-h"`, "Recheck", `<dialog id="settings-dialog"`,
		`id="alerts"`, `aria-label="Alerts"`, `id="tab-history"`, ">History<", "CONFLICT", "Ignore this MAC",
		"Enable desktop alerts", "Export diff CSV", `role="alert"`} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html missing %q", want)
		}
	}
	for _, want := range []string{"{id: 'conflicts', label: 'Conflicts'}", "on('notice'", "on('health'", "on('conflict'",
		"n.replay", "Health: ", "Notification.requestPermission", "/api/history", "/api/compare", "/api/conflicts/ignore", "/api/health/run"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js missing %q", want)
		}
	}
	// Desktop permission must only be requested from a click handler.
	if strings.Count(string(js), "requestPermission") != 1 {
		t.Errorf("expected exactly one requestPermission call")
	}
}

// Notice ids restart at n-1 for every scan, so the UI must dedupe per scan
// and reset its seen set when a new scan starts. Otherwise a second scan's
// alerts are silently dropped.
func TestNoticeDedupeScopedToScan(t *testing.T) {
	js, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, want := range []string{
		"onNotice(n, scanId)",
		"const key = (scanId || '') + ':' + n.id;",
		"this.seenNotices[key]",
		"on('notice', n => this.onNotice(n, id));",
		"this.seenNotices = {};",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("app.js missing %q", want)
		}
	}
	if strings.Contains(src, "this.seenNotices[n.id]") {
		t.Errorf("app.js still dedupes on bare notice id")
	}
}

// Banner identity must be unique across scans (scan id + notice id), used as
// the x-for key and for dismiss.
func TestBannerIdentityUniqueAcrossScans(t *testing.T) {
	js, _ := readAsset("app.js")
	html, _ := readAsset("index.html")
	for _, want := range []string{"{bid: key}", "b.bid !== bid"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js missing %q", want)
		}
	}
	for _, want := range []string{`:key="b.bid"`, "dismiss(b.bid)"} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html missing %q", want)
		}
	}
	if strings.Contains(string(html), `:key="b.id"`) || strings.Contains(string(html), "dismiss(b.id)") {
		t.Errorf("index.html still keys banners on notice id")
	}
}
