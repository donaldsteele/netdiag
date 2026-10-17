// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0,
// pkg/bubblesclient/connstring.go.
// Modified for netdiag: the package name.

package bubbles

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// DefaultPort is the HTTPS port assumed when a connection string omits it.
const DefaultPort = "28225"

// ParseConnectionString parses bubbles://host:port/?token=bub_...&fp=sha256:<64 hex>.
func ParseConnectionString(s string) (Config, error) {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if err != nil {
		return Config{}, errors.New("bubblesclient: malformed connection string")
	}
	if u.Scheme != "bubbles" {
		return Config{}, errors.New("bubblesclient: connection string must start with bubbles://")
	}
	if u.Hostname() == "" {
		return Config{}, errors.New("bubblesclient: connection string has no host")
	}
	if u.User != nil {
		return Config{}, errors.New("bubblesclient: connection string must not contain user info")
	}
	if !validHost(u.Hostname()) {
		return Config{}, errors.New("bubblesclient: connection string has an invalid host")
	}
	port := u.Port()
	if port == "" {
		port = DefaultPort
	}
	q := u.Query()
	token := q.Get("token")
	if token == "" {
		return Config{}, errors.New("bubblesclient: connection string has no token")
	}
	fp, err := NormalizeFingerprint(q.Get("fp"))
	if err != nil {
		return Config{}, fmt.Errorf("bubblesclient: connection string fp: %w", err)
	}
	return Config{
		BaseURL:       "https://" + net.JoinHostPort(u.Hostname(), port),
		Token:         token,
		CAFingerprint: fp,
	}, nil
}

// NormalizeFingerprint accepts "sha256:<64 hex>", bare hex, or the grouped
// upper-case form the server UI shows, and returns "sha256:<64 lowercase hex>".
func NormalizeFingerprint(s string) (string, error) {
	t := strings.TrimSpace(s)
	if len(t) >= 7 && strings.EqualFold(t[:7], "sha256:") {
		t = t[7:]
	}
	t = strings.NewReplacer(" ", "", ":", "").Replace(t)
	t = strings.ToLower(t)
	if len(t) != 64 {
		return "", errors.New("a SHA-256 fingerprint is 64 hex digits")
	}
	if _, err := hex.DecodeString(t); err != nil {
		return "", errors.New("a SHA-256 fingerprint is 64 hex digits")
	}
	return "sha256:" + t, nil
}

// FormatFingerprint renders a normalized fingerprint in the grouped
// upper-case form shown by the server (groups of four, space separated).
func FormatFingerprint(fp string) string {
	n, err := NormalizeFingerprint(fp)
	if err != nil {
		return fp
	}
	h := strings.ToUpper(n[len("sha256:"):])
	var b strings.Builder
	for i := 0; i < len(h); i += 4 {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(h[i : i+4])
	}
	return b.String()
}

// ConnectionString renders cfg as a bubbles:// URL.
func (c Config) ConnectionString() (string, error) {
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" {
		return "", errors.New("bubblesclient: invalid BaseURL")
	}
	q := url.Values{}
	q.Set("token", c.Token)
	if c.CAFingerprint != "" {
		fp, err := NormalizeFingerprint(c.CAFingerprint)
		if err != nil {
			return "", err
		}
		q.Set("fp", fp)
	}
	return "bubbles://" + u.Host + "/?" + q.Encode(), nil
}

// validHost accepts an IP literal or a DNS-style name.
func validHost(h string) bool {
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	if h == "" || len(h) > 253 {
		return false
	}
	for _, r := range h {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}
