package mcpserver

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/advice"
)

// bubblesV020Tools are the tools of bubbles v0.2.0's own MCP server, the
// version netdiag v0.6 works against, in its order (ToolNames in
// bubbles/internal/mcpserver/tools.go at the tag v0.2.0, commit 2315dd1).
// bubbles v0.2.0 has no prompts and no resources. TestBubblesToolsAtV020
// checks this copy against the bubbles repository when it is next to this
// one.
var bubblesV020Tools = []string{
	"list_channels", "read_messages", "search_messages", "post_message",
	"list_alerts", "post_alert", "ack_alert", "resolve_alert",
}

// bubblesToolNames are the bubbles MCP tools a runbook may name (v0.6 §5.4):
// check_chat_alerts uses list_alerts, resolve_alert and post_message, and
// says never to call post_alert or ack_alert.
var bubblesToolNames = []string{"list_alerts", "resolve_alert", "post_message", "post_alert", "ack_alert"}

// bubblesScopes are the scopes each bubbles v0.2.0 tool needs besides
// "mcp" (each tool's "Needs scopes" in bubbles/internal/mcpserver/tools.go,
// checked by TestBubblesToolsAtV020). docs/mcp.md names those of every
// bubbles tool check_chat_alerts uses (TestDocsChatTokenScopes).
var bubblesScopes = map[string][]string{
	"list_channels": {"messages:read"}, "read_messages": {"messages:read"}, "search_messages": {"messages:read"},
	"post_message": {"messages:write"}, "list_alerts": {"alerts:write", "messages:read"},
	"post_alert": {"alerts:write"}, "ack_alert": {"alerts:write"}, "resolve_alert": {"alerts:write"},
}

// bubblesFields are the bubbles tools' input and output names a runbook
// may use (json names in bubbles/internal/mcpserver/tools.go).
var bubblesFields = []string{"dedupe_key", "next_cursor", "thread_root_id", "renotify_after_s", "author_kind"}

// runbookFields are the snake_case output, argument and value names a
// runbook may use that are not tools or prompts: the conflict kinds, the
// storm guard reason network_changed, the team chat states and reasons of
// get_status (chat.state, chat.reason) and the bubbles fields (dedupe_key
// and the like).
var runbookFields = slices.Concat([]string{"mac_flap", "mac_multi_ip", "duplicate_name", "network_changed",
	"no_channel", "needs_reconnect", "identity_changed", "not_allowed"}, bubblesFields)

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

// control matches a control or a heading of the page and its content.
var control = regexp.MustCompile(`(?s)<(button|a|label|legend|summary|h2|h3)\b[^>]*>(.*?)</(?:button|a|label|legend|summary|h2|h3)>`)

// srText is text for screen readers only, which is not part of a label
// people see.
var srText = regexp.MustCompile(`(?s)<span class="sr"[^>]*>.*?</span>`)

// pageLabels is the visible text of every control and heading in
// web/index.html (buttons, links, labels, fieldset legends, summaries,
// headings), tags and screen-reader text removed: "Ignore this MAC <span
// x-text>" is "Ignore this MAC".
func pageLabels(t *testing.T) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	tag := regexp.MustCompile(`<[^>]*>`)
	out := map[string]bool{}
	for _, m := range control.FindAllStringSubmatch(string(b), -1) {
		txt := tag.ReplaceAllString(srText.ReplaceAllString(m[2], ""), " ")
		if txt = strings.Join(strings.Fields(txt), " "); txt != "" {
			out[txt] = true
		}
	}
	return out
}

