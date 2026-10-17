// Package model defines the shared data types exchanged between scan stages,
// the HTTP server and exporters.
package model

// DefaultPorts is the fixed set of TCP ports probed on every host: web,
// printing, then remote access (SSH, Remote Desktop, VNC screen sharing).
var DefaultPorts = []int{80, 443, 8080, 8443, 9100, 631, 515, 22, 3389, 5900}

// Remote access ports in DefaultPorts. A connection to one of them can show
// up in that device's own logs, so live presence checks avoid them.
const (
	PortSSH = 22
	PortRDP = 3389
	PortVNC = 5900
)

// IsRemotePort reports whether p is one of the remote access ports.
func IsRemotePort(p int) bool { return p == PortSSH || p == PortRDP || p == PortVNC }

// DefaultCommunity is the SNMP community used when none is supplied.
const DefaultCommunity = "public"

// ScanRequest describes a scan to run.
type ScanRequest struct {
	CIDR      string `json:"cidr"`
	Interface string `json:"interface"`
	Community string `json:"community"`
	Ports     []int  `json:"ports"`
	// Live keeps watching the range for new devices after the initial scan.
	Live bool `json:"live,omitempty"`
	// LiveIntervalSec is the pause between live checks (default 15, 5..300).
	LiveIntervalSec int `json:"liveIntervalSec,omitempty"`
}

// Live mode interval limits, in seconds.
const (
	DefaultLiveIntervalSec = 15
	MinLiveIntervalSec     = 5
	MaxLiveIntervalSec     = 300
)

// Host is the aggregated state for one IP address.
type Host struct {
	IP            string       `json:"ip"`
	IsSelf        bool         `json:"isSelf"`
	Alive         bool         `json:"alive"`
	FoundBy       []string     `json:"foundBy"`
	RttMs         float64      `json:"rttMs"`
	MAC           string       `json:"mac"`
	Vendor        string       `json:"vendor"`
	Hostname      string       `json:"hostname"`
	OpenPorts     []int        `json:"openPorts"`
	Web           []WebService `json:"web"`
	Snmp          *SnmpInfo    `json:"snmp"`
	IsPrinter     bool         `json:"isPrinter"`
	PrinterReason string       `json:"printerReason"`
	// DeviceType is the classified device category (see internal/devtype).
	DeviceType string `json:"deviceType,omitempty"`
	// DeviceTypeReason names the signal that decided DeviceType.
	DeviceTypeReason string       `json:"deviceTypeReason,omitempty"`
	Printer          *PrinterInfo `json:"printer"`
	Errors           []string     `json:"errors"`
	UpdatedAt        string       `json:"updatedAt"`
	// FirstSeen is when the host was first discovered (RFC 3339).
	FirstSeen string `json:"firstSeen,omitempty"`
	// IsNew is true for hosts discovered by live mode after the initial scan.
	IsNew bool `json:"isNew,omitempty"`
	// Online is true while the host answers presence checks (always true at
	// discovery; live mode flips it after OfflineAfterMisses missed checks).
	Online bool `json:"online"`
	// LastSeen is when the host last answered (discovery or presence check).
	LastSeen string `json:"lastSeen,omitempty"`
	// OfflineSince is when the host was marked offline (RFC 3339).
	OfflineSince string `json:"offlineSince,omitempty"`
	// MissedChecks counts consecutive failed presence checks.
	MissedChecks int `json:"missedChecks,omitempty"`
	// MDNS is the Bonjour/mDNS record for this host (nil when none).
	MDNS *MDNSInfo `json:"mdns"`
	// Conflicts lists ids into ScanResult.Conflicts that involve this host.
	Conflicts []string `json:"conflicts,omitempty"`
	// MACHistory holds the last 8 MAC observations (live mode only).
	MACHistory []MACObservation `json:"macHistory,omitempty"`
	// Advice is set for a printer that does not report its status, so
	// netdiag can only see whether it answers (internal/advice).
	Advice *Advice `json:"advice,omitempty"`
}

