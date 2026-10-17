package bubbles

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"netdiag/internal/bubbles/protocol"
)

// fakeBubbles mimics the bubbles v0.2.0 endpoints netdiag uses, with the
// same status codes and rules: the onboarding listener's /ca.crt (plain
// HTTP), GET /status, pairing (create, poll with the poll secret, the token
// handed out once with state claimed), GET /channels, POST /alerts (201 new,
// 200 repeat, 404 for a channel the bot is not in, 409 for a key open in
// another channel), POST /alerts/resolve (404 for a key never posted,
// idempotent otherwise), POST /channels/{id}/messages and GET /apps/self.
type fakeBubbles struct {
	t   *testing.T
	pki *testPKI

	mu       sync.Mutex
	state    string // pairing state: pending, approved, denied, expired, claimed
	scopes   []string
	token    string // the issued token; "" before the claim
	revoked  bool
	channels []protocol.Channel // the bot's channels
	alerts   map[string]*protocol.Message
	posted   []protocol.AlertInput
	messages []string // "<channel> <body>"
	polls    int
}

func newFakeBubbles(t *testing.T) (*fakeBubbles, *httptest.Server, *httptest.Server) {
	t.Helper()
	f := &fakeBubbles{t: t, pki: newPKI(t), state: protocol.PairingPending, alerts: map[string]*protocol.Message{}}
	name := "netdiag-alerts"
	f.channels = []protocol.Channel{{ID: "C1", Kind: protocol.ChannelPublic, Name: &name}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeFakeJSON(w, 200, protocol.ServerStatus{InstanceName: "Office"})
	})
	mux.HandleFunc("POST /api/v1/pairing/requests", f.createPairing)
	mux.HandleFunc("GET /api/v1/pairing/requests/{id}", f.pollPairing)
	mux.HandleFunc("GET /api/v1/channels", f.app(protocol.ScopeChannelsRead, func(w http.ResponseWriter, r *http.Request) {
		writeFakeJSON(w, 200, protocol.ChannelList{Items: f.channels})
	}))
	mux.HandleFunc("POST /api/v1/alerts", f.app(protocol.ScopeAlertsWrite, f.postAlert))
	mux.HandleFunc("POST /api/v1/alerts/resolve", f.app(protocol.ScopeAlertsWrite, f.resolve))
	mux.HandleFunc("POST /api/v1/channels/{id}/messages", f.app(protocol.ScopeMessagesWrite, func(w http.ResponseWriter, r *http.Request) {
		var in protocol.NewMessage
		_ = json.NewDecoder(r.Body).Decode(&in)
		if !f.inChannel(r.PathValue("id")) {
			writeFakeErr(w, 404, protocol.CodeNotFound, "not found")
			return
		}
		f.messages = append(f.messages, r.PathValue("id")+" "+in.Body)
		writeFakeJSON(w, 201, protocol.Message{ID: "M" + r.PathValue("id"), ChannelID: r.PathValue("id"), Kind: protocol.MessageText, Body: in.Body})
	}))
	mux.HandleFunc("GET /api/v1/apps/self", f.app("", func(w http.ResponseWriter, r *http.Request) {
		writeFakeJSON(w, 200, protocol.AppSelf{App: protocol.App{ID: "A1", Name: "netdiag"},
			Token: protocol.AppToken{Prefix: f.token[:12], Scopes: f.scopes}})
	}))
	api := f.pki.server(t, mux, true)
	onboard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ca.crt" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/x-x509-ca-cert")
		_, _ = w.Write(f.pki.caDER)
	}))
	t.Cleanup(onboard.Close)
	return f, api, onboard
}

// setState is the admin's approve or deny (or the server expiring it).
func (f *fakeBubbles) setState(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = s
}

func (f *fakeBubbles) createPairing(w http.ResponseWriter, r *http.Request) {
	var in protocol.PairingRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.AppName == "" {
		writeFakeErr(w, 400, protocol.CodeBadRequest, "app_name is required")
		return
	}
	if r.Header.Get("Authorization") != "" {
		f.t.Error("pairing request sent a bearer token")
	}
	f.mu.Lock()
	f.scopes = in.RequestedScopes
	f.mu.Unlock()
	writeFakeJSON(w, 201, protocol.PairingCreated{ID: "p1", PollSecret: "sec", Code: "123456",
		ExpiresAt: time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339Nano)})
}

