// Package advice says in plain words what a health check result, an alert
// notice, a conflict or a printer without status means, and what to try
// (v0.6 F6, phase 1). It is written for the person at the desk (v0.6-D9);
// the raw detail goes in Technical. It is pure and imports only model: the
// catalogue keys on structured fields (a check's Data["reason"], a notice's
// trigger and printer alert code, a conflict's kind), never on parsed text.
//
// Every action target is built and checked here, so the page renders links
// only from server data: the router page only for a gateway on a local
// network, a device page only when it is http(s) on the device's own IP,
// and Windows settings pages only on Windows.
package advice

import (
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"slices"
	"strings"

	"netdiag/internal/model"
)

// Ctx is what the advice needs beyond the item itself.
type Ctx struct {
	// GOOS is the operating system netdiag runs on (runtime.GOOS).
	GOOS string
	// Gateway is the router of the scanned network (may be empty).
	Gateway string
	// Hosts are the scan's devices by IP (may be nil).
	Hosts map[string]model.Host
	// Network is the id of the network the health checks looked at
	// (HealthReport.Network). Empty means netdiag can't tell which network
	// this is (the router's MAC is unknown), so the "This network has no
	// internet" switch can't be turned on and the advice doesn't offer it.
	Network string
}

// Button labels. ActionLabels lists them for the runbook tests.
const (
	LabelCheckAgain   = "Check again"
	LabelOpenRouter   = "Open router page"
	LabelOpenSettings = "Open Settings"
	LabelShowDevice   = "Show device"
	LabelOpenLocation = "Open Location settings"
	LabelOpenPage     = "Open its page"
)

// ActionLabels returns every button label the advice can show.
func ActionLabels() []string {
	return []string{LabelCheckAgain, LabelOpenRouter, LabelOpenSettings, LabelShowDevice, LabelOpenLocation, LabelOpenPage}
}

// MaxSteps is the most steps one piece of advice lists.
const MaxSteps = 3

// Reasons lists, by check id, every reason the health checks report
// (model.Reason*). The catalogue has advice for each.
func Reasons() map[string][]string {
	common := []string{model.ReasonTimeout, model.ReasonError}
	return map[string][]string{
		"gateway": append([]string{model.ReasonNoReply, model.ReasonTCPOnly, model.ReasonSlow,
			model.ReasonNoGateway, model.ReasonPingError}, common...),
		"dhcp": append([]string{model.ReasonAPIPA, model.ReasonNoLease}, common...),
		"dns": append([]string{model.ReasonSlow, model.ReasonLocalMismatch, model.ReasonNXDomain,
			model.ReasonNoInternet, model.ReasonNoReply, model.ReasonNoServer}, common...),
		"webserver": append([]string{model.ReasonNotSet, model.ReasonStatus, model.ReasonContent,
			model.ReasonRequest, model.ReasonTLS, model.ReasonRedirect, model.ReasonInvalid,
			model.ReasonSlow, model.ReasonExpired}, common...),
		"internet": append([]string{model.ReasonPortal, model.ReasonNoResponse, model.ReasonStatus,
			model.ReasonInvalid}, common...),
		"wifi": append([]string{model.ReasonWeak, model.ReasonNotConnected, model.ReasonHiddenLocation}, common...),
	}
}

// Triggers lists every notice trigger the catalogue has advice for.
func Triggers() []string {
	return []string{model.TriggerPrinterOffline, model.TriggerDeviceOffline, model.TriggerSupplyLow,
		model.TriggerPrinterAlert, model.TriggerNewDevice, model.TriggerIPConflict, model.TriggerHealthFailed,
		model.TriggerNetdiagLink, model.TriggerChatTest}
}

// ConflictKinds lists every conflict kind the catalogue has advice for.
func ConflictKinds() []string {
	return []string{model.ConflictMACFlap, model.ConflictMACMultiIP, model.ConflictDuplicateName}
}

func recheck() model.AdviceAction {
	return model.AdviceAction{Kind: model.ActionRecheck, Label: LabelCheckAgain}
}

