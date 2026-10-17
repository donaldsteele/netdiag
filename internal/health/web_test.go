package health

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

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

// A user name and password written in the Web server URL never show in the
// check's detail or data, through a relative redirect (which keeps them)
// or a Location header that carries its own.
func TestWebHidesURLPassword(t *testing.T) {
	const pass = "pa55word"
	var away string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.Redirect(w, r, "/login", http.StatusFound)
		case "/login":
			if u, p, ok := r.BasicAuth(); !ok || u != "admin" || p != pass {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte("<html>Welcome</html>"))
		case "/away":
			http.Redirect(w, r, away, http.StatusFound)
		case "/moved":
			http.Redirect(w, r, "/new", http.StatusMovedPermanently)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		}
	}))
	defer srv.Close()
	away = strings.Replace(otherHostURL(srv.URL), "http://", "http://sso:"+pass+"@", 1) + "/sso"
	withPass := strings.Replace(srv.URL, "http://", "http://admin:"+pass+"@", 1)
	shown := strings.Replace(srv.URL, "http://", "http://admin:xxxxx@", 1)
	r := newTestRunner(fakePinger{}, fakeResolver{}, "")
	check := func(name string, cfg Config) model.HealthCheck {
		t.Helper()
		c := r.checkWeb(context.Background(), Input{Config: cfg})
		if b, _ := json.Marshal(c); strings.Contains(string(b), pass) {
			t.Errorf("%s: password in %s", name, b)
		}
		return c
	}

	c := check("relative redirect", Config{WebURL: withPass + "/", MustContain: "Welcome"})
	if c.Status != model.HealthOK || c.Data["url"] != shown+"/" || c.Data["finalURL"] != shown+"/login" ||
		!strings.HasSuffix(c.Detail, ", redirected to "+shown+"/login") {
		t.Errorf("relative redirect: %+v", c)
	}
	c = check("location with userinfo", Config{WebURL: withPass + "/away"})
	wantLoc := strings.Replace(away, pass, "xxxxx", 1)
	if c.Status != model.HealthFail || c.Data["location"] != wantLoc ||
		c.Detail != "HTTP 302 (expected 200): it redirects to another server, "+wantLoc {
		t.Errorf("location with userinfo: %+v", c)
	}
	c = check("expected redirect", Config{WebURL: withPass + "/moved", ExpectStatus: 302})
	if c.Status != model.HealthFail || c.Data["location"] != shown+"/new" ||
		c.Detail != "HTTP 301 (expected 302): it redirects to "+shown+"/new" {
		t.Errorf("expected redirect: %+v", c)
	}
	c = check("too many redirects", Config{WebURL: withPass + "/loop"})
	if c.Status != model.HealthFail || !strings.Contains(c.Detail, "the last one to "+shown+"/loop") {
		t.Errorf("too many redirects: %+v", c)
	}
	check("invalid URL", Config{WebURL: withPass + "/%zz"})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://admin:" + pass + "@" + l.Addr().String() + "/"
	l.Close()
	c = check("request failed", Config{WebURL: closed})
	if c.Status != model.HealthFail || !strings.Contains(c.Detail, "admin:xxxxx@") {
		t.Errorf("request failed: %+v", c)
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

// selfSigned returns a self-signed certificate for 127.0.0.1, valid from
// notBefore to notAfter.
func selfSigned(t *testing.T, cn string, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: notBefore, NotAfter: notAfter, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// tlsServer starts an https test server with cert (nil: httptest's own).
func tlsServer(t *testing.T, cert *tls.Certificate) *httptest.Server {
	t.Helper()
	return tlsServerWith(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>Intranet</html>"))
	}))
}

// tlsServerWith starts an https test server with cert and handler h.
func tlsServerWith(t *testing.T, cert *tls.Certificate, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	if cert != nil {
		srv.TLS = &tls.Config{Certificates: []tls.Certificate{*cert}}
	}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // refused handshakes are expected
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// colonHex writes a fingerprint the way browsers show it: AB:CD:...
func colonHex(fp string) string {
	var b strings.Builder
	for i := 0; i < len(fp); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(strings.ToUpper(fp[i : i+2]))
	}
	return b.String()
}