func (f *fakeBubbles) pollPairing(w http.ResponseWriter, r *http.Request) {
	// A wrong secret reveals nothing, not even that the id exists.
	if r.PathValue("id") != "p1" || r.Header.Get(protocol.PairingHeader) != "sec" || r.Header.Get("Authorization") != "" {
		writeFakeErr(w, 404, protocol.CodeNotFound, "not found")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls++
	st := protocol.PairingStatus{State: f.state}
	if f.state == protocol.PairingApproved {
		// The first poll after the approval mints the token and claims it.
		f.state = protocol.PairingClaimed
		f.token = protocol.AppTokenPrefix + strings.Repeat("t", 52)
		st = protocol.PairingStatus{State: protocol.PairingClaimed, Token: f.token}
	}
	writeFakeJSON(w, 200, st)
}

// app wraps a handler that needs the issued token and, when scope is set,
// that scope.
func (f *fakeBubbles) app(scope string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.token == "" || f.revoked || r.Header.Get("Authorization") != "Bearer "+f.token {
			writeFakeErr(w, 401, protocol.CodeUnauthorized, "unauthorized")
			return
		}
		if scope != "" && !slices.Contains(f.scopes, scope) {
			writeFakeErr(w, 403, protocol.CodeForbidden, "the token lacks "+scope)
			return
		}
		h(w, r)
	}
}

func (f *fakeBubbles) inChannel(id string) bool {
	return slices.ContainsFunc(f.channels, func(c protocol.Channel) bool { return c.ID == id || "#"+*c.Name == id || *c.Name == id })
}

func (f *fakeBubbles) postAlert(w http.ResponseWriter, r *http.Request) {
	var in protocol.AlertInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.DedupeKey == "" || in.Title == "" {
		writeFakeErr(w, 400, protocol.CodeBadRequest, "title and dedupe_key are required")
		return
	}
	if !f.inChannel(in.Channel) {
		writeFakeErr(w, 404, protocol.CodeNotFound, "not found")
		return
	}
	f.posted = append(f.posted, in)
	if m, ok := f.alerts[in.DedupeKey]; ok && m.Alert.State != protocol.AlertResolved {
		if m.ChannelID != in.Channel {
			writeFakeErr(w, 409, protocol.CodeConflict, "an alert with this dedupe_key is already open in another channel")
			return
		}
		m.Alert.Occurrences++
		writeFakeJSON(w, 200, protocol.PostedAlert{Message: *m, Transition: protocol.AlertTransitionUpdatedOther})
		return
	}
	m := &protocol.Message{ID: "M-" + in.DedupeKey, ChannelID: in.Channel, Kind: protocol.MessageAlert,
		Alert: &protocol.Alert{Severity: in.Severity, State: protocol.AlertOpen, Title: in.Title, DedupeKey: in.DedupeKey, Occurrences: 1}}
	f.alerts[in.DedupeKey] = m
	writeFakeJSON(w, 201, protocol.PostedAlert{Message: *m, Transition: protocol.AlertTransitionCreated})
}

func (f *fakeBubbles) resolve(w http.ResponseWriter, r *http.Request) {
	var in protocol.ResolveAlertByKeyRequest
	_ = json.NewDecoder(r.Body).Decode(&in)
	m, ok := f.alerts[in.DedupeKey]
	if !ok {
		writeFakeErr(w, 404, protocol.CodeNotFound, "not found")
		return
	}
	m.Alert.State = protocol.AlertResolved
	m.Body = in.Text
	writeFakeJSON(w, 200, *m)
}

func writeFakeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeFakeErr(w http.ResponseWriter, status int, code, msg string) {
	writeFakeJSON(w, status, protocol.ErrorBody{Error: protocol.Error{Code: code, Message: msg}})
}
