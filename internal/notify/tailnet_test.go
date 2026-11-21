package notify

import (
	"strings"
	"testing"
	"time"

	"netdiag/internal/model"
)

func tsSnap(state string, devs ...model.TailnetDevice) *model.TailnetSnapshot {
	return &model.TailnetSnapshot{State: state, Suffix: "tail1234.ts.net",
		Self: &model.TailnetDevice{ID: "self", Name: "office-pc", IPs: []string{"100.101.102.103"}, Online: true}, Devices: devs}
}

func dev(id, name string, online bool) model.TailnetDevice {
	return model.TailnetDevice{ID: id, Name: name, IPs: []string{"100.64.0." + id[len(id)-1:]}, Online: online}
}

func triggers(ns []model.Notice) string {
	var out []string
	for _, n := range ns {
		s := n.Trigger
		if n.OK {
			s += ":ok"
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func TestTailscaleDown(t *testing.T) {
	e := New(DefaultConfig(), nil)
	if ns := e.ObserveTailnet(tsSnap(model.TailnetStopped)); len(ns) != 0 {
		t.Fatalf("stopped from the start (never ran while watching): %v", triggers(ns))
	}
	e.ObserveTailnet(tsSnap(model.TailnetRunning))
	if ns := e.ObserveTailnet(tsSnap(model.TailnetStopped)); len(ns) != 0 {
		t.Fatalf("one reading down: %v", triggers(ns))
	}
	ns := e.ObserveTailnet(tsSnap(model.TailnetNeedsLogin))
	if triggers(ns) != model.TriggerTailscaleDown || !strings.Contains(ns[0].Message, "sign in") || ns[0].Severity != sevWarn {
		t.Fatalf("two readings down: %+v", ns)
	}
	if ns := e.ObserveTailnet(nil); len(ns) != 0 {
		t.Fatalf("already open: %v", triggers(ns))
	}
	if ns := e.ObserveTailnet(tsSnap(model.TailnetRunning)); len(ns) != 0 {
		t.Fatalf("one reading back: %v", triggers(ns))
	}
	if ns := e.ObserveTailnet(tsSnap(model.TailnetRunning)); triggers(ns) != model.TriggerTailscaleDown+":ok" {
		t.Fatalf("two readings back: %v", triggers(ns))
	}
}

func TestTailnetDeviceOffline(t *testing.T) {
	e := New(DefaultConfig(), nil)
	nas, phone, sleepy := dev("n1", "nas", true), dev("p2", "phone", true), dev("s3", "sleepy", false)
	phone.Phone = true
	e.ObserveTailnet(tsSnap(model.TailnetRunning, nas, phone, sleepy))
	nas.Online, phone.Online = false, false
	if ns := e.ObserveTailnet(tsSnap(model.TailnetRunning, nas, phone, sleepy)); len(ns) != 0 {
		t.Fatalf("one reading offline: %v", triggers(ns))
	}
	ns := e.ObserveTailnet(tsSnap(model.TailnetRunning, nas, phone, sleepy))
	if triggers(ns) != model.TriggerTailnetDeviceOffline || ns[0].Name != "nas" || ns[0].Key != TailnetDeviceKey("n1") || ns[0].IP == "" {
		t.Fatalf("nas offline (phone and never-online device quiet): %+v", ns)
	}
	// While Tailscale is off here, devices say nothing.
	if ns := e.ObserveTailnet(tsSnap(model.TailnetStopped, nas)); len(ns) != 0 {
		t.Fatalf("Tailscale off: %v", triggers(ns))
	}
	nas.Online = true
	if ns := e.ObserveTailnet(tsSnap(model.TailnetRunning, nas)); triggers(ns) != model.TriggerTailnetDeviceOffline+":ok" || !strings.Contains(ns[0].Message, "back online") {
		t.Fatalf("back online: %+v", ns)
	}
	// Offline again, then removed from the tailnet: closed.
	nas.Online = false
	e.ObserveTailnet(tsSnap(model.TailnetRunning, nas))
	e.ObserveTailnet(tsSnap(model.TailnetRunning, nas))
	if ns := e.ObserveTailnet(tsSnap(model.TailnetRunning)); triggers(ns) != model.TriggerTailnetDeviceOffline+":ok" || !strings.Contains(ns[0].Message, "no longer on your tailnet") {
		t.Fatalf("removed: %+v", ns)
	}
	// Switched off: nothing.
	cfg := DefaultConfig()
	cfg.Triggers[model.TriggerTailnetDeviceOffline] = false
	off := New(cfg, nil)
	nas.Online = true
	off.ObserveTailnet(tsSnap(model.TailnetRunning, nas))
	nas.Online = false
	off.ObserveTailnet(tsSnap(model.TailnetRunning, nas))
	if ns := off.ObserveTailnet(tsSnap(model.TailnetRunning, nas)); len(ns) != 0 {
		t.Fatalf("trigger off: %v", triggers(ns))
	}
}

func TestTailnetKeyExpiring(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	e := New(DefaultConfig(), func() time.Time { return now })
	vm := dev("v1", "cloud-vm", true)
	vm.KeyExpiresSoon, vm.KeyExpiry = true, now.Add(3*24*time.Hour+time.Hour).Format(time.RFC3339)
	snap := tsSnap(model.TailnetRunning, vm)
	snap.Self.KeyExpiresSoon, snap.Self.KeyExpiry = true, now.Add(26*time.Hour).Format(time.RFC3339)
	ns := e.ObserveTailnet(snap)
	if triggers(ns) != model.TriggerTailnetKeyExpiring+","+model.TriggerTailnetKeyExpiring {
		t.Fatalf("both keys: %v", triggers(ns))
	}
	if !strings.Contains(ns[0].Message, "This computer's Tailscale key expires tomorrow") || !strings.Contains(ns[1].Message, "cloud-vm's Tailscale key expires in 3 days") {
		t.Fatalf("messages: %q / %q", ns[0].Message, ns[1].Message)
	}
	if ns := e.ObserveTailnet(snap); len(ns) != 0 {
		t.Fatalf("again: %v", triggers(ns))
	}
	snap.Self.KeyExpiresSoon = false
	if ns := e.ObserveTailnet(snap); triggers(ns) != model.TriggerTailnetKeyExpiring+":ok" || ns[0].Key != TailnetKeyKey("self") {
		t.Fatalf("renewed: %+v", ns)
	}
}

// TestTailnetCarriedOverCloses: a problem from an earlier run (seeded from
// the ledger) stays open through Reconcile and closes on the first tailnet
// reading that shows it gone.
func TestTailnetCarriedOverCloses(t *testing.T) {
	e := New(DefaultConfig(), nil)
	e.Seed([]OpenFault{{Key: TailscaleDownKey(), Trigger: model.TriggerTailscaleDown, Severity: sevWarn, Title: "Tailscale stopped", Detail: "Tailscale"},
		{Key: TailnetDeviceKey("n1"), Trigger: model.TriggerTailnetDeviceOffline, Severity: sevWarn, Name: "nas", Detail: "nas"}})
	if ns := e.Reconcile(nil, nil, nil); len(ns) != 0 {
		t.Fatalf("reconcile closed tailnet problems: %v", triggers(ns))
	}
	e.ObserveTailnet(tsSnap(model.TailnetRunning, dev("n1", "nas", true)))
	ns := e.ObserveTailnet(tsSnap(model.TailnetRunning, dev("n1", "nas", true)))
	all := triggers(ns)
	if !strings.Contains(all, model.TriggerTailscaleDown+":ok") {
		t.Fatalf("Tailscale problem not closed: %v", all)
	}
	if len(e.Open()) != 0 {
		t.Fatalf("still open: %+v", e.Open())
	}
}
