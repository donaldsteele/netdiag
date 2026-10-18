package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The docs/mcp.md tables (v0.6 §8): the Runbooks table, the Tools table
// with its Kind and Since columns, and the "What Claude can never do"
// list, each checked against what the server registers.

// toolSince is the version that added each tool (master roadmap §8.9).
var toolSince = map[string]string{
	"get_status": since060, "list_problems": since060, "get_runbook": since060,
}

// mcpDoc is docs/mcp.md.
func mcpDoc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "mcp.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// docSection is the text under "## <title>" up to the next "## " heading.
func docSection(t *testing.T, doc, title string) string {
	t.Helper()
	_, s, ok := strings.Cut(doc, "\n## "+title+"\n")
	if !ok {
		t.Fatalf("docs/mcp.md has no \"## %s\" section", title)
	}
	s, _, _ = strings.Cut(s, "\n## ")
	return s
}

// tableRows are the cells of the markdown table rows in s whose first cell
// holds `name` for a name in names, keyed by that name. Only the first
// table row for a name counts.
func tableRows(s string, col int, names []string) map[string][]string {
	out := map[string][]string{}
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), " | ")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if len(cells) <= col {
			continue
		}
		for _, n := range names {
			if _, seen := out[n]; !seen && cells[col] == "`"+n+"`" {
				out[n] = cells
			}
		}
	}
	return out
}

var boldLabel = regexp.MustCompile(`\*\*([^*]+)\*\*`)

// boldSet is the sorted set of **labels** in s.
func boldSet(s string) []string {
	var out []string
	for _, m := range boldLabel.FindAllStringSubmatch(s, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	slices.Sort(out)
	return out
}

// TestDocsRunbooksTable: the Runbooks table has one row per runbook with
// its Since, and its buttons column names exactly the buttons the runbook
// text names in bold ("none" when it names none).
func TestDocsRunbooksTable(t *testing.T) {
	sec := docSection(t, mcpDoc(t), "Runbooks")
	table, _, _ := strings.Cut(sec, "\n### ")
	rows := tableRows(table, 2, PromptNames)
	texts := runbookTexts(t)
	for _, rb := range runbooks {
		row, ok := rows[rb.Name]
		if !ok {
			t.Errorf("the Runbooks table has no row for %s", rb.Name)
			continue
		}
		if len(row) != 7 {
			t.Errorf("%s: %d columns, want 7 (Situation | Say this to Claude | Runbook | What Claude does | Buttons | Needs the window? | Since)", rb.Name, len(row))
			continue
		}
		if row[6] != rb.Since {
			t.Errorf("%s: Since %q, want %q", rb.Name, row[6], rb.Since)
		}
		var want []string
		for _, txt := range texts[rb.Name] {
			for _, l := range boldSet(txt) {
				if !slices.Contains(want, l) {
					want = append(want, l)
				}
			}
		}
		slices.Sort(want)
		got := boldSet(row[4])
		if len(want) == 0 {
			if row[4] != "none" {
				t.Errorf("%s names no button, but its row lists %q", rb.Name, row[4])
			}
		} else if !slices.Equal(got, want) {
			t.Errorf("%s: buttons %v, the runbook names %v", rb.Name, got, want)
		}
	}
}

// TestDocsToolsTable: the Tools table has one row per tool, its Kind
// follows the tool's annotations (Read: read-only; Probe: sends network
// traffic; Stop: cancel_scan) and its Since §8.9.
func TestDocsToolsTable(t *testing.T) {
	rows := tableRows(docSection(t, mcpDoc(t), "Tools"), 0, ToolNames)
	e := setup(t, "http", true, fakePinger{})
	res, err := e.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		row, ok := rows[tl.Name]
		if !ok {
			t.Errorf("the Tools table has no row for %s", tl.Name)
			continue
		}
		if len(row) != 5 {
			t.Errorf("%s: %d columns, want 5 (Tool | What it does | Key inputs | Kind | Since)", tl.Name, len(row))
			continue
		}
		kind := "Stop"
		switch a := tl.Annotations; {
		case a.ReadOnlyHint:
			kind = "Read"
		case a.OpenWorldHint != nil && *a.OpenWorldHint:
			kind = "Probe"
		}
		if row[3] != kind {
			t.Errorf("%s: Kind %q, want %q", tl.Name, row[3], kind)
		}
		since := toolSince[tl.Name]
		if since == "" {
			since = since050
		}
		if row[4] != since {
			t.Errorf("%s: Since %q, want %q", tl.Name, row[4], since)
		}
	}
}

// TestDocsNeverList: "What Claude can never do" lists exactly the footer's
// phrases (uiOnlyWords), one per line, in its order.
func TestDocsNeverList(t *testing.T) {
	var got []string
	for _, line := range strings.Split(docSection(t, mcpDoc(t), "What Claude can never do"), "\n") {
		if s, ok := strings.CutPrefix(line, "- "); ok {
			got = append(got, s)
		}
	}
	if !slices.Equal(got, uiOnlyWords) {
		t.Errorf("docs/mcp.md \"What Claude can never do\" = %q, want uiOnlyWords %q", got, uiOnlyWords)
	}
}
