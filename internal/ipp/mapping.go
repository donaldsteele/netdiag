package ipp

import (
	"strconv"
	"strings"

	"netdiag/internal/model"
)

// SourceIPP is the PrinterInfo source value for data that came from IPP.
const SourceIPP = "ipp"

// lowDefault is the low-supply threshold used when the printer does not
// report marker-low-levels (matches the SNMP mapping).
const lowDefault = 15

// Result is the IPP view of a printer. Source/State/StateReasons mirror the
// fields now set directly on Printer (PrinterInfo.Source/State/StateReasons);
// they are kept here for convenience.
type Result struct {
	Printer      *model.PrinterInfo
	Source       string   // always "ipp"
	State        string   // idle|processing|stopped|"" (unknown)
	StateReasons []string // printer-state-reasons minus "none"
	Name         string   // printer-name
	Location     string   // printer-location
	Errors       []string // "ipp: ..." partial-failure notes
}

// Map converts a Get-Printer-Attributes response into a Result. It never
// fails; missing attributes leave zero values (Level/Max -2 for unknown).
func Map(m *Message) *Result {
	a := attrs(m)
	p := &model.PrinterInfo{
		ConsoleDisplay: []string{},
		Supplies:       []model.Supply{},
		InputTrays:     []model.InputTray{},
		Alerts:         []model.Alert{},
		Covers:         []model.Cover{},
	}
	p.Source = SourceIPP
	r := &Result{Printer: p, Source: SourceIPP, StateReasons: []string{},
		Name: a.str("printer-name"), Location: a.str("printer-location")}

	switch a.int("printer-state", 0) {
	case 3:
		r.State = "idle"
	case 4:
		r.State = "processing"
	case 5:
		r.State = "stopped"
	}

	dev := parseDeviceID(a.str("printer-device-id"))
	p.Model = a.str("printer-make-and-model")
	if p.Model == "" {
		p.Model = strings.TrimSpace(firstOf(dev, "MFG", "MANUFACTURER") + " " + firstOf(dev, "MDL", "MODEL"))
	}
	if p.Model == "" {
		p.Model = a.str("printer-info")
	}
	p.Serial = firstOf(dev, "SN", "SERN", "SERIALNUMBER")
	if v, ok := a.intOK("printer-impressions-completed"); ok && v >= 0 {
		p.PageCount = int64(v)
	}
	if msg := strings.TrimSpace(a.str("printer-state-message")); msg != "" {
		p.ConsoleDisplay = append(p.ConsoleDisplay, msg)
	}

	for _, reason := range a.strs("printer-state-reasons") {
		reason = strings.TrimSpace(reason)
		if reason == "" || reason == "none" {
			continue
		}
		r.StateReasons = append(r.StateReasons, reason)
		base, sev := splitReason(reason)
		p.Alerts = append(p.Alerts, model.Alert{Severity: sev, Code: reason, Description: humanize(base)})
		switch base {
		case "door-open", "cover-open", "interlock-open":
			p.Covers = append(p.Covers, model.Cover{Description: humanize(strings.TrimSuffix(base, "-open")), Status: "open"})
		}
	}

	names := a.strs("marker-names")
	levels := a.ints("marker-levels")
	lows := a.ints("marker-low-levels")
	colors := a.strs("marker-colors")
	types := a.strs("marker-types")
	for i, name := range names {
		s := model.Supply{Description: name, Level: -2, Max: 100, Percent: -1, Unit: "percent", State: "unknown"}
		if i < len(types) {
			s.Type = camel(types[i])
		}
		if i < len(colors) {
			s.Color = colorName(colors[i])
		}
		if s.Color == "" {
			s.Color = colorFromText(name)
		}
		if i < len(levels) {
			lv := int(levels[i])
			s.Level = lv
			low := lowDefault
			if i < len(lows) && lows[i] > 0 && lows[i] < 100 {
				low = int(lows[i])
			}
			switch {
			case lv == -3: // "some remaining"
				s.State = "ok"
			case lv < 0 || lv > 100:
				s.State = "unknown"
			case lv == 0:
				s.Percent, s.State = 0, "empty"
			case lv <= low:
				s.Percent, s.State = lv, "low"
			default:
				s.Percent, s.State = lv, "ok"
			}
		}
		p.Supplies = append(p.Supplies, s)
	}

	for _, t := range a.strs("printer-input-tray") {
		if tr, ok := parseTray(t); ok {
			p.InputTrays = append(p.InputTrays, tr)
		}
	}
	p.State = r.State
	p.StateReasons = r.StateReasons
	return r
}

