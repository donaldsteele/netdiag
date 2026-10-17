package model

// MDNSInfo is what mDNS/DNS-SD browsing learned about one IPv4 address
// (Host.mdns in v0.2). Services are DNS-SD service types without the
// ".local." suffix, e.g. "_ipp._tcp".
//
// TXT merges the TXT keys of every service the address announced (the
// first service to name a key wins); it is kept for compatibility. Details
// keeps each service apart, with its own port and TXT keys, because keys
// such as "v" or "path" mean different things in different services.
type MDNSInfo struct {
	Instance string            `json:"instance"`
	Hostname string            `json:"hostname"`
	Services []string          `json:"services"`
	TXT      map[string]string `json:"txt,omitempty"`
	Details  []MDNSService     `json:"details,omitempty"`
}

// MDNSService is one DNS-SD service instance an address announced: its
// service type (e.g. "_bubbles._tcp"), instance label, SRV port (0 when no
// SRV record was seen) and TXT keys. Details are sorted by Type, then
// Instance.
type MDNSService struct {
	Type     string            `json:"type"`
	Instance string            `json:"instance"`
	Port     int               `json:"port,omitempty"`
	TXT      map[string]string `json:"txt,omitempty"`
}
