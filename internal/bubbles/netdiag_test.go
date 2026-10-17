package bubbles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"netdiag/internal/bubbles/bubblestest"
	"netdiag/internal/bubbles/protocol"
)

// netdiagScopes are the scopes netdiag asks for at the first pairing
// (v0.6-D2).
var netdiagScopes = []string{protocol.ScopeAlertsWrite, protocol.ScopeChannelsRead, protocol.ScopeMessagesWrite, protocol.ScopeFilesWrite}

// pair runs the wizard's client side against the fake: fetch the CA, pin
// it, request pairing, let the admin approve, wait for the token. It
// returns the paired configuration and the pairing, already claimed.
func pair(t *testing.T, f *bubblestest.Server) (Config, *Pairing) {
	t.Helper()
	ctx := context.Background()
	_, fp, err := FetchCA(ctx, f.Onboard.URL)
	if err != nil {
		t.Fatal(err)
	}
	if fp != f.CAFingerprint {
		t.Fatalf("fingerprint %s, want %s", fp, f.CAFingerprint)
	}
	if g := strings.Fields(FormatFingerprint(fp)); len(g) != 16 {
		t.Fatalf("fingerprint groups = %v, want 16", g)
	}
	p, err := RequestPairing(ctx, f.API.URL, fp, protocol.PairingRequest{AppName: "netdiag", Description: "netdiag on TEST-PC", RequestedScopes: netdiagScopes})
	if err != nil {
		t.Fatal(err)
	}
	p.SetPollInterval(5 * time.Millisecond)
	if p.Code() != "123456" || p.ExpiresAt().IsZero() {
		t.Fatalf("code %q expires %v", p.Code(), p.ExpiresAt())
	}
	time.AfterFunc(20*time.Millisecond, func() { f.SetPairingState(protocol.PairingApproved) })
	cfg, err := p.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfg.Token, protocol.AppTokenPrefix) || cfg.Token != f.Token() || cfg.CAFingerprint != fp || cfg.BaseURL != f.API.URL {
		t.Fatalf("paired config %v (token %q)", cfg, cfg.Token)
	}
	return cfg, p
}

// apiStatus is err's HTTP status; it fails the test for any other error.
func apiStatus(t *testing.T, err error) *APIError {
	t.Helper()
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("not an API error: %v", err)
	}
	return ae
}

