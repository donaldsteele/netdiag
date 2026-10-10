package model

// Health check statuses.
const (
	HealthOK          = "ok"
	HealthWarn        = "warn"
	HealthFail        = "fail"
	HealthInfo        = "info"
	HealthUnavailable = "unavailable"
)

// HealthCheck is the result of one health probe.
type HealthCheck struct {
	ID        string         `json:"id"` // gateway|dhcp|dns|webserver|internet|wifi
	Label     string         `json:"label"`
	Status    string         `json:"status"` // ok|warn|fail|info|unavailable
	Detail    string         `json:"detail"`
	LatencyMs float64        `json:"latencyMs"`
	Data      map[string]any `json:"data,omitempty"`
}

// HealthReport is the set of health checks at one moment.
type HealthReport struct {
	CheckedAt string        `json:"checkedAt"`
	Checks    []HealthCheck `json:"checks"`
}
