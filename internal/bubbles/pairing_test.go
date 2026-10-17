// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0,
// pkg/bubblesclient/pairing_test.go.
// Modified for netdiag: the package name and the protocol import path.

package bubbles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"netdiag/internal/bubbles/protocol"
)

func pairingServer(t *testing.T, pki *testPKI, states []protocol.PairingStatus) *httptest.Server {
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/pairing/requests", func(w http.ResponseWriter, r *http.Request) {
		var in protocol.PairingRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.Header.Get("Authorization") != "" || in.AppName != "netdiag" {
			t.Errorf("bad create: auth=%q app=%q", r.Header.Get("Authorization"), in.AppName)
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(protocol.PairingCreated{ID: "p1", PollSecret: "sec", Code: "123456",
			ExpiresAt: time.Now().Add(10 * time.Minute).Format(time.RFC3339Nano)})
	})
	mux.HandleFunc("GET /api/v1/pairing/requests/p1", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(protocol.PairingHeader) != "sec" || r.Header.Get("Authorization") != "" {
			w.WriteHeader(404)
			return
		}
		i := int(polls.Add(1)) - 1
		i = min(i, len(states)-1)
		_ = json.NewEncoder(w).Encode(states[i])
	})
	return pki.server(t, mux, true)
}

func TestPairingApproved(t *testing.T) {
	pki := newPKI(t)
	srv := pairingServer(t, pki, []protocol.PairingStatus{
		{State: protocol.PairingPending}, {State: protocol.PairingClaimed, Token: "bub_new"},
	})
	p, err := RequestPairing(context.Background(), srv.URL, pki.caFP, protocol.PairingRequest{AppName: "netdiag"})
	if err != nil {
		t.Fatal(err)
	}
	p.poll = 5 * time.Millisecond
	if p.Code() != "123456" {
		t.Fatalf("code %q", p.Code())
	}
	cfg, err := p.Wait(context.Background())
	if err != nil || cfg.Token != "bub_new" || cfg.CAFingerprint != pki.caFP || cfg.BaseURL != srv.URL {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestPairingTerminalStates(t *testing.T) {
	tests := []struct {
		state string
		want  error
	}{
		{protocol.PairingDenied, ErrPairingDenied},
		{protocol.PairingExpired, ErrPairingExpired},
		{protocol.PairingClaimed, ErrPairingClaimed},
	}
	for _, tc := range tests {
		t.Run(tc.state, func(t *testing.T) {
			pki := newPKI(t)
			srv := pairingServer(t, pki, []protocol.PairingStatus{{State: tc.state}})
			p, err := RequestPairing(context.Background(), srv.URL, pki.caFP, protocol.PairingRequest{AppName: "netdiag"})
			if err != nil {
				t.Fatal(err)
			}
			p.poll = time.Millisecond
			if _, err := p.Wait(context.Background()); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestPairingWaitContext(t *testing.T) {
	pki := newPKI(t)
	srv := pairingServer(t, pki, []protocol.PairingStatus{{State: protocol.PairingPending}})
	p, err := RequestPairing(context.Background(), srv.URL, pki.caFP, protocol.PairingRequest{AppName: "netdiag"})
	if err != nil {
		t.Fatal(err)
	}
	p.poll = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := p.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestPairingWrongPin(t *testing.T) {
	pki := newPKI(t)
	srv := pairingServer(t, pki, nil)
	_, err := RequestPairing(context.Background(), srv.URL, newPKI(t).caFP, protocol.PairingRequest{AppName: "netdiag"})
	if !errors.Is(err, ErrServerIdentityChanged) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchCA(t *testing.T) {
	pki := newPKI(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ca.crt" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/x-x509-ca-cert")
		_, _ = w.Write(pki.caDER)
	}))
	defer srv.Close()
	der, fp, err := FetchCA(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(pki.caDER)
	if string(der) != string(pki.caDER) || fp != "sha256:"+hex.EncodeToString(sum[:]) || fp != pki.caFP {
		t.Fatalf("fp %q", fp)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("junk")) }))
	defer bad.Close()
	if _, _, err := FetchCA(context.Background(), bad.URL); err == nil {
		t.Fatal("accepted junk")
	}
	if _, _, err := FetchCA(context.Background(), "ftp://x"); err == nil {
		t.Fatal("accepted ftp")
	}
}
