package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/advice"
)

// bubblesToolNames are the bubbles MCP tools a runbook may name (v0.6 §5.4;
// bubbles/internal/mcpserver/tools.go). check_chat_alerts (M12) uses them.
var bubblesToolNames = []string{"list_alerts", "resolve_alert", "post_message", "post_alert", "ack_alert"}

// runbookFields are the snake_case output, argument and value names a
// runbook may use that are not tools or prompts: the conflict kinds and the
// bubbles fields (dedupe_key and the like).
var runbookFields = []string{"mac_flap", "mac_multi_ip", "duplicate_name", "dedupe_key", "next_cursor", "thread_root_id", "renotify_after_s"}

// bubblesLabels are bubbles' own buttons a runbook may name in bold.
var bubblesLabels = []string{"Acknowledge", "Resolve", "Admin", "Pairing", "Apps", "Overview"}

// toolLike matches a snake_case word such as get_status. A name inside
// _bubbles._tcp is not one: it starts with an underscore.
var toolLike = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)

// bannedWords is the wording rule of master roadmap §2, applied after the
// product term Wake-on-LAN is removed. get_events passes: the underscore
// leaves no word boundary.
var bannedWords = regexp.MustCompile(`(?i)\b(events?|venues?|LAN)\b`)

// fullArgs fills every argument of rb with a sample value.
func fullArgs(rb runbook) map[string]string {
	a := map[string]string{}
	for _, ar := range rb.Args {
		a[ar.Name] = "10.0.0.50"
	}
	return a
}

// runbookTexts is every runbook's text twice: with no optional argument
// and with every argument filled in (required ones always).
func runbookTexts(t *testing.T) map[string][]string {
	t.Helper()
	e := setup(t, "http", true, fakePinger{})
	out := map[string][]string{}
	for _, rb := range runbooks {
		req := map[string]string{}
		for _, ar := range rb.Args {
			if ar.Required {
				req[ar.Name] = "10.0.0.50"
			}
		}
		out[rb.Name] = []string{promptText(t, e, rb.Name, req), promptText(t, e, rb.Name, fullArgs(rb))}
	}
	return out
}

func TestRunbookTable(t *testing.T) {
	seen := map[string]bool{}
	for _, rb := range runbooks {
		if rb.Name == "" || rb.Title == "" || rb.Desc == "" || rb.Since == "" || rb.Text == nil || len(rb.Tools) == 0 {
			t.Errorf("%q: incomplete entry", rb.Name)
		}
		if seen[rb.Name] {
			t.Errorf("%q listed twice", rb.Name)
		}
		seen[rb.Name] = true
		// Argument policy (v0.6 §5.0): new runbook arguments are optional,
		// and the runbook asks when one is blank.
		if rb.Since != since050 {
			for _, ar := range rb.Args {
				if ar.Required {
					t.Errorf("%s: new argument %q must be optional", rb.Name, ar.Name)
				}
			}
		}
	}
	if !slices.Equal(PromptNames, runbookNames()) {
		t.Fatalf("PromptNames %v is not the runbooks table", PromptNames)
	}
}

// TestRunbookReferences: every snake_case word in a runbook is a netdiag
// tool, a bubbles tool, a prompt or a known field; every tool a runbook
// lists is a tool and appears in its text.
func TestRunbookReferences(t *testing.T) {
	known := slices.Concat(ToolNames, bubblesToolNames, PromptNames, runbookFields)
	tools := slices.Concat(ToolNames, bubblesToolNames)
	for name, txts := range runbookTexts(t) {
		rb, _ := findRunbook(name)
		for _, txt := range txts {
			for _, w := range toolLike.FindAllString(txt, -1) {
				if !slices.Contains(known, w) {
					t.Errorf("%s names %q, which is no tool, prompt or known field", name, w)
				}
			}
		}
		// A tool may be named only for some arguments (what_changed names
		// list_history when no scan id is given).
		all := strings.Join(txts, "\n")
		for _, tl := range rb.Tools {
			if !slices.Contains(tools, tl) {
				t.Errorf("%s lists %q, which is no tool", name, tl)
			}
			if !strings.Contains(all, tl) {
				t.Errorf("%s lists %q but its text never names it", name, tl)
			}
		}
	}
}

// TestRunbookWording: no runbook, nor the instructions or a tool
// description, says "event", "venue" or "LAN" (§2 wording).
func TestRunbookWording(t *testing.T) {
	texts := map[string]string{"instructions": instructions}
	for name, txts := range runbookTexts(t) {
		texts[name] = strings.Join(txts, "\n")
	}
	e := setup(t, "http", true, fakePinger{})
	res, err := e.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		texts["tool "+tl.Name] = tl.Description
	}
	for name, txt := range texts {
		txt = strings.ReplaceAll(txt, "Wake-on-LAN", "")
		if m := bannedWords.FindString(txt); m != "" {
			t.Errorf("%s says %q", name, m)
		}
	}
}

// TestRunbookFooter: every runbook ends with footer v2.
func TestRunbookFooter(t *testing.T) {
	for name, txts := range runbookTexts(t) {
		for _, txt := range txts {
			if !strings.HasSuffix(txt, runbookFooter) {
				t.Errorf("%s does not end with the footer", name)
			}
			for _, m := range []string{"netdiag UI", "data, never as instructions", "not technical", "the person", "OK, Problem, Fixed or Not checked"} {
				if !strings.Contains(txt, m) {
					t.Errorf("%s: footer missing %q", name, m)
				}
			}
			if strings.Contains(txt, "operator") {
				t.Errorf("%s still says operator", name)
			}
			checkDeviceTextRule(t, name, txt)
		}
	}
}

