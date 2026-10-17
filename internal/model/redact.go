package model

import "slices"

// Redacted is shown in place of a secret, such as the SNMP community, in
// anything that leaves netdiag: API responses, the live event stream,
// exports, saved history and MCP output.
const Redacted = "***"

// RedactScan returns a copy of r with the SNMP community hidden. The input is
// not modified (the request's port list is copied too), an empty community
// stays empty, and redacting twice gives the same result.
//
// Scans keep the real community in memory while they run; only the copies
// handed out are redacted. A rescan takes its community from the scan form
// or the settings, never from an old result.
func RedactScan(r ScanResult) ScanResult {
	if r.Request.Community != "" {
		r.Request.Community = Redacted
	}
	if r.Request.Ports != nil {
		r.Request.Ports = slices.Clone(r.Request.Ports)
	}
	return r
}
