package model

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestRedactScan(t *testing.T) {
	const secret = "s3cret-community"
	in := ScanResult{ID: "x", Request: ScanRequest{CIDR: "10.0.0.0/24", Community: secret, Ports: []int{80, 443}}}
	out := RedactScan(in)
	if in.Request.Community != secret {
		t.Fatal("input mutated")
	}
	if out.Request.Community != Redacted || out.Request.CIDR != "10.0.0.0/24" || !slices.Equal(out.Request.Ports, []int{80, 443}) {
		t.Fatalf("redacted = %+v", out.Request)
	}
	out.Request.Ports[0] = 1
	if in.Request.Ports[0] != 80 {
		t.Fatal("ports slice shared with the input")
	}
	if b, _ := json.Marshal(RedactScan(in)); strings.Contains(string(b), secret) {
		t.Fatalf("secret in %s", b)
	}
	if again := RedactScan(RedactScan(in)); again.Request.Community != Redacted {
		t.Fatalf("not idempotent: %q", again.Request.Community)
	}
	if got := RedactScan(ScanResult{}); got.Request.Community != "" || got.Request.Ports != nil {
		t.Fatalf("empty request changed: %+v", got.Request)
	}
}

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                                 "",
		"https://intranet.local/":          "https://intranet.local/",
		"http://admin:pa55@10.0.0.5/login": "http://admin:xxxxx@10.0.0.5/login",
		"http://admin@10.0.0.5/":           "http://admin@10.0.0.5/",
		"http://admin:@10.0.0.5/":          "http://admin:xxxxx@10.0.0.5/",
		// Does not parse (bad escape): the password is still cut out.
		"http://admin:pa55@10.0.0.5/%zz":  "http://admin:xxxxx@10.0.0.5/%zz",
		"http://admin:p@ss@10.0.0.5:8%zz": "http://admin:xxxxx@10.0.0.5:8%zz",
		"http://10.0.0.5/a@b%zz":          "http://10.0.0.5/a@b%zz",
	} {
		got := RedactURL(in)
		if got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
		if strings.Contains(got, "pa55") || strings.Contains(got, "p@ss") {
			t.Errorf("RedactURL(%q) = %q keeps the password", in, got)
		}
		if again := RedactURL(got); again != got {
			t.Errorf("RedactURL not idempotent on %q: %q", got, again)
		}
	}
}
