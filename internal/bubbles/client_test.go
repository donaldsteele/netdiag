// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0,
// pkg/bubblesclient/client_test.go and pkg/bubblesclient/read_test.go.
// Modified for netdiag: the package name and the protocol import path; the
// upload, slash-command and event-stream tests are left out with the code
// they test, and the request list covers only the calls netdiag keeps.

package bubbles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"netdiag/internal/bubbles/protocol"
)

func TestPinnedTLS(t *testing.T) {
	pki := newPKI(t)
	other := newPKI(t)
	var hits atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer bub_test" {
			t.Error("missing bearer")
		}
		_ = json.NewEncoder(w).Encode(protocol.PostedAlert{Message: protocol.Message{ID: "m1"}})
	})
	tests := []struct {
		name   string
		fp     string
		sendCA bool
		want   error
	}{
		{"pinned ok", pki.caFP, true, nil},
		{"wrong pin", other.caFP, true, ErrServerIdentityChanged},
		{"leaf pin is not the CA", pki.leafFP, true, ErrServerIdentityChanged},
		{"CA not presented", pki.caFP, false, ErrServerIdentityChanged},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hits.Store(0)
			srv := pki.server(t, h, tc.sendCA)
			c := newClient(t, srv, tc.fp)
			m, err := c.PostAlert(context.Background(), protocol.AlertInput{DedupeKey: "k"})
			if tc.want == nil {
				if err != nil || m.ID != "m1" {
					t.Fatalf("err=%v m=%v", err, m)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if hits.Load() != 0 {
				t.Fatal("a request reached the server before the pin check passed")
			}
		})
	}
}

func TestNewValidation(t *testing.T) {
	for _, cfg := range []Config{
		{BaseURL: ""},
		{BaseURL: "http://127.0.0.1:28225"},
		{BaseURL: "https://127.0.0.1:28225/x"},
		{BaseURL: "https://127.0.0.1:28225", CAFingerprint: "nope"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%+v) succeeded", cfg)
		}
	}
}

func TestAPIErrors(t *testing.T) {
	pki := newPKI(t)
	tests := []struct {
		name   string
		status int
		code   string
		header map[string]string
		is     error
	}{
		{"unauthorized", 401, "unauthorized", nil, ErrUnauthorized},
		{"rate limited", 429, "rate_limited", map[string]string{"Retry-After": "7"}, ErrRateLimited},
		{"unavailable", 503, "unavailable", nil, ErrUnavailable},
		{"internal", 500, "internal", nil, ErrUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := pki.server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(protocol.ErrorBody{Error: protocol.Error{Code: tc.code, Message: "msg",
					Details: map[string]any{"fields": map[string]any{"title": "required"}}}})
			}), true)
			c := newClient(t, srv, pki.caFP)
			err := c.ResolveAlert(context.Background(), "k", "")
			if !errors.Is(err, tc.is) {
				t.Fatalf("err = %v, want %v", err, tc.is)
			}
			var ae *APIError
			if !errors.As(err, &ae) || ae.Status != tc.status || ae.Code != tc.code || ae.Message != "msg" {
				t.Fatalf("api error = %+v", ae)
			}
			if ae.FieldErrors()["title"] != "required" {
				t.Fatalf("fields = %v", ae.FieldErrors())
			}
			if tc.status == 429 && ae.RetryAfter != 7*time.Second {
				t.Fatalf("RetryAfter = %v", ae.RetryAfter)
			}
			if errors.Is(err, ErrServerIdentityChanged) {
				t.Fatal("must not match unrelated sentinel")
			}
		})
	}
}

func TestUnreachableIsUnavailable(t *testing.T) {
	pki := newPKI(t)
	srv := pki.server(t, http.NotFoundHandler(), true)
	c := newClient(t, srv, pki.caFP)
	srv.Close()
	if err := c.ResolveAlert(context.Background(), "k", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := map[string]time.Duration{
		"": 0, "5": 5 * time.Second, "-3": 0, "junk": 0,
		now.Add(9 * time.Second).Format(http.TimeFormat): 9 * time.Second,
	}
	for in, want := range tests {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("%q = %v want %v", in, got, want)
		}
	}
}

func TestNilTransportWithPinUsesPinnedDefault(t *testing.T) {
	pki := newPKI(t)
	srv := pki.server(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), true)
	c, err := New(Config{BaseURL: srv.URL, Token: "bub_test", CAFingerprint: pki.caFP, HTTPClient: &http.Client{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := c.http.Get(srv.URL)
	if err != nil {
		t.Fatalf("pinned request: %v", err)
	}
	_ = resp.Body.Close()

	other := newPKI(t)
	bad, err := New(Config{BaseURL: srv.URL, Token: "bub_test", CAFingerprint: other.caFP, HTTPClient: &http.Client{}})
	if err != nil {
		t.Fatalf("New with wrong pin: %v", err)
	}
	if resp, err := bad.http.Get(srv.URL); err == nil {
		_ = resp.Body.Close()
		t.Fatal("request succeeded against a wrong pin")
	}
}

// An IP-literal host is verified against the leaf's IP SAN: crypto/tls leaves
// ServerName empty for IPs, so a leaf for another address must be refused.
func TestPinnedTLSChecksIPSAN(t *testing.T) {
	var hits atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(protocol.PostedAlert{Message: protocol.Message{ID: "m1"}})
	})
	wrong := newPKIFor(t, "10.9.8.7") // the test server listens on 127.0.0.1
	c := newClient(t, wrong.server(t, h, true), wrong.caFP)
	if _, err := c.PostAlert(context.Background(), protocol.AlertInput{DedupeKey: "k"}); !errors.Is(err, ErrServerIdentityChanged) {
		t.Fatalf("leaf without the dialed IP: err = %v, want ErrServerIdentityChanged", err)
	}
	if hits.Load() != 0 {
		t.Fatal("a request reached the server despite the SAN mismatch")
	}
	right := newPKIFor(t, "127.0.0.1")
	c = newClient(t, right.server(t, h, true), right.caFP)
	if _, err := c.PostAlert(context.Background(), protocol.AlertInput{DedupeKey: "k"}); err != nil {
		t.Fatalf("leaf with the dialed IP: %v", err)
	}
}

func TestReadMethodsRequests(t *testing.T) {
	pki := newPKI(t)
	var got []string
	srv := pki.server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"id":"m1","message":{"id":"m1"},"transition":"created"}`))
	}), true)
	c := newClient(t, srv, pki.caFP)
	ctx := context.Background()

	if _, err := c.ListChannels(ctx); err != nil {
		t.Fatal(err)
	}
	if res, err := c.PostAlertResult(ctx, protocol.AlertInput{DedupeKey: "k"}); err != nil || res.Transition != "created" {
		t.Fatalf("PostAlertResult = %+v, %v", res, err)
	}
	if _, err := c.ResolveAlertKey(ctx, "k", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PostMessage(ctx, "C 1", protocol.NewMessage{Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Self(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Status(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /api/v1/channels",
		"POST /api/v1/alerts",
		"POST /api/v1/alerts/resolve",
		"POST /api/v1/channels/C%201/messages",
		"GET /api/v1/apps/self",
		"GET /api/v1/status",
	}
	if len(got) != len(want) {
		t.Fatalf("requests = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d = %q, want %q", i, got[i], want[i])
		}
	}
}
