// Package bubblestest is a fake bubbles v0.2.0 server for tests. It answers
// the endpoints netdiag's client uses with the status codes, error codes
// and messages bubbles v0.2.0 gives, checked in the same order: the
// onboarding listener's /ca.crt (plain HTTP), GET /status, pairing (create,
// poll with the poll secret, the token handed out once with state
// claimed), GET /channels, POST /alerts, POST /alerts/resolve,
// POST /channels/{id}/messages and GET /apps/self.
//
// It is netdiag's own code, not copied from bubbles. It uses the standard
// library only and is imported only by tests: internal/bubbles now, and the
// chat.Manager, router and outbox tests later (v0.6 M10b, M11).
package bubblestest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"netdiag/internal/bubbles/protocol"
)

// tokenPrefixLen is how many leading token characters bubbles v0.2.0 keeps
// and shows (internal/apps/tokens.go PrefixLen).
const tokenPrefixLen = 8

// The 409 messages of bubbles v0.2.0. Both are code "conflict": only the
// message, or the channel's archived_at in GET /channels, tells them apart.
const (
	msgArchived      = "the channel is archived"
	msgOpenElsewhere = "an alert with this dedupe_key is already open in another channel"
)

// Server is a fake bubbles server: API is the HTTPS listener, whose chain
// ends in the CA pinned by CAFingerprint, and Onboard is the plain-HTTP
// onboarding listener that serves the CA. Both close when the test ends.
type Server struct {
	API           *httptest.Server
	Onboard       *httptest.Server
	CADER         []byte
	CAFingerprint string

	t        testing.TB
	mu       sync.Mutex
	pairings []*pairing
	tokens   map[string][]string // issued, unrevoked tokens and their scopes
	last     string              // the latest token issued
	channels []protocol.Channel  // the channels the bot is in
	alerts   map[string]*protocol.Message
	posted   []protocol.AlertInput
	resolved []protocol.ResolveAlertByKeyRequest
	messages []string // "<channel id> <body>"
	polls    int
	seq      int // the last message id
	fail     []failure
}

type pairing struct {
	id, secret string
	req        protocol.PairingRequest
	state      string
}

type failure struct {
	status        int
	code, message string
	retryAfter    time.Duration
}

// New starts a fake server whose bot is in one channel, C1 #netdiag-alerts.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{t: t, tokens: map[string][]string{}, alerts: map[string]*protocol.Message{}}
	s.AddChannel("C1", "netdiag-alerts")
	caDER, chain := newPKI(t)
	sum := sha256.Sum256(caDER)
	s.CADER, s.CAFingerprint = caDER, "sha256:"+hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, protocol.ServerStatus{InstanceName: "Office"})
	})
	mux.HandleFunc("POST /api/v1/pairing/requests", s.createPairing)
	mux.HandleFunc("GET /api/v1/pairing/requests/{id}", s.pollPairing)
	mux.HandleFunc("GET /api/v1/channels", s.app(s.listChannels, protocol.ScopeChannelsRead, protocol.ScopeMessagesRead))
	mux.HandleFunc("POST /api/v1/alerts", s.app(s.postAlert, protocol.ScopeAlertsWrite))
	mux.HandleFunc("POST /api/v1/alerts/resolve", s.app(s.resolve, protocol.ScopeAlertsWrite))
	mux.HandleFunc("POST /api/v1/channels/{id}/messages", s.app(s.postMessage, protocol.ScopeMessagesWrite))
	mux.HandleFunc("GET /api/v1/apps/self", s.app(s.self))
	s.API = httptest.NewUnstartedServer(mux)
	s.API.TLS = &tls.Config{Certificates: []tls.Certificate{chain}}
	s.API.StartTLS()
	t.Cleanup(s.API.Close)

	s.Onboard = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ca.crt" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/x-x509-ca-cert")
		_, _ = w.Write(s.CADER)
	}))
	t.Cleanup(s.Onboard.Close)
	return s
}

// SetPairingState is the admin's approve or deny of the latest pairing
// request, or the server expiring it. The first poll after an approval
// mints the token and hands it out, and the state becomes claimed.
func (s *Server) SetPairingState(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.pairings); n > 0 {
		s.pairings[n-1].state = state
	}
}

// Polls counts the pairing polls with the right secret.
func (s *Server) Polls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.polls
}

// Token is the latest token issued, or "" before the first claim.
func (s *Server) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// Scopes are the scopes the latest token carries (those its pairing asked
// for), or nil before the first claim.
func (s *Server) Scopes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.tokens[s.last])
}