// splitReason strips the -error/-warning/-report suffix and maps it to an
// alert severity. Per RFC 8011 5.4.12 a reason without suffix is an error.
func splitReason(r string) (base, severity string) {
	switch {
	case strings.HasSuffix(r, "-error"):
		return strings.TrimSuffix(r, "-error"), "critical"
	case strings.HasSuffix(r, "-warning"):
		return strings.TrimSuffix(r, "-warning"), "warning"
	case strings.HasSuffix(r, "-report"):
		return strings.TrimSuffix(r, "-report"), "other"
	}
	return r, "critical"
}

// humanize turns "media-empty" into "Media empty".
func humanize(k string) string {
	s := strings.ReplaceAll(k, "-", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// camel turns "toner-cartridge" into "tonerCartridge" (SNMP naming style).
func camel(k string) string {
	parts := strings.Split(strings.TrimSpace(k), "-")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

var hexColors = map[string]string{
	"#000000": "black", "#00ffff": "cyan", "#ff00ff": "magenta", "#ffff00": "yellow",
	"#ffffff": "white", "#808080": "gray",
}

// colorName maps marker-colors ("#00FFFF", "none") to a color word. Unknown
// or multi-color values are returned lower-cased as-is.
func colorName(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	if c == "" || c == "none" {
		return ""
	}
	if n, ok := hexColors[c]; ok {
		return n
	}
	return c
}

func colorFromText(s string) string {
	low := strings.ToLower(s)
	for _, c := range []string{"black", "cyan", "magenta", "yellow"} {
		if strings.Contains(low, c) {
			return c
		}
	}
	return ""
}

// parseDeviceID parses an IEEE 1284 device ID ("MFG:HP;MDL:LaserJet;").
// Keys are upper-cased.
func parseDeviceID(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		k = strings.ToUpper(strings.TrimSpace(k))
		if _, dup := out[k]; !dup && k != "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

func firstOf(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := m[k]; v != "" {
			return v
		}
	}
	return ""
}

// parseTray parses a PWG 5100.13 printer-input-tray value such as
// "type=sheetFeedAutoRemovableTray;mediafeed=0;maxcapacity=250;level=-2;status=0;name=Tray 1;".
func parseTray(s string) (model.InputTray, bool) {
	t := model.InputTray{Level: -2, Max: -2}
	kv := map[string]string{}
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(part, "=")
		if ok {
			kv[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	if len(kv) == 0 {
		return t, false
	}
	t.Name = kv["name"]
	if t.Name == "" {
		t.Name = kv["type"]
	}
	t.MediaName = kv["medianame"]
	if n, err := strconv.Atoi(kv["level"]); err == nil {
		t.Level = n
	}
	if n, err := strconv.Atoi(kv["maxcapacity"]); err == nil {
		t.Max = n
	}
	switch st := kv["status"]; st {
	case "", "0":
		t.Status = "ok"
	default:
		t.Status = "status " + st
	}
	return t, true
}

// attrSet is the merged printer-group attributes (first occurrence wins).
type attrSet map[string]*Attribute

func attrs(m *Message) attrSet {
	out := attrSet{}
	if m == nil {
		return out
	}
	for gi := range m.Groups {
		g := &m.Groups[gi]
		if g.Tag != TagPrinter {
			continue
		}
		for ai := range g.Attrs {
			if _, dup := out[g.Attrs[ai].Name]; !dup {
				out[g.Attrs[ai].Name] = &g.Attrs[ai]
			}
		}
	}
	return out
}

func isStringTag(t byte) bool {
	return t == TagOctetString || t == TagTextWithLang || t == TagNameWithLang || (t >= 0x40 && t <= 0x5F)
}

func (a attrSet) strs(name string) []string {
	at := a[name]
	if at == nil {
		return nil
	}
	out := make([]string, 0, len(at.Values))
	for _, v := range at.Values {
		if isStringTag(v.Tag) {
			out = append(out, v.Str)
		}
	}
	return out
}

func (a attrSet) str(name string) string {
	if s := a.strs(name); len(s) > 0 {
		return strings.TrimSpace(s[0])
	}
	return ""
}

func (a attrSet) ints(name string) []int32 {
	at := a[name]
	if at == nil {
		return nil
	}
	out := make([]int32, 0, len(at.Values))
	for _, v := range at.Values {
		switch v.Tag {
		case TagInteger, TagEnum:
			out = append(out, v.Int)
		default:
			out = append(out, -2) // keep positions aligned with marker-names
		}
	}
	return out
}

func (a attrSet) intOK(name string) (int32, bool) {
	at := a[name]
	if at == nil || len(at.Values) == 0 {
		return 0, false
	}
	if v := at.Values[0]; v.Tag == TagInteger || v.Tag == TagEnum {
		return v.Int, true
	}
	return 0, false
}

func (a attrSet) int(name string, def int32) int32 {
	if v, ok := a.intOK(name); ok {
		return v
	}
	return def
}
