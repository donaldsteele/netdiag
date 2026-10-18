package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// root is the repository root, from tools/docscheck.
var root = filepath.Join("..", "..")

// userDocs are the files people read: the README, the zip's HOW-TO-OPEN,
// the MCP reference, the field checklist and the site.
var userDocs = []string{"README.md", "HOW-TO-OPEN.txt", "docs/mcp.md", "docs/field-checklist.md", "docs/index.html"}

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// bannedWords is the wording rule of master roadmap §2, applied after the
// product names in productTerms are removed (the same rule as mcpserver's
// TestRunbookWording). A tool name such as get_events passes: the
// underscore leaves no word boundary.
var bannedWords = regexp.MustCompile(`(?i)\b(events?|venues?|LAN)\b`)

// productTerms are names of things people see elsewhere, kept as they are
// written there: the Wake-on-LAN feature and Windows' Event Viewer, which
// the field checklist sends testers to.
var productTerms = []string{"Wake-on-LAN", "Event Viewer"}

// TestDocsWording: say "local network", never event, venue or LAN.
func TestDocsWording(t *testing.T) {
	for _, name := range userDocs {
		for i, line := range strings.Split(read(t, name), "\n") {
			for _, p := range productTerms {
				line = strings.ReplaceAll(line, p, "")
			}
			if m := bannedWords.FindString(line); m != "" {
				t.Errorf("%s:%d says %q: say \"local network\" (or name the thing) instead", name, i+1, m)
			}
		}
	}
}

// mdImage is a Markdown image, ![alt](path); htmlImage an img src.
var (
	mdImage   = regexp.MustCompile(`!\[[^\]]*\]\(([^)\s]+)\)`)
	htmlImage = regexp.MustCompile(`<img[^>]*\ssrc="([^"]+)"`)
)

// TestDocsImagesExist: every screenshot the README and the site show is in
// the repository, so a renamed or missing shot fails here, not on GitHub.
func TestDocsImagesExist(t *testing.T) {
	check := func(doc, dir, ref string) {
		if strings.Contains(ref, "://") {
			return
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), filepath.FromSlash(ref))); err != nil {
			t.Errorf("%s shows %s, which is missing", doc, ref)
		}
	}
	n := 0
	for _, m := range mdImage.FindAllStringSubmatch(read(t, "README.md"), -1) {
		check("README.md", ".", m[1])
		n++
	}
	for _, m := range htmlImage.FindAllStringSubmatch(read(t, "docs/index.html"), -1) {
		if m[1] != "" {
			check("docs/index.html", "docs", m[1])
			n++
		}
	}
	if n < 10 {
		t.Fatalf("found only %d images; the patterns no longer match the docs", n)
	}
}

// v06Section is the field checklist's v0.6 section heading (master
// roadmap §8.13): before "## Results".
const v06Section = "## 6. v0.6 chat alerts and watching"

// v06Items are the v0.6 field checks, by their bold title: the real
// hardware runs of v0.6 §7.4 (1-6), the standard-user runs of §7.5, the
// checks earlier milestones left for the test rig, and Defender (§7.6).
var v06Items = []string{
	// §7.4
	"Cover open, then closed.", "Printer offline, then back.", "Restart while a problem is open.",
	"This computer loses the network.", "Chat server down.", "One netdiag at a time.",
	// §7.5 Windows 11
	"First start uses 127.0.0.1:8787.", "A second start opens the same page.", "Buttons work in Edge, Chrome and Firefox.",
	"Connect to team chat without admin.", "Firewall prompt while looking for the chat server.", "The chat key is protected (DPAPI).",
	"Open Location settings.", "The chat server moves.",
	// §7.5 macOS 15
	"Only one netdiag on the Mac.", "The page opens with its buttons working.", "netdiag finds the chat server.", "The chat file is private.",
	"No Location button on a Mac.", "The Mac build.",
	// §7.5 both
	"Separate data folders, separate netdiags.", "Another user's netdiag does not take your page.",
	// M12's checks with a live agent
	"Claude checks the chat alerts.", "Claude Desktop sees the window.",
	// §7.6
	"Release candidate scan.", "Release build scan.",
}

// item is one checklist entry: "- [ ] **Title.** Steps: …" followed by its
// "Date: … · OS: … · Result: …" line.
var (
	itemLine   = regexp.MustCompile(`^- \[( |x)\] \*\*([^*]+)\*\* (.*)$`)
	resultLine = regexp.MustCompile(`^  Date:(.*)· OS:(.*)· Result:(.*)$`)
)

// TestFieldChecklistV06: the v0.6 section holds every check in v06Items,
// each with its steps and a Date, OS and Result line, and an item is ticked
// only with its date, OS and result filled in (v0.6 §6 M13 acceptance).
func TestFieldChecklistV06(t *testing.T) {
	doc := read(t, "docs/field-checklist.md")
	at, results := strings.Index(doc, "\n"+v06Section+"\n"), strings.Index(doc, "\n## Results\n")
	if at < 0 || results < at {
		t.Fatalf("docs/field-checklist.md needs %q before \"## Results\"", v06Section)
	}
	lines := strings.Split(doc[at+1:results], "\n")
	seen := map[string]bool{}
	for i, line := range lines {
		m := itemLine.FindStringSubmatch(line)
		if m == nil {
			if strings.HasPrefix(line, "- [") {
				t.Errorf("%q is not a check with a bold title", line)
			}
			continue
		}
		ticked, title, rest := m[1] == "x", m[2], m[3]
		if seen[title] {
			t.Errorf("%q is listed twice", title)
		}
		seen[title] = true
		if !strings.HasPrefix(rest, "Steps: ") || len(rest) < 80 {
			t.Errorf("%q has no steps (\"Steps: …\" with what to do and what to see)", title)
		}
		if i+1 >= len(lines) {
			t.Errorf("%q has no Date, OS and Result line", title)
			continue
		}
		r := resultLine.FindStringSubmatch(lines[i+1])
		if r == nil {
			t.Errorf("%q: the next line is %q, want \"  Date: · OS: · Result:\"", title, lines[i+1])
			continue
		}
		if ticked && (strings.TrimSpace(r[1]) == "" || strings.TrimSpace(r[2]) == "" || strings.TrimSpace(r[3]) == "") {
			t.Errorf("%q is ticked without its date, OS and result", title)
		}
	}
	for _, want := range v06Items {
		if !seen[want] {
			t.Errorf("the v0.6 section has no %q check", want)
		}
	}
	if len(seen) != len(v06Items) {
		t.Errorf("the v0.6 section has %d checks, v06Items %d: add the new one to v06Items", len(seen), len(v06Items))
	}
}
