package mcpserver

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"netdiag/internal/model"
	"netdiag/internal/problems"
	"netdiag/internal/scan"
	"netdiag/internal/service"
)

// verdictRule is one case of the first line explain_problems asks for
// (§5.1): where the runbook states the case (cond), the line it asks for,
// which the runbook quotes (first; "" for a heading whose cases follow),
// and when the case applies, read from get_status and list_problems.
type verdictRule struct {
	cond, first string
	when        func(StatusOut, ProblemsOut) bool
}

// urgent reports whether an open problem is a warning or critical.
func urgent(p ProblemsOut) bool {
	for _, it := range p.Open {
		if it.Severity != problems.SevInfo {
			return true
		}
	}
	return false
}

func heldFor(reason string) func(StatusOut, ProblemsOut) bool {
	return func(_ StatusOut, p ProblemsOut) bool { return p.Held && p.HeldReason == reason }
}

// explainVerdicts are explain_problems' steps 3 to 7, in the runbook's
// order: the first that applies gives the first line of the answer.
// checkVerdictRules ties them to the runbook text, so a change to its
// cases or their order fails the test until this table follows it.
var explainVerdicts = []verdictRule{
	{`3. If everythingFine is true`, `OK: everything looks fine (checked <time>)`,
		func(_ StatusOut, p ProblemsOut) bool { return p.EverythingFine }},
	{`4. If source is "none"`, `Not checked: netdiag has not looked at the local network yet.`,
		func(_ StatusOut, p ProblemsOut) bool { return p.Source == problems.SourceNone }},
	{`If source is "health"`, `Not checked: the health checks ran, but netdiag has not looked at the devices yet.`,
		func(_ StatusOut, p ProblemsOut) bool { return p.Source == problems.SourceHealth && !urgent(p) }},
	{`5. If held is true`, "", nil},
	{`- "link": `, `Problem: this computer lost the local network, so netdiag paused its checks.`, heldFor(model.HeldLink)},
	{`- "gateway": `, `Problem: the router stopped answering, so netdiag paused its checks.`, heldFor(model.HeldGateway)},
	{`- "network_changed": `, `Not checked: this computer is on a different network now, so netdiag stopped its alerts.`, heldFor(model.HeldNetworkChanged)},
	{`6. If no open problem is a warning or critical`, "", nil},
	{`- source is "live": `, `Not checked: netdiag has not finished checking yet.`,
		func(_ StatusOut, p ProblemsOut) bool { return !urgent(p) && p.Source == problems.SourceLive }},
	{`- otherwise: `, `Not checked: no problems found, but the health checks did not run for this scan.`,
		func(_ StatusOut, p ProblemsOut) bool { return !urgent(p) }},
	{`7. Otherwise start your answer with`, `Problem:`, func(StatusOut, ProblemsOut) bool { return true }},
}

// checkVerdictRules checks that the runbook text states explainVerdicts'
// cases in their order, each quoting its first line before the next case.
func checkVerdictRules(t *testing.T, txt string) {
	t.Helper()
	conds, firsts := make([]string, len(explainVerdicts)), make([]string, len(explainVerdicts))
	for i, r := range explainVerdicts {
		conds[i], firsts[i] = r.cond, r.first
	}
	checkCaseOrder(t, "explain_problems", txt, "The first line of your answer comes from the first of steps 3 to 7 that applies.", conds, firsts)
}

// checkCaseOrder checks that runbook name's text says its cases are tried
// in order (intro), then states each case (conds) in that order after it,
// each quoting its first line (firsts; "" for a heading whose cases follow)
// before the next case.
func checkCaseOrder(t *testing.T, name, txt, intro string, conds, firsts []string) {
	t.Helper()
	pos := strings.Index(txt, intro)
	if pos < 0 {
		t.Fatalf("%s does not say its cases are tried in order (%q)", name, intro)
	}
	for i, cond := range conds {
		at := strings.Index(txt[pos:], cond)
		if at < 0 {
			t.Fatalf("%s does not state %q after the case before it", name, cond)
		}
		start, end := pos+at+len(cond), len(txt)
		if i+1 < len(conds) {
			next := strings.Index(txt[start:], conds[i+1])
			if next < 0 {
				t.Fatalf("%s does not state %q after %q", name, conds[i+1], cond)
			}
			end = start + next
		}
		if firsts[i] != "" && !strings.Contains(txt[start:end], `"`+firsts[i]+`"`) {
			t.Fatalf("%s' case %q does not ask for %q: %s", name, cond, firsts[i], txt[start:end])
		}
		pos = start
	}
}

