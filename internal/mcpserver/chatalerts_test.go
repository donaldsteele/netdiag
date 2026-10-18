package mcpserver

import (
	"slices"
	"strings"
	"testing"

	"netdiag/internal/chat"
	"netdiag/internal/model"
	"netdiag/internal/notify"
	"netdiag/internal/problems"
	"netdiag/internal/service"
)

// chatCase is what the first line of a check_chat_alerts answer depends on:
// get_status, list_problems and the dedupe_key of each netdiag alert that
// list_alerts gave (step 4). alerts is nil when Claude has no bubbles tools
// (step 2), so it could not read them.
type chatCase struct {
	st     StatusOut
	p      ProblemsOut
	alerts []string
}

func (c chatCase) inOpen(key string) bool {
	return slices.ContainsFunc(c.p.Open, func(it OpenProblem) bool { return it.Key == key })
}

func (c chatCase) inFixed(key string) bool {
	return slices.ContainsFunc(c.p.Fixed, func(it problems.FixedItem) bool { return it.Key == key })
}

// chatRule is one case of check_chat_alerts' step 9: where the runbook
// states it (cond), the first line it asks for, and when it applies.
type chatRule struct {
	cond, first string
	when        func(chatCase) bool
}

// chatVerdicts are step 9's cases, in the runbook's order: the first that
// applies gives the first line. checkCaseOrder ties them to the text.
var chatVerdicts = []chatRule{
	{"- Still a problem: ", "Problem:", func(c chatCase) bool {
		if slices.ContainsFunc(c.alerts, c.inOpen) {
			return true
		}
		return c.alerts == nil && slices.ContainsFunc(c.p.Open, func(it OpenProblem) bool { return it.Chat.Posted || it.Chat.Waiting })
	}},
	{"- Not confirmed: ", "Not checked:", func(c chatCase) bool {
		return c.alerts == nil || c.st.Chat.State != chat.StateConnected ||
			slices.ContainsFunc(c.alerts, func(k string) bool { return !c.inOpen(k) && !c.inFixed(k) })
	}},
	{"- Otherwise: ", "OK: no netdiag alert needs attention", func(chatCase) bool { return true }},
}

// chatVerdict is the first line check_chat_alerts asks for in c.
func chatVerdict(c chatCase) string {
	for _, r := range chatVerdicts {
		if r.when(c) {
			return r.first
		}
	}
	return ""
}

