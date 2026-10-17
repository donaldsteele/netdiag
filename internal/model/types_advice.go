package model

// Advice says in plain words what a problem means and what to try, for the
// person at the desk (v0.6-D9). It is built by internal/advice from
// structured fields (a health check's Data["reason"], a notice's trigger
// and printer alert code, a conflict's kind), never from parsed text.
type Advice struct {
	// Meaning is one or two sentences: "What this means".
	Meaning string `json:"meaning"`
	// Steps are at most three things to try, in order.
	Steps []string `json:"steps,omitempty"`
	// Actions are the buttons shown with the advice.
	Actions []AdviceAction `json:"actions,omitempty"`
	// Technical is the raw detail, shown under "Technical details".
	Technical string `json:"technical,omitempty"`
}

// AdviceAction is one advice button. Targets are built and checked on the
// server: the page renders a link only from a Target sent here.
type AdviceAction struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Target string `json:"target,omitempty"`
}

// Advice action kinds.
const (
	// ActionOpenRouter opens the router's own page (Target: its http URL),
	// only for a gateway on a local network.
	ActionOpenRouter = "openRouter"
	// ActionOpenDevicePage opens a device's own web page (Target: an
	// http(s) URL whose host is the device's IP).
	ActionOpenDevicePage = "openDevicePage"
	// ActionOpenSettings opens Settings (Target: a section, see
	// SettingsSection*).
	ActionOpenSettings = "openSettings"
	// ActionRecheck runs the health checks again (POST /api/health/run).
	ActionRecheck = "recheck"
	// ActionShowDevice opens the device's details (Target: its IP).
	ActionShowDevice = "showDevice"
	// ActionOSSettings opens a page of the computer's own settings through
	// POST /api/os/open (Target: a page name, see OSPage*).
	ActionOSSettings = "osSettings"
)

// Settings sections an ActionOpenSettings can point at; the page puts
// focus on the matching field.
const (
	SettingsSectionWebServer  = "webserver"  // the Web server URL
	SettingsSectionHealth     = "health"     // the Internet check URL
	SettingsSectionNoInternet = "noInternet" // the "This network has no internet" switch
	SettingsSectionAlerts     = "alerts"
	SettingsSectionChat       = "chat"
)

// OSPageLocation is the Windows Location privacy page: Windows hides Wi-Fi
// details from apps until Location is on.
const OSPageLocation = "location"
