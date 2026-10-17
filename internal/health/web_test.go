package health

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"netdiag/internal/model"
)

// otherHostURL rewrites a 127.0.0.1 test server URL to localhost: the
// same server under another host name.
func otherHostURL(u string) string { return strings.Replace(u, "127.0.0.1", "localhost", 1) }

func TestWebFollowsSameHostRedirects(t *testing.T) {
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>Portal home</html>"))
	}))
	defer dst.Close()
	target := dst.URL + "/home" // same host name, another port
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := 0
		fmt.Sscanf(r.URL.Query().Get("n"), "%d", &n)
		switch r.URL.Path {
		case "/":
			http.Redirect(w, r, "/login", http.StatusFound)
		case "/login":
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		case "/away":
			http.Redirect(w, r, otherHostURL(dst.URL)+"/sso", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, fmt.Sprintf("/loop?n=%d", n+1), http.StatusFound)
		case "/five":
			if n < 5 {
				http.Redirect(w, r, fmt.Sprintf("/five?n=%d", n+1), http.StatusFound)
				return
			}
			w.Write([]byte("done"))
		case "/moved":
			http.Redirect(w, r, "/new", http.StatusMovedPermanently)
		}
	}))
	defer src.Close()
	r := newTestRunner(fakePinger{}, fakeResolver{}, "")
	check := func(cfg Config) model.HealthCheck {
		return r.checkWeb(context.Background(), Input{Config: cfg})
	}

	c := check(Config{WebURL: src.URL + "/", MustContain: "Portal home"})
	if c.Status != model.HealthOK || !strings.HasSuffix(c.Detail, ", redirected to "+target) ||
		c.Data["finalURL"] != target || c.Data["url"] != src.URL+"/" || c.Data["status"] != 200 {
		t.Fatalf("same host: %+v", c)
	}

	c = check(Config{WebURL: src.URL + "/away"})
	want := "HTTP 302 (expected 200): it redirects to another server, " + otherHostURL(dst.URL) + "/sso"
	if c.Status != model.HealthFail || c.Detail != want || c.Data["finalURL"] != nil {
		t.Fatalf("other host: %+v", c)
	}

	c = check(Config{WebURL: src.URL + "/five"})
	if c.Status != model.HealthOK || c.Data["finalURL"] != src.URL+"/five?n=5" {
		t.Fatalf("five hops must be followed: %+v", c)
	}

	c = check(Config{WebURL: src.URL + "/loop"})
	if c.Status != model.HealthFail || !strings.Contains(c.Detail, "more than 5 redirects, the last one to "+src.URL+"/loop?n=6") ||
		c.Data["finalURL"] != src.URL+"/loop?n=5" {
		t.Fatalf("redirect loop: %+v", c)
	}

	// Expecting a redirect checks the redirect itself.
	c = check(Config{WebURL: src.URL + "/moved", ExpectStatus: 301})
	if c.Status != model.HealthOK || c.Data["finalURL"] != nil {
		t.Fatalf("expected 301: %+v", c)
	}
	c = check(Config{WebURL: src.URL + "/moved", ExpectStatus: 302})
	if c.Status != model.HealthFail || c.Detail != "HTTP 301 (expected 302): it redirects to "+src.URL+"/new" {
		t.Fatalf("expected 302: %+v", c)
	}
}

func TestWebNotConfigured(t *testing.T) {
	r := newTestRunner(fakePinger{}, fakeResolver{}, "")
	c := r.checkWeb(context.Background(), Input{Config: Config{WebURL: "  "}})
	if c.Status != model.HealthInfo || c.Data["configured"] != false {
		t.Fatalf("%+v", c)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := "http://" + l.Addr().String() + "/"
	l.Close()
	c = r.checkWeb(context.Background(), Input{Config: Config{WebURL: u}})
	if c.Status != model.HealthFail || c.Data["configured"] != true || c.Data["url"] != u {
		t.Fatalf("%+v", c)
	}
}

// The internet check still reports a redirect instead of following it: a
// captive portal answers that way.
func TestInternetDoesNotFollowRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/204" {
			w.WriteHeader(204)
			return
		}
		http.Redirect(w, r, "/204", http.StatusFound)
	}))
	defer srv.Close()
	r := newTestRunner(fakePinger{}, fakeResolver{}, "")
	c := r.checkInternet(context.Background(), Input{Config: Config{InternetURL: srv.URL + "/"}})
	if c.Status != model.HealthInfo || !strings.Contains(c.Detail, "captive portal") {
		t.Fatalf("%+v", c)
	}
}
