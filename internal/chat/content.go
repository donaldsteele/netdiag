package chat

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"netdiag/internal/advice"
	"netdiag/internal/bubbles/protocol"
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
		if name, ok := strings.CutSuffix(n.Message, " check failed."); ok && name != "" {
			return name
		}
		return strings.TrimPrefix(n.Key, model.TriggerHealthFailed+"||")
	}
	return nameAt(n.Name, n.IP)
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
// device when the scan knows it, conflictID the id of the conflict an
// ipConflict notice is about. The channel is filled in when it is sent.
func alertFor(n model.Notice, h *model.Host, conflictID string) protocol.AlertInput {
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
	mac := n.MAC
	if h != nil {
		if mac == "" {
			mac = h.MAC
		}
	}
	fs = addField(fs, "MAC", mac)
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
	case model.TriggerIPConflict:
		fs = addField(fs, "Conflict id", conflictID)
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
// there is no notice to build it from: the first-connect burst and Change
// channel.
func alertForEntry(e problems.Entry) protocol.AlertInput {
	n := model.Notice{Trigger: e.Trigger, Severity: e.Severity, IP: e.IP, MAC: e.MAC, Name: e.Name,
		Title: e.Title, Key: e.Key, OpenedAt: e.OpenedAt, Detail: e.Detail}
	if e.Trigger == model.TriggerHealthFailed {
		n.Message = strings.TrimPrefix(e.Key, model.TriggerHealthFailed+"||") + " check failed."
	}
	in := alertFor(n, nil, "")
	if e.Detail != "" && in.Text == "" {
		in.Text = clip(e.Detail, protocol.MaxAlertText)
	}
	return in
}

// fixedText is the resolve note of an OK notice: "Fixed at 10:42 (was down
// 12 min)." and what was fixed, in this computer's time.
func fixedText(n model.Notice, now time.Time) string {
	end := now
	if t, err := time.Parse(time.RFC3339, n.At); err == nil {
		end = t
	}
	s := "Fixed at " + end.Local().Format("15:04")
	if from, err := time.Parse(time.RFC3339, n.OpenedAt); err == nil && !end.Before(from) {
		s += " (was down " + downFor(end.Sub(from)) + ")"
	}
	s += "."
	if m := strings.TrimSpace(n.Message); m != "" {
		s += " " + m
	}
	return clip(s, protocol.MaxAlertText)
}

// downFor is a duration as the page says it: "less than a minute", "12
// min", "2 h 5 min".
func downFor(d time.Duration) string {
	m := int(d / time.Minute)
	switch {
	case m < 1:
		return "less than a minute"
	case m < 60:
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}