// SetScopes changes the scopes of every issued token.
func (s *Server) SetScopes(scopes ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for tok := range s.tokens {
		s.tokens[tok] = slices.Clone(scopes)
	}
}

// Revoke revokes every issued token (Admin > Apps, or a server reset):
// every app call is 401 from then on.
func (s *Server) Revoke() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.tokens)
}

// AddChannel adds the bot to a public channel, creating it if needed.
func (s *Server) AddChannel(id, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channel(id) == nil {
		s.channels = append(s.channels, protocol.Channel{ID: id, Kind: protocol.ChannelPublic, Name: &name, MemberCount: 2,
			CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
	}
}

// RemoveChannel takes the bot out of a channel: posts there are 404.
func (s *Server) RemoveChannel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels = slices.DeleteFunc(s.channels, func(c protocol.Channel) bool { return c.ID == id })
}

// ArchiveChannel archives a channel the bot is in: it stays listed, with
// archived_at set, and posts, resolves and messages there are 409.
func (s *Server) ArchiveChannel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.channel(id); c != nil {
		at := time.Now().UTC().Format(time.RFC3339Nano)
		c.ArchivedAt = &at
	}
}

// Posted lists every alert post that passed validation, in order.
func (s *Server) Posted() []protocol.AlertInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.posted)
}

// Resolved lists every resolve that closed an open alert, in order.
func (s *Server) Resolved() []protocol.ResolveAlertByKeyRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.resolved)
}

// Messages lists the messages posted, as "<channel id> <body>".
func (s *Server) Messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.messages)
}

// Alert is the latest alert card with the dedupe key.
func (s *Server) Alert(key string) (protocol.Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.alerts[key]
	if !ok {
		return protocol.Message{}, false
	}
	c := *m
	a := *m.Alert
	c.Alert = &a
	return c, true
}

// FailNext answers the next app call (one that needs the token) with this
// error instead: 429 rate_limited with a Retry-After, a 5xx, and so on.
// Calls queue up.
func (s *Server) FailNext(status int, code, message string, retryAfter time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = append(s.fail, failure{status: status, code: code, message: message, retryAfter: retryAfter})
}

func (s *Server) createPairing(w http.ResponseWriter, r *http.Request) {
	var in protocol.PairingRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.AppName) == "" {
		writeFields(w, map[string]string{"app_name": "app_name is required"})
		return
	}
	if r.Header.Get("Authorization") != "" {
		s.t.Error("bubblestest: the pairing request sent a bearer token")
	}
	s.mu.Lock()
	p := &pairing{id: "p" + strconv.Itoa(len(s.pairings)+1), secret: "sec-" + strconv.Itoa(len(s.pairings)+1), req: in, state: protocol.PairingPending}
	s.pairings = append(s.pairings, p)
	s.mu.Unlock()
	writeJSON(w, 201, protocol.PairingCreated{ID: p.id, PollSecret: p.secret, Code: "123456",
		ExpiresAt: time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339Nano)})
}

func (s *Server) pollPairing(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.pairings, func(p *pairing) bool { return p.id == r.PathValue("id") })
	// A wrong secret reveals nothing, not even that the id exists.
	if i < 0 || r.Header.Get(protocol.PairingHeader) != s.pairings[i].secret || r.Header.Get("Authorization") != "" {
		writeError(w, 404, protocol.CodeNotFound, "not found")
		return
	}
	p := s.pairings[i]
	s.polls++
	st := protocol.PairingStatus{State: p.state}
	if p.state == protocol.PairingApproved {
		// The first poll after the approval mints the token and claims it.
		p.state = protocol.PairingClaimed
		s.last = protocol.AppTokenPrefix + fmt.Sprintf("%04d", len(s.tokens)+1) + strings.Repeat("t", 48)
		s.tokens[s.last] = slices.Clone(p.req.RequestedScopes)
		st = protocol.PairingStatus{State: protocol.PairingClaimed, Token: s.last}
	}
	writeJSON(w, 200, st)
}

// appHandler answers an app call; tok is the caller's token.
type appHandler func(w http.ResponseWriter, r *http.Request, tok string)