func settings(section string) model.AdviceAction {
	return model.AdviceAction{Kind: model.ActionOpenSettings, Label: LabelOpenSettings, Target: section}
}

func showDevice(ip string) []model.AdviceAction {
	if ip == "" {
		return nil
	}
	return []model.AdviceAction{{Kind: model.ActionShowDevice, Label: LabelShowDevice, Target: ip}}
}

func adv(meaning string, steps []string, actions ...model.AdviceAction) *model.Advice {
	return &model.Advice{Meaning: meaning, Steps: steps, Actions: actions}
}

func steps(s ...string) []string { return s }

// Health returns the advice for one health check result, or nil for a
// result that needs none (ok, except a router that answers only on its web
// ports, and any check without a known reason).
func Health(c model.HealthCheck, x Ctx) *model.Advice {
	reason := model.HealthReason(c)
	if reason == "" {
		return nil
	}
	a := health(c, reason, x)
	if a != nil {
		a.Technical = c.Detail
	}
	return a
}

// Report sets the advice on every check of rep, for the network rep names
// (x.Network is rep.Network). The checks are copied first, so a report
// that shares its slice with another is left alone.
func Report(rep *model.HealthReport, x Ctx) {
	if rep == nil {
		return
	}
	x.Network = rep.Network
	rep.Checks = slices.Clone(rep.Checks)
	for i := range rep.Checks {
		rep.Checks[i].Advice = Health(rep.Checks[i], x)
	}
}

