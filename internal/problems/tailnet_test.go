package problems

import (
	"testing"
	"time"

	"netdiag/internal/model"
	"netdiag/internal/notify"
	"netdiag/internal/store"
)

func TestTailnetItems(t *testing.T) {
	set := store.DefaultSettings()
	recent := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	old := time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339)
	r := model.ScanResult{Tailnet: &model.TailnetSnapshot{
		State: model.TailnetRunning,
		Self:  &model.TailnetDevice{ID: "self", Name: "office-pc", KeyExpiresSoon: true},
		Devices: []model.TailnetDevice{
			{ID: "n1", Name: "nas", IPs: []string{"100.64.12.34"}, LastSeen: recent},
			{ID: "p1", Name: "phone", Phone: true, LastSeen: recent},
			{ID: "o1", Name: "old-box", LastSeen: old},
			{ID: "v1", Name: "cloud-vm", Online: true, KeyExpiresSoon: true},
		},
	}}
	got := map[string]Problem{}
	for _, p := range Current(r, set) {
		got[p.Key] = p
	}
	if p, ok := got[notify.TailnetDeviceKey("n1")]; !ok || p.Severity != SevWarning || p.Detail != "nas" || p.Advice == nil {
		t.Errorf("nas offline item: %+v", p)
	}
	for _, id := range []string{"p1", "o1"} {
		if _, ok := got[notify.TailnetDeviceKey(id)]; ok {
			t.Errorf("%s listed as offline (phone, or off for days)", id)
		}
	}
	if _, ok := got[notify.TailnetKeyKey("self")]; !ok {
		t.Error("this computer's key not listed")
	}
	if _, ok := got[notify.TailnetKeyKey("v1")]; !ok {
		t.Error("cloud-vm's key not listed")
	}

	// Tailscale off: one note, nothing else.
	r.Tailnet.State = model.TailnetStopped
	ps := Current(r, set)
	if len(ps) != 1 || ps[0].Key != notify.TailscaleDownKey() || ps[0].Severity != SevInfo {
		t.Fatalf("stopped: %+v", ps)
	}
	// Switched off, or not installed: nothing.
	set.Alerts.Triggers.TailscaleDown = false
	if ps := Current(r, set); len(ps) != 0 {
		t.Fatalf("trigger off: %+v", ps)
	}
	r.Tailnet.State = model.TailnetNotInstalled
	set.Alerts.Triggers.TailscaleDown = true
	if ps := Current(r, set); len(ps) != 0 {
		t.Fatalf("not installed: %+v", ps)
	}
}