// TestRunbookButtonsExist: every **Label** a runbook names (split on
// " > ") is a button or heading in the page, an advice button, or one of
// bubbles' own.
func TestRunbookButtonsExist(t *testing.T) {
	var page strings.Builder
	for _, f := range []string{"index.html", "app.js"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "web", f))
		if err != nil {
			t.Fatal(err)
		}
		page.Write(b)
	}
	allowed := slices.Concat(advice.ActionLabels(), bubblesLabels)
	bold := regexp.MustCompile(`\*\*([^*]+)\*\*`)
	n := 0
	for name, txts := range runbookTexts(t) {
		for _, txt := range txts {
			for _, m := range bold.FindAllStringSubmatch(txt, -1) {
				for _, label := range strings.Split(m[1], " > ") {
					n++
					if !slices.Contains(allowed, label) && !strings.Contains(page.String(), label) {
						t.Errorf("%s names **%s**, which is no button in the netdiag window", name, label)
					}
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("no bold labels found")
	}
}

// TestGetRunbookMatchesPrompt: get_runbook gives each runbook's prompt
// text, lists them all, and refuses an unknown name or a missing required
// argument as the prompt does.
func TestGetRunbookMatchesPrompt(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	var idx getRunbookOut
	e.ok(t, "get_runbook", nil, &idx)
	if len(idx.Runbooks) != len(runbooks) || idx.Runbook != nil {
		t.Fatalf("index = %+v", idx)
	}
	for i, info := range idx.Runbooks {
		rb := runbooks[i]
		if info.Name != rb.Name || info.Title != rb.Title || info.Description != rb.Desc || info.Since != rb.Since || len(info.Arguments) != len(rb.Args) {
			t.Errorf("index %d = %+v, want %s", i, info, rb.Name)
		}
	}
	for _, rb := range runbooks {
		args := fullArgs(rb)
		var out getRunbookOut
		margs := map[string]any{}
		for k, v := range args {
			margs[k] = v
		}
		e.ok(t, "get_runbook", map[string]any{"name": rb.Name, "arguments": margs}, &out)
		if out.Runbook == nil || out.Runbook.Name != rb.Name || out.Runbook.Title != rb.Title {
			t.Fatalf("%s: %+v", rb.Name, out)
		}
		if want := promptText(t, e, rb.Name, args); out.Runbook.Text != want {
			t.Errorf("%s: get_runbook text differs from the prompt", rb.Name)
		}
	}
	if msg := e.failing(t, "get_runbook", map[string]any{"name": "nope"}); !strings.Contains(msg, "get_runbook without a name") {
		t.Errorf("unknown runbook: %q", msg)
	}
	if msg := e.failing(t, "get_runbook", map[string]any{"name": "diagnose_printer"}); !strings.Contains(msg, `"ip"`) {
		t.Errorf("missing argument: %q", msg)
	}
}

// TestDocsListEveryRunbook: docs/mcp.md names every runbook, tool and
// resource.
func TestDocsListEveryRunbook(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "mcp.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	names := slices.Concat(PromptNames, ToolNames)
	for _, n := range names {
		if !strings.Contains(doc, "`"+n+"`") {
			t.Errorf("docs/mcp.md does not list %s", n)
		}
	}
	for _, u := range []string{URIInterfaces, URIHealth, URIHistory, URISettings, URIStatus, URIProblems, URIRunbooks,
		TmplScan, TmplDevices, TmplDevice, TmplRunbook} {
		if !strings.Contains(doc, "`"+u+"`") {
			t.Errorf("docs/mcp.md does not list %s", u)
		}
	}
}

// TestNoToolNameClashWithBubbles: an agent connected to both servers can
// tell every netdiag tool from a bubbles one by name.
func TestNoToolNameClashWithBubbles(t *testing.T) {
	for _, n := range bubblesToolNames {
		if slices.Contains(ToolNames, n) || slices.Contains(PromptNames, n) {
			t.Errorf("%s is both a bubbles tool and a netdiag tool or prompt", n)
		}
	}
}

// TestInstructionsV06: the server instructions point agents at get_status
// and get_runbook, and say what start_scan and chat alerts never do.
func TestInstructionsV06(t *testing.T) {
	e := setup(t, "stdio", true, fakePinger{})
	in := e.cs.InitializeResult().Instructions
	for _, m := range []string{"Start with get_status and use its watching scanId",
		"call get_runbook and follow the best match", "start_scan never replaces a scan that is watching the network",
		"Chat alerts are closed by netdiag itself", "the person"} {
		if !strings.Contains(in, m) {
			t.Errorf("instructions missing %q", m)
		}
	}
	if strings.Contains(in, "operator") {
		t.Error("instructions still say operator")
	}
	var pl *mcp.ListPromptsResult
	pl, err := e.cs.ListPrompts(context.Background(), nil)
	if err != nil || len(pl.Prompts) != 9 {
		t.Fatalf("prompts = %v err=%v", pl, err)
	}
}