// app wraps a handler that needs an issued token carrying one of scopes
// (any token when there are none). The handler runs with s.mu held.
func (s *Server) app(h appHandler, scopes ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		has, ok := s.tokens[tok]
		if !ok {
			writeError(w, 401, protocol.CodeUnauthorized, "unauthorized")
			return
		}
		if len(s.fail) > 0 {
			f := s.fail[0]
			s.fail = s.fail[1:]
			if f.retryAfter > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(int((f.retryAfter+time.Second-1)/time.Second)))
			}
			writeError(w, f.status, f.code, f.message)
			return
		}
		if len(scopes) > 0 && !slices.ContainsFunc(scopes, func(sc string) bool { return slices.Contains(has, sc) }) {
			writeError(w, 403, protocol.CodeForbidden, "the token lacks the "+scopes[0]+" scope")
			return
		}
		h(w, r, tok)
	}
}

// channel finds a channel the bot is in by id or by name (a leading # and
// letter case are ignored), or nil.
func (s *Server) channel(ref string) *protocol.Channel {
	name := strings.ToLower(strings.TrimPrefix(ref, "#"))
	for i := range s.channels {
		c := &s.channels[i]
		if c.ID == ref || (c.Name != nil && strings.ToLower(*c.Name) == name) {
			return c
		}
	}
	return nil
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request, _ string) {
	writeJSON(w, 200, protocol.ChannelList{Items: slices.Clone(s.channels)})
}

func (s *Server) postAlert(w http.ResponseWriter, r *http.Request, _ string) {
	var in protocol.AlertInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, 400, protocol.CodeBadRequest, "the request body is not valid JSON")
		return
	}
	if fields := validateAlert(in); len(fields) > 0 {
		writeFields(w, fields)
		return
	}
	c := s.channel(strings.TrimSpace(in.Channel))
	if c == nil {
		writeError(w, 404, protocol.CodeNotFound, "not found")
		return
	}
	if c.ArchivedAt != nil {
		writeError(w, 409, protocol.CodeConflict, msgArchived)
		return
	}
	s.posted = append(s.posted, in)
	if m, ok := s.alerts[in.DedupeKey]; ok && m.Alert.State != protocol.AlertResolved {
		if m.ChannelID != c.ID {
			writeError(w, 409, protocol.CodeConflict, msgOpenElsewhere)
			return
		}
		m.Alert.Occurrences++
		m.Alert.Title = in.Title
		writeJSON(w, 200, protocol.PostedAlert{Message: *m, Transition: protocol.AlertTransitionUpdatedOther})
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.seq++
	m := &protocol.Message{ID: "M" + strconv.Itoa(s.seq), ChannelID: c.ID, Kind: protocol.MessageAlert, CreatedAt: now,
		Alert: &protocol.Alert{Severity: in.Severity, State: protocol.AlertOpen, Title: in.Title, Fields: in.Fields, Link: in.Link,
			DedupeKey: in.DedupeKey, Occurrences: 1, FirstSeenAt: now, LastSeenAt: now}}
	s.alerts[in.DedupeKey] = m
	writeJSON(w, 201, protocol.PostedAlert{Message: *m, Transition: protocol.AlertTransitionCreated})
}

// validateAlert is bubbles v0.2.0's AlertInput validation: field name to
// reason, empty when the input is valid.
func validateAlert(in protocol.AlertInput) map[string]string {
	f := map[string]string{}
	if ch := strings.TrimSpace(in.Channel); ch == "" || len(ch) > 80 {
		f["channel"] = "channel is required (a channel name or id)"
	}
	if !slices.Contains([]string{protocol.SeverityInfo, protocol.SeverityWarning, protocol.SeverityCritical}, in.Severity) {
		f["severity"] = "severity must be info, warning or critical"
	}
	switch title := strings.TrimSpace(in.Title); {
	case title == "":
		f["title"] = "title is required"
	case utf8.RuneCountInString(title) > protocol.MaxAlertTitle:
		f["title"] = fmt.Sprintf("title must be at most %d characters", protocol.MaxAlertTitle)
	}
	if utf8.RuneCountInString(in.Text) > protocol.MaxAlertText {
		f["text"] = fmt.Sprintf("text must be at most %d characters", protocol.MaxAlertText)
	}
	switch {
	case in.DedupeKey == "":
		f["dedupe_key"] = "dedupe_key is required"
	case utf8.RuneCountInString(in.DedupeKey) > protocol.MaxAlertDedupeKey:
		f["dedupe_key"] = fmt.Sprintf("dedupe_key must be at most %d characters", protocol.MaxAlertDedupeKey)
	}
	if len(in.Fields) > protocol.MaxAlertFields {
		f["fields"] = fmt.Sprintf("at most %d fields", protocol.MaxAlertFields)
	} else {
		for i, fl := range in.Fields {
			switch {
			case strings.TrimSpace(fl.Name) == "" || utf8.RuneCountInString(fl.Name) > protocol.MaxAlertFieldName:
				f[fmt.Sprintf("fields[%d]", i)] = fmt.Sprintf("name is required, at most %d characters", protocol.MaxAlertFieldName)
			case utf8.RuneCountInString(fl.Value) > protocol.MaxAlertFieldValue:
				f[fmt.Sprintf("fields[%d]", i)] = fmt.Sprintf("value must be at most %d characters", protocol.MaxAlertFieldValue)
			}
		}
	}
	if l := in.Link; l != nil && (len(*l) > protocol.MaxAlertLink || !(strings.HasPrefix(*l, "http://") || strings.HasPrefix(*l, "https://"))) {
		f["link"] = "link must be an http or https URL of at most 2048 characters"
	}
	if n := in.RenotifyAfterS; n != nil && *n != 0 && (*n < 10 || *n > 86400) {
		f["renotify_after_s"] = "renotify_after_s must be 0 or 10-86400"
	}
	if at := in.OccurredAt; at != nil {
		if _, err := time.Parse(time.RFC3339Nano, *at); err != nil {
			f["occurred_at"] = "occurred_at must be an RFC 3339 timestamp"
		}
	}
	return f
}

