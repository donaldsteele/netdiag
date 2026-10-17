package server

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"netdiag/internal/remote"
	"netdiag/internal/scan"
)

// fakeOSPages records the pages it is asked to open, so tests never start
// the real Settings app.
type fakeOSPages struct {
	mu    sync.Mutex
	pages []string
	err   error
}

func (f *fakeOSPages) open(page string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages = append(f.pages, page)
	return f.err
}

func startOSPages(t *testing.T, f *fakeOSPages) string {
	t.Helper()
	s := New(Config{
		Registry:   scan.NewRegistry(scan.New(scan.Deps{})),
		Static:     fstest.MapFS{"index.html": {Data: []byte("<html>hi</html>")}},
		OpenOSPage: f.open,
	})
	if err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	exited := make(chan struct{})
	go func() { s.Serve(ctx); close(exited) }()
	t.Cleanup(func() { cancel(); <-exited })
	return s.URL()
}

// POST /api/os/open opens only a page from the fixed list: any other name
// gets 400 and opens nothing, a computer without the page (macOS) gets
// 501, and a cross-site form post is refused by the guard.
func TestOSOpenWhitelist(t *testing.T) {
	f := &fakeOSPages{}
	u := startOSPages(t, f) + "/api/os/open"
	for _, tc := range []struct{ name, body string }{
		{"unknown page", `{"page":"printers"}`},
		{"a link", `{"page":"ms-settings:privacy-location"}`},
		{"a path", `{"page":"C:\\Windows\\System32\\cmd.exe"}`},
		{"a URL", `{"page":"https://example.com/"}`},
		{"other case", `{"page":"Location"}`},
		{"empty", `{}`},
		{"not JSON", `nope`},
	} {
		if r, b := do(t, "POST", u, tc.body, ""); r.StatusCode != 400 {
			t.Errorf("%s: %d %s", tc.name, r.StatusCode, b)
		}
	}
	if len(f.pages) != 0 {
		t.Fatalf("opened on a refused request: %v", f.pages)
	}
	if r, b := do(t, "POST", u, `{"page":"location"}`, ""); r.StatusCode != 204 {
		t.Fatalf("location: %d %s", r.StatusCode, b)
	}
	if len(f.pages) != 1 || f.pages[0] != "location" {
		t.Fatalf("pages %v", f.pages)
	}
	f.err = remote.ErrOSPageUnsupported
	if r, b := do(t, "POST", u, `{"page":"location"}`, ""); r.StatusCode != 501 || !strings.Contains(b, "can't open this settings page") {
		t.Fatalf("unsupported: %d %s", r.StatusCode, b)
	}
	req, _ := http.NewRequest("POST", u, strings.NewReader(`{"page":"location"}`))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 415 || len(f.pages) != 2 {
		t.Fatalf("text/plain: %d, pages %v", resp.StatusCode, f.pages)
	}
	if r, _ := do(t, "GET", u, "", ""); r.StatusCode < 400 || len(f.pages) != 2 {
		t.Fatalf("GET: %d, pages %v", r.StatusCode, f.pages)
	}
}