// TestRunbookButtonsExist: every **Label** a runbook names (split on
// " > ") is the whole visible text of a button, link, switch or heading in
// the page, an advice button (advice.ActionLabels, built by the server), or
// one of bubbles' own. A runbook that names **Ignore this MAC** says it
// asks to confirm, as the page does (window.confirm), and none quotes
// "Are you sure?", which the page never asks.
func TestRunbookButtonsExist(t *testing.T) {
	labels := pageLabels(t)
	for _, want := range []string{"Scan", "Recheck", "Open Health", "Live mode", "Ignore this MAC", "Team chat (bubbles)"} {
		if !labels[want] {
			t.Fatalf("pageLabels misses %q: %v", want, labels)
		}
	}
	appJS, err := os.ReadFile(filepath.Join("..", "..", "web", "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	allowed := slices.Concat(advice.ActionLabels(), bubblesLabels)
	bold := regexp.MustCompile(`\*\*([^*]+)\*\*`)
	n := 0
	for name, txts := range runbookTexts(t) {
		for _, txt := range txts {
			for _, m := range bold.FindAllStringSubmatch(txt, -1) {
				for _, label := range strings.Split(m[1], " > ") {
					n++
					if !slices.Contains(allowed, label) && !labels[label] {
						t.Errorf("%s names **%s**, which is no button in the netdiag window", name, label)
					}
				}
			}
			if strings.Contains(txt, "**Ignore this MAC**") &&
				(!strings.Contains(txt, "asks them to confirm") || !strings.Contains(string(appJS), "window.confirm(this.ignoreConfirmText(")) {
				t.Errorf("%s: Ignore this MAC asks to confirm (ignoreConfirmText), and the runbook should say so", name)
			}
			if strings.Contains(txt, "Are you sure") {
				t.Errorf("%s quotes a confirmation the page never shows", name)
			}
		}
	}
	if n == 0 {
		t.Fatal("no bold labels found")
	}
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "mcp.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(doc), "Are you sure") {
		t.Error("docs/mcp.md quotes a confirmation the page never shows")
	}
}

// TestGetRunbookMatchesPrompt: get_runbook gives each runbook's prompt
// text, lists them all, and refuses an unknown name or a missing required
// argument as the prompt does.
func TestGetRunbookMatchesPrompt(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	var idx getRunbookOut
	e.ok(t, "get_runbook", nil, &idx)
	if len(idx.Runbooks) != len(runbooks) || idx.Name != "" || idx.Text != "" {
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
		res := e.ok(t, "get_runbook", map[string]any{"name": rb.Name, "arguments": margs}, &out)
		if out.Name != rb.Name || out.Title != rb.Title || out.Runbooks != nil {
			t.Fatalf("%s: %+v", rb.Name, out)
		}
		if want := promptText(t, e, rb.Name, args); out.Text != want {
			t.Errorf("%s: get_runbook text differs from the prompt", rb.Name)
		}
		// The plan's shape (v0.6 §4.14), {name,title,text} at the top
		// level, the same as netdiag://runbooks/{name} gives.
		var raw map[string]any
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) != 3 || raw["name"] != rb.Name || raw["title"] != rb.Title || raw["text"] != out.Text {
			t.Errorf("%s: get_runbook shape %v", rb.Name, slices.Sorted(maps.Keys(raw)))
		}
		if len(rb.Args) == 0 {
			txt, err := e.read(t, URIRunbooks+"/"+rb.Name)
			if err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(txt), &doc); err != nil || !maps.Equal(doc, raw) {
				t.Errorf("%s: the resource %s differs from get_runbook %s", rb.Name, txt, b)
			}
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
// resource, and the README's "What you can ask Claude" every runbook.
func TestDocsListEveryRunbook(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, ask, _ := strings.Cut(string(readme), "### What you can ask Claude")
	ask, _, _ = strings.Cut(ask, "\n### ")
	for _, n := range PromptNames {
		if !strings.Contains(ask, "(`"+n+"`)") {
			t.Errorf("README.md \"What you can ask Claude\" does not list %s", n)
		}
	}
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

// TestSiteToolCount: the site's "N tools for Claude over MCP" stat
// (docs/index.html) is the number of tools.
func TestSiteToolCount(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`<b>(\d+) tools</b><span>for Claude over MCP</span>`).FindSubmatch(b)
	if m == nil || string(m[1]) != strconv.Itoa(len(ToolNames)) {
		t.Fatalf("docs/index.html tool stat = %q, want %d tools", m, len(ToolNames))
	}
}

// TestNoToolNameClashWithBubbles: an agent connected to both servers
// (check_chat_alerts) can tell every netdiag tool and prompt from a bubbles
// v0.2.0 tool by name, and the bubbles tools and fields the runbooks name
// are bubbles v0.2.0's.
func TestNoToolNameClashWithBubbles(t *testing.T) {
	for _, n := range bubblesV020Tools {
		if slices.Contains(ToolNames, n) || slices.Contains(PromptNames, n) {
			t.Errorf("%s is both a bubbles tool and a netdiag tool or prompt", n)
		}
	}
	for _, n := range bubblesToolNames {
		if !slices.Contains(bubblesV020Tools, n) {
			t.Errorf("runbooks may name %s, which bubbles v0.2.0 does not have", n)
		}
	}
	rb, ok := findRunbook("check_chat_alerts")
	if !ok {
		t.Fatal("no check_chat_alerts runbook")
	}
	for _, n := range rb.Tools {
		if !slices.Contains(ToolNames, n) && !slices.Contains(bubblesV020Tools, n) {
			t.Errorf("check_chat_alerts lists %s, which is neither server's tool", n)
		}
	}
}

// bubblesRepo is a checkout of the bubbles repository: $NETDIAG_BUBBLES_REPO,
// else a "bubbles" folder next to this repository or one of its parents (a
// worktree lives inside the main checkout). "" when there is none.
func bubblesRepo(t *testing.T) string {
	t.Helper()
	if d := os.Getenv("NETDIAG_BUBBLES_REPO"); d != "" {
		return d
	}
	dir, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return ""
	}
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
		if fi, err := os.Stat(filepath.Join(dir, "bubbles", ".git")); err == nil && fi != nil {
			return filepath.Join(dir, "bubbles")
		}
	}
}

