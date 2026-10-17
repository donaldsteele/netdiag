package model

import (
	"net/url"
	"slices"
	"strings"
)

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

// urlPasswordMask is what (*url.URL).Redacted writes in place of a password.
const urlPasswordMask = "xxxxx"

// RedactURL hides the password of a user name and password written in a
// URL (http://user:pass@host/ becomes http://user:xxxxx@host/), the way
// (*url.URL).Redacted does. A URL that does not parse has its password
// cut out by hand, so an invalid address never shows it either. A URL
// without a password is returned as it is.
func RedactURL(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Redacted()
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return raw
	}
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	at := strings.LastIndex(rest[:end], "@")
	if at < 0 {
		return raw
	}
	user, _, hasPass := strings.Cut(rest[:at], ":")
	if !hasPass {
		return raw
	}
	return scheme + "://" + user + ":" + urlPasswordMask + rest[at:]
}
