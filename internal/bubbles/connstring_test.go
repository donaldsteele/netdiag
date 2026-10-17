// Copyright 2026 The bubbles Authors
// SPDX-License-Identifier: Apache-2.0
//
// Copied from github.com/donaldsteele/bubbles v0.2.0,
// pkg/bubblesclient/connstring_test.go.
// Modified for netdiag: the package name.

package bubbles

import (
	"strings"
	"testing"
)

const goodFP = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestParseConnectionString(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Config
		bad  bool
	}{
		{"full", "bubbles://192.168.1.20:28225/?token=bub_abc&fp=sha256:" + goodFP,
			Config{BaseURL: "https://192.168.1.20:28225", Token: "bub_abc", CAFingerprint: "sha256:" + goodFP}, false},
		{"default port, upper fp", "bubbles://bubbles.local/?token=t&fp=sha256:" + strings.ToUpper(goodFP),
			Config{BaseURL: "https://bubbles.local:28225", Token: "t", CAFingerprint: "sha256:" + goodFP}, false},
		{"ipv6", "bubbles://[fd00::1]:9000/?token=t&fp=" + goodFP,
			Config{BaseURL: "https://[fd00::1]:9000", Token: "t", CAFingerprint: "sha256:" + goodFP}, false},
		{"wrong scheme", "https://h/?token=t&fp=" + goodFP, Config{}, true},
		{"no host", "bubbles:///?token=t&fp=" + goodFP, Config{}, true},
		{"no token", "bubbles://h/?fp=" + goodFP, Config{}, true},
		{"no fp", "bubbles://h/?token=t", Config{}, true},
		{"short fp", "bubbles://h/?token=t&fp=sha256:abcd", Config{}, true},
		{"non-hex fp", "bubbles://h/?token=t&fp=sha256:" + strings.Repeat("zz", 32), Config{}, true},
		{"userinfo", "bubbles://u:p@h/?token=t&fp=" + goodFP, Config{}, true},
		{"empty", "", Config{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseConnectionString(tc.in)
			if tc.bad {
				if err == nil {
					t.Fatalf("accepted %+v", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v err %v want %+v", got, err, tc.want)
			}
		})
	}
}

func TestConnectionStringRoundTrip(t *testing.T) {
	cfg := Config{BaseURL: "https://10.0.0.5:28225", Token: "bub_x y", CAFingerprint: goodFP}
	s, err := cfg.ConnectionString()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseConnectionString(s)
	if err != nil || got.Token != cfg.Token || got.BaseURL != cfg.BaseURL || got.CAFingerprint != "sha256:"+goodFP {
		t.Fatalf("got %+v err %v from %s", got, err, s)
	}
}

func TestFormatFingerprint(t *testing.T) {
	got := FormatFingerprint("sha256:" + goodFP)
	if !strings.HasPrefix(got, "0123 4567 89AB CDEF 0123") || len(got) != 79 {
		t.Fatalf("got %q", got)
	}
	if n, err := NormalizeFingerprint(got); err != nil || n != "sha256:"+goodFP {
		t.Fatalf("round trip %q %v", n, err)
	}
}

func FuzzParseConnectionString(f *testing.F) {
	f.Add("bubbles://192.168.1.20:28225/?token=bub_abc&fp=sha256:" + goodFP)
	f.Add("bubbles://[fd00::1]/?token=t&fp=" + goodFP)
	f.Add("bubbles://h")
	f.Add("")
	f.Add("bubbles://%zz/?token=&fp=")
	f.Fuzz(func(t *testing.T, s string) {
		cfg, err := ParseConnectionString(s)
		if err != nil {
			return
		}
		if cfg.Token == "" || !strings.HasPrefix(cfg.BaseURL, "https://") {
			t.Fatalf("accepted bad config %+v from %q", cfg, s)
		}
		if _, err := NormalizeFingerprint(cfg.CAFingerprint); err != nil {
			t.Fatalf("accepted bad fp %q", cfg.CAFingerprint)
		}
		if _, err := New(cfg); err != nil {
			t.Fatalf("parsed config rejected by New: %v (%q)", err, s)
		}
	})
}
