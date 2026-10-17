// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0,
// pkg/bubblesclient/client.go.
// Modified for netdiag: the package name and comment, the protocol import
// path; the event-stream fields and codeForStatus are left out.

// Package bubbles is netdiag's built-in client for a bubbles team chat
// server: CA-pinned TLS, pairing, alerts posted and resolved by dedupe key,
// channels, messages and the server status. It is a trimmed copy of the
// bubbles v0.2.0 Go SDK (pkg/bubblesclient) and uses the standard library
// only: no queue, no mDNS discovery and no WebSocket. It never uses a proxy
// and never follows redirects.
package bubbles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"netdiag/internal/bubbles/protocol"
)

const (
	apiPrefix        = "/api/v1"
	defaultUserAgent = "bubbles-sdk-go/1"
	maxErrorBody     = 64 << 10
	maxResponseBody  = 32 << 20
)

// Config configures a Client.
type Config struct {
	// BaseURL is the server root, for example https://192.168.1.20:28225.
	BaseURL string
	// Token is the app token (bub_...).
	Token string
	// CAFingerprint pins the server's CA ("sha256:<64 hex>", bare hex or the
	// grouped form shown by the server). When empty, normal system-root
	// verification applies.
	CAFingerprint string
	// UserAgent overrides the default User-Agent.
	UserAgent string
	// HTTPClient is optional; see newHTTPClient for how the pin is applied.
	HTTPClient *http.Client
}

// Client talks to one bubbles server. It is safe for concurrent use.
type Client struct {
	base  *url.URL
	token string
	ua    string
	http  *http.Client
	fp    string
}

// New validates cfg and builds a Client. TLS is verified only against the
// pinned CA fingerprint when one is configured.
func New(cfg Config) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"))
	if err != nil || u.Host == "" {
		return nil, errors.New("bubblesclient: invalid BaseURL")
	}
	if u.Scheme != "https" {
		return nil, errors.New("bubblesclient: BaseURL must use https")
	}
	if u.Path != "" || u.RawQuery != "" {
		return nil, errors.New("bubblesclient: BaseURL must not contain a path or query")
	}
	hc, err := newHTTPClient(cfg)
	if err != nil {
		return nil, err
	}
	ua := cfg.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}
	c := &Client{base: u, token: cfg.Token, ua: ua, http: hc}
	if cfg.CAFingerprint != "" {
		c.fp, _ = NormalizeFingerprint(cfg.CAFingerprint)
	}
	return c, nil
}

// Config returns the settings this client was built from (without the
// custom HTTP client).
func (c *Client) Config() Config {
	return Config{BaseURL: c.base.String(), Token: c.token, CAFingerprint: c.fp, UserAgent: c.ua}
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

// do performs a JSON request. in and out may be nil.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("bubblesclient: encode request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.roundTrip(req, out)
}

func (c *Client) roundTrip(req *http.Request, out any) error {
	res, err := c.http.Do(req)
	if err != nil {
		return wrapTransportErr(err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return readAPIError(res)
	}
	if out == nil || res.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maxResponseBody))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxResponseBody)).Decode(out); err != nil {
		return fmt.Errorf("bubblesclient: decode response: %w", err)
	}
	return nil
}

// wrapTransportErr maps TLS pin failures to ErrServerIdentityChanged and
// every other transport failure to ErrUnavailable. Context errors pass
// through unchanged.
func wrapTransportErr(err error) error {
	switch {
	case errors.Is(err, errPinMismatch):
		return fmt.Errorf("%w: %v", ErrServerIdentityChanged, err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
}

func readAPIError(res *http.Response) error {
	e := &APIError{Status: res.StatusCode, Code: protocol.CodeInternal, Message: http.StatusText(res.StatusCode)}
	var eb protocol.ErrorBody
	b, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
	if json.Unmarshal(b, &eb) == nil && eb.Error.Code != "" {
		e.Code, e.Message, e.Details = eb.Error.Code, eb.Error.Message, eb.Error.Details
	}
	e.RetryAfter = parseRetryAfter(res.Header.Get("Retry-After"), time.Now())
	return e
}

// parseRetryAfter reads delta-seconds or an HTTP date; 0 when absent.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0
		}
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func pathEscape(s string) string { return url.PathEscape(s) }