// check_chat_alerts answers OK only when it read netdiag's alerts in chat
// and netdiag confirmed each one: without the bubbles tools, over a
// connection that cannot see the window's team chat, or with an alert
// netdiag does not list, the answer is Not checked, never OK.
func TestCheckChatAlertsVerdict(t *testing.T) {
	e := setup(t, "stdio", true, fakePinger{})
	conds, firsts := make([]string, len(chatVerdicts)), make([]string, len(chatVerdicts))
	for i, r := range chatVerdicts {
		conds[i], firsts[i] = r.cond, r.first
	}
	txt := promptText(t, e, "check_chat_alerts", nil)
	checkCaseOrder(t, "check_chat_alerts", txt, "The first line of your answer comes from the first of these cases that applies:", conds, firsts)

	const gw, printer, old = "healthFailed||gateway", "printerOffline|ip:10.0.0.50|", "printerOffline|ip:10.0.0.9|"
	status := func(state string) StatusOut {
		return StatusOut{Status: service.Status{Chat: service.ChatStatus{State: state, AppName: "netdiag"}}}
	}
	openGW := []OpenProblem{{Key: gw, Severity: problems.SevCritical, Chat: ProblemChat{Posted: true}}}
	fixedPrinter := []problems.FixedItem{{Key: printer}}
	cases := []struct {
		name string
		c    chatCase
		want string
	}{
		// Plain `netdiag mcp` (chat unknown) and no bubbles tools, nothing
		// open in the saved list: no alert was read.
		{"stdio, no bubbles tools", chatCase{st: status(service.ChatUnknown), p: ProblemsOut{Fixed: fixedPrinter}}, "Not checked:"},
		{"no bubbles tools, an alert posted", chatCase{st: status(chat.StateConnected), p: ProblemsOut{Open: openGW}}, "Problem:"},
		// Attached and with bubbles: two netdiag alerts netdiag lists in
		// neither list (another computer's, or fixed over a day ago).
		{"alerts netdiag does not list", chatCase{st: status(chat.StateConnected), p: ProblemsOut{Fixed: fixedPrinter},
			alerts: []string{old, "ipConflict|mac_flap|10.0.0.7"}}, "Not checked:"},
		{"an alert still open", chatCase{st: status(chat.StateConnected), p: ProblemsOut{Open: openGW, Fixed: fixedPrinter},
			alerts: []string{gw, printer, old}}, "Problem:"},
		{"chat server away", chatCase{st: status(chat.StateUnreachable), p: ProblemsOut{Fixed: fixedPrinter}, alerts: []string{printer}}, "Not checked:"},
		{"connection not the window's", chatCase{st: status(service.ChatUnknown), p: ProblemsOut{Fixed: fixedPrinter}, alerts: []string{printer}}, "Not checked:"},
		{"every alert fixed", chatCase{st: status(chat.StateConnected), p: ProblemsOut{Fixed: fixedPrinter}, alerts: []string{printer}}, "OK: no netdiag alert needs attention"},
		{"no alerts", chatCase{st: status(chat.StateConnected), alerts: []string{}}, "OK: no netdiag alert needs attention"},
	}
	for _, tc := range cases {
		if got := chatVerdict(tc.c); got != tc.want {
			t.Errorf("%s: first line %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Step 1 has a bullet for every team chat state but connected, as the
// manager names them, and reads chat.reason where one state has two causes
// the window words differently (app.js chatTrouble). Refused is the
// "otherwise" of needs_reconnect.
func TestCheckChatAlertsStates(t *testing.T) {
	e := setup(t, "stdio", true, fakePinger{})
	txt := promptText(t, e, "check_chat_alerts", nil)
	for _, s := range []string{chat.StateUnreachable, chat.StateNeedsReconnect, chat.StateIdentityChanged, chat.StateNoChannel, chat.StateOff, service.ChatUnknown} {
		if !strings.Contains(txt, "   - \""+s+"\": ") {
			t.Errorf("step 1 has no bullet for %q", s)
		}
	}
	for _, r := range []string{chat.ReasonUnreadable, chat.ReasonNotAllowed, chat.ReasonArchived} {
		if !strings.Contains(txt, `chat.reason is "`+r+`"`) {
			t.Errorf("step 1 does not read chat.reason %q", r)
		}
	}
	if !strings.Contains(txt, `chat.state is not "`+chat.StateConnected+`"`) {
		t.Error("step 1 does not name the connected state")
	}
}

// Step 4 finds netdiag's alerts by author and, since a chat admin can
// rename netdiag's app after netdiag learned its name, by key: every key
// netdiag posts to team chat starts with one of the shapes the runbook
// lists, and its local-only and test keys with none.
func TestCheckChatAlertsKeyShapes(t *testing.T) {
	e := setup(t, "stdio", true, fakePinger{})
	txt := promptText(t, e, "check_chat_alerts", nil)
	if !strings.Contains(txt, `author_kind "app"`) || !strings.Contains(txt, "starts with "+chatKeyShapes) ||
		!strings.Contains(txt, "renamed netdiag's app") {
		t.Fatalf("step 4 does not keep netdiag's alerts by key: %s", txt)
	}
	shapes := strings.Split(strings.NewReplacer(" or ", ", ").Replace(chatKeyShapes), ", ")
	if len(shapes) != 7 {
		t.Fatalf("shapes %q", shapes)
	}
	h := model.Host{IP: "10.0.0.50", MAC: "AA:BB:CC:DD:EE:FF"}
	posted := []string{
		notify.Key(model.TriggerPrinterOffline, h, ""), notify.Key(model.TriggerDeviceOffline, model.Host{IP: "10.0.0.51"}, ""),
		notify.Key(model.TriggerSupplyLow, h, "black"), notify.Key(model.TriggerPrinterAlert, h, "cover"),
		notify.Key(model.TriggerNewDevice, h, h.IP), notify.HealthKey("gateway"),
		notify.ConflictKey(model.Conflict{Kind: model.ConflictMACFlap, IPs: []string{"10.0.0.7"}, MACs: []string{"AA:BB:CC:00:00:01", "AA:BB:CC:00:00:02"}}),
	}
	starts := func(k string) bool {
		return slices.ContainsFunc(shapes, func(s string) bool { return strings.HasPrefix(k, s) })
	}
	for _, k := range posted {
		if !starts(k) {
			t.Errorf("netdiag posts %q, which step 4 would not keep", k)
		}
	}
	for _, k := range []string{model.TriggerNetdiagLink + "||10.0.0.0/24", model.TriggerChatTest} {
		if starts(k) {
			t.Errorf("%q never reaches team chat as an open alert, yet step 4 keeps it", k)
		}
	}
}
