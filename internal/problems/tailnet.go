package problems

import (
	"time"

	"netdiag/internal/advice"
	"netdiag/internal/model"
	"netdiag/internal/notify"
)

// tailnetRecent is how long after a tailnet device was last online it
// still counts as having just dropped off (an older one was simply off).
const tailnetRecent = 24 * time.Hour

// addTailnet adds the tailnet items of the Problems right now card (v0.6.1):
// Tailscale not connected on this computer (a note: it may be off on
// purpose), a tailnet computer that dropped off in the last day (not a
// phone), and Tailscale keys about to expire. Each uses its alert key, so
// an item netdiag alerted on is matched with its ledger entry.
func addTailnet(t *model.TailnetSnapshot, tr model.SettingsTriggers, add func(Problem)) {
	if t == nil || t.State == model.TailnetNotInstalled {
		return
	}
	item := func(key, kind, sev, title, detail, name, ip string) {
		n := model.Notice{Trigger: kind, Name: name, IP: ip, Message: title + ": " + detail}
		p := Problem{Key: key, Kind: kind, Severity: sev, Title: title, Detail: detail, Advice: advice.Notice(n, nil, advice.Ctx{})}
		p.keys = []string{key}
		add(p)
	}
	if t.State != model.TailnetRunning {
		if tr.TailscaleDown {
			detail := "Tailscale is not connected on this computer"
			if t.State == model.TailnetNeedsLogin {
				detail = "Tailscale on this computer needs someone to sign in"
			}
			item(notify.TailscaleDownKey(), model.TriggerTailscaleDown, SevInfo, "Tailscale not connected", detail, "", "")
		}
		return
	}
	now := time.Now()
	if tr.TailnetDeviceOffline {
		for _, d := range t.Devices {
			if d.Online || d.Phone || d.LastSeen == "" {
				continue
			}
			seen, err := time.Parse(time.RFC3339, d.LastSeen)
			if err != nil || now.Sub(seen) > tailnetRecent {
				continue
			}
			item(notify.TailnetDeviceKey(d.ID), model.TriggerTailnetDeviceOffline, SevWarning, "Tailnet device offline", d.Name, d.Name, firstOf(d.IPs))
		}
	}
	if tr.TailnetKeyExpiring {
		if s := t.Self; s != nil && s.KeyExpiresSoon {
			item(notify.TailnetKeyKey("self"), model.TriggerTailnetKeyExpiring, SevWarning, "Tailscale key expiring", "This computer", s.Name, firstOf(s.IPs))
		}
		for _, d := range t.Devices {
			if d.KeyExpiresSoon {
				item(notify.TailnetKeyKey(d.ID), model.TriggerTailnetKeyExpiring, SevWarning, "Tailscale key expiring", d.Name, d.Name, firstOf(d.IPs))
			}
		}
	}
}

func firstOf(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}
