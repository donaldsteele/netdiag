package notify

import (
	"fmt"
	"time"

	"netdiag/internal/model"
)

// Tailnet alerts (v0.6.1). Tailscale is read from this computer's own
// Tailscale service, so none of them is held by the storm guard.

// TailnetDownRuns is how many readings in a row Tailscale must be down, or
// a tailnet device offline, before netdiag says so; TailnetUpRuns how many
// readings it must be back before Tailscale's fixed notice.
const (
	TailnetDownRuns = 2
	TailnetUpRuns   = 2
)

// TailscaleDownKey is the alert key of Tailscale stopping on this computer.
func TailscaleDownKey() string { return keyFor(model.TriggerTailscaleDown, "", "") }

// TailnetDeviceKey is the alert key of a tailnet device going offline.
func TailnetDeviceKey(id string) string { return keyFor(model.TriggerTailnetDeviceOffline, "", id) }

// TailnetKeyKey is the alert key of a Tailscale key about to expire (id is
// the device's id, or "self" for this computer).
func TailnetKeyKey(id string) string { return keyFor(model.TriggerTailnetKeyExpiring, "", id) }

// tailnetWatch is the engine's memory of the tailnet between readings.
type tailnetWatch struct {
	seenRunning bool            // Tailscale ran at some point while watching
	down, up    int             // readings in a row Tailscale was down / back
	online      map[string]bool // devices seen online while watching
	offline     map[string]int  // readings in a row each was offline since
}

// ObserveTailnet takes one reading of the tailnet (nil: Tailscale isn't on
// this computer) and returns the notices it causes:
//   - Tailscale stopped: it ran earlier in this watch and is now stopped,
//     signed out or gone for TailnetDownRuns readings; fixed after
//     TailnetUpRuns readings running.
//   - A tailnet device offline: a device that is not a phone or tablet, seen
//     online earlier in this watch, offline for TailnetDownRuns readings;
//     fixed when it is online again or leaves the tailnet.
//   - A key about to expire: this computer's or a device's key expires
//     within model.TailnetKeySoonDays; fixed when it no longer does or the
//     device leaves.
//
// Problems carried over from an earlier run (the ledger) close here too, on
// the first reading that shows them gone.
func (e *Engine) ObserveTailnet(snap *model.TailnetSnapshot) []model.Notice {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tn.online == nil {
		e.tn.online, e.tn.offline = map[string]bool{}, map[string]int{}
	}
	var out []model.Notice
	running := snap != nil && snap.State == model.TailnetRunning

	// Tailscale on this computer.
	downKey := TailscaleDownKey()
	if running {
		e.tn.seenRunning, e.tn.down = true, 0
		if f := e.open[downKey]; f != nil {
			if e.tn.up++; e.tn.up >= TailnetUpRuns {
				e.tn.up = 0
				out = e.emitOK(out, f, "Tailscale connected again", "Tailscale is connected again on this computer.")
			}
		}
	} else if e.tn.seenRunning {
		e.tn.up = 0
		if e.tn.down++; e.tn.down >= TailnetDownRuns {
			out = e.emit(out, model.Notice{Trigger: model.TriggerTailscaleDown,
				Severity: severityFor(model.TriggerTailscaleDown, sevCtx{}),
				Title:    "Tailscale stopped", Message: tailscaleDownMessage(snap), Key: downKey, Detail: "Tailscale"}, "Tailscale")
		}
	}
	if !running {
		// Device state means nothing while Tailscale is off here.
		return out
	}

	present := map[string]bool{}
	devices := append([]model.TailnetDevice{}, snap.Devices...)
	for _, d := range devices {
		present[d.ID] = true
		key := TailnetDeviceKey(d.ID)
		name := tailnetName(d)
		switch {
		case d.Online:
			e.tn.online[d.ID] = true
			delete(e.tn.offline, d.ID)
			if f := e.open[key]; f != nil {
				out = e.emitOK(out, f, "Tailnet device back online", name+" is back online on your tailnet.")
			}
		case d.Phone || !e.tn.online[d.ID]:
			// Phones sleep; a device never seen online here was already off.
		default:
			if e.tn.offline[d.ID]++; e.tn.offline[d.ID] >= TailnetDownRuns {
				out = e.emit(out, model.Notice{Trigger: model.TriggerTailnetDeviceOffline,
					Severity: severityFor(model.TriggerTailnetDeviceOffline, sevCtx{}),
					Title:    "Tailnet device offline", Message: name + " went offline on your tailnet.",
					Key: key, Name: d.Name, IP: firstIP(d.IPs), Detail: name}, name)
			}
		}
	}

	// Keys about to expire: this computer's, then every device's.
	checkKey := func(id string, d model.TailnetDevice, whose string) {
		present[id] = true
		key := TailnetKeyKey(id)
		if d.KeyExpiresSoon {
			out = e.emit(out, model.Notice{Trigger: model.TriggerTailnetKeyExpiring,
				Severity: severityFor(model.TriggerTailnetKeyExpiring, sevCtx{}),
				Title:    "Tailscale key expiring", Message: fmt.Sprintf("%s Tailscale key expires %s.", whose, expiryWhen(d.KeyExpiry, e.now())),
				Key: key, Name: d.Name, IP: firstIP(d.IPs), Detail: whose}, whose)
		} else if f := e.open[key]; f != nil {
			out = e.emitOK(out, f, "Tailscale key renewed", whose+" Tailscale key no longer expires soon.")
		}
	}
	if snap.Self != nil {
		checkKey("self", *snap.Self, "This computer's")
	}
	for _, d := range devices {
		checkKey(d.ID, d, tailnetName(d)+"'s")
	}

	// Problems about devices that left the tailnet close.
	for _, f := range e.sorted() {
		switch f.Trigger {
		case model.TriggerTailnetDeviceOffline, model.TriggerTailnetKeyExpiring:
			if id := keyPart(f.Key); id != "" && !present[id] {
				delete(e.tn.online, id)
				delete(e.tn.offline, id)
				out = e.emitOK(out, f, "Tailnet device removed", f.Detail+" is no longer on your tailnet.")
			}
		}
	}
	return out
}

func tailscaleDownMessage(snap *model.TailnetSnapshot) string {
	switch {
	case snap == nil || snap.State == model.TailnetNotInstalled:
		return "Tailscale stopped running on this computer."
	case snap.State == model.TailnetNeedsLogin:
		return "Tailscale on this computer needs someone to sign in again."
	}
	return "Tailscale is no longer connected on this computer."
}

func tailnetName(d model.TailnetDevice) string {
	if d.Name != "" {
		return d.Name
	}
	return firstIP(d.IPs)
}

func firstIP(ips []string) string {
	if len(ips) > 0 {
		return ips[0]
	}
	return ""
}

// keyPart is the last part of a key (trigger|id|part).
func keyPart(key string) string {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '|' {
			return key[i+1:]
		}
	}
	return ""
}

func expiryWhen(rfc string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return "soon"
	}
	days := int(t.Sub(now).Hours() / 24)
	switch {
	case t.Before(now):
		return "now (it has expired)"
	case days == 0:
		return "today"
	case days == 1:
		return "tomorrow"
	}
	return fmt.Sprintf("in %d days, on %s", days, t.UTC().Format("2 January"))
}
