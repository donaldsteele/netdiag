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
