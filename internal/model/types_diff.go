package model

// DiffScanRef identifies one side of a scan comparison.
type DiffScanRef struct {
	ID         string `json:"id"`
	CIDR       string `json:"cidr"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
}

// DiffFieldChange is one changed field of a matched host.
type DiffFieldChange struct {
	Field string `json:"field"` // mac|ip|openPorts|printer.state|supply:<desc>|online|web:<port>
	From  string `json:"from"`
	To    string `json:"to"`
}

// DiffHostChange lists the changes of one host present in both scans.
type DiffHostChange struct {
	IP      string            `json:"ip"`
	MAC     string            `json:"mac"`
	Vendor  string            `json:"vendor,omitempty"`
	Changes []DiffFieldChange `json:"changes"`
}

// DiffSummary holds counts of the diff sections.
type DiffSummary struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Changed int `json:"changed"`
}

// Diff is the comparison of a baseline scan against a current scan.
type Diff struct {
	Base    DiffScanRef      `json:"base"`
	Current DiffScanRef      `json:"current"`
	Added   []Host           `json:"added"`
	Removed []Host           `json:"removed"`
	Changed []DiffHostChange `json:"changed"`
	Summary DiffSummary      `json:"summary"`
}
