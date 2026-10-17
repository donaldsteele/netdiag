// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0,
// pkg/bubblesclient/helpers_test.go.
// Modified for netdiag: the package name.

package bubbles

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type testPKI struct {
	caDER  []byte
	caFP   string
	chain  tls.Certificate // leaf + CA
	leafFP string
}

func newPKI(t *testing.T) *testPKI { return newPKIFor(t, "127.0.0.1") }

// newPKIFor is newPKI with a leaf whose only SAN is the IP address ip.
func newPKIFor(t *testing.T, ip string) *testPKI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "leaf"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP(ip)},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(caDER)
	lsum := sha256.Sum256(leafDER)
	return &testPKI{
		caDER: caDER, caFP: "sha256:" + hex.EncodeToString(sum[:]), leafFP: "sha256:" + hex.EncodeToString(lsum[:]),
		chain: tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey},
	}
}

// tlsServer starts an HTTPS server presenting pki's chain. sendCA=false
// presents the leaf alone.
func (p *testPKI) server(t *testing.T, h http.Handler, sendCA bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	cert := p.chain
	if !sendCA {
		cert.Certificate = cert.Certificate[:1]
	}
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func newClient(t *testing.T, srv *httptest.Server, fp string) *Client {
	t.Helper()
	c, err := New(Config{BaseURL: srv.URL, Token: "bub_test", CAFingerprint: fp})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