// OfflineAfterMisses is the number of consecutive failed presence checks
// after which a host is reported offline.
const OfflineAfterMisses = 2

// WebService is the fingerprint of an HTTP(S) endpoint.
type WebService struct {
	Port   int      `json:"port"`
	Scheme string   `json:"scheme"`
	URL    string   `json:"url"`
	Status int      `json:"status"`
	Title  string   `json:"title"`
	Server string   `json:"server"`
	TLS    *TLSInfo `json:"tls"`
	Error  string   `json:"error"`
}

// TLSInfo summarises a server certificate.
type TLSInfo struct {
	Subject    string `json:"subject"`
	Issuer     string `json:"issuer"`
	NotAfter   string `json:"notAfter"`
	SelfSigned bool   `json:"selfSigned"`
	Expired    bool   `json:"expired"`
}

// SnmpInfo holds decoded SNMP system and host-resources data.
type SnmpInfo struct {
	Version                     string   `json:"version"`
	SysDescr                    string   `json:"sysDescr"`
	SysObjectID                 string   `json:"sysObjectID"`
	SysName                     string   `json:"sysName"`
	SysLocation                 string   `json:"sysLocation"`
	SysContact                  string   `json:"sysContact"`
	SysUpTime                   string   `json:"sysUpTime"`
	HrDeviceDescr               string   `json:"hrDeviceDescr"`
	HrDeviceStatus              string   `json:"hrDeviceStatus"`
	HrPrinterStatus             string   `json:"hrPrinterStatus"`
	HrPrinterDetectedErrorState []string `json:"hrPrinterDetectedErrorState"`
}

// PrinterInfo holds Printer-MIB derived data.
type PrinterInfo struct {
	Model          string      `json:"model"`
	Serial         string      `json:"serial"`
	PageCount      int64       `json:"pageCount"`
	ConsoleDisplay []string    `json:"consoleDisplay"`
	Supplies       []Supply    `json:"supplies"`
	InputTrays     []InputTray `json:"inputTrays"`
	Alerts         []Alert     `json:"alerts"`
	Covers         []Cover     `json:"covers"`
	// Source is snmp, ipp or snmp+ipp.
	Source string `json:"source,omitempty"`
	// State is the IPP printer-state: idle|processing|stopped.
	State string `json:"state,omitempty"`
	// StateReasons are IPP printer-state-reasons (minus none).
	StateReasons []string `json:"stateReasons,omitempty"`
}

// Supply is a consumable such as toner or ink.
type Supply struct {
	Description string `json:"description"`
	Type        string `json:"type"`
	Color       string `json:"color"`
	Level       int    `json:"level"`
	Max         int    `json:"max"`
	Percent     int    `json:"percent"`
	Unit        string `json:"unit"`
	State       string `json:"state"` // ok|low|empty|unknown
}

// InputTray is a paper tray.
type InputTray struct {
	Name      string `json:"name"`
	MediaName string `json:"mediaName"`
	Level     int    `json:"level"`
	Max       int    `json:"max"`
	Status    string `json:"status"`
}

// Alert is a Printer-MIB alert table entry.
type Alert struct {
	Severity    string `json:"severity"`
	Code        string `json:"code"`
	Description string `json:"description"`
}

// Cover is a printer cover/door state.
type Cover struct {
	Description string `json:"description"`
	Status      string `json:"status"`
}

// SelfInfo identifies the machine running the scan.
type SelfInfo struct {
	IP        string `json:"ip"`
	MAC       string `json:"mac"`
	Interface string `json:"interface"`
	// CIDR is the interface's network, its address prefix (such as
	// 192.168.1.0/24), when known: part of the network's id (NetworkID).
	CIDR string `json:"cidr,omitempty"`
}

// Stats are summary counters for a scan.
type Stats struct {
	Scanned   int `json:"scanned"`
	Alive     int `json:"alive"`
	Printers  int `json:"printers"`
	Web       int `json:"web"`
	Offline   int `json:"offline,omitempty"`
	Conflicts int `json:"conflicts,omitempty"`
	MDNS      int `json:"mdns,omitempty"`
}