func TestWebUntrustedCertificate(t *testing.T) {
	srv := tlsServer(t, nil) // httptest's own certificate: from no known authority
	fp := Fingerprint(srv.Certificate())
	r := newTestRunner(fakePinger{}, fakeResolver{}, "")
	check := func(cfg Config) model.HealthCheck {
		cfg.WebURL = srv.URL + "/"
		return r.checkWeb(context.Background(), Input{Config: cfg})
	}

	// This computer's own trust store (the platform verifier on Windows
	// and macOS) does not know httptest's authority either.
	c := check(Config{})
	if c.Status != model.HealthFail || !strings.HasPrefix(c.Detail, "the certificate is not trusted: ") || c.Data["certSHA256"] != fp {
		t.Fatalf("system roots: %+v", c)
	}
	if c = check(Config{TrustedCert: fp}); c.Status != model.HealthOK {
		t.Fatalf("system roots, trusted: %+v", c)
	}

	r.roots = x509.NewCertPool() // trust nothing, like a computer that has never seen the server's CA
	c = check(Config{MustContain: "Intranet"})
	if c.Status != model.HealthFail || c.Detail != "the certificate is not trusted: self-signed or from a private authority" ||
		c.Data["certSHA256"] != fp || c.Data["certSubject"] == "" || c.Data["url"] != srv.URL+"/" ||
		c.Data["certExpires"] != srv.Certificate().NotAfter.UTC().Format(time.RFC3339) {
		t.Fatalf("untrusted: %+v", c)
	}

	for _, pin := range []string{fp, colonHex(fp)} {
		c = check(Config{MustContain: "Intranet", TrustedCert: pin})
		if c.Status != model.HealthOK || c.Data["trustedCert"] != true || c.Data["certSHA256"] != nil {
			t.Fatalf("trusted %q: %+v", pin, c)
		}
	}

	// Another certificate than the trusted one, and not valid either.
	other := strings.Repeat("0", 64)
	c = check(Config{TrustedCert: other})
	if c.Status != model.HealthFail || !strings.HasPrefix(c.Detail, "the certificate changed since you trusted it") || c.Data["certSHA256"] != fp {
		t.Fatalf("changed: %+v", c)
	}

	// A certificate that is valid in the normal way passes, trusted or not.
	r.roots.AddCert(srv.Certificate())
	for _, pin := range []string{"", other} {
		if c = check(Config{TrustedCert: pin}); c.Status != model.HealthOK || c.Data["trustedCert"] != nil {
			t.Fatalf("valid, pin %q: %+v", pin, c)
		}
	}

	// The right authority but a name the certificate is not for.
	c = r.checkWeb(context.Background(), Input{Config: Config{WebURL: otherHostURL(srv.URL) + "/"}})
	if c.Status != model.HealthFail || c.Data["certSHA256"] != fp ||
		!strings.HasPrefix(c.Detail, "the certificate is not trusted: it is made out to example.com, ") || !strings.HasSuffix(c.Detail, ", not localhost") {
		t.Fatalf("wrong name: %+v", c)
	}
}

func TestWebExpiredCertificate(t *testing.T) {
	now := time.Now()
	cert := selfSigned(t, "old-nas", now.Add(-48*time.Hour), now.Add(-24*time.Hour))
	srv := tlsServer(t, &cert)
	leaf := srv.Certificate()
	fp := Fingerprint(leaf)
	r := newTestRunner(fakePinger{}, fakeResolver{}, "")
	r.roots = x509.NewCertPool()
	r.roots.AddCert(leaf)
	c := r.checkWeb(context.Background(), Input{Config: Config{WebURL: srv.URL}})
	want := "the certificate is not trusted: it expired on " + certDate(leaf.NotAfter)
	if c.Status != model.HealthFail || c.Detail != want || c.Data["certSHA256"] != fp || c.Data["certSubject"] != "old-nas" {
		t.Fatalf("expired: %+v", c)
	}
	// Trusting it makes the check work, but it still warns.
	c = r.checkWeb(context.Background(), Input{Config: Config{WebURL: srv.URL, TrustedCert: fp}})
	if c.Status != model.HealthWarn || !strings.HasSuffix(c.Detail, "; the certificate you trusted expired on "+certDate(leaf.NotAfter)) {
		t.Fatalf("trusted expired: %+v", c)
	}
}

// An http address that moves to https on the same server is followed, and
// the trusted certificate is used for the https part.
func TestWebRedirectToTrustedHTTPS(t *testing.T) {
	cert := selfSigned(t, "intranet", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	tsrv := tlsServer(t, &cert)
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, tsrv.URL+"/portal", http.StatusMovedPermanently)
	}))
	defer plain.Close()
	r := newTestRunner(fakePinger{}, fakeResolver{}, "")
	r.roots = x509.NewCertPool()
	c := r.checkWeb(context.Background(), Input{Config: Config{WebURL: plain.URL + "/"}})
	if c.Status != model.HealthFail || c.Data["certSHA256"] != Fingerprint(tsrv.Certificate()) {
		t.Fatalf("untrusted: %+v", c)
	}
	c = r.checkWeb(context.Background(), Input{Config: Config{WebURL: plain.URL + "/", MustContain: "Intranet",
		TrustedCert: Fingerprint(tsrv.Certificate())}})
	if c.Status != model.HealthOK || c.Data["finalURL"] != tsrv.URL+"/portal" {
		t.Fatalf("trusted: %+v", c)
	}
}