func (s *Server) resolve(w http.ResponseWriter, r *http.Request, _ string) {
	var in protocol.ResolveAlertByKeyRequest
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.DedupeKey == "" || utf8.RuneCountInString(in.DedupeKey) > protocol.MaxAlertDedupeKey {
		writeFields(w, map[string]string{"dedupe_key": "dedupe_key is required, at most 200 characters"})
		return
	}
	m, ok := s.alerts[in.DedupeKey]
	if !ok {
		writeError(w, 404, protocol.CodeNotFound, "not found")
		return
	}
	c := s.channel(m.ChannelID)
	switch {
	case c == nil:
		writeError(w, 404, protocol.CodeNotFound, "not found")
		return
	case m.Alert.State == protocol.AlertResolved:
		writeJSON(w, 200, *m) // resolving twice changes nothing
		return
	case c.ArchivedAt != nil:
		writeError(w, 409, protocol.CodeConflict, msgArchived)
		return
	}
	m.Alert.State = protocol.AlertResolved
	at := time.Now().UTC().Format(time.RFC3339Nano)
	m.Alert.ResolvedAt = &at
	s.resolved = append(s.resolved, in)
	writeJSON(w, 200, *m)
}

func (s *Server) postMessage(w http.ResponseWriter, r *http.Request, _ string) {
	var in protocol.NewMessage
	_ = json.NewDecoder(r.Body).Decode(&in)
	c := s.channel(r.PathValue("id"))
	switch {
	case c == nil || c.ID != r.PathValue("id"):
		writeError(w, 404, protocol.CodeNotFound, "not found")
		return
	case c.ArchivedAt != nil:
		writeError(w, 409, protocol.CodeConflict, msgArchived)
		return
	case strings.TrimSpace(in.Body) == "":
		writeFields(w, map[string]string{"body": "body must not be empty"})
		return
	}
	s.messages = append(s.messages, c.ID+" "+in.Body)
	s.seq++
	writeJSON(w, 201, protocol.Message{ID: "M" + strconv.Itoa(s.seq), ChannelID: c.ID, Kind: protocol.MessageText, Body: in.Body,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
}

func (s *Server) self(w http.ResponseWriter, r *http.Request, tok string) {
	writeJSON(w, 200, protocol.AppSelf{App: protocol.App{ID: "A1", Name: "netdiag", Tokens: []protocol.AppToken{}},
		Token: protocol.AppToken{ID: "T1", AppID: "A1", Prefix: tok[:tokenPrefixLen], Scopes: slices.Clone(s.tokens[tok])}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, protocol.ErrorBody{Error: protocol.Error{Code: code, Message: msg}})
}

// writeFields is bubbles' validation answer: 400 with details.fields.
func writeFields(w http.ResponseWriter, fields map[string]string) {
	f := make(map[string]any, len(fields))
	for k, v := range fields {
		f[k] = v
	}
	writeJSON(w, 400, protocol.ErrorBody{Error: protocol.Error{Code: protocol.CodeBadRequest, Message: "one or more fields are invalid",
		Details: map[string]any{protocol.DetailsFields: f}}})
}

// newPKI makes a CA and a leaf for 127.0.0.1 signed by it, and returns the
// CA's DER and the chain (leaf, then CA) the API listener presents.
func newPKI(t testing.TB) ([]byte, tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "bubblestest CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "bubblestest"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return caDER, tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}
}
