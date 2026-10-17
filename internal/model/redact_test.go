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
