// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0,
// pkg/bubblesclient/pairing.go, plus sleepCtx from
// pkg/bubblesclient/events.go.
// Modified for netdiag: the package name and the protocol import path,
// FetchCA's client uses a transport without a proxy, and SetPollInterval
// lets callers' tests poll quickly.

package bubbles

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"netdiag/internal/bubbles/protocol"
)

const maxCABytes = 64 << 10

// FetchCA downloads the CA certificate from the plain-HTTP onboarding
// listener (GET /ca.crt) and returns its DER and its fingerprint
// ("sha256:<64 hex>"). The download itself is unauthenticated: show
// FormatFingerprint(fingerprint) to the user, who must compare it with the
// server's admin UI before trusting it.
func FetchCA(ctx context.Context, onboardingURL string) (der []byte, fingerprint string, err error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(onboardingURL), "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, "", errors.New("bubblesclient: onboarding URL must be http(s)://host:port")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String()+"/ca.crt", nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", defaultUserAgent)
	res, err := fetchCAClient().Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("bubblesclient: fetch CA: HTTP %d", res.StatusCode)
	}
	der, err = io.ReadAll(io.LimitReader(res.Body, maxCABytes))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil || !cert.IsCA {
		return nil, "", errors.New("bubblesclient: the server did not return a CA certificate")
	}
	sum := sha256.Sum256(der)
	return der, "sha256:" + hex.EncodeToString(sum[:]), nil
}

// fetchCAClient is the client FetchCA uses: no proxy, no redirects.
func fetchCAClient() *http.Client {
	return &http.Client{
		Transport:     directTransport(nil),
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Pairing is a pending pairing request.
type Pairing struct {
	c       *Client
	id      string
	secret  string
	code    string
	expires time.Time
	cfg     Config
	poll    time.Duration // initial poll interval (tests)
}

// RequestPairing asks the admin for a token. serverURL is the HTTPS base
// URL; caFingerprint pins the CA (use the fingerprint the user confirmed
// after FetchCA). Show Code() to the user, then call Wait.
func RequestPairing(ctx context.Context, serverURL, caFingerprint string, req protocol.PairingRequest) (*Pairing, error) {
	cfg := Config{BaseURL: serverURL, CAFingerprint: caFingerprint}
	c, err := New(cfg)
	if err != nil {
		return nil, err
	}
	var created protocol.PairingCreated
	if err := c.do(ctx, http.MethodPost, apiPrefix+"/pairing/requests", req, &created); err != nil {
		return nil, err
	}
	p := &Pairing{c: c, id: created.ID, secret: created.PollSecret, code: created.Code, cfg: c.Config(), poll: time.Second}
	if t, err := time.Parse(time.RFC3339Nano, created.ExpiresAt); err == nil {
		p.expires = t
	}
	return p, nil
}

// Code is the short code to show the user; the admin compares it.
func (p *Pairing) Code() string { return p.code }

// ExpiresAt is when the request lapses (zero if unknown).
func (p *Pairing) ExpiresAt() time.Time { return p.expires }

// SetPollInterval sets how long Wait waits before each poll while the
// request is pending (1 s by default; d <= 0 keeps it). Call it before
// Wait. Tests use it to pair in milliseconds.
func (p *Pairing) SetPollInterval(d time.Duration) {
	if d > 0 {
		p.poll = d
	}
}

// Wait polls with backoff until the request is approved, denied or expired,
// or ctx ends. On approval it returns a Config carrying the token and the
// pinned fingerprint; the server hands the token out exactly once.
func (p *Pairing) Wait(ctx context.Context) (Config, error) {
	wait := p.poll
	for {
		if !sleepCtx(ctx, wait) {
			return Config{}, ctx.Err()
		}
		var st protocol.PairingStatus
		err := p.pollOnce(ctx, &st)
		switch {
		case err == nil:
			// The server mints the token on the first poll after approval and
			// reports it once, together with state "claimed".
			if st.Token != "" {
				cfg := p.cfg
				cfg.Token = st.Token
				return cfg, nil
			}
			switch st.State {
			case protocol.PairingDenied:
				return Config{}, ErrPairingDenied
			case protocol.PairingExpired:
				return Config{}, ErrPairingExpired
			case protocol.PairingClaimed:
				return Config{}, ErrPairingClaimed
			}
			wait = p.poll
			continue
		case ctx.Err() != nil:
			return Config{}, ctx.Err()
		case errors.Is(err, ErrServerIdentityChanged):
			return Config{}, err
		case retryable(err):
			var ae *APIError
			wait = min(wait*2, 10*time.Second)
			if errors.As(err, &ae) && ae.RetryAfter > 0 {
				wait = ae.RetryAfter
			}
		default:
			return Config{}, err
		}
		if !p.expires.IsZero() && time.Now().After(p.expires.Add(time.Minute)) {
			return Config{}, ErrPairingExpired
		}
	}
}

func (p *Pairing) pollOnce(ctx context.Context, out *protocol.PairingStatus) error {
	req, err := p.c.newRequest(ctx, http.MethodGet, apiPrefix+"/pairing/requests/"+pathEscape(p.id), nil)
	if err != nil {
		return err
	}
	req.Header.Del("Authorization")
	req.Header.Set(protocol.PairingHeader, p.secret)
	return p.c.roundTrip(req, out)
}

// sleepCtx waits d or until ctx ends; it reports whether the full wait
// passed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
