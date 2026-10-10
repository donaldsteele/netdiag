// Package mcpserver holds the MCP-facing data shaping helpers. The functions
// in this package are pure and do not depend on the MCP SDK.
package mcpserver

import (
	"netdiag/internal/model"
	"netdiag/internal/service"
)

// Redacted replaces secret values in anything handed to an agent.
const Redacted = "***"

// DefaultTextCap is the default byte cap for text content.
const DefaultTextCap = 25000

// RedactScan returns a copy of r with secrets hidden. The input is not
// modified.
func RedactScan(r model.ScanResult) model.ScanResult {
	if r.Request.Community != "" {
		r.Request.Community = Redacted
	}
	if r.Request.Ports != nil {
		r.Request.Ports = append([]int(nil), r.Request.Ports...)
	}
	return r
}

// RedactSettings returns a copy of s with the SNMP community hidden.
func RedactSettings(s model.Settings) model.Settings {
	return service.RedactSettings(s)
}

// CapText truncates s to at most max bytes (on a rune boundary), appending a
// hint when truncated. max <= 0 uses DefaultTextCap.
func CapText(s string, max int) string {
	if max <= 0 {
		max = DefaultTextCap
	}
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "\n…truncated; use cursor/limit"
}