// ScanResult is the full state of a scan.
type ScanResult struct {
	ID         string      `json:"id"`
	Request    ScanRequest `json:"request"`
	StartedAt  string      `json:"startedAt"`
	FinishedAt string      `json:"finishedAt"`
	State      string      `json:"state"` // running|live|done|cancelled|error
	Self       SelfInfo    `json:"self"`
	Gateway    string      `json:"gateway"`
	Hosts      []Host      `json:"hosts"`
	Stats      Stats       `json:"stats"`
	// Warnings holds non-fatal scan-level problems (e.g. ICMP unavailable).
	Warnings []string `json:"warnings,omitempty"`
	// Health is the latest health report (nil when not run).
	Health    *HealthReport `json:"health"`
	Conflicts []Conflict    `json:"conflicts,omitempty"`
	// Notices holds the last 100 alert notices.
	Notices []Notice `json:"notices,omitempty"`
	// SchemaVersion is HistorySchemaVersion for v0.2 results.
	SchemaVersion int `json:"schemaVersion"`
}

// Event is one SSE message. Type is host|progress|stage|snapshot|live|presence|
// done|error and Data is a Host, Progress, StageInfo, ScanResult, LiveStatus,
// Presence, ScanResult or {"message": ...} accordingly.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Progress reports completion of a pipeline stage.
type Progress struct {
	Stage string `json:"stage"` // icmp|arp|probe
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

// Event types.
const (
	EventHost     = "host"
	EventProgress = "progress"
	EventStage    = "stage"
	EventDone     = "done"
	EventLive     = "live"     // LiveStatus, once per live cycle
	EventSnapshot = "snapshot" // ScanResult when the initial scan completes in live mode
	EventPresence = "presence" // Presence, when a known host goes offline or comes back
	EventError    = "error"
	EventHealth   = "health"   // HealthReport, after each health run
	EventConflict = "conflict" // ConflictEvent, when a conflict appears or resolves
	EventNotice   = "notice"   // NoticeEvent, one alert notice
)

// ConflictEvent is the data of a conflict event. Replay marks events replayed
// to a new subscriber.
type ConflictEvent struct {
	Conflict Conflict `json:"conflict"`
	Resolved bool     `json:"resolved"`
	Replay   bool     `json:"replay,omitempty"`
}

// NoticeEvent is the data of a notice event: the Notice fields plus Replay,
// set on notices replayed to a new subscriber (the UI must not re-alert).
type NoticeEvent struct {
	Notice
	Replay bool `json:"replay,omitempty"`
}

// Scan states.
const (
	StateRunning   = "running"
	StateLive      = "live" // initial scan complete, watching for new hosts
	StateDone      = "done"
	StateCancelled = "cancelled"
	StateError     = "error"
)

// Pipeline stage names.
const (
	StageICMP  = "icmp"
	StageARP   = "arp"
	StageProbe = "probe"
	StageLive  = "live"
)

// StageInfo is the payload of a "stage" event.
type StageInfo struct {
	Stage string `json:"stage"`
}

// Message is the payload of an "error" event.
type Message struct {
	Message string `json:"message"`
}

// LiveStatus is the payload of a "live" event.
type LiveStatus struct {
	Cycle       int    `json:"cycle"`
	Unseen      int    `json:"unseen"`   // target addresses still unknown
	NewHosts    int    `json:"newHosts"` // hosts found since live mode started
	Online      int    `json:"online"`   // known hosts currently online
	Offline     int    `json:"offline"`  // known hosts currently offline
	IntervalSec int    `json:"intervalSec"`
	LastCheckAt string `json:"lastCheckAt"`
	NextCheckAt string `json:"nextCheckAt"`
}

// Presence is the payload of a "presence" event, published only when a known
// host transitions between online and offline.
type Presence struct {
	IP     string `json:"ip"`
	Online bool   `json:"online"`
	At     string `json:"at"`
	Reason string `json:"reason"`
}