// verdict is the first line explain_problems asks for, by explainVerdicts,
// from the two tool outputs it relies on.
func verdict(st StatusOut, p ProblemsOut) string {
	for _, r := range explainVerdicts {
		if r.when == nil || !r.when(st, p) {
			continue
		}
		at := p.CheckedAt
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			at = t.Local().Format("15:04")
		}
		line := strings.Replace(r.first, "<time>", at, 1)
		if r.first == "Problem:" {
			line += " " + p.Open[0].Title
		}
		return line
	}
	return ""
}

// explain reads explain_problems, get_status and list_problems as an agent
// following the runbook does, and returns the first line of its answer.
// get_status and list_problems are two calls, and a live scan can change
// between them (TestExplainProblemsHeld drops the link while it reads), so
// a pair is compared only when get_status reads the same after
// list_problems as before it.
func explain(t *testing.T, e *env) (string, StatusOut, ProblemsOut) {
	t.Helper()
	checkVerdictRules(t, promptText(t, e, "explain_problems", nil))
	held := func(st StatusOut) bool { return st.Watching != nil && st.Watching.Held }
	for try := 0; ; try++ {
		var st, again StatusOut
		e.ok(t, "get_status", nil, &st)
		var out ProblemsOut
		e.ok(t, "list_problems", nil, &out)
		e.ok(t, "get_status", nil, &again)
		if (st.Problems != again.Problems || held(st) != held(again)) && try < 20 {
			continue
		}
		if st.Problems.Source != out.Source || st.Problems.Open != len(out.Open) || st.Problems.EverythingFine != out.EverythingFine {
			t.Fatalf("status %+v does not match the list %+v", st.Problems, out)
		}
		return verdict(st, out), st, out
	}
}

// The explain_problems acceptance check (M9) without a live agent, over
// stdio: against the fixture networks, get_status and list_problems give
// what the runbook needs, and its cases give an answer that starts with
// "OK:" or "Problem:", or "Not checked:" when nothing was checked yet or
// the health checks did not run (everythingFine false with no warning is
// never a Problem).
func TestExplainProblemsFixtures(t *testing.T) {
	noHealth := fineFixture()
	noHealth.ID, noHealth.Health = "nohealth1", nil
	for _, c := range []struct {
		name   string
		scan   *model.ScanResult
		source string
		prefix string
	}{
		{"nothing yet", nil, problems.SourceNone, "Not checked: netdiag has not looked at the local network yet."},
		{"fine", ptr(fineFixture()), problems.SourceSaved, "OK: everything looks fine (checked "},
		{"router down", ptr(problemsFixture(t, true)), problems.SourceSaved, "Problem: Health check failed"},
		{"devices only", ptr(problemsFixture(t, false)), problems.SourceSaved, "Problem: "},
		{"health checks did not run", &noHealth, problems.SourceSaved, "Not checked: no problems found, but the health checks did not run for this scan."},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t, "stdio", true, fakePinger{})
			if c.scan != nil {
				if err := e.st.SaveScan(*c.scan); err != nil {
					t.Fatal(err)
				}
			}
			txt := promptText(t, e, "explain_problems", nil)
			for _, m := range []string{"advice.meaning", "advice.steps", "most urgent"} {
				if !strings.Contains(txt, m) {
					t.Fatalf("runbook text missing %q", m)
				}
			}
			got, st, out := explain(t, e)
			if out.Source != c.source || st.Window.ThisProcess {
				t.Fatalf("source %q thisProcess %v, want %q over stdio", out.Source, st.Window.ThisProcess, c.source)
			}
			if !strings.HasPrefix(got, c.prefix) {
				t.Fatalf("verdict %q, want prefix %q", got, c.prefix)
			}
			if !out.EverythingFine && !urgent(out) && !strings.HasPrefix(got, "Not checked:") {
				t.Fatalf("not fine with no warning gave %q", got)
			}
			if !urgent(out) {
				return
			}
			// What the runbook reads for each problem is there: the title,
			// the device, the meaning and at most three steps.
			for _, p := range out.Open {
				if p.Title == "" || p.Advice == nil || p.Advice.Meaning == "" || len(p.Advice.Steps) > 3 {
					t.Errorf("item %s lacks what the runbook reads: %+v", p.Key, p)
				}
			}
			if out.Open[0].Severity == problems.SevInfo {
				t.Errorf("the most urgent item is a note: %+v", out.Open[0])
			}
		})
	}
}

// Only the health checks ran (run_health_check, the window's Check again)
// and no scan: list_problems speaks for them (source health), and with
// nothing wrong the answer is Not checked, since no device was looked at.
func TestExplainProblemsHealthOnly(t *testing.T) {
	e := setup(t, "stdio", true, fakePinger{})
	var rep model.HealthReport
	e.ok(t, "run_health_check", nil, &rep)
	got, _, out := explain(t, e)
	if out.Source != problems.SourceHealth || out.EverythingFine ||
		got != "Not checked: the health checks ran, but netdiag has not looked at the devices yet." {
		t.Fatalf("health checks only: %q %+v", got, out)
	}
}

