package notify

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"netdiag/internal/model"
)

// MaxKeyLen is the longest alert key. bubbles caps an alert's dedupe_key at
// 200 characters (MaxAlertDedupeKey, bubbles pkg/protocol/alert.go), and the
// chat card's dedupe_key is exactly the alert key.
const MaxKeyLen = 200

// Key is the alert key of a problem on host h: <trigger>|<id>|<part>, where
// id is the host's MAC, or "ip:"+IP while the MAC is unknown, and part tells
// apart several problems of one trigger on one device (empty for offline).
// A key longer than MaxKeyLen is shortened to <trigger>|<id>|h:<hash>, which
// is just as stable.
func Key(trigger string, h model.Host, part string) string {
	return keyFor(trigger, hostID(h), part)
}

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

// hostID is the id part of a host's keys: its MAC, else "ip:"+IP.
func hostID(h model.Host) string {
	if m := strings.ToUpper(strings.TrimSpace(h.MAC)); m != "" {
		return m
	}
	return "ip:" + h.IP
}

// hostIDs lists the ids h's problems may be open under: its MAC key first,
// then the IP key a problem opened under before the MAC was known.
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

// supplyPart and alertPart are the key parts of a low supply and of one
// printer alert.
func supplyPart(desc string) string { return shortHash(desc) }

func alertPart(a model.Alert) string { return a.Code + "|" + shortHash(a.Description) }

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}
