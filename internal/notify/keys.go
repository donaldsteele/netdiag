package notify

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"netdiag/internal/model"
)

// MaxKeyLen is the longest alert key. bubbles caps an alert's dedupe_key at
// 200 characters (MaxAlertDedupeKey, bubbles pkg/protocol/alert.go), and the
// chat card's dedupe_key is exactly the alert key.
const MaxKeyLen = 200

// Key is the alert key of a problem on host h: <trigger>|<id>|<part>, where
// id is the host's MAC, or "ip:"+IP while the MAC is unknown, and part tells
// apart several problems of one trigger on one device (empty for offline,
// the IP for a new device). A key longer than MaxKeyLen is shortened to
// <trigger>|<id>|h:<hash>, which is just as stable. The engine opens a
// problem under the "ip:" key instead while another device behind the same
// MAC (a range extender) has this key open.
func Key(trigger string, h model.Host, part string) string {
	return keyFor(trigger, hostID(h), part)
}

// HostKeys lists the alert keys a problem of trigger on host h may be open
// under: Key first, then the "ip:" key a problem opened under before the
// MAC was known, or while another device behind the same MAC had the MAC
// key open. The Problems card (internal/problems) matches the ledger with
// them, so it shows the key team chat has.
func HostKeys(trigger string, h model.Host, part string) []string {
	ids := hostIDs(h)
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = keyFor(trigger, id, part)
	}
	return out
}

// SupplyParts are the key parts of a printer's supplies, in list order (see
// Key): the same parts the engine opens supplyLow problems under.
func SupplyParts(ss []model.Supply) []string { return supplyParts(ss) }

// AlertPart is the key part of one printer alert (see Key).
func AlertPart(a model.Alert) string { return alertPart(a) }

// HealthKey is the alert key of a failed health check:
// healthFailed||<check id>.
func HealthKey(check string) string {
	return keyFor(model.TriggerHealthFailed, "", check)
}

// ConflictKey is the alert key of a conflict, ipConflict|<kind>|<anchor>
// (v0.6-D7). The anchor is what stays the same while the conflict changes:
// the shared IP for mac_flap, the MAC for mac_multi_ip and a hash of the
// name for duplicate_name. The conflict id hashes every IP and MAC involved,
// so a conflict that gains a MAC gets a new id but keeps its key, and its
// chat card is neither resolved nor posted again.
func ConflictKey(c model.Conflict) string {
	var anchor string
	switch c.Kind {
	case model.ConflictMACFlap:
		anchor = first(c.IPs)
	case model.ConflictMACMultiIP:
		anchor = strings.ToUpper(first(c.MACs))
	case model.ConflictDuplicateName:
		anchor = shortHash(c.Name)
	default:
		anchor = c.ID
	}
	return keyFor(model.TriggerIPConflict, c.Kind, anchor)
}

func keyFor(trigger, id, part string) string {
	k := trigger + "|" + id + "|" + part
	if len(k) <= MaxKeyLen {
		return k
	}
	sum := sha256.Sum256([]byte(k))
	h := hex.EncodeToString(sum[:])[:32]
	if k = trigger + "|" + id + "|h:" + h; len(k) <= MaxKeyLen {
		return k
	}
	return trigger + "||h:" + h
}

// macOf is h's MAC as keys carry it (upper case; "" when unknown).
func macOf(h model.Host) string { return strings.ToUpper(strings.TrimSpace(h.MAC)) }

// hostID is the id part of a host's keys: its MAC, else "ip:"+IP.
func hostID(h model.Host) string {
	if m := macOf(h); m != "" {
		return m
	}
	return "ip:" + h.IP
}

// hostIDs lists the ids h's problems may be open under: its MAC key first,
// then the IP key a problem opened under before the MAC was known (or while
// another device behind the same MAC had the MAC key open).
func hostIDs(h model.Host) []string {
	if id := hostID(h); !strings.HasPrefix(id, "ip:") {
		return []string{id, "ip:" + h.IP}
	}
	return []string{"ip:" + h.IP}
}

// shortHash is the first 8 hex digits of the SHA-256 of s: a short, stable
// stand-in for free text such as a supply or alert description.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

// supplyParts are the key parts of a printer's low supplies, in list order:
// the hash of the description, plus ".2", ".3" and so on for the second and
// later supplies with the same description, so two supplies named alike are
// two problems.
func supplyParts(ss []model.Supply) []string {
	out := make([]string, len(ss))
	seen := map[string]int{}
	for i, s := range ss {
		seen[s.Description]++
		out[i] = shortHash(s.Description)
		if n := seen[s.Description]; n > 1 {
			out[i] += "." + strconv.Itoa(n)
		}
	}
	return out
}

// alertPart is the key part of one printer alert.
func alertPart(a model.Alert) string { return a.Code + "|" + shortHash(a.Description) }

// devicePart is the key part of a new device: its IP when the key has its
// MAC, because several devices can answer with one MAC (a range extender)
// and each of them is a new device; "" when the key already has the IP.
func devicePart(h model.Host) string {
	if macOf(h) == "" {
		return ""
	}
	return h.IP
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