// A fixed item as explain_problems step 11 asks Claude to say it, the way
// the card and team chat do: it names what was fixed (detail), and an
// outage of a day or more reads in days. list_problems carries both: the
// check's plain name, and downForSec from the ledger's start of the fault.
func TestExplainProblemsFixedItems(t *testing.T) {
	e := setupWindow(t, fakePinger{}, nil)
	txt := promptText(t, e, "explain_problems", nil)
	step := txt[strings.Index(txt, "\n11. ")+1:]
	step = step[:strings.Index(step, "\n")]
	for _, m := range []string{"fixedAt", "downForSec", `"8 days 1 h"`, `"1 day"`, "detail", "Health check failed · Router", "(lasted 12 min)"} {
		if !strings.Contains(step, m) {
			t.Fatalf("step 11 misses %q: %s", m, step)
		}
	}

	if err := e.st.SaveScan(fineFixture()); err != nil {
		t.Fatal(err)
	}
	fixedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	opened := fixedAt.Add(-(193*time.Hour + 52*time.Minute))
	e.ledger.Apply(testNetwork, []model.Notice{{Trigger: model.TriggerHealthFailed, Key: "healthFailed||dns", Severity: "warn",
		Title: "Health check failed", At: opened.Format(time.RFC3339)}})
	e.ledger.Apply(testNetwork, []model.Notice{{Trigger: model.TriggerHealthFailed, Key: "healthFailed||dns", Severity: "info", OK: true,
		Title: "Health check OK again", OpenedAt: opened.Format(time.RFC3339), At: fixedAt.Format(time.RFC3339)}})
	var out ProblemsOut
	e.ok(t, "list_problems", nil, &out)
	if len(out.Fixed) != 1 {
		t.Fatalf("fixed: %+v", out.Fixed)
	}
	// 193 h 52 min, which step 11 says as "8 days 1 h".
	if f := out.Fixed[0]; f.Title != "Health check failed" || f.Detail != "Name lookup (DNS)" || f.DownForSec != (193*60+52)*60 {
		t.Fatalf("fixed item: %+v", f)
	}
}

// The explain_problems acceptance check over the window's own MCP address
// (Claude Code over HTTP): this process; a saved scan, with the key, start
// and chat state from the window's in-memory ledger; a first pass still
// sweeping (Not checked); then the same scan watching with nothing wrong
// (OK).
func TestExplainProblemsWindow(t *testing.T) {
	gate := make(chan struct{})
	e := setupWindow(t, fakePinger{gate: gate}, nil)

	got, st, out := explain(t, e)
	if !st.Window.ThisProcess || st.Transport != "http" || st.Note != service.NoteNotChecked || out.Source != problems.SourceNone ||
		got != "Not checked: netdiag has not looked at the local network yet." {
		t.Fatalf("nothing yet: %q %+v", got, st)
	}

	// A saved scan with the router down; the window's ledger (in memory)
	// opened it at 09:50 and posted it to team chat.
	if err := e.st.SaveScan(problemsFixture(t, true)); err != nil {
		t.Fatal(err)
	}
	e.ledger.Apply(testNetwork, []model.Notice{{Trigger: model.TriggerHealthFailed, Key: "healthFailed||gateway", Severity: "critical",
		Title: "Health check failed", At: "2026-10-17T09:50:00Z"}})
	e.ledger.MarkChat(testNetwork, "healthFailed||gateway", problems.ChatPosted)
	got, st, out = explain(t, e)
	if !st.Window.ThisProcess || out.Source != problems.SourceSaved || got != "Problem: Health check failed" {
		t.Fatalf("saved: %q %+v %+v", got, st, out)
	}
	if gw := out.Open[0]; gw.Key != "healthFailed||gateway" || gw.Since != "2026-10-17T09:50:00Z" || !gw.Chat.Posted || gw.Chat.Waiting ||
		gw.Recheck.Tool != "run_health_check" {
		t.Fatalf("gateway item: %+v", gw)
	}
	for _, p := range out.Open[1:] {
		if p.Chat.Posted {
			t.Errorf("only the gateway was posted: %+v", p)
		}
	}

	// The window starts watching; its first pass is still sweeping.
	sc, err := e.svc.Start(model.ScanRequest{CIDR: "10.0.0.0/28", Live: true, LiveIntervalSec: 300}.WithOrigin(model.OriginUI))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Cancel(); <-sc.Done() })
	waitScanState(t, sc, model.StateRunning)
	got, st, out = explain(t, e)
	if out.Source != problems.SourceLive || !out.Watching || out.EverythingFine || st.Watching == nil || st.Watching.ScanID != sc.ID ||
		st.Watching.Since != "" || st.Running == nil || st.Running.State != model.StateRunning ||
		got != "Not checked: netdiag has not finished checking yet." {
		t.Fatalf("first pass: %q %+v %+v", got, st, out)
	}

	// The first pass ends: watching, nothing wrong.
	close(gate)
	waitScanState(t, sc, model.StateLive)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, st, out = explain(t, e)
		if out.EverythingFine {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watching: %q %+v", got, out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.HasPrefix(got, "OK: everything looks fine (checked ") || out.Source != problems.SourceLive || st.Watching == nil || st.Watching.Since == "" {
		t.Fatalf("watching: %q %+v %+v", got, st, out)
	}
}

