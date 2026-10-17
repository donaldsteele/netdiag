// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0, pkg/bubblesclient/pin.go.
// Modified for netdiag: every transport the package builds has Proxy=nil,
// and a caller's *http.Transport is cloned with the proxy cleared even
// without a pin (directTransport).

package bubbles

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// pinnedTLSConfig verifies the server only against the pinned CA: the chain
// presented by the server must contain a certificate whose SHA-256 equals fp,
// and the leaf must verify against that CA (validity, serverAuth, host name).
// Built-in verification is replaced, not skipped: VerifyConnection always
// runs the pin check, so a connection can never succeed without it.
// host is the host the client talks to (BaseURL's host). crypto/tls leaves
// ConnectionState.ServerName empty for IP-literal hosts, so verification falls
// back to it and still checks the leaf's IP SAN.
func pinnedTLSConfig(fp, host string) (*tls.Config, error) {
	norm, err := NormalizeFingerprint(fp)
	if err != nil {
		return nil, err
	}
	want, _ := hex.DecodeString(norm[len("sha256:"):])
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // replaced by VerifyConnection below
		VerifyConnection: func(cs tls.ConnectionState) error {
			name := cs.ServerName
			if name == "" {
				name = host
			}
			return verifyPinned(cs.PeerCertificates, name, want, time.Now())
		},
	}, nil
}

func verifyPinned(chain []*x509.Certificate, serverName string, want []byte, now time.Time) error {
	if len(chain) == 0 {
		return fmt.Errorf("%w: no certificates", errPinMismatch)
	}
	leaf := chain[0]
	for _, c := range chain[1:] {
		sum := sha256.Sum256(c.Raw)
		if !bytes.Equal(sum[:], want) {
			continue
		}
		if !c.IsCA {
			continue
		}
		roots := x509.NewCertPool()
		roots.AddCert(c)
		_, err := leaf.Verify(x509.VerifyOptions{
			Roots:       roots,
			DNSName:     serverName,
			CurrentTime: now,
			KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		if err != nil {
			return fmt.Errorf("%w: leaf does not verify against the pinned CA: %v", errPinMismatch, err)
		}
		return nil
	}
	return errPinMismatch
}

// directTransport clones base (or the default transport) and clears its
// proxy: the chat server is on the local network, and a proxy taken from
// HTTPS_PROXY or the system settings must never see the token.
func directTransport(base *http.Transport) *http.Transport {
	if base == nil {
		base = http.DefaultTransport.(*http.Transport)
	}
	tr := base.Clone()
	tr.Proxy = nil
	return tr
}

// newHTTPClient builds the client used for all requests. A caller-supplied
// client is used as given only when its transport is not an *http.Transport
// and no pin is set (its settings are then the caller's responsibility);
// otherwise its transport is cloned without a proxy and the pin applied.
func newHTTPClient(cfg Config) (*http.Client, error) {
	var tr *http.Transport
	switch {
	case cfg.HTTPClient == nil || cfg.HTTPClient.Transport == nil:
		tr = directTransport(nil)
	default:
		t, ok := cfg.HTTPClient.Transport.(*http.Transport)
		if !ok {
			if cfg.CAFingerprint == "" {
				return cfg.HTTPClient, nil
			}
			return nil, errors.New("bubblesclient: HTTPClient with a pinned CA must use an *http.Transport")
		}
		tr = directTransport(t)
	}
	if cfg.CAFingerprint != "" {
		var host string
		if u, err := url.Parse(cfg.BaseURL); err == nil {
			host = u.Hostname()
		}
		tc, err := pinnedTLSConfig(cfg.CAFingerprint, host)
		if err != nil {
			return nil, fmt.Errorf("bubblesclient: CAFingerprint: %w", err)
		}
		tr.TLSClientConfig = tc
		tr.ForceAttemptHTTP2 = false
	}
	cl := &http.Client{Transport: tr}
	if cfg.HTTPClient != nil {
		cl.Timeout = cfg.HTTPClient.Timeout
		cl.CheckRedirect = cfg.HTTPClient.CheckRedirect
	}
	if cl.CheckRedirect == nil {
		cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return cl, nil
}
