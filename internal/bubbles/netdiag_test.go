package bubbles

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"netdiag/internal/bubbles/protocol"
)

// netdiagScopes are the scopes netdiag asks for at the first pairing
// (v0.6-D2).
var netdiagScopes = []string{protocol.ScopeAlertsWrite, protocol.ScopeChannelsRead, protocol.ScopeMessagesWrite, protocol.ScopeFilesWrite}

// pair runs the wizard's client side against the fake: fetch the CA, pin
// it, request pairing, let the admin approve, wait for the token.
func pair(t *testing.T, f *fakeBubbles, api, onboard string) Config {
	t.Helper()
	ctx := context.Background()
	_, fp, err := FetchCA(ctx, onboard)
	if err != nil {
		t.Fatal(err)
	}
	if fp != f.pki.caFP {
		t.Fatalf("fingerprint %s, want %s", fp, f.pki.caFP)
	}
	if g := strings.Fields(FormatFingerprint(fp)); len(g) != 16 {
		t.Fatalf("fingerprint groups = %v, want 16", g)
	}
	p, err := RequestPairing(ctx, api, fp, protocol.PairingRequest{AppName: "netdiag", Description: "netdiag on TEST-PC", RequestedScopes: netdiagScopes})
	if err != nil {
		t.Fatal(err)
	}
	p.poll = 5 * time.Millisecond
	if p.Code() != "123456" || p.ExpiresAt().IsZero() {
		t.Fatalf("code %q expires %v", p.Code(), p.ExpiresAt())
	}
	time.AfterFunc(20*time.Millisecond, func() { f.setState(protocol.PairingApproved) })
	cfg, err := p.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfg.Token, protocol.AppTokenPrefix) || cfg.CAFingerprint != fp || cfg.BaseURL != api {
		t.Fatalf("paired config %+v", cfg)
	}
	return cfg
}