// linkEnv is this computer's link: Self fails while down is set, which the
// storm guard takes for a lost link (v0.6-D36).
type linkEnv struct{ down *atomic.Bool }

func (e linkEnv) Self(string) (model.SelfInfo, error) {
	if e.down.Load() {
		return model.SelfInfo{}, errors.New("no usable IPv4 interface")
	}
	return model.SelfInfo{IP: "10.0.0.30", Interface: "eth0"}, nil
}
func (linkEnv) Gateway() (string, error) { return "10.0.0.1", nil }

// While the storm guard holds the window's live scan, explain_problems says
// netdiag paused its checks, never that everything is fine.
func TestExplainProblemsHeld(t *testing.T) {
	down := &atomic.Bool{}
	e := setupWindow(t, fakePinger{}, func(d *scan.Deps) { d.Env, d.LiveInterval = linkEnv{down}, 50*time.Millisecond })
	sc, err := e.svc.Start(model.ScanRequest{CIDR: "10.0.0.0/30", Live: true}.WithOrigin(model.OriginUI))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Cancel(); <-sc.Done() })
	waitScanState(t, sc, model.StateLive)
	down.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, st, out := explain(t, e)
		if out.Held {
			if out.HeldReason != model.HeldLink || got != "Problem: this computer lost the local network, so netdiag paused its checks." ||
				st.Watching == nil || !st.Watching.Held {
				t.Fatalf("held: %q %+v %+v", got, st, out)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("never held: %q %+v", got, out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// explain_conflict follows netdiag's own verdict: a mac_flap that the
// analyzer marked "fail" in a scan without a MAC history is a swap, as the
// card's title ("Network conflict") and the advice say, and the runbook
// states that rule; one marked "warn" is a Possible conflict everywhere.
func TestExplainConflictVerdict(t *testing.T) {
	txt := promptText(t, setup(t, "stdio", true, fakePinger{}), "explain_conflict", map[string]string{"ip": "10.0.0.5"})
	for _, m := range []string{`list_problems titles it "Network conflict"`, `severity "fail"`, "macHistory", `"Possible conflict"`, "taking turns"} {
		if !strings.Contains(txt, m) {
			t.Fatalf("explain_conflict misses %q", m)
		}
	}
	for _, c := range []struct {
		severity, title, meaning string
	}{
		{"fail", "Network conflict", "Two devices are taking turns using 10.0.0.5"},
		{"warn", problems.TitlePossible, "Possible conflict: the device answering at 10.0.0.5 changed."},
	} {
		t.Run(c.severity, func(t *testing.T) {
			e := setup(t, "stdio", true, fakePinger{})
			r := fineFixture()
			r.Hosts = append(r.Hosts, model.Host{IP: "10.0.0.5", Alive: true, Online: true, MAC: "02:00:5E:00:00:05"})
			r.Conflicts = []model.Conflict{{ID: "c1", Kind: model.ConflictMACFlap, Severity: c.severity, IPs: []string{"10.0.0.5"},
				MACs: []string{"02:00:5E:00:00:05", "02:00:5E:00:00:06"}, Detail: "10.0.0.5 answered from two MACs"}}
			if err := e.st.SaveScan(r); err != nil {
				t.Fatal(err)
			}
			var d DeviceDetail
			e.ok(t, "get_device", map[string]any{"scanId": r.ID, "ip": "10.0.0.5"}, &d)
			if len(d.Host.MACHistory) != 0 || len(d.Conflicts) != 1 || d.Conflicts[0].Advice == nil ||
				!strings.HasPrefix(d.Conflicts[0].Advice.Meaning, c.meaning) {
				t.Fatalf("get_device conflicts: %+v", d.Conflicts)
			}
			var out ProblemsOut
			e.ok(t, "list_problems", nil, &out)
			found := false
			for _, p := range out.Open {
				if p.Kind == model.TriggerIPConflict {
					found = true
					if p.Title != c.title || p.Advice == nil || p.Advice.Meaning != d.Conflicts[0].Advice.Meaning {
						t.Errorf("list_problems conflict: %+v", p)
					}
				}
			}
			if !found {
				t.Fatalf("no conflict in list_problems: %+v", out.Open)
			}
		})
	}
}
