package notify

import "netdiag/internal/model"

// Banner severities. The chat router maps warn to bubbles' "warning".
const (
	sevInfo     = "info"
	sevWarn     = "warn"
	sevCritical = "critical"
)

// sevCtx is what severityFor needs to know beyond the trigger.
type sevCtx struct {
	check   string // healthFailed: the check id
	printer string // printerAlert: the printer's own severity
	kind    string // ipConflict: the conflict kind
	swap    bool   // ipConflict: the devices were seen taking turns
}

// TriggerSeverity is the severity map's answer for a trigger that needs no
// more context (offline, supplies, new devices, the storm guard).
func TriggerSeverity(trigger string) string { return severityFor(trigger, sevCtx{}) }

// HealthSeverity is the severity of failed health check check.
func HealthSeverity(check string) string {
	return severityFor(model.TriggerHealthFailed, sevCtx{check: check})
}

// AlertSeverity is the severity of printer alert a.
func AlertSeverity(a model.Alert) string {
	return severityFor(model.TriggerPrinterAlert, sevCtx{printer: a.Severity})
}

// ConflictSeverity is the severity of conflict c. The analyzer marks a
// mac_flap "fail" only when the MACs were seen taking turns (a swap), not on
// a single change.
func ConflictSeverity(c model.Conflict) string {
	return severityFor(model.TriggerIPConflict, sevCtx{kind: c.Kind, swap: c.Kind == model.ConflictMACFlap && c.Severity == "fail"})
}

// severityFor is the one severity map (v0.6 §4.7, README §8.2) for local
// banners and team chat alike (v0.6-D3). Critical reaches chat members who
// only want mentions, so only a dead gateway, a printer that declares an
// error and two devices seen swapping one address are critical.
func severityFor(trigger string, ctx sevCtx) string {
	switch trigger {
	case model.TriggerPrinterOffline, model.TriggerSupplyLow, model.TriggerNetdiagLink:
		return sevWarn
	case model.TriggerPrinterAlert:
		if ctx.printer == "critical" || ctx.printer == "error" {
			return sevCritical
		}
		return sevWarn
	case model.TriggerIPConflict:
		if ctx.kind == model.ConflictMACFlap && ctx.swap {
			return sevCritical
		}
		return sevWarn
	case model.TriggerHealthFailed:
		if ctx.check == "gateway" {
			return sevCritical
		}
		return sevWarn
	}
	// deviceOffline (off by default), newDevice and the chat test alert.
	return sevInfo
}