func health(c model.HealthCheck, reason string, x Ctx) *model.Advice {
	noInternet, _ := c.Data["noInternet"].(bool)
	switch reason {
	case model.ReasonTimeout:
		return adv("This check took too long and was stopped. The other checks still work.",
			steps("Check again in a minute."), recheck())
	case model.ReasonError:
		switch c.ID {
		case "wifi":
			if x.GOOS == "darwin" {
				return adv("macOS doesn't share Wi-Fi details with netdiag. The other checks still work.", nil)
			}
			return adv("netdiag couldn't read the Wi-Fi details on this computer. The other checks still work.", nil)
		case "dhcp":
			return adv("netdiag couldn't read how this computer got its address. The other checks still work.", nil)
		}
		return adv("netdiag couldn't run this check on this computer. The other checks still work.",
			steps("Check again in a minute."), recheck())
	}
	switch c.ID + "|" + reason {
	case "gateway|" + model.ReasonNoReply:
		return adv("This computer can't reach your router.",
			steps("Check that the router is switched on and its lights look normal.",
				"Check this computer's network cable or Wi-Fi.",
				"If both look fine, restart the router."), recheck())
	case "gateway|" + model.ReasonTCPOnly:
		var acts []model.AdviceAction
		if u := RouterURL(x.Gateway); u != "" {
			acts = append(acts, model.AdviceAction{Kind: model.ActionOpenRouter, Label: LabelOpenRouter, Target: u})
		}
		if pingErr, _ := c.Data["pingError"].(bool); pingErr {
			// Ping could not run on this computer: not the router's doing.
			return adv("netdiag couldn't send its test message from this computer, but your router's web page answers, so it is working.", nil, acts...)
		}
		return adv("Your router doesn't answer netdiag's test message, but its web page answers, so it is working.", nil, acts...)
	case "gateway|" + model.ReasonSlow:
		return adv(fmt.Sprintf("Your router answers, but slowly (%s).", millis(c.LatencyMs)),
			steps("This can happen when the network is busy or the Wi-Fi signal is weak.",
				"If it stays slow, restart the router."), recheck())
	case "gateway|" + model.ReasonNoGateway:
		return adv("netdiag doesn't know the router of this network, so it can't check it.",
			steps("Check that this computer is connected to the network.",
				"If it is, check again in a minute."), recheck())
	case "gateway|" + model.ReasonPingError:
		return adv("netdiag couldn't send its test message from this computer, so it can't tell whether the router answers.",
			steps("Check again in a minute."), recheck())

	case "dhcp|" + model.ReasonAPIPA:
		return adv("This computer didn't get an address from the router, so it can't talk to other devices.",
			steps("Turn Wi-Fi off and on again, or unplug the network cable and plug it back in.",
				"If that doesn't help, restart the router."), recheck())
	case "dhcp|" + model.ReasonNoLease:
		return adv("This computer uses an address that was set by hand, so it doesn't ask the router for one. That is fine if it was set up that way.", nil)

	case "dns|" + model.ReasonSlow:
		return adv("Your name server works, but it answers slowly.",
			steps("If it stays slow, restart the router."), recheck())
	case "dns|" + model.ReasonNoInternet:
		if noInternet {
			return adv("This network is set up without internet, so internet names can't be looked up. That is expected.",
				steps(`If this network has internet now, turn off "This network has no internet" in Settings.`),
				settings(model.SettingsSectionNoInternet))
		}
		const meaning = "Your local network works, but internet names can't be looked up. Usually this means there is no internet connection right now."
		const restart = "If this network should have internet, restart the router or modem."
		if x.Network == "" {
			// netdiag can't tell which network this is, so the switch,
			// which is kept per network, can't be turned on.
			return adv(meaning, steps(restart), recheck())
		}
		return adv(meaning,
			steps(restart, `If this network has no internet on purpose, turn on "This network has no internet" in Settings.`),
			recheck(), settings(model.SettingsSectionNoInternet))
	case "dns|" + model.ReasonLocalMismatch:
		return adv("Your name server works, but this computer asks somewhere else first, for example an old VPN setting.",
			steps("Disconnect any VPN and check again.",
				"Ask whoever looks after this computer to check its settings."), recheck())
	case "dns|" + model.ReasonNXDomain:
		return adv("The name server answers, but doesn't know the name in "+fieldInternetURL+".",
			steps("Check "+fieldInternetURL+" in Settings."), settings(model.SettingsSectionHealth))
	case "dns|" + model.ReasonNoReply:
		return adv("No name server answers, so this computer can't look up names.",
			steps("Restart the router.", "If you use a VPN, disconnect it and check again."), recheck())
	case "dns|" + model.ReasonNoServer:
		return adv("This computer has no name server set.",
			steps("Ask whoever looks after this computer to set the network adapter to get its settings automatically."))

	case "webserver|" + model.ReasonNotSet:
		return adv("No web server is set, so this check is skipped.", nil, settings(model.SettingsSectionWebServer))
	case "webserver|" + model.ReasonStatus:
		m := "The web server isn't answering as expected."
		if st, ok := num(c.Data["status"]); ok && st > 0 {
			m = fmt.Sprintf("The web server isn't answering as expected (HTTP %d).", int(st))
		}
		return adv(m, webSteps(), recheck(), settings(model.SettingsSectionWebServer))
	case "webserver|" + model.ReasonContent:
		return adv("The web server answers, but its page doesn't show the text you set.",
			webSteps(), recheck(), settings(model.SettingsSectionWebServer))
	case "webserver|" + model.ReasonRequest:
		return adv("The web server isn't answering.", webSteps(), recheck(), settings(model.SettingsSectionWebServer))
	case "webserver|" + model.ReasonTLS:
		if fp, _ := c.Data["certSHA256"].(string); fp == "" {
			// No certificate offered to trust: another address of the
			// server (data.finalURL), with a different certificate,
			// answered.
			first := "The server sends you to another address that uses a different certificate. If that is the right page, set it as " + fieldWebURL + " in Settings."
			if u := shownURL(c.Data["finalURL"]); u != "" {
				first = "The server sends you to " + u + ", which uses a different certificate. If that is the right page, set it as " + fieldWebURL + " in Settings."
			}
			return adv("The web server's certificate isn't trusted.",
				steps(first, "Otherwise, ask whoever looks after it."), settings(model.SettingsSectionWebServer))
		}
		// The Web server card has the Trust this certificate button; the
		// card itself says so in its own words (web/app.js cardSteps).
		return adv("The web server's certificate isn't trusted.",
			steps(StepTrustCert, "Otherwise, ask whoever looks after it."))
	case "webserver|" + model.ReasonRedirect:
		// A redirect to another server may be a sign-in page: the step
		// names the address, and switching to it is the person's choice.
		// The web check gives this reason only for another server, and
		// only when the expected answer is not a redirect itself (a loop,
		// or a redirect being checked, is a status answer).
		first := "It sends you to another address. If that is the right page, set it as " + fieldWebURL + " in Settings."
		if u := shownURL(c.Data["location"]); u != "" {
			first = "It sends you to " + u + ". If that is the right page, set it as " + fieldWebURL + " in Settings."
		}
		return adv("The web server sends visitors to another address, so the page you set isn't the one that answers.",
			steps(first), recheck(), settings(model.SettingsSectionWebServer))
	case "webserver|" + model.ReasonInvalid:
		return adv("The "+fieldWebURL+" in Settings isn't a valid web address.",
			steps("Check "+fieldWebURL+" in Settings."), settings(model.SettingsSectionWebServer))
	case "webserver|" + model.ReasonSlow:
		return adv("The web server answers, but slowly.",
			steps("If it stays slow, ask whoever looks after it."), recheck())
	case "webserver|" + model.ReasonExpired:
		return adv("The certificate you trusted for the web server has expired.",
			steps("Ask whoever looks after the server to renew it, then trust the new one."),
			settings(model.SettingsSectionWebServer))

	case "internet|" + model.ReasonPortal:
		return adv("This network wants you to sign in first, on a Wi-Fi login page.",
			steps("Open any website in your browser to see the login page."))
	case "internet|" + model.ReasonNoResponse:
		if noInternet {
			return adv("This network is set up without internet, so this check is only for information.", nil,
				settings(model.SettingsSectionNoInternet))
		}
		return adv("netdiag couldn't reach the internet. Devices on the local network still work.",
			steps("If this network should have internet, restart the router or modem."), recheck())
	case "internet|" + model.ReasonStatus:
		m := "The internet check got an unexpected answer."
		if st, ok := num(c.Data["status"]); ok && st > 0 {
			m = fmt.Sprintf("The internet check got an unexpected answer (HTTP %d).", int(st))
		}
		return adv(m, steps("Check "+fieldInternetURL+" in Settings."), settings(model.SettingsSectionHealth))
	case "internet|" + model.ReasonInvalid:
		return adv("The "+fieldInternetURL+" in Settings isn't a valid web address.",
			steps("Check "+fieldInternetURL+" in Settings."), settings(model.SettingsSectionHealth))

	case "wifi|" + model.ReasonWeak:
		m := "The Wi-Fi signal is weak."
		if p, ok := num(c.Data["signalPct"]); ok && p > 0 {
			m = fmt.Sprintf("The Wi-Fi signal is weak (%d%%).", int(p))
		}
		return adv(m, steps("Move closer to the router, or use a network cable."), recheck())
	case "wifi|" + model.ReasonNotConnected:
		return adv("This computer isn't using Wi-Fi right now, for example because it uses a network cable.", nil)
	case "wifi|" + model.ReasonHiddenLocation:
		// Open Location settings comes before Check again here, in the
		// order the step names them (v0.6-D56).
		var acts []model.AdviceAction
		if x.GOOS == "windows" {
			acts = append(acts, model.AdviceAction{Kind: model.ActionOSSettings, Label: LabelOpenLocation, Target: model.OSPageLocation})
		}
		return adv("Windows hides Wi-Fi details until Location is turned on. The other checks still work.",
			steps("Turn on Location, then press Check again."), append(acts, recheck())...)
	}
	return nil
}

