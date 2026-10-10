package model

// MDNSInfo is what mDNS/DNS-SD browsing learned about one IPv4 address
// (Host.mdns in v0.2). Services are DNS-SD service types without the
// ".local." suffix, e.g. "_ipp._tcp".
type MDNSInfo struct {
	Instance string            `json:"instance"`
	Hostname string            `json:"hostname"`
	Services []string          `json:"services"`
	TXT      map[string]string `json:"txt,omitempty"`
}
