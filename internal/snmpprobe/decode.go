package snmpprobe

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// text decodes a PDU to a display string; binary/invalid UTF-8 becomes hex.
func text(p PDU) string {
	switch p.Kind {
	case KindNull:
		return ""
	case KindInt, KindTicks:
		return strconv.FormatInt(p.Int, 10)
	case KindOID:
		return p.Str
	}
	b := []byte(strings.TrimRight(string(p.Bytes), "\x00"))
	if utf8.Valid(b) {
		s := string(b)
		ok := true
		for _, r := range s {
			if r == unicode.ReplacementChar || (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') {
				ok = false
				break
			}
		}
		if ok {
			return strings.TrimSpace(s)
		}
	}
	return hex.EncodeToString(p.Bytes)
}

func num(p PDU) (int64, bool) {
	switch p.Kind {
	case KindInt, KindTicks:
		return p.Int, true
	case KindOctet:
		if n, err := strconv.ParseInt(strings.TrimSpace(string(p.Bytes)), 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func uptime(p PDU) string {
	t, ok := num(p)
	if !ok {
		return ""
	}
	s := t / 100
	d, h, m := s/86400, s%86400/3600, s%3600/60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm %ds", m, s%60)
	}
}

func deviceStatus(n int64) string {
	switch n {
	case 2:
		return "running"
	case 3:
		return "warning"
	case 4:
		return "testing"
	case 5:
		return "down"
	}
	return "unknown"
}

func printerStatus(n int64) string {
	switch n {
	case 1:
		return "other"
	case 3:
		return "idle"
	case 4:
		return "printing"
	case 5:
		return "warmup"
	}
	return "unknown"
}

var errStateNames = []string{
	"lowPaper", "noPaper", "lowToner", "noToner", "doorOpen", "jammed", "offline",
	"serviceRequested", "inputTrayMissing", "outputTrayMissing", "markerSupplyMissing",
	"outputNearFull", "outputFull", "inputTrayEmpty", "overduePreventMaintenance",
}

// errorStates decodes the hrPrinterDetectedErrorState bitmask (bit 0 = MSB of first octet).
func errorStates(b []byte) []string {
	var out []string
	for i, name := range errStateNames {
		idx := i / 8
		if idx >= len(b) {
			break
		}
		if b[idx]&(0x80>>(uint(i)%8)) != 0 {
			out = append(out, name)
		}
	}
	return out
}

var supplyTypes = map[int64]string{
	3: "toner", 4: "wasteToner", 5: "ink", 6: "inkCartridge", 7: "inkRibbon", 8: "wasteInk",
	9: "opc", 10: "developer", 11: "fuserOil", 15: "fuser", 16: "coronaWire", 18: "cleanerUnit",
	19: "fuserCleaningPad", 20: "transferUnit", 21: "tonerCartridge", 22: "fuserOiler",
	23: "water", 24: "wasteWater", 26: "wastePaper", 32: "staples",
}

var supplyUnits = map[int64]string{
	3: "tenThousandthsOfInches", 4: "micrometers", 7: "impressions", 8: "sheets", 11: "hours",
	12: "thousandthsOfOunces", 13: "tenthsOfGrams", 14: "hundredthsOfFluidOunces",
	15: "tenthsOfMilliliters", 16: "feet", 17: "meters", 18: "items", 19: "percent",
}

// supplyPercent returns percent (-1 unknown) and state from level/max.
// level -2 = unknown, -3 = "some remaining"; max <= 0 = unknown capacity.
func supplyPercent(level, max int64) (int, string) {
	switch {
	case level == -3:
		return -1, "ok"
	case level < 0, max <= 0:
		return -1, "unknown"
	}
	pct := int(level * 100 / max)
	if pct > 100 {
		pct = 100
	}
	switch {
	case level == 0:
		return 0, "empty"
	case pct <= 15:
		return pct, "low"
	}
	return pct, "ok"
}

func severity(n int64) string {
	switch n {
	case 3:
		return "critical"
	case 4, 5:
		return "warning"
	}
	return "other"
}

func coverStatus(n int64) string {
	switch n {
	case 3, 5:
		return "open"
	case 4, 6:
		return "closed"
	}
	return "unknown"
}

func inputStatus(n int64) string {
	if n == 0 {
		return "ok"
	}
	return "status " + strconv.FormatInt(n, 10)
}

func colorFromText(s string) string {
	s = strings.ToLower(s)
	for _, c := range []string{"black", "cyan", "magenta", "yellow"} {
		if strings.Contains(s, c) {
			return c
		}
	}
	return ""
}

// column indexes PDUs under "<root>.<col>." by the remaining index suffix.
func column(pdus []PDU, root string, col int) map[string]PDU {
	prefix := root + "." + strconv.Itoa(col) + "."
	m := map[string]PDU{}
	for _, p := range pdus {
		if strings.HasPrefix(p.OID, prefix) {
			m[strings.TrimPrefix(p.OID, prefix)] = p
		}
	}
	return m
}

// keysOf returns the union of keys of the maps, ordered numerically per component.
func keysOf(ms ...map[string]PDU) []string {
	set := map[string]bool{}
	for _, m := range ms {
		for k := range m {
			set[k] = true
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return lessIdx(keys[i], keys[j]) })
	return keys
}

func lessIdx(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, e1 := strconv.Atoi(as[i])
		y, e2 := strconv.Atoi(bs[i])
		if e1 != nil || e2 != nil {
			if as[i] != bs[i] {
				return as[i] < bs[i]
			}
			continue
		}
		if x != y {
			return x < y
		}
	}
	return len(as) < len(bs)
}
