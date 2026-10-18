package server

import (
	"regexp"
	"strings"
	"testing"
)

// Every request that changes something goes through app.js api(), which
// sends X-Netdiag-Session: no method:'POST'|'PUT'|'DELETE' outside it, no
// post() helper, and no fetch that sets a method but api()'s (v0.6 4.1). The page
// takes the token from #s= (or ?s=), keeps it in localStorage and takes it
// out of the address bar; a 401 shows the buttons-off banner.
func TestSessionTokenUI(t *testing.T) {
	jsb, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := readAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsb)
	const head = "    async api(method, url, body) {\n"
	i := strings.Index(js, head)
	if i < 0 {
		t.Fatal("app.js has no api(method, url, body)")
	}
	n := strings.Index(js[i:], "\n    },\n")
	if n < 0 {
		t.Fatal("api() has no end")
	}
	body, rest := js[i:i+n], js[:i]+js[i+n:]
	for _, want := range []string{"'X-Netdiag-Session': this.sessionToken()", "fetch(url, {method: method,", "r.status === 401"} {
		if !strings.Contains(body, want) {
			t.Errorf("api() lacks %q", want)
		}
	}
	if m := regexp.MustCompile(`(?i)method\s*:\s*['"](POST|PUT|DELETE|PATCH)['"]`).FindString(js); m != "" {
		t.Errorf("app.js has %q: send it through api()", m)
	}
	if m := regexp.MustCompile(`fetch\([^)]*,\s*\{[^}]*\bmethod\b`).FindString(rest); m != "" {
		t.Errorf("app.js calls %q outside api()", m)
	}
	for _, bad := range []string{"this.post(", "async post(", "XMLHttpRequest", "sendBeacon"} {
		if strings.Contains(js, bad) {
			t.Errorf("app.js has %q", bad)
		}
	}
	if !strings.Contains(js, "this.api('GET', '/api/settings')") || strings.Contains(js, "fetch('/api/settings'") {
		t.Error("GET /api/settings must go through api() (v0.6-D14)")
	}
	for _, want := range []string{"location.hash", "location.search", "localStorage.setItem('netdiag.session', session)",
		"history.replaceState(null, '', '/')", "localStorage.getItem('netdiag.session')", "ev.key === 'netdiag.session'"} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js missing %q", want)
		}
	}
	for _, want := range []string{`id="session-off"`, `x-show="sessionOff && !quitted"`,
		"<strong>Buttons on this page are turned off for safety.</strong> Double-click netdiag to open a fresh page."} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html missing %q", want)
		}
	}
}
