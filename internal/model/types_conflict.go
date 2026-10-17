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
	// TriggerNetdiagLink is the storm guard's "netdiag lost the network"
	// notice. It is local only and never sent to team chat (v0.6-D16).
	TriggerNetdiagLink = "netdiagLink"
	// TriggerChatTest is the team chat test alert. It is chat only.
	TriggerChatTest = "netdiag:test"
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
	// Advice says what the conflict means and what to try.
	Advice *Advice `json:"advice,omitempty"`
}

// Notice is an alert emitted by the notify engine. A fault opens a problem
// under Key; the OK notice that closes it (OK true) carries the same Key and
// the fault's time in OpenedAt.
type Notice struct {
	ID       string `json:"id"`
	Trigger  string `json:"trigger"`
	Severity string `json:"severity"` // info|warn|critical
	IP       string `json:"ip"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	At       string `json:"at"`
	Key      string `json:"key"`
	// OK marks the notice that says the problem under Key is fixed.
	OK bool `json:"ok,omitempty"`
	// OpenedAt is when the problem under Key was first reported (RFC 3339).
	OpenedAt string `json:"openedAt,omitempty"`
	// MAC is the device's MAC address, when known.
	MAC string `json:"mac,omitempty"`
	// Name is the best name netdiag has for the device (empty: use IP).
	Name string `json:"name,omitempty"`
	// Code is the printer alert's code (printerAlert faults only), such as
	// hrPrinterDetectedErrorState.doorOpen or media-jam-error.
	Code string `json:"code,omitempty"`
	// Detail names what the fault is about: the printer alert's or the
	// low supply's description (printerAlert and supplyLow faults).
	Detail string `json:"detail,omitempty"`
	// Advice says what the fault means and what to try (nil on OK notices).
	Advice *Advice `json:"advice,omitempty"`
}
