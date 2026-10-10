package model

// Conflict kinds.
const (
	ConflictMACFlap       = "mac_flap"
	ConflictMACMultiIP    = "mac_multi_ip"
	ConflictDuplicateName = "duplicate_name"
)

// Notice triggers.
const (
	TriggerPrinterOffline = "printerOffline"
	TriggerDeviceOffline  = "deviceOffline"
	TriggerSupplyLow      = "supplyLow"
	TriggerPrinterAlert   = "printerAlert"
	TriggerNewDevice      = "newDevice"
	TriggerIPConflict     = "ipConflict"
	TriggerHealthFailed   = "healthFailed"
)

// MACObservation records that a MAC was seen for an IP at a time (RFC3339).
type MACObservation struct {
	MAC string `json:"mac"`
	At  string `json:"at"`
}

// Conflict is a detected IP/name conflict.
type Conflict struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`     // mac_flap|mac_multi_ip|duplicate_name
	Severity  string   `json:"severity"` // warn|fail
	IPs       []string `json:"ips"`
	MACs      []string `json:"macs"`
	Name      string   `json:"name"`
	FirstSeen string   `json:"firstSeen"`
	LastSeen  string   `json:"lastSeen"`
	Detail    string   `json:"detail"`
}

// Notice is an alert emitted by the notify engine.
type Notice struct {
	ID       string `json:"id"`
	Trigger  string `json:"trigger"`
	Severity string `json:"severity"` // info|warn|critical
	IP       string `json:"ip"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	At       string `json:"at"`
	Key      string `json:"key"`
}
