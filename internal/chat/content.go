package chat

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"netdiag/internal/advice"
	"netdiag/internal/bubbles/protocol"
	"netdiag/internal/health"
	"netdiag/internal/model"
	"netdiag/internal/problems"
)

// This file turns netdiag's notices into team chat alerts (v0.6 4.9
// "Notice to alert"). The alert never carries the scan request (the SNMP
// community lives there), only what the notice and its device say.

// chatSeverity maps a notice severity (info|warn|critical, the 4.7 map) to
// the chat's (info|warning|critical).
func chatSeverity(s string) string {
	switch s {
	case "critical":
		return protocol.SeverityCritical
	case "warn", protocol.SeverityWarning:
		return protocol.SeverityWarning
	}
	return protocol.SeverityInfo
}

// severityRank orders chat severities: info 0, warning 1, critical 2.
func severityRank(s string) int {
	switch chatSeverity(s) {
	case protocol.SeverityCritical:
		return 2
	case protocol.SeverityWarning:
		return 1
	}
	return 0
}

// sends reports whether a fault of severity reaches team chat at the Send
// level minSeverity (settings.chat.minSeverity; "" is the default,
// warning).
func sends(severity, minSeverity string) bool {
	if minSeverity == "" {
		minSeverity = model.ChatSeverityWarning
	}
	return severityRank(severity) >= severityRank(minSeverity)
}

// subject names what a notice is about: "Front Desk HP (192.168.1.50)",
// the address alone, or the health check ("Web server").
func subject(n model.Notice) string {
	if n.Trigger == model.TriggerHealthFailed {
		if n.Detail != "" {
			return n.Detail
		}
		if name, ok := strings.CutSuffix(n.Message, " check failed."); ok && name != "" {
			return name
		}
		return health.Label(healthCheckID(n.Key))
	}
	return nameAt(n.Name, n.IP)
}

// healthCheckID is the check of a healthFailed key (healthFailed||<id>).
func healthCheckID(key string) string {
	return strings.TrimPrefix(key, model.TriggerHealthFailed+"||")
}

// nameAt is "name (ip)", or whichever of the two there is.
func nameAt(name, ip string) string {
	switch {
	case name != "" && ip != "" && name != ip:
		return name + " (" + ip + ")"
	case name != "":
		return name
	}
	return ip
}

// bestName is the best name netdiag has for h, for the Device field: the
// mDNS instance, SNMP system name, host name, then printer model (the
// order of notify's, which names the notice; v0.7 names.Best replaces
// both).
func bestName(h model.Host) string {
	switch {
	case h.MDNS != nil && h.MDNS.Instance != "":
		return h.MDNS.Instance
	case h.Snmp != nil && h.Snmp.SysName != "":
		return h.Snmp.SysName
	case h.Hostname != "":
		return h.Hostname
	case h.Printer != nil && h.Printer.Model != "":
		return h.Printer.Model
	}
	return ""
}

// clip cuts s to at most n characters (runes), ending in "…" when cut.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// field adds a name/value pair when the value is not empty, within
// bubbles' limits (at most 20 fields, values of at most 512 characters).
func addField(fs []protocol.AlertField, name, value string) []protocol.AlertField {
	value = strings.TrimSpace(value)
	if value == "" || len(fs) >= protocol.MaxAlertFields {
		return fs
	}
	return append(fs, protocol.AlertField{Name: name, Value: clip(value, protocol.MaxAlertFieldValue), Short: true})
}

// localTime is the RFC 3339 time t as this computer's clock shows it
// ("10:42"), or "" when it cannot be read.
func localTime(t string) string {
	tt, err := time.Parse(time.RFC3339, t)
	if err != nil {
		return ""
	}
	return tt.Local().Format("15:04")
}