// The whole client path netdiag uses, against a server that answers like
// bubbles v0.2.0: pair by code with the CA pinned, list channels, post an
// alert, repeat it, resolve it by key, post a message, read self and status.
func TestPairAndAlertAgainstV020(t *testing.T) {
	f, api, onboard := newFakeBubbles(t)
	cfg := pair(t, f, api.URL, onboard.URL)
	f.mu.Lock()
	scopes := f.scopes
	f.mu.Unlock()
	if strings.Join(scopes, ",") != strings.Join(netdiagScopes, ",") {
		t.Fatalf("requested scopes %v", scopes)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := c.Status(ctx)
	if err != nil || st.InstanceName != "Office" {
		t.Fatalf("status %+v %v", st, err)
	}
	chs, err := c.ListChannels(ctx)
	if err != nil || len(chs) != 1 || *chs[0].Name != "netdiag-alerts" {
		t.Fatalf("channels %+v %v", chs, err)
	}
	zero := 0
	at := time.Date(2026, 10, 17, 10, 30, 0, 0, time.UTC).Format(time.RFC3339)
	in := protocol.AlertInput{Channel: "C1", Severity: protocol.SeverityWarning, Title: "Printer offline: Front Desk (192.168.1.50)",
		DedupeKey: "printerOffline|00:11:22:33:44:55", RenotifyAfterS: &zero, OccurredAt: &at}
	res, err := c.PostAlertResult(ctx, in)
	if err != nil || res.Transition != protocol.AlertTransitionCreated || res.Message.Alert.State != protocol.AlertOpen {
		t.Fatalf("first post %+v %v", res, err)
	}
	res, err = c.PostAlertResult(ctx, in)
	if err != nil || res.Transition != protocol.AlertTransitionUpdatedOther || res.Message.Alert.Occurrences != 2 {
		t.Fatalf("repeat post %+v %v", res, err)
	}
	f.mu.Lock()
	got := f.posted[0]
	f.mu.Unlock()
	if got.RenotifyAfterS == nil || *got.RenotifyAfterS != 0 || got.OccurredAt == nil || *got.OccurredAt != at {
		t.Fatalf("renotify_after_s and occurred_at not sent: %+v", got)
	}
	m, err := c.ResolveAlertKey(ctx, in.DedupeKey, "Fixed at 10:42 (was down 12 min).")
	if err != nil || m.Alert.State != protocol.AlertResolved {
		t.Fatalf("resolve %+v %v", m, err)
	}
	// A key never posted is 404: the outbox drops it (v0.6 plan §4.9).
	var ae *APIError
	if err := c.ResolveAlert(ctx, "never|posted", ""); !errors.As(err, &ae) || ae.Status != 404 {
		t.Fatalf("resolve unknown key: %v", err)
	}
	if _, err := c.PostMessage(ctx, "C1", protocol.NewMessage{Body: "netdiag on TEST-PC is connected."}); err != nil {
		t.Fatal(err)
	}
	self, err := c.Self(ctx)
	if err != nil || self.App.Name != "netdiag" {
		t.Fatalf("self %+v %v", self, err)
	}
}

// The errors the outbox maps to states (v0.6 plan §4.9): 404 when the bot
// is not in the channel, 409 when the key is open in another channel, 403
// for a missing scope, 401 once the token is revoked.
func TestAlertErrorsAgainstV020(t *testing.T) {
	f, api, onboard := newFakeBubbles(t)
	c, err := New(pair(t, f, api.URL, onboard.URL))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	status := func(err error) int {
		var ae *APIError
		if !errors.As(err, &ae) {
			t.Fatalf("not an API error: %v", err)
		}
		return ae.Status
	}
	in := protocol.AlertInput{Channel: "C9", Severity: protocol.SeverityCritical, Title: "Gateway down", DedupeKey: "healthFailed|gateway"}
	if _, err := c.PostAlertResult(ctx, in); status(err) != 404 {
		t.Fatalf("post to a channel the bot is not in: %v", err)
	}
	in.Channel = "C1"
	if _, err := c.PostAlertResult(ctx, in); err != nil {
		t.Fatal(err)
	}
	other := "other"
	f.mu.Lock()
	f.channels = append(f.channels, protocol.Channel{ID: "C2", Name: &other})
	f.mu.Unlock()
	in.Channel = "C2"
	if _, err := c.PostAlertResult(ctx, in); status(err) != 409 {
		t.Fatalf("post of a key open in another channel: %v", err)
	}
	f.mu.Lock()
	f.scopes = []string{protocol.ScopeChannelsRead}
	f.mu.Unlock()
	if _, err := c.PostAlertResult(ctx, in); status(err) != 403 {
		t.Fatalf("post without alerts:write: %v", err)
	}
	f.mu.Lock()
	f.revoked = true
	f.mu.Unlock()
	if _, err := c.ListChannels(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked token: %v", err)
	}
}

// bubbles hands the token out once: a second wait on the same request sees
// claimed without a token.
func TestPairingTokenOnlyOnce(t *testing.T) {
	f, api, onboard := newFakeBubbles(t)
	pair(t, f, api.URL, onboard.URL)
	p, err := RequestPairing(context.Background(), api.URL, f.pki.caFP, protocol.PairingRequest{AppName: "netdiag"})
	if err != nil {
		t.Fatal(err)
	}
	p.poll = time.Millisecond
	if _, err := p.Wait(context.Background()); !errors.Is(err, ErrPairingClaimed) {
		t.Fatalf("second wait: %v", err)
	}
}

func TestPairingDeniedAndExpiredAgainstV020(t *testing.T) {
	for state, want := range map[string]error{protocol.PairingDenied: ErrPairingDenied, protocol.PairingExpired: ErrPairingExpired} {
		t.Run(state, func(t *testing.T) {
			f, api, _ := newFakeBubbles(t)
			p, err := RequestPairing(context.Background(), api.URL, f.pki.caFP, protocol.PairingRequest{AppName: "netdiag"})
			if err != nil {
				t.Fatal(err)
			}
			p.poll = time.Millisecond
			time.AfterFunc(10*time.Millisecond, func() { f.setState(state) })
			if _, err := p.Wait(context.Background()); !errors.Is(err, want) {
				t.Fatalf("err = %v, want %v", err, want)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.token != "" {
				t.Fatal("a token was issued")
			}
		})
	}
}

// netdiag never sends chat traffic through a proxy, even when HTTPS_PROXY
// is set or the caller's transport has one (v0.6 plan §4.9).
func TestNoProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://192.0.2.1:3128")
	t.Setenv("HTTP_PROXY", "http://192.0.2.1:3128")
	pki := newPKI(t)
	check := func(name string, hc *http.Client) {
		t.Helper()
		tr, ok := hc.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("%s: transport %T", name, hc.Transport)
		}
		if tr.Proxy != nil {
			t.Fatalf("%s: Proxy is set", name)
		}
	}
	c, err := New(Config{BaseURL: "https://192.168.1.20:28225", CAFingerprint: pki.caFP})
	if err != nil {
		t.Fatal(err)
	}
	check("pinned client", c.http)
	c, err = New(Config{BaseURL: "https://192.168.1.20:28225"})
	if err != nil {
		t.Fatal(err)
	}
	check("unpinned client", c.http)
	proxied := &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return url.Parse("http://192.0.2.1:3128") }}
	c, err = New(Config{BaseURL: "https://192.168.1.20:28225", CAFingerprint: pki.caFP, HTTPClient: &http.Client{Transport: proxied}})
	if err != nil {
		t.Fatal(err)
	}
	check("caller transport", c.http)
	if proxied.Proxy == nil {
		t.Fatal("the caller's transport was changed instead of cloned")
	}
	check("FetchCA", fetchCAClient())
	if http.DefaultTransport.(*http.Transport).Proxy == nil {
		t.Fatal("the default transport was changed instead of cloned")
	}
}

// A redirect is answered as an error and never followed, for API calls and
// for the CA download.
func TestRedirectsNotFollowed(t *testing.T) {
	pki := newPKI(t)
	var followed atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/channels", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	mux.HandleFunc("/elsewhere", func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
		writeFakeJSON(w, 200, protocol.ChannelList{})
	})
	srv := pki.server(t, mux, true)
	c := newClient(t, srv, pki.caFP)
	var ae *APIError
	if _, err := c.ListChannels(context.Background()); !errors.As(err, &ae) || ae.Status != http.StatusFound {
		t.Fatalf("err = %v, want an HTTP 302 error", err)
	}
	onboard := http.NewServeMux()
	onboard.HandleFunc("/ca.crt", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	onboard.HandleFunc("/elsewhere", func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
		_, _ = w.Write(pki.caDER)
	})
	osrv := httptest.NewServer(onboard)
	defer osrv.Close()
	if _, _, err := FetchCA(context.Background(), osrv.URL); err == nil {
		t.Fatal("FetchCA accepted a redirect")
	}
	if followed.Load() != 0 {
		t.Fatal("a redirect was followed")
	}
}

// TestBubblesStdlibOnly keeps the copied client free of third-party code:
// everything it builds on is the standard library or this package's own
// protocol types.
func TestBubblesStdlibOnly(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, p := range strings.Fields(string(out)) {
		if p != "netdiag/internal/bubbles" && p != "netdiag/internal/bubbles/protocol" {
			t.Errorf("internal/bubbles depends on %s; it may use the standard library only", p)
		}
	}
}