// A redirect chain across two https ports of one server with different
// certificates: with the first one trusted, the second is reported as
// another address on this server, not as a changed certificate, and
// trusting it is not offered (it would replace the trusted one).
func TestWebTrustedCertOtherPort(t *testing.T) {
	now := time.Now()
	certB := selfSigned(t, "portal", now.Add(-time.Hour), now.Add(time.Hour))
	portal := tlsServer(t, &certB)
	certA := selfSigned(t, "front", now.Add(-time.Hour), now.Add(time.Hour))
	front := tlsServerWith(t, &certA, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, portal.URL+"/portal", http.StatusFound)
	}))
	fpA, fpB := Fingerprint(front.Certificate()), Fingerprint(portal.Certificate())
	r := newTestRunner(fakePinger{}, fakeResolver{}, "")
	r.roots = x509.NewCertPool()
	check := func(pin string) model.HealthCheck {
		return r.checkWeb(context.Background(), Input{Config: Config{WebURL: front.URL + "/", TrustedCert: pin}})
	}

	c := check(fpA)
	want := "another address on this server, " + portal.URL + "/portal, uses a different certificate that is not trusted " +
		"(self-signed or from a private authority); netdiag trusts one certificate for the web server, so to check that address set it as the Web server URL"
	if c.Status != model.HealthFail || c.Detail != want || c.Data["certSHA256"] != nil || c.Data["certSubject"] != "portal" ||
		c.Data["url"] != front.URL+"/" {
		t.Fatalf("other port: %+v", c)
	}
	// The first address itself has another certificate than the trusted one.
	c = check(fpB)
	if c.Status != model.HealthFail || !strings.HasPrefix(c.Detail, "the certificate changed since you trusted it") || c.Data["certSHA256"] != fpA {
		t.Fatalf("first address changed: %+v", c)
	}
	// Nothing trusted yet: the first certificate is offered.
	c = check("")
	if c.Status != model.HealthFail || !strings.HasPrefix(c.Detail, "the certificate is not trusted: ") || c.Data["certSHA256"] != fpA {
		t.Fatalf("nothing trusted: %+v", c)
	}
	// With the second address as the Web server URL, its own certificate
	// can be trusted and works.
	c = r.checkWeb(context.Background(), Input{Config: Config{WebURL: portal.URL + "/portal", TrustedCert: fpA}})
	if c.Status != model.HealthFail || !strings.HasPrefix(c.Detail, "the certificate changed since you trusted it") || c.Data["certSHA256"] != fpB {
		t.Fatalf("second address as the URL: %+v", c)
	}
	if c = r.checkWeb(context.Background(), Input{Config: Config{WebURL: portal.URL + "/portal", TrustedCert: fpB}}); c.Status != model.HealthOK {
		t.Fatalf("second address trusted: %+v", c)
	}
}

// An http address that moves to https on the same server: the trusted
// certificate was for the https address, so a new certificate there is a
// changed certificate even though the URL itself is http.
func TestWebRedirectToChangedHTTPSCert(t *testing.T) {
	cert := selfSigned(t, "intranet", time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	tsrv := tlsServer(t, &cert)
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, tsrv.URL+"/portal", http.StatusMovedPermanently)
	}))
	defer plain.Close()
	r := newTestRunner(fakePinger{}, fakeResolver{}, "")
	r.roots = x509.NewCertPool()
	c := r.checkWeb(context.Background(), Input{Config: Config{WebURL: plain.URL + "/", TrustedCert: strings.Repeat("0", 64)}})
	if c.Status != model.HealthFail || !strings.HasPrefix(c.Detail, "the certificate changed since you trusted it") ||
		c.Data["certSHA256"] != Fingerprint(tsrv.Certificate()) {
		t.Fatalf("changed after upgrade: %+v", c)
	}
}

func TestConfigFor(t *testing.T) {
	s := model.Settings{InternetCheckURL: "http://i/", WebServer: model.SettingsWebServer{URL: "https://w/",
		MustContain: "x", ExpectStatus: 204, TrustedCert: strings.Repeat("a", 64)}, NoInternetNetworks: []string{"n-0123456789ab"}}
	want := Config{WebURL: "https://w/", MustContain: "x", ExpectStatus: 204, TrustedCert: strings.Repeat("a", 64), InternetURL: "http://i/",
		NoInternetNetworks: []string{"n-0123456789ab"}}
	got := ConfigFor(s)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	// The config has its own copy of the list.
	s.NoInternetNetworks[0] = "n-ffffffffffff"
	if got.NoInternetNetworks[0] != "n-0123456789ab" {
		t.Fatal("ConfigFor shares the settings' list")
	}
}
