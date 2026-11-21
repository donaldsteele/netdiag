package model

// Tailscale states (TailnetView.State).
const (
	TailnetNotInstalled = "not-installed"
	TailnetStopped      = "stopped"
	TailnetNeedsLogin   = "needs-login"
	TailnetRunning      = "running"
)

// Paths traffic takes to a tailnet device (TailnetDevice.Path).
const (
	TailnetPathDirect  = "direct"
	TailnetPathRelayed = "relayed"
	TailnetPathIdle    = "idle"
	TailnetPathOffline = "offline"
)

// TailnetAdminTarget is the Tailscale admin console's machines page, sent
// to the page so app.js holds no URL (the static rules ban URL literals).
const TailnetAdminTarget = "https://login.tailscale.com/admin/machines"

// TailnetKeySoonDays is how many days before a key expires netdiag warns.
const TailnetKeySoonDays = 7

// TailnetDevice is one device on this computer's tailnet (v0.6.1).
type TailnetDevice struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	DNSName        string   `json:"dnsName,omitempty"`
	OS             string   `json:"os,omitempty"`
	IPs            []string `json:"ips"`
	Online         bool     `json:"online"`
	Active         bool     `json:"active"`
	LastSeen       string   `json:"lastSeen,omitempty"` // RFC 3339, when it was last online
	Path           string   `json:"path"`               // direct | relayed | idle | offline
	Endpoint       string   `json:"endpoint,omitempty"`
	Relay          string   `json:"relay,omitempty"`
	KeyExpiry      string   `json:"keyExpiry,omitempty"` // RFC 3339; "" when it never expires
	KeyExpiresSoon bool     `json:"keyExpiresSoon"`
	ExitNode       bool     `json:"exitNode"`
	ExitNodeOption bool     `json:"exitNodeOption"`
	SubnetRoutes   []string `json:"subnetRoutes"`
	Tags           []string `json:"tags"`
	// Phone marks iOS and Android devices, which sleep often and are never
	// alerted as offline.
	Phone bool `json:"phone"`
}

// TailnetView is GET /api/tailnet: this computer's Tailscale state, its
// tailnet's devices and the Tailscale card of the Health view. The
// tailnet is named by its MagicDNS suffix only: a personal tailnet's own
// name is its owner's email address, which netdiag never shows or sends.
type TailnetView struct {
	State       string          `json:"state"`
	Version     string          `json:"version,omitempty"`
	Suffix      string          `json:"suffix"`
	MagicDNS    bool            `json:"magicDNS"`
	Self        *TailnetDevice  `json:"self"`
	Devices     []TailnetDevice `json:"devices"`
	Health      []string        `json:"health"`
	Card        HealthCheck     `json:"card"`
	CheckedAt   string          `json:"checkedAt"`
	AdminTarget string          `json:"adminTarget"`
	// Error says why the Tailscale service could not be read, when it is
	// there but answered with an error.
	Error string `json:"error,omitempty"`
}

// TailnetPing is POST /api/tailnet/ping: a Tailscale ping to one device.
type TailnetPing struct {
	IP        string  `json:"ip"`
	Name      string  `json:"name,omitempty"`
	OK        bool    `json:"ok"`
	Error     string  `json:"error,omitempty"`
	LatencyMs float64 `json:"latencyMs,omitempty"`
	Path      string  `json:"path,omitempty"`
	Endpoint  string  `json:"endpoint,omitempty"`
	Relay     string  `json:"relay,omitempty"`
}

// TailnetSnapshot is what a live scan last read of the tailnet, kept on
// the scan result so the alerts, the Problems right now card and Claude
// see the same thing (v0.6.1). Nil when Tailscale isn't on this computer.
type TailnetSnapshot struct {
	State     string          `json:"state"`
	Suffix    string          `json:"suffix,omitempty"`
	Self      *TailnetDevice  `json:"self,omitempty"`
	Devices   []TailnetDevice `json:"devices"`
	CheckedAt string          `json:"checkedAt"`
}
