package model

import "strings"

// SettingsSchemaVersion is the current settings.json schema version.
const SettingsSchemaVersion = 1

// HistorySchemaVersion is the schemaVersion written into saved scans.
const HistorySchemaVersion = 2

// Settings is the persisted user configuration (settings.json).
type Settings struct {
	SchemaVersion int    `json:"schemaVersion"`
	Community     string `json:"community"`
	// PrinterStatusPollSec is the live-mode printer status poll interval
	// (2-300 s; 0 or missing means the default 5).
	PrinterStatusPollSec int               `json:"printerStatusPollSec"`
	WebServer            SettingsWebServer `json:"webServer"`
	InternetCheckURL     string            `json:"internetCheckURL"`
	Alerts               SettingsAlerts    `json:"alerts"`
	IgnoredConflictMACs  []string          `json:"ignoredConflictMACs"`
	MCP                  SettingsMCP       `json:"mcp"`
	Chat                 SettingsChat      `json:"chat"`
}

// SettingsMCP controls the HTTP MCP endpoint (agent access). Off by default.
type SettingsMCP struct {
	Enabled bool `json:"enabled"`
}

// Team chat (bubbles) "Send:" levels, lowest first. A fault goes to chat
// when its severity (the severity map) is at least MinSeverity.
const (
	ChatSeverityInfo     = "info"     // everything, including notes
	ChatSeverityWarning  = "warning"  // problems and warnings (the default)
	ChatSeverityCritical = "critical" // only urgent problems
)

// SettingsChat controls sending alerts to team chat (bubbles). Off by
// default; the connection itself (server, channel, token) lives in
// bubbles.json, never in settings.
type SettingsChat struct {
	Enabled bool `json:"enabled"`
	// MinSeverity is the lowest severity sent: info, warning or critical.
	MinSeverity string `json:"minSeverity"`
}

// ValidChatSeverity reports whether s is one of the chat Send levels.
func ValidChatSeverity(s string) bool {
	return s == ChatSeverityInfo || s == ChatSeverityWarning || s == ChatSeverityCritical
}

// SettingsWebServer configures the optional web server health check.
type SettingsWebServer struct {
	URL          string `json:"url"`
	MustContain  string `json:"mustContain"`
	ExpectStatus int    `json:"expectStatus"`
	// TrustedCert is the SHA-256 fingerprint (64 lowercase hex digits) of
	// a certificate the user chose to trust for this server, such as a
	// self-signed one on an intranet server. Empty means normal checking.
	TrustedCert string `json:"trustedCert"`
}

// NormalizeFingerprint returns a SHA-256 certificate fingerprint as 64
// lowercase hex digits. Upper case and colons or spaces between the bytes
// are accepted; ok is false when s is not a SHA-256 fingerprint.
func NormalizeFingerprint(s string) (fp string, ok bool) {
	s = strings.ToLower(strings.NewReplacer(":", "", " ", "").Replace(strings.TrimSpace(s)))
	if len(s) != 64 {
		return "", false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}
	return s, true
}

// SettingsAlerts configures alerting.
type SettingsAlerts struct {
	Sound              bool             `json:"sound"`
	Desktop            bool             `json:"desktop"`
	CooldownSec        int              `json:"cooldownSec"`
	SupplyThresholdPct int              `json:"supplyThresholdPct"`
	Triggers           SettingsTriggers `json:"triggers"`
}

// SettingsTriggers enables individual alert triggers.
type SettingsTriggers struct {
	PrinterOffline bool `json:"printerOffline"`
	DeviceOffline  bool `json:"deviceOffline"`
	SupplyLow      bool `json:"supplyLow"`
	PrinterAlert   bool `json:"printerAlert"`
	NewDevice      bool `json:"newDevice"`
	IPConflict     bool `json:"ipConflict"`
	HealthFailed   bool `json:"healthFailed"`
}

// HistoryEntry is the index record of one saved scan.
type HistoryEntry struct {
	ID         string `json:"id"`
	CIDR       string `json:"cidr"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
	State      string `json:"state"`
	Live       bool   `json:"live"`
	Stats      Stats  `json:"stats"`
	// Error is "unreadable" for a corrupt history file; other fields are then empty.
	Error string `json:"error,omitempty"`
}
