package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"netdiag/internal/bubbles/bubblestest"
	"netdiag/internal/model"
)

// keysOf is the key of every item in a list_problems list.
func keysOf(list any) map[string]map[string]any {
	out := map[string]map[string]any{}
	items, _ := list.([]any)
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			out[fmt.Sprint(m["key"])] = m
		}
	}
	return out
}

// The data check_chat_alerts works from (v0.6 5.4, M12 acceptance without a
// live agent): over the window's /mcp, get_status.chat names the channel
// and app the alerts are in, and list_problems' keys are exactly the chat
// alerts' dedupe_key. A problem still open is in open[] and its card stays
// open; a fixed one is in fixed[] and netdiag itself already closed its
// card, so the runbook has nothing to close (D11).
func TestCheckChatAlertsWindowData(t *testing.T) {
	f := bubblestest.New(t)
	s, st, l, sc, served := quitServer(t, f)
	const gwKey = "healthFailed||gateway"
	now := time.Now().UTC()
	res := model.ScanResult{ID: "gw-down", State: model.StateDone, Request: model.ScanRequest{CIDR: "10.0.0.0/24"}, Gateway: "10.0.0.1",
		StartedAt: now.Add(-2 * time.Minute).Format(time.RFC3339), FinishedAt: now.Add(-time.Minute).Format(time.RFC3339),
		Hosts: []model.Host{{IP: "10.0.0.20", Alive: true, Online: true}},
		Health: &model.HealthReport{CheckedAt: now.Add(-90 * time.Second).Format(time.RFC3339), Network: quitNet,
			Checks: []model.HealthCheck{{ID: "gateway", Label: "Gateway", Status: model.HealthFail, Detail: "10.0.0.1 did not reply to ping",
				Data: map[string]any{"reason": model.ReasonNoReply}}}}}
	if err := st.SaveScan(res); err != nil {
		t.Fatal(err)
	}
	gw := model.Notice{Trigger: model.TriggerHealthFailed, Severity: "critical", Title: "Health check failed",
		Message: "Gateway: 10.0.0.1 did not reply to ping", At: now.Add(-3 * time.Minute).Format(time.RFC3339), Key: gwKey}
	l.Apply(quitNet, []model.Notice{gw})
	sc.ch <- model.Event{Type: model.EventNotice, Data: model.NoticeEvent{Notice: gw}}
	sc.quitFault(l)
	waitFor(t, "both cards", func() bool {
		a, ok1 := f.Alert(gwKey)
		b, ok2 := f.Alert(quitKey)
		return ok1 && ok2 && a.Alert.State == "open" && b.Alert.State == "open"
	})
	ok := model.Notice{Trigger: model.TriggerPrinterOffline, OK: true, IP: "192.168.1.50", Title: "Printer back online",
		Message: "Printer 192.168.1.50 answers again.", At: time.Now().UTC().Format(time.RFC3339), Key: quitKey}
	l.Apply(quitNet, []model.Notice{ok})
	sc.ch <- model.Event{Type: model.EventNotice, Data: model.NoticeEvent{Notice: ok}}
	waitFor(t, "netdiag closing the printer's card", func() bool { a, _ := f.Alert(quitKey); return a.Alert.State == "resolved" })

	setMCP(t, s.URL(), true)
	cs := mcpConnect(t, s.URL())
	status := callJSON(t, cs, "get_status", nil)
	ch, _ := status["chat"].(map[string]any)
	appName, _ := ch["appName"].(string)
	if ch["state"] != "connected" || ch["channel"] != "netdiag-alerts" || appName == "" || ch["sendAlerts"] != true || fmt.Sprint(ch["waiting"]) != "0" {
		t.Fatalf("get_status.chat = %v", ch)
	}
	if b, _ := json.Marshal(status); strings.Contains(string(b), "bub_") || strings.Contains(strings.ToLower(string(b)), "token") {
		t.Fatalf("get_status holds the chat token: %s", b)
	}

	lp := callJSON(t, cs, "list_problems", nil)
	open, fixed := keysOf(lp["open"]), keysOf(lp["fixed"])
	// The runbook's step 5, for each netdiag card in the channel. Step 4
	// found them by their author, the app's bot, named chat.appName.
	for _, key := range []string{gwKey, quitKey} {
		card, found := f.Alert(key)
		if !found || card.Alert.DedupeKey != key || card.ChannelID != "C1" {
			t.Fatalf("card %s: %+v", key, card)
		}
		if card.Author.DisplayName != appName || card.Author.Kind != "app" {
			t.Errorf("card %s is by %+v, not chat.appName %q", key, card.Author, appName)
		}
		_, isOpen := open[key]
		_, isFixed := fixed[key]
		switch {
		case isOpen && !isFixed:
			// Still a problem: re-checked and left open.
			if card.Alert.State != "open" {
				t.Errorf("%s is open in netdiag, but its card is %s", key, card.Alert.State)
			}
			if c, _ := open[key]["chat"].(map[string]any); c["posted"] != true {
				t.Errorf("%s: chat %v", key, c)
			}
		case isFixed && !isOpen:
			// Fixed: netdiag closed its own card; nothing for the agent to do.
			if card.Alert.State != "resolved" {
				t.Errorf("%s is fixed, but its card is %s", key, card.Alert.State)
			}
		default:
			t.Errorf("%s: open %v fixed %v (list_problems %v)", key, isOpen, isFixed, lp)
		}
	}
	if _, ok := open[gwKey]; !ok {
		t.Fatalf("the router is not in open: %v", lp)
	}
	if _, ok := fixed[quitKey]; !ok {
		t.Fatalf("the printer is not in fixed: %v", lp)
	}
	if n := len(f.Resolved()); n != 1 {
		t.Fatalf("%d resolves, want netdiag's one", n)
	}

	// The chat admin renames netdiag's app: bubbles renames its bot, so
	// the open card's author is the new name, which netdiag learns only
	// when it pairs again. Step 4 keeps the card all the same, as an app's
	// card whose key list_problems has.
	f.RenameApp("Office netdiag")
	card, _ := f.Alert(gwKey)
	now2, _ := callJSON(t, cs, "get_status", nil)["chat"].(map[string]any)
	byAuthor := card.Author.DisplayName == now2["appName"]
	_, byKey := keysOf(callJSON(t, cs, "list_problems", nil)["open"])[gwKey]
	if card.Author.DisplayName != "Office netdiag" || card.Author.Kind != "app" || card.Alert.State != "open" || (!byAuthor && !byKey) {
		t.Fatalf("after the rename, step 4 loses the open card: %+v (chat %v)", card, now2)
	}
	quit(t, s, served)
}
