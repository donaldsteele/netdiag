package portprobe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func splitAddr(t *testing.T, hostport string) (string, int) {
	h, p, err := net.SplitHostPort(hostport)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(p)
	return h, n
}

func TestExtractTitleFixtures(t *testing.T) {
	cases := map[string]string{
		"basic.html":   "HP LaserJet M404",
		"broken.html":  "Broken & Tolerant",
		"notitle.html": "",
	}
	for f, want := range cases {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "html", f))
		if err != nil {
			t.Fatal(err)
		}
		if got := ExtractTitle(b); got != want {
			t.Errorf("%s: got %q want %q", f, got, want)
		}
	}
}

func TestProbeWebHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "TestSrv/1")
		w.Write([]byte("<html><title>Hello</title></html>"))
	}))
	defer srv.Close()
	ip, port := splitAddr(t, strings.TrimPrefix(srv.URL, "http://"))
	ws := ProbeWeb(context.Background(), ip, port, "http")
	if ws.Status != 200 || ws.Title != "Hello" || ws.Server != "TestSrv/1" || ws.Error != "" || ws.TLS != nil {
		t.Fatalf("%+v", ws)
	}
}

func TestProbeWebRedirects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/a", http.StatusFound) })
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<title>Final</title>")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ip, port := splitAddr(t, strings.TrimPrefix(srv.URL, "http://"))
	ws := ProbeWeb(context.Background(), ip, port, "http")
	if ws.Title != "Final" || !strings.HasSuffix(ws.URL, "/a") {
		t.Fatalf("%+v", ws)
	}
}

func TestProbeWebRedirectLoopAndForeign(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	}))
	defer srv.Close()
	ip, port := splitAddr(t, strings.TrimPrefix(srv.URL, "http://"))
	if ws := ProbeWeb(context.Background(), ip, port, "http"); ws.Error == "" {
		t.Fatalf("expected redirect error: %+v", ws)
	}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.invalid/x", http.StatusFound)
	}))
	defer srv2.Close()
	ip, port = splitAddr(t, strings.TrimPrefix(srv2.URL, "http://"))
	ws := ProbeWeb(context.Background(), ip, port, "http")
	if ws.Status != 302 || ws.Error != "" {
		t.Fatalf("%+v", ws)
	}
}

func TestProbeWebTLSSelfSigned(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<title>Secure</title>"))
	}))
	defer srv.Close()
	ip, port := splitAddr(t, strings.TrimPrefix(srv.URL, "https://"))
	ws := ProbeWeb(context.Background(), ip, port, "https")
	if ws.Title != "Secure" || ws.TLS == nil || !ws.TLS.SelfSigned || ws.TLS.Expired || ws.TLS.NotAfter == "" {
		t.Fatalf("%+v tls=%+v", ws, ws.TLS)
	}
}

func TestProbeAllSkipsNonWeb(t *testing.T) {
	if got := ProbeAll(context.Background(), "127.0.0.1", []int{9100, 631}); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}
