package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// raw sends a request with the JSON content type and, when token is not
// empty, that X-Netdiag-Session; unlike do it never adds the test token.
func raw(t *testing.T, method, url, body, token string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(SessionHeader, token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// sessionRefused checks a 401 with the session body.
func sessionRefused(t *testing.T, what string, r *http.Response, b string) {
	t.Helper()
	if r.StatusCode != http.StatusUnauthorized {
		t.Errorf("%s: %d %s, want 401", what, r.StatusCode, b)
		return
	}
	var j struct {
		Error   string `json:"error"`
		Session *bool  `json:"session"`
	}
	if err := json.Unmarshal([]byte(b), &j); err != nil || j.Error != "Double-click netdiag to open a fresh page." ||
		j.Session == nil || *j.Session {
		t.Errorf("%s: body %s", what, b)
	}
}

// fill puts a value in every {wildcard} of a route path.
func fill(p string) string {
	for {
		i := strings.Index(p, "{")
		if i < 0 {
			return p
		}
		j := strings.Index(p[i:], "}")
		p = p[:i] + "x" + p[i+j+1:]
	}
}

// Every route in the table that changes something under /api/ (POST, PUT,
// DELETE) answers 401 without X-Netdiag-Session, or with another token, so
// a route added later is covered by itself (v0.6 4.1, roadmap 8.3). The
// handler never runs: a refused quit leaves netdiag serving.
func TestSessionGuardEveryMutatingRoute(t *testing.T) {
	s, u, done := start(t, nil)
	n := 0
	for _, rt := range s.routes() {
		method, p, _ := strings.Cut(rt.pattern, " ")
		if method == http.MethodGet || !strings.HasPrefix(p, "/api/") {
			continue
		}
		n++
		url := u + fill(p)
		r, b := raw(t, method, url, `{}`, "")
		sessionRefused(t, rt.pattern+" without the header", r, b)
		r, b = raw(t, method, url, `{}`, testToken+"x")
		sessionRefused(t, rt.pattern+" with another token", r, b)
		r, b = raw(t, method, url, `{}`, strings.ToUpper(testToken))
		sessionRefused(t, rt.pattern+" with the token in other case", r, b)
	}
	// The v0.6 table: scans, cancel, quit, settings, health/run, history,
	// conflicts/ignore, remote (v0.5.7), os/open (M7) and the eight team
	// chat actions (M10).
	if n < 17 {
		t.Fatalf("only %d mutating /api routes in the table", n)
	}
	select {
	case err := <-done:
		t.Fatalf("a refused request stopped netdiag: %v", err)
	default:
	}
	if r, _ := do(t, "GET", u+"/api/identity", "", ""); r.StatusCode != 200 {
		t.Fatalf("identity after refused quit: %d", r.StatusCode)
	}
}

// Methods the table has no route for, and other spellings of a gated path,
// are refused by the guard before the router sees them.
func TestSessionGuardOtherMethodsAndSpellings(t *testing.T) {
	_, u, _ := start(t, nil)
	for _, c := range []struct{ method, path string }{
		{"PATCH", "/api/settings"},
		{"DELETE", "/api/scans/x"},
		{"POST", "/api/quit/"},
		{"POST", "/api//quit"},
		{"PUT", "/api/nothing-here"},
		{"GET", "/api/settings/"},
		{"GET", "/api//settings"},
		{"GET", "/api/./settings"},
		{"HEAD", "/api/settings"},
	} {
		r, b := raw(t, c.method, u+c.path, `{}`, "")
		if c.method == "HEAD" {
			if r.StatusCode != 401 {
				t.Errorf("HEAD /api/settings: %d", r.StatusCode)
			}
			continue
		}
		sessionRefused(t, c.method+" "+c.path, r, b)
	}
}

// The reads that return a secret need the token (GET /api/settings has the
// SNMP community; the team chat status and pairing state name the chat
// server and the code its admin compares, v0.6-D14). Other reads, the export links, the remote
// capabilities and the page itself stay token-free GETs.
func TestSessionGuardReads(t *testing.T) {
	_, u, _ := start(t, nil)
	r, b := raw(t, "GET", u+"/api/settings", "", "")
	sessionRefused(t, "GET /api/settings", r, b)
	for _, p := range []string{"/api/bubbles", "/api/bubbles/pair/status", "/api/bubbles/", "/api//bubbles"} {
		r, b := raw(t, "GET", u+p, "", "")
		sessionRefused(t, "GET "+p, r, b)
	}
	// The test server has no store, so a passing request gets 503.
	if r, b := raw(t, "GET", u+"/api/settings", "", testToken); r.StatusCode != 503 {
		t.Fatalf("GET /api/settings with the token: %d %s", r.StatusCode, b)
	}
	for _, p := range []string{"/", "/api/identity", "/api/interfaces", "/api/scans/running", "/api/remote",
		"/api/scans/nope", "/api/scans/nope/export", "/api/history", "/api/compare"} {
		if r, b := raw(t, "GET", u+p, "", ""); r.StatusCode == 401 {
			t.Errorf("GET %s needs no token: %d %s", p, r.StatusCode, b)
		}
	}
}

// /mcp is not under /api/: the Agent access switch and the Origin check
// stay its consent, so a request without the token reaches mcpGate (404
// while Agent access is off).
func TestSessionGuardLeavesMCP(t *testing.T) {
	_, u, _ := start(t, nil)
	if r, b := raw(t, "POST", u+"/mcp", `{}`, ""); r.StatusCode != 404 || !strings.Contains(b, "Agent access") {
		t.Fatalf("POST /mcp: %d %s", r.StatusCode, b)
	}
}

// The Host and content-type guards still come first: a foreign Host gets
// 403 and a form post 415, with or without the token.
func TestSessionGuardAfterHostGuard(t *testing.T) {
	_, u, _ := start(t, nil)
	if r, _ := do(t, "POST", u+"/api/quit", "", "evil.example.com"); r.StatusCode != 403 {
		t.Fatalf("foreign host: %d", r.StatusCode)
	}
	req, _ := http.NewRequest("POST", u+"/api/quit", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 415 {
		t.Fatalf("form post: %d", resp.StatusCode)
	}
}

// POST /api/quit without the header gets 401; with the launch token it gets
// 204 and netdiag stops.
func TestQuitNeedsToken(t *testing.T) {
	_, u, done := start(t, nil)
	r, b := raw(t, "POST", u+"/api/quit", "", "")
	sessionRefused(t, "quit without the header", r, b)
	if r, b := raw(t, "POST", u+"/api/quit", "", testToken); r.StatusCode != 204 {
		t.Fatalf("quit with the header: %d %s", r.StatusCode, b)
	}
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

// A server made without a token gets one nobody knows, so it is never left
// open by mistake.
func TestSessionTokenDefault(t *testing.T) {
	a, b := New(Config{}), New(Config{})
	if a.token == "" || a.token == b.token || len(a.token) != 43 {
		t.Fatalf("default tokens %q %q", a.token, b.token)
	}
	if s := New(Config{SessionToken: "given"}); s.token != "given" {
		t.Fatalf("configured token = %q", s.token)
	}
}