// alertFor is the alert for the fault notice n (4.9's table). h is its
// device when the scan knows it. The channel is filled in when it is sent.
// The fields are for the people in the channel, so an IP conflict's own id
// is not one of them (the notice's key is the alert's dedupe key).
func alertFor(n model.Notice, h *model.Host) protocol.AlertInput {
	zero := 0
	occurred := n.OpenedAt
	if occurred == "" {
		occurred = n.At
	}
	in := protocol.AlertInput{
		Severity:       chatSeverity(n.Severity),
		Title:          clip(n.Title+titleTail(subject(n)), protocol.MaxAlertTitle),
		Text:           clip(alertText(n.Message, n.Advice), protocol.MaxAlertText),
		DedupeKey:      n.Key,
		RenotifyAfterS: &zero, // reminders come with v0.8
	}
	if occurred != "" {
		in.OccurredAt = &occurred
	}
	var fs []protocol.AlertField
	name := n.Name
	if h != nil && name == "" {
		name = bestName(*h)
	}
	if n.Trigger != model.TriggerHealthFailed {
		fs = addField(fs, "Device", name)
		fs = addField(fs, "Address", n.IP)
	}
	mac, macField := n.MAC, "MAC address"
	if h != nil {
		if mac == "" {
			mac = h.MAC
		}
	}
	if n.Trigger == model.TriggerIPConflict && n.Detail != "" {
		// The conflict's own MACs (notify.ObserveConflicts): both devices
		// of a mac_flap, which whoever reserves an address needs, not
		// only the one answering now.
		mac = n.Detail
		if strings.Contains(mac, ",") {
			macField = "MAC addresses"
		}
	}
	fs = addField(fs, macField, mac)
	if h != nil {
		fs = addField(fs, "Maker", h.Vendor)
		if h.Printer != nil {
			fs = addField(fs, "Model", h.Printer.Model)
		}
	}
	fs = addField(fs, "Since", localTime(occurred))
	switch n.Trigger {
	case model.TriggerHealthFailed:
		fs = addField(fs, "Check", subject(n))
	case model.TriggerSupplyLow:
		fs = addField(fs, "Supply", n.Detail)
	}
	in.Fields = fs
	if h != nil {
		// Only the device's own page (http or https on its own address);
		// netdiag's page is on 127.0.0.1 and no use to anyone else.
		if u := advice.DevicePageURL(*h); u != "" && len(u) <= protocol.MaxAlertLink {
			in.Link = &u
		}
	}
	return in
}

// titleTail is ": <subject>", or "" when there is none.
func titleTail(s string) string {
	if s == "" {
		return ""
	}
	return ": " + s
}

// alertText is the notice's message, then "What to try:" with up to 3
// numbered steps of its advice.
func alertText(msg string, a *model.Advice) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(msg))
	if a != nil && len(a.Steps) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("What to try:")
		for i, s := range a.Steps {
			if i == 3 {
				break
			}
			b.WriteString("\n" + strconv.Itoa(i+1) + ". " + s)
		}
	}
	return b.String()
}

// alertForEntry is the alert for an open problem the ledger holds, when
// netdiag has no notice of it to build it from (the router keeps the last
// one it saw, Manager.alertOf): a burst or Change channel after a restart,
// or a problem a scan found before. It says what the notice would: the
// check's label for a health check, the catalogue's advice (v0.6 4.8) as
// "What to try", and the device's maker, model and page when h, its device,
// is known.
func alertForEntry(e problems.Entry, h *model.Host) protocol.AlertInput {
	n := model.Notice{Trigger: e.Trigger, Severity: e.Severity, IP: e.IP, MAC: e.MAC, Name: e.Name,
		Title: e.Title, Key: e.Key, OpenedAt: e.OpenedAt, Detail: e.Detail}
	if e.Trigger == model.TriggerHealthFailed {
		n.Message = health.Label(healthCheckID(e.Key)) + " check failed."
	}
	n.Advice = advice.Notice(n, h, advice.Ctx{GOOS: runtime.GOOS})
	if n.Message == "" && n.Advice != nil {
		n.Message = n.Advice.Meaning
	}
	in := alertFor(n, h)
	if e.Detail != "" && in.Text == "" {
		in.Text = clip(e.Detail, protocol.MaxAlertText)
	}
	return in
}

// fixedText is the resolve note of an OK notice: "Fixed at 10:42 (lasted 12
// min)." and what was fixed, in this computer's time. "lasted" fits every
// problem (an open cover or low toner was never down), as the page says it.
func fixedText(n model.Notice, now time.Time) string {
	end := now
	if t, err := time.Parse(time.RFC3339, n.At); err == nil {
		end = t
	}
	s := "Fixed at " + end.Local().Format("15:04")
	if from, err := time.Parse(time.RFC3339, n.OpenedAt); err == nil && !end.Before(from) {
		s += " (lasted " + downFor(end.Sub(from)) + ")"
	}
	s += "."
	if m := strings.TrimSpace(n.Message); m != "" {
		s += " " + m
	}
	return clip(s, protocol.MaxAlertText)
}

// downFor is a duration in words, with the page's days (app.js downFor):
// "less than a minute", "12 min", "2 h 5 min", "2 h", and from a day on
// "1 day", "8 days 2 h".
func downFor(d time.Duration) string {
	m := int(d / time.Minute)
	switch {
	case m < 1:
		return "less than a minute"
	case m < 60:
		return fmt.Sprintf("%d min", m)
	case m < 24*60 && m%60 == 0:
		return fmt.Sprintf("%d h", m/60)
	case m < 24*60:
		return fmt.Sprintf("%d h %d min", m/60, m%60)
	}
	days, h := m/(24*60), m%(24*60)/60
	s := fmt.Sprintf("%d days", days)
	if days == 1 {
		s = "1 day"
	}
	if h > 0 {
		s += fmt.Sprintf(" %d h", h)
	}
	return s
}