// The Settings labels the advice names, in quotes, as Settings shows them.
const (
	fieldWebURL      = `"Web server URL"`
	fieldInternetURL = `"Internet check URL"`
)

// StepTrustCert is the web server certificate step for every place but
// the Web server card, which has the Trust this certificate button and
// says it in its own words (web/app.js cardSteps).
const StepTrustCert = "If this is your own server on this network, open Health in netdiag and press Trust this certificate on the Web server card."

func webSteps() []string {
	return steps("Check that the server is switched on.", "Ask whoever looks after it.")
}

// shownURL is a web address from check data, for a step: an absolute
// http(s) URL, or "". A step goes to the banner, team chat and Claude, and
// a person may type it into Settings, so an address with a user name (the
// check shows its password as xxxxx), a query (a sign-in page's one-time
// ticket) or a fragment is left out; the Web server card still offers it
// (web/app.js webNewURL).
func shownURL(v any) string {
	s, _ := v.(string)
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return ""
	}
	if sc := strings.ToLower(u.Scheme); sc != "http" && sc != "https" {
		return ""
	}
	return u.String()
}

// millis formats a latency in whole milliseconds.
func millis(ms float64) string { return fmt.Sprintf("%.0f ms", math.Round(ms)) }

// num reads a number from check data: an int when the check made it, a
// float64 after a trip through JSON (history, the page).
func num(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// Notice returns the advice for a fault notice, or nil for an OK notice.
// h is the device the notice is about, when known. For an ipConflict or a
// healthFailed notice, callers that have the conflict or the check use
// Conflict or Health instead; this gives a short general answer.
func Notice(n model.Notice, h *model.Host, x Ctx) *model.Advice {
	if n.OK {
		return nil
	}
	a := notice(n, h)
	if a != nil {
		a.Technical = n.Message
	}
	return a
}

func notice(n model.Notice, h *model.Host) *model.Advice {
	switch n.Trigger {
	case model.TriggerPrinterOffline:
		return adv("The printer stopped answering.",
			steps("Check that it is switched on and not showing an error.",
				"Check its network cable, or its Wi-Fi light.",
				"Turn it off, wait 10 seconds and turn it on again."), showDevice(n.IP)...)
	case model.TriggerDeviceOffline:
		return adv(deviceName(n, h)+" stopped answering.",
			steps("Check that it is switched on and connected.",
				"Phones and laptops stop answering when they sleep; that can be normal."), showDevice(n.IP)...)
	case model.TriggerSupplyLow:
		m := "A printer supply is low."
		if s, ok := supply(h, n.Detail); ok && s.Percent >= 0 {
			m = fmt.Sprintf("%s is at %d%%.", s.Description, s.Percent)
		} else if n.Detail != "" {
			m = n.Detail + " is low."
		}
		return adv(m, steps("Order a replacement soon."), showDevice(n.IP)...)
	case model.TriggerPrinterAlert:
		return printerAlert(n)
	case model.TriggerNewDevice:
		return adv("A device netdiag hadn't seen before joined the local network.",
			steps("If you don't recognise it, ask whoever looks after the network."), showDevice(n.IP)...)
	case model.TriggerIPConflict:
		return adv("Two devices may be trying to use the same address, so they can work badly.",
			steps("In netdiag, press "+LabelShowDevice+" to see the two devices."), showDevice(n.IP)...)
	case model.TriggerHealthFailed:
		// The banner has an Open Health button for every healthFailed notice.
		return adv("A network health check keeps failing.", steps("In netdiag, press Open Health to see what to try."))
	case model.TriggerNetdiagLink:
		// The banner says only "netdiag paused its checks." (scan
		// heldNotice): the Meaning says why, and when they start again.
		switch n.Code {
		case model.HeldGateway:
			return adv("The router and most devices stopped answering at once, so netdiag paused its checks instead of reporting every device offline. They start again by themselves when the router answers.",
				steps("Check that the router is switched on and its lights look normal.",
					"Check this computer's network cable or Wi-Fi."))
		case model.HeldNetworkChanged:
			return adv("This computer is on a different network now, so netdiag stopped the alerts for the network it was watching. It checks nothing more until you restart watching.",
				steps("If this computer moved on purpose, press Restart watching to watch the network it is on now."))
		}
		return adv("This computer lost the local network, so netdiag paused its checks instead of reporting every device offline. They start again by themselves when the network is back.",
			steps("Check this computer's network cable or Wi-Fi."))
	case model.TriggerChatTest:
		return adv("This is a test alert from netdiag. Nothing needs fixing.", nil)
	}
	return nil
}

// printerAlertKind maps a printer alert code (SNMP
// hrPrinterDetectedErrorState.<state> or an IPP printer-state-reason) to a
// catalogue entry.
func printerAlertKind(code string) string {
	if s, ok := strings.CutPrefix(code, "hrPrinterDetectedErrorState."); ok {
		return s
	}
	base := code
	for _, suf := range []string{"-error", "-warning", "-report"} {
		base = strings.TrimSuffix(base, suf)
	}
	switch base {
	case "door-open", "cover-open", "interlock-open":
		return "doorOpen"
	case "media-jam":
		return "jammed"
	case "media-empty", "media-needed":
		return "noPaper"
	case "media-low":
		return "lowPaper"
	case "toner-empty", "marker-supply-empty":
		return "noToner"
	case "toner-low", "marker-supply-low":
		return "lowToner"
	case "paused", "offline":
		return "offline"
	}
	return ""
}

func printerAlert(n model.Notice) *model.Advice {
	show := showDevice(n.IP)
	switch printerAlertKind(n.Code) {
	case "doorOpen":
		return adv("The printer says a door or cover is open.", steps("Close the printer's cover or door."), show...)
	case "jammed":
		return adv("The printer has a paper jam.",
			steps("Open the printer and remove the jammed paper.", "Close it again."), show...)
	case "noPaper":
		return adv("The printer is out of paper.", steps("Add paper."), show...)
	case "lowPaper":
		return adv("The printer is running low on paper.", steps("Add paper soon."), show...)
	case "noToner":
		return adv("The printer is out of toner or ink.", steps("Replace the toner or ink."), show...)
	case "lowToner":
		return adv("The printer is running low on toner or ink.", steps("Order a replacement soon."), show...)
	case "offline":
		return adv("The printer is paused or offline, so it won't print.",
			steps("Look at the printer's screen for a message.",
				"If it has a pause or resume button, press it.",
				"Check that its covers are closed."), show...)
	}
	what := n.Detail
	if what == "" {
		what = "a problem"
	}
	return adv(fmt.Sprintf("The printer reports: %s.", strings.TrimSuffix(what, ".")),
		steps("Look at the printer's screen for details."), show...)
}

// deviceName is the best name for the notice's device: its name, else
// "The device at <IP>".
func deviceName(n model.Notice, h *model.Host) string {
	if n.Name != "" {
		return n.Name
	}
	if h != nil && h.Hostname != "" {
		return h.Hostname
	}
	if n.IP != "" {
		return "The device at " + n.IP
	}
	return "A device"
}

// supply finds the printer supply with this description on h.
func supply(h *model.Host, desc string) (model.Supply, bool) {
	if h == nil || h.Printer == nil || desc == "" {
		return model.Supply{}, false
	}
	i := slices.IndexFunc(h.Printer.Supplies, func(s model.Supply) bool { return s.Description == desc })
	if i < 0 {
		return model.Supply{}, false
	}
	return h.Printer.Supplies[i], true
}

// Alternations counts how often the MAC answering at an address changed
// in its history: A, B, A is two. A conflict counts as seen when it is 2
// or more (the two devices were seen taking turns).
func Alternations(hist []model.MACObservation) int {
	n, last := 0, ""
	for _, o := range hist {
		m := strings.ToUpper(o.MAC)
		if m == "" {
			continue
		}
		if last != "" && m != last {
			n++
		}
		last = m
	}
	return n
}

// SwapSeen reports whether a mac_flap conflict was seen as a swap: the
// device's MAC history shows two alternations, or, without a history (a
// scan that is not live), the analyzer marked it "fail", which it does
// only for MACs seen taking turns.
func SwapSeen(c model.Conflict, x Ctx) bool {
	if c.Kind != model.ConflictMACFlap {
		return false
	}
	if len(c.IPs) > 0 {
		if h, ok := x.Hosts[c.IPs[0]]; ok && len(h.MACHistory) > 0 {
			return Alternations(h.MACHistory) >= 2
		}
	}
	return c.Severity == "fail"
}

// Conflict returns the advice for a conflict.
func Conflict(c model.Conflict, x Ctx) *model.Advice {
	ip := ""
	if len(c.IPs) > 0 {
		ip = c.IPs[0]
	}
	var a *model.Advice
	switch c.Kind {
	case model.ConflictMACFlap:
		// The drawer shows Device A and Device B for every mac_flap; the
		// banner, the Problems right now card, chat and Claude reach them
		// through Show device.
		if SwapSeen(c, x) {
			a = adv(fmt.Sprintf("Two devices are taking turns using %s, so both work badly.", orAddress(ip)),
				steps(StepCompareDevices,
					"Restart one of them so it gets a new address.",
					"If it keeps happening, ask whoever looks after the router to reserve an address for one of them."),
				showDevice(ip)...)
		} else {
			a = adv(fmt.Sprintf("The device answering at %s changed.", orAddress(ip)),
				// True under any title: the banner and team chat already
				// call it a Network conflict (ConflictSeverity has no MAC
				// history to tell a swap).
				steps("If you just replaced a device, this is expected.",
					"If it happens again, netdiag will tell you."),
				showDevice(ip)...)
		}
	case model.ConflictMACMultiIP:
		a = adv("One device answers for several addresses. This is normal for Wi-Fi extenders, mesh points and some routers.",
			steps("If this is an extender or mesh point, you can ignore it."))
	case model.ConflictDuplicateName:
		name := c.Name
		if name == "" {
			name = "the same name"
		} else {
			name = `the same name, "` + name + `"`
		}
		a = adv("Two devices use "+name+".", steps("Rename one of them in its own settings."))
	default:
		return nil
	}
	a.Technical = c.Detail
	return a
}

// StepCompareDevices is the swap's first step for every place but the
// drawer, which shows the two devices itself and drops "In netdiag,
// press Show device and" (web/app.js drawerSteps).
const StepCompareDevices = "In netdiag, press " + LabelShowDevice + " and compare the two devices (Device A and Device B)."

func orAddress(ip string) string {
	if ip == "" {
		return "one address"
	}
	return ip
}

// PrinterNoStatus returns the advice for a printer that reports no status
// (no SNMP data and no IPP data), or nil for any other device.
func PrinterNoStatus(h model.Host) *model.Advice {
	if !h.IsPrinter || h.Snmp != nil || h.Printer != nil && strings.Contains(h.Printer.Source, "ipp") {
		return nil
	}
	a := adv("This printer doesn't report its status, so netdiag can only see whether it answers.",
		steps("Turn on SNMP in the printer's settings page (often under Network), or ask whoever looks after it."))
	if u := DevicePageURL(h); u != "" {
		a.Actions = []model.AdviceAction{{Kind: model.ActionOpenDevicePage, Label: LabelOpenPage, Target: u}}
	}
	return a
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// local reports whether a is a private, link-local or carrier-grade NAT
// address (the rule remote.Local uses).
func local(a netip.Addr) bool {
	return a.IsPrivate() || a.IsLinkLocalUnicast() || cgnat.Contains(a)
}

// RouterURL is the router's own page, http://<gateway>/, or "" unless the
// gateway is a plain IPv4 address on a local network.
func RouterURL(gateway string) string {
	a, err := netip.ParseAddr(gateway)
	if err != nil || !a.Is4() || !local(a) {
		return ""
	}
	return (&url.URL{Scheme: "http", Host: a.String(), Path: "/"}).String()
}

// DevicePageURL is the first web page the device serves on its own IP,
// over http or https, or "". v0.7's drawer Open its page reuses it.
func DevicePageURL(h model.Host) string {
	ip, err := netip.ParseAddr(h.IP)
	if err != nil {
		return ""
	}
	for _, w := range h.Web {
		if u := pageURL(w.URL, ip); u != "" {
			return u
		}
	}
	return ""
}

// pageURL returns raw, normalized, when it is an http(s) URL whose host is
// ip (any port), with no user name or password; else "".
func pageURL(raw string, ip netip.Addr) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.Opaque != "" {
		return ""
	}
	if s := strings.ToLower(u.Scheme); s != "http" && s != "https" {
		return ""
	}
	a, err := netip.ParseAddr(u.Hostname())
	if err != nil || a.Unmap() != ip.Unmap() {
		return ""
	}
	return u.String()
}
