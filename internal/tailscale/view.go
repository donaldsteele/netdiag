package tailscale

import (
	"fmt"
	"strings"
	"time"

	"netdiag/internal/model"
)

// ViewOf turns a Tailscale reading into the page's view: the tailnet
// named by its suffix only, phones and soon-expiring keys marked, and the
// Tailscale card.
func ViewOf(st Status, err error, now time.Time) model.TailnetView {
	v := model.TailnetView{
		State: st.State, Version: st.Version, Suffix: st.Suffix, MagicDNS: st.MagicDNS,
		Devices: []model.TailnetDevice{}, Health: append([]string{}, st.Health...),
		CheckedAt: now.UTC().Format(time.RFC3339), AdminTarget: model.TailnetAdminTarget,
	}
	if err != nil {
		v.State = model.TailnetStopped
		v.Error = err.Error()
	}
	if v.State == "" {
		v.State = model.TailnetNotInstalled
	}
	if st.Self != nil {
		d := tailnetDevice(*st.Self, now)
		v.Self = &d
	}
	for _, d := range st.Devices {
		v.Devices = append(v.Devices, tailnetDevice(d, now))
	}
	v.Card = tailnetCard(v, now)
	return v
}

func tailnetDevice(d Device, now time.Time) model.TailnetDevice {
	out := model.TailnetDevice{
		ID: d.ID, Name: d.Name, DNSName: d.DNSName, OS: d.OS, IPs: append([]string{}, d.IPs...),
		Online: d.Online, Active: d.Active, Path: d.Path, Endpoint: d.Endpoint, Relay: d.Relay,
		ExitNode: d.ExitNode, ExitNodeOption: d.ExitNodeOption,
		SubnetRoutes: append([]string{}, d.SubnetRoutes...), Tags: append([]string{}, d.Tags...),
		Phone: isPhoneOS(d.OS),
	}
	if !d.LastSeen.IsZero() {
		out.LastSeen = d.LastSeen.UTC().Format(time.RFC3339)
	}
	if !d.KeyExpiry.IsZero() {
		out.KeyExpiry = d.KeyExpiry.UTC().Format(time.RFC3339)
		out.KeyExpiresSoon = d.KeyExpiry.Sub(now) < model.TailnetKeySoonDays*24*time.Hour
	}
	return out
}

func isPhoneOS(os string) bool {
	switch strings.ToLower(os) {
	case "ios", "ipados", "android":
		return true
	}
	return false
}

// tailnetCard is the Tailscale card of the Health view: a status word, one
// line, and what it means and what to try when it is not fine.
func tailnetCard(v model.TailnetView, now time.Time) model.HealthCheck {
	c := model.HealthCheck{ID: "tailscale", Label: "Tailscale"}
	switch v.State {
	case model.TailnetNotInstalled:
		c.Status, c.Detail = model.HealthInfo, "Tailscale isn't on this computer."
		return c
	case model.TailnetNeedsLogin:
		c.Status, c.Detail = model.HealthWarn, "Tailscale needs you to sign in."
		c.Advice = &model.Advice{
			Meaning: "This computer isn't signed in to your tailnet, so the other computers on it can't be reached through Tailscale.",
			Steps:   []string{"Open the Tailscale app on this computer and sign in.", "Sign in with the same account as your other devices."},
		}
		return c
	case model.TailnetStopped:
		c.Status, c.Detail = model.HealthWarn, "Tailscale is installed but not connected."
		if v.Error != "" {
			c.Status, c.Detail = model.HealthUnavailable, "netdiag couldn't read Tailscale on this computer."
			c.Advice = &model.Advice{
				Meaning:   "Tailscale is on this computer, but it didn't answer netdiag.",
				Steps:     []string{"Check that the Tailscale app is running.", "Click Check again in a moment."},
				Technical: "raw: " + v.Error,
			}
			return c
		}
		c.Advice = &model.Advice{
			Meaning: "This computer isn't connected to your tailnet, so the other computers on it can't be reached through Tailscale.",
			Steps:   []string{"Open the Tailscale app on this computer and connect.", "If it asks you to sign in, sign in with your tailnet account."},
		}
		return c
	}
	online := 0
	for _, d := range v.Devices {
		if d.Online {
			online++
		}
	}
	where := "your tailnet"
	if v.Suffix != "" {
		where = v.Suffix
	}
	c.Status = model.HealthOK
	c.Detail = fmt.Sprintf("Connected to %s. %d of %d other devices online.", where, online, len(v.Devices))
	if v.Self != nil && v.Self.KeyExpiresSoon {
		c.Status = model.HealthWarn
		c.Detail = "This computer's Tailscale key expires " + whenText(v.Self.KeyExpiry, now) + "."
		c.Advice = &model.Advice{
			Meaning: "When the key expires, this computer drops off your tailnet until someone signs in again.",
			Steps:   []string{"Sign in to Tailscale again on this computer.", "Or turn off key expiry for this computer in the Tailscale admin console."},
		}
		return c
	}
	if len(v.Health) > 0 {
		c.Status = model.HealthWarn
		c.Detail = "Tailscale reports a problem: " + v.Health[0]
		c.Advice = &model.Advice{
			Meaning:   "Tailscale itself noticed something that may stop it from working well.",
			Steps:     []string{"Read the message below and open the Tailscale app.", "Restart Tailscale if the message stays."},
			Technical: "raw: " + strings.Join(v.Health, "; "),
		}
	}
	return c
}

// whenText says when an RFC 3339 time is, in days ("in 3 days", "today").
func whenText(rfc string, now time.Time) string {
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
	default:
		return fmt.Sprintf("in %d days (%s)", days, t.Local().Format("2 Jan"))
	}
}