// The whole client path netdiag uses, against a server that answers like
// bubbles v0.2.0: pair by code with the CA pinned, list channels, post an
// alert, repeat it, resolve it by key, post a message, read self and status.
func TestPairAndAlertAgainstV020(t *testing.T) {
	f := bubblestest.New(t)
	cfg, _ := pair(t, f)
	if !slices.Equal(f.Scopes(), netdiagScopes) {
		t.Fatalf("requested scopes %v", f.Scopes())
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
	if err != nil || len(chs) != 1 || *chs[0].Name != "netdiag-alerts" || chs[0].ArchivedAt != nil {
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
	got := f.Posted()[0]
	if got.RenotifyAfterS == nil || *got.RenotifyAfterS != 0 || got.OccurredAt == nil || *got.OccurredAt != at {
		t.Fatalf("renotify_after_s and occurred_at not sent: %+v", got)
	}
	m, err := c.ResolveAlertKey(ctx, in.DedupeKey, "Fixed at 10:42 (was down 12 min).")
	if err != nil || m.Alert.State != protocol.AlertResolved {
		t.Fatalf("resolve %+v %v", m, err)
	}
	if r := f.Resolved(); len(r) != 1 || r[0].Text != "Fixed at 10:42 (was down 12 min)." {
		t.Fatalf("resolved %+v", r)
	}
	// Resolving again changes nothing; a key never posted is 404, which
	// the outbox drops (v0.6 plan §4.9).
	if err := c.ResolveAlert(ctx, in.DedupeKey, ""); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if err := c.ResolveAlert(ctx, "never|posted", ""); apiStatus(t, err).Status != 404 {
		t.Fatalf("resolve unknown key: %v", err)
	}
	if _, err := c.PostMessage(ctx, "C1", protocol.NewMessage{Body: "netdiag on TEST-PC is connected."}); err != nil {
		t.Fatal(err)
	}
	if msgs := f.Messages(); len(msgs) != 1 || msgs[0] != "C1 netdiag on TEST-PC is connected." {
		t.Fatalf("messages %q", msgs)
	}
	// bubbles shows the first 8 characters of the token in Admin > Apps;
	// chat.Connection.TokenPrefix assumes the same.
	self, err := c.Self(ctx)
	if err != nil || self.App.Name != "netdiag" || self.Token.Prefix != cfg.Token[:8] {
		t.Fatalf("self %+v %v", self, err)
	}
}

// The errors the outbox maps to states (v0.6 plan §4.9): 404 when the bot
// is not in the channel, 409 when the key is open in another channel, 400
// for an alert bubbles refuses, 403 for a missing scope, 429 with
// Retry-After, 401 once the token is revoked.
func TestAlertErrorsAgainstV020(t *testing.T) {
	f := bubblestest.New(t)
	cfg, _ := pair(t, f)
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	in := protocol.AlertInput{Channel: "C9", Severity: protocol.SeverityCritical, Title: "Gateway down", DedupeKey: "healthFailed|gateway"}
	if _, err := c.PostAlertResult(ctx, in); apiStatus(t, err).Status != 404 {
		t.Fatalf("post to a channel the bot is not in: %v", err)
	}
	in.Channel = "C1"
	if _, err := c.PostAlertResult(ctx, in); err != nil {
		t.Fatal(err)
	}
	f.AddChannel("C2", "other")
	in.Channel = "C2"
	if ae := apiStatus(t, func() error { _, err := c.PostAlertResult(ctx, in); return err }()); ae.Status != 409 || ae.Code != protocol.CodeConflict {
		t.Fatalf("post of a key open in another channel: %v", ae)
	}
	long := in
	long.Title = strings.Repeat("x", protocol.MaxAlertTitle+1)
	notHTTP := "file:///etc/passwd"
	long.Link = &notHTTP
	ae := apiStatus(t, func() error { _, err := c.PostAlertResult(ctx, long); return err }())
	if fe := ae.FieldErrors(); ae.Status != 400 || fe["title"] == "" || fe["link"] == "" {
		t.Fatalf("an alert bubbles refuses: %v %v", ae, fe)
	}
	f.FailNext(http.StatusTooManyRequests, protocol.CodeRateLimited, "too many requests; try again later", 7*time.Second)
	_, err = c.PostAlertResult(ctx, in)
	if !errors.Is(err, ErrRateLimited) || apiStatus(t, err).RetryAfter != 7*time.Second {
		t.Fatalf("rate limited: %v", err)
	}
	f.FailNext(http.StatusServiceUnavailable, protocol.CodeUnavailable, "the server is busy or shutting down; try again", 0)
	if _, err := c.ListChannels(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("busy: %v", err)
	}
	f.SetScopes(protocol.ScopeChannelsRead)
	if _, err := c.PostAlertResult(ctx, in); apiStatus(t, err).Status != 403 {
		t.Fatalf("post without alerts:write: %v", err)
	}
	f.Revoke()
	if _, err := c.ListChannels(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked token: %v", err)
	}
}

// bubbles v0.2.0 answers 409 conflict both for a key open in another
// channel and for an archived channel, for posts, resolves and messages
// alike. GET /channels tells them apart: an archived channel stays listed
// with archived_at set. The outbox maps an archived channel to no_channel,
// not to a move (v0.6-D24).
func TestArchivedChannelConflictAgainstV020(t *testing.T) {
	f := bubblestest.New(t)
	cfg, _ := pair(t, f)
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	in := protocol.AlertInput{Channel: "C1", Severity: protocol.SeverityWarning, Title: "Printer offline", DedupeKey: "printerOffline|aa"}
	if _, err := c.PostAlertResult(ctx, in); err != nil {
		t.Fatal(err)
	}
	archived := func(id string) bool {
		t.Helper()
		chs, err := c.ListChannels(ctx)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(chs, func(ch protocol.Channel) bool { return ch.ID == id })
		return i < 0 || chs[i].ArchivedAt != nil
	}
	// The move: the key is open in C1 and the post names C2, which is fine.
	f.AddChannel("C2", "front-desk")
	moved := in
	moved.Channel = "C2"
	move := apiStatus(t, func() error { _, err := c.PostAlertResult(ctx, moved); return err }())
	if move.Status != 409 || move.Code != protocol.CodeConflict || archived("C2") {
		t.Fatalf("move: %v, C2 archived %v", move, archived("C2"))
	}
	// The archived channel: every write there is 409 with the same code.
	f.ArchiveChannel("C1")
	other := in
	other.DedupeKey = "printerOffline|bb"
	for name, call := range map[string]func() error{
		"repeat post": func() error { _, err := c.PostAlertResult(ctx, in); return err },
		"new post":    func() error { _, err := c.PostAlertResult(ctx, other); return err },
		"resolve":     func() error { return c.ResolveAlert(ctx, in.DedupeKey, "Fixed.") },
		"message":     func() error { _, err := c.PostMessage(ctx, "C1", protocol.NewMessage{Body: "hello"}); return err },
	} {
		ae := apiStatus(t, call())
		if ae.Status != 409 || ae.Code != move.Code || ae.Message == move.Message {
			t.Errorf("%s in an archived channel: %v", name, ae)
		}
	}
	if !archived("C1") {
		t.Fatal("GET /channels does not show C1 archived")
	}
	if a, _ := f.Alert(in.DedupeKey); a.Alert.State != protocol.AlertOpen {
		t.Fatalf("the card in the archived channel is %s", a.Alert.State)
	}
}

// bubbles hands the token out once: polling the same request again after
// the claim sees claimed without a token.
func TestPairingTokenOnlyOnce(t *testing.T) {
	f := bubblestest.New(t)
	_, p := pair(t, f)
	p.SetPollInterval(time.Millisecond)
	if _, err := p.Wait(context.Background()); !errors.Is(err, ErrPairingClaimed) {
		t.Fatalf("second wait: %v", err)
	}
	// A new request is a new app with a new token (Reconnect).
	first := f.Token()
	cfg, _ := pair(t, f)
	if cfg.Token == first {
		t.Fatal("a second pairing handed out the same token")
	}
}

func TestPairingDeniedAndExpiredAgainstV020(t *testing.T) {
	for state, want := range map[string]error{protocol.PairingDenied: ErrPairingDenied, protocol.PairingExpired: ErrPairingExpired} {
		t.Run(state, func(t *testing.T) {
			f := bubblestest.New(t)
			p, err := RequestPairing(context.Background(), f.API.URL, f.CAFingerprint, protocol.PairingRequest{AppName: "netdiag"})
			if err != nil {
				t.Fatal(err)
			}
			p.SetPollInterval(time.Millisecond)
			time.AfterFunc(10*time.Millisecond, func() { f.SetPairingState(state) })
			if _, err := p.Wait(context.Background()); !errors.Is(err, want) {
				t.Fatalf("err = %v, want %v", err, want)
			}
			if f.Token() != "" || f.Polls() == 0 {
				t.Fatalf("token %q after %d polls", f.Token(), f.Polls())
			}
		})
	}
}

// SetPollInterval sets the wait before each poll; d <= 0 keeps it.
func TestSetPollInterval(t *testing.T) {
	p := &Pairing{poll: time.Second}
	p.SetPollInterval(0)
	if p.poll != time.Second {
		t.Fatalf("poll %v after 0", p.poll)
	}
	p.SetPollInterval(3 * time.Millisecond)
	if p.poll != 3*time.Millisecond {
		t.Fatalf("poll %v", p.poll)
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
		_ = json.NewEncoder(w).Encode(protocol.ChannelList{})
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
// everything it and the test fake build on is the standard library or this
// package's own protocol types.
func TestBubblesStdlibOnly(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not on PATH")
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	own := []string{"netdiag/internal/bubbles", "netdiag/internal/bubbles/protocol", "netdiag/internal/bubbles/bubblestest"}
	pkgs := strings.Fields(string(out))
	for _, p := range pkgs {
		if !slices.Contains(own, p) {
			t.Errorf("internal/bubbles depends on %s; it may use the standard library only", p)
		}
	}
	if !slices.Contains(pkgs, "netdiag/internal/bubbles/bubblestest") {
		t.Errorf("go list did not cover the fake: %v", pkgs)
	}
	// The fake is for tests only: no package of the binary imports it.
	out, err = exec.Command("go", "list", "-f", `{{.ImportPath}} {{join .Imports " "}}`, "netdiag/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && slices.Contains(f[1:], "netdiag/internal/bubbles/bubblestest") {
			t.Errorf("%s imports bubblestest; only tests may", f[0])
		}
	}
}