// TestBubblesToolsAtV020: bubblesV020Tools and bubblesFields are what
// bubbles v0.2.0 has, read from its repository with git (skipped when there
// is no bubbles checkout, as in CI).
func TestBubblesToolsAtV020(t *testing.T) {
	repo := bubblesRepo(t)
	if repo == "" {
		t.Skip("no bubbles checkout next to this repository (set NETDIAG_BUBBLES_REPO)")
	}
	out, err := exec.Command("git", "-C", repo, "show", "v0.2.0:internal/mcpserver/tools.go").Output()
	if err != nil {
		t.Skipf("git show v0.2.0 in %s: %v", repo, err)
	}
	src := string(out)
	m := regexp.MustCompile(`(?s)var ToolNames = \[\]string\{(.*?)\}`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("bubbles v0.2.0 tools.go has no ToolNames")
	}
	var got []string
	for _, q := range regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(m[1], -1) {
		got = append(got, q[1])
	}
	if !slices.Equal(got, bubblesV020Tools) {
		t.Fatalf("bubbles v0.2.0 ToolNames = %v, the copy here is %v", got, bubblesV020Tools)
	}
	for _, f := range bubblesFields {
		if !strings.Contains(src, `json:"`+f+`"`) && !strings.Contains(src, `json:"`+f+`,`) {
			t.Errorf("bubbles v0.2.0 tools have no field %s", f)
		}
	}
	for _, f := range []string{"author", "dedupe_key", "state"} {
		if !strings.Contains(src, `json:"`+f+`"`) {
			t.Errorf("bubbles v0.2.0 alerts have no %s, which check_chat_alerts reads", f)
		}
	}
	// Each tool's description ends "Needs scopes mcp and x." or "Needs
	// scopes mcp, x and y.".
	needs := regexp.MustCompile(`(?s)Name: "([a-z_]+)",.*?Description: "[^"]*Needs scopes ([^."]+)\.`)
	seen := 0
	for _, m := range needs.FindAllStringSubmatch(src, -1) {
		var scopes []string
		for _, s := range strings.FieldsFunc(strings.ReplaceAll(m[2], " and ", ","), func(r rune) bool { return r == ',' || r == ' ' }) {
			if s != "mcp" {
				scopes = append(scopes, s)
			}
		}
		if !slices.Equal(scopes, bubblesScopes[m[1]]) {
			t.Errorf("bubbles v0.2.0 %s needs %v, bubblesScopes says %v", m[1], scopes, bubblesScopes[m[1]])
		}
		seen++
	}
	if seen != len(bubblesV020Tools) {
		t.Errorf("read the scopes of %d bubbles tools, want %d", seen, len(bubblesV020Tools))
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
	if err != nil || len(pl.Prompts) != 10 {
		t.Fatalf("prompts = %v err=%v", pl, err)
	}
}
