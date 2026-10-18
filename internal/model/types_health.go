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
	// Advice says what a result that is not ok means and what to try
	// (internal/advice); nil when there is nothing to say.
	Advice *Advice `json:"advice,omitempty"`
}

// healthLabels are the plain names of the health checks, in the order
// netdiag runs them. The Health panel, the Problems right now card, the
// alerts and team chat all name a check by these words, so a failed router
// check never reads as "Gateway reachable".
var healthLabels = []struct{ id, label string }{
	{"gateway", "Router"},
	{"dhcp", "Getting an address (DHCP)"},
	{"dns", "Name lookup (DNS)"},
	{"webserver", "Web server"},
	{"internet", "Internet"},
	{"wifi", "Wi-Fi"},
}

// HealthLabel is the plain name of the health check id ("Router" for
// gateway), or "" for an id netdiag has no check of.
func HealthLabel(id string) string {
	for _, c := range healthLabels {
		if c.id == id {
			return c.label
		}
	}
	return ""
}

// Name is what a notice or the card calls c: the plain name of its check,
// so a report saved by an older netdiag reads the same, else its label,
// else its id.
func (c HealthCheck) Name() string {
	if l := HealthLabel(c.ID); l != "" {
		return l
	}
	if c.Label != "" {
		return c.Label
	}
	return c.ID
}

// HealthReport is the set of health checks at one moment.
type HealthReport struct {
	CheckedAt string        `json:"checkedAt"`
	Checks    []HealthCheck `json:"checks"`
	// Network is the id (NetworkID) of the network the checks looked at,
	// or empty when netdiag could not tell (no router MAC). The "This
	// network has no internet" switch is set for this network.
	Network string `json:"network,omitempty"`
}

// Health check reasons, in Data["reason"]. Every result that is not ok
// carries one, and so does a gateway that answers only on its web ports.
// The advice catalogue keys on check id and reason, never on Detail text.
const (
	// Any check.
	ReasonTimeout = "timeout" // the check did not finish in time
	ReasonError   = "error"   // the check could not run (a local error)

	// gateway
	ReasonNoReply   = "no_reply"   // also dns: no reply at all
	ReasonTCPOnly   = "tcp_only"   // no ping reply, but its web ports answer (status ok); Data["pingError"] when ping could not run here
	ReasonSlow      = "slow"       // also dns and webserver
	ReasonNoGateway = "no_gateway" // no router known for the adapter
	ReasonPingError = "ping_error" // ping could not run on this computer

	// dhcp
	ReasonAPIPA   = "apipa"    // self-assigned 169.254 address
	ReasonNoLease = "no_lease" // a fixed address, set by hand

	// dns
	ReasonLocalMismatch = "local_mismatch" // the name server answers, this computer asks elsewhere
	ReasonNXDomain      = "nxdomain"       // the name server says the name does not exist
	ReasonNoInternet    = "no_internet"    // the name server answers but cannot reach the internet
	ReasonNoServer      = "no_server"      // no name server set

	// webserver
	ReasonNotSet   = "not_set"  // no web server address in Settings
	ReasonStatus   = "status"   // also internet: an unexpected HTTP status
	ReasonContent  = "content"  // the page lacks the expected text
	ReasonRequest  = "request"  // no answer
	ReasonTLS      = "tls"      // the certificate is not trusted
	ReasonRedirect = "redirect" // it sends visitors to another address
	ReasonInvalid  = "invalid"  // also internet: the address in Settings is not valid
	ReasonExpired  = "expired"  // the certificate the user trusted has expired

	// internet
	ReasonPortal     = "portal"      // a sign-in page (redirect)
	ReasonNoResponse = "no_response" // no answer

	// wifi
	ReasonWeak           = "weak"            // weak signal
	ReasonNotConnected   = "not_connected"   // not on Wi-Fi
	ReasonHiddenLocation = "hidden_location" // Windows hides Wi-Fi details until Location is on
)

// HealthReason returns c's Data["reason"], or "".
func HealthReason(c HealthCheck) string {
	s, _ := c.Data["reason"].(string)
	return s
}
