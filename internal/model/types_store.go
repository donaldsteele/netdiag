package model

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
}

// SettingsMCP controls the HTTP MCP endpoint (agent access). Off by default.
type SettingsMCP struct {
	Enabled bool `json:"enabled"`
}

// SettingsWebServer configures the optional web server health check.
type SettingsWebServer struct {
	URL          string `json:"url"`
	MustContain  string `json:"mustContain"`
	ExpectStatus int    `json:"expectStatus"`
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
