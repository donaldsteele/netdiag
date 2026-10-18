package docscheck

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
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

// retiredWords are words the page no longer shows, or that said something
// the page does not do (the v0.6 words audit). Each must stay gone from the
// docs and from the page itself, so a label changed in one place cannot
// live on in another.
var retiredWords = []struct {
	re  *regexp.Regexp
	why string
}{
	{regexp.MustCompile(`Recheck`), "the button that runs the health checks again is Check again"},
	{regexp.MustCompile(`was down|\(after (\d+|N) min\)`), `a fixed problem says how long it lasted: "(lasted 12 min)"`},
	{regexp.MustCompile(`(?i)gateway reachable|DNS resolving|internet reachability|\bDHCP lease\b|\bGateway (row|check)\b`), "the checks are Router, Getting an address (DHCP), Name lookup (DNS), Web server, Internet and Wi-Fi"},
	{regexp.MustCompile(`netdiag lost the network`), `the notices are "This computer lost the network" and "The router stopped answering"`},
	{regexp.MustCompile(`with (\*\*)?Live mode(\*\*)? ticked\.`), "ticking Live mode alone does not start watching: tick Live mode and press Scan"},
	{regexp.MustCompile(`Windows Firewall or a separate`), "the wizard says a firewall on this computer, on every OS"},
	{regexp.MustCompile(`(?i)\bno typing\b`), "typing the address is needed when netdiag can't find the server: say usually no typing"},
	{regexp.MustCompile(`(?i)by typing the address|typed by hand`), "a typed address in the browser netdiag opened works; another browser, or a page from before a restart, is locked"},
	{regexp.MustCompile(`Critical: |Notice: `), "the banners say Urgent, Warning, Note and Fixed, as the card does"},
	{regexp.MustCompile(`Health: [^"]*\bwarn\b`), `the header chip says "Health: 1 failing, 2 notes"`},
	{regexp.MustCompile(`(?i)\*\*critical\*\*|<b>critical</b>|\bare critical\b|\b(a|named) critical alerts?\b`), "the urgent level is called urgent, as on the banners, the card and Settings > Send"},
	{regexp.MustCompile(`off before, it stays off`), "a new connection and Reconnect turn Send alerts to team chat on; only Change channel leaves it as it was"},
	{regexp.MustCompile(`pages already open work again too\.`), "only the pages in the browser netdiag opens get the fresh key; another browser stays off"},
	{regexp.MustCompile(`\(lasted [^)]*\): Health check OK again`), "a fixed item keeps the problem's own title (Health check failed), as the ledger does"},
}

// retiredAllowed are the uses of a retired word that stay: "usually no
// typing", and a DHCP lease as the thing a computer gets (not the old name
// of the check).
var retiredAllowed = []string{"usually no typing", "Usually no typing", "on a DHCP lease", "its DHCP lease"}

// TestDocsRetiredWords: see retiredWords and retiredAllowed.
func TestDocsRetiredWords(t *testing.T) {
	for _, name := range append([]string{"web/index.html", "web/app.js"}, userDocs...) {
		for i, line := range strings.Split(read(t, name), "\n") {
			for _, a := range retiredAllowed {
				line = strings.ReplaceAll(line, a, "")
			}
			for _, w := range retiredWords {
				if m := w.re.FindString(line); m != "" {
					t.Errorf("%s:%d says %q: %s", name, i+1, m, w.why)
				}
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

// rigShots are the screenshots only the test rig can take: the real Zebra
// ZD621 and a bubbles server. Field check "Cover open, then closed." (6.1)
// retakes them all in one run.
var rigShots = []string{"real-zebra-fault", "real-zebra-fault-drawer", "real-zebra-recovered", "real-zebra-activity", "bubbles-resolved"}

// TestShotsGenerated: every screenshot the README and the site show is made
// by a spec (docs-shots for docs/img, site-shots for docs/assets/shots), or
// is a test-rig shot that field check 6.1 retakes, so a release never shows
// a stale shot that nothing refreshes.
func TestShotsGenerated(t *testing.T) {
	docsSpec := read(t, "tools/uitest/tests/docs-shots.spec.mjs")
	siteSpec := read(t, "tools/uitest/tests/site-shots.spec.mjs")
	saved := map[string]bool{}
	for _, m := range regexp.MustCompile(`save\(\w+, '([\w-]+)'\)`).FindAllStringSubmatch(docsSpec, -1) {
		saved[m[1]] = true
	}
	// site-shots names a shot 'name', 'name.png' or 'name-' + theme.
	siteMakes := func(name string) bool {
		if strings.Contains(siteSpec, "'"+name+"'") || strings.Contains(siteSpec, "'"+name+".png'") {
			return true
		}
		for _, theme := range []string{"-dark", "-light"} {
			if p, ok := strings.CutSuffix(name, theme); ok && strings.Contains(siteSpec, "'"+p+"-' + theme") {
				return true
			}
		}
		return false
	}
	_, rig, _ := strings.Cut(read(t, "docs/field-checklist.md"), "**Cover open, then closed.**")
	rig, _, _ = strings.Cut(rig, "\n- [")
	shown := map[string]bool{}
	for _, m := range mdImage.FindAllStringSubmatch(read(t, "README.md"), -1) {
		shown[m[1]] = true
	}
	for _, m := range htmlImage.FindAllStringSubmatch(read(t, "docs/index.html"), -1) {
		shown["docs/"+m[1]] = true
	}
	for ref := range shown {
		dir, file := path.Split(ref)
		name, ok := strings.CutSuffix(file, ".png")
		if !ok {
			continue
		}
		switch {
		case slices.Contains(rigShots, name):
			if !strings.Contains(rig, file+"`") {
				t.Errorf("%s is a test-rig shot, but field check 6.1 does not retake it", ref)
			}
		case dir == "docs/img/":
			if !saved[name] {
				t.Errorf("the README shows %s, which docs-shots.spec.mjs does not make", ref)
			}
		case dir == "docs/assets/shots/":
			if !siteMakes(name) {
				t.Errorf("%s is shown, but site-shots.spec.mjs does not make it", ref)
			}
		default:
			t.Errorf("%s is neither in docs/img nor in docs/assets/shots", ref)
		}
	}
	for _, name := range rigShots {
		if !strings.Contains(rig, name+".png`") {
			t.Errorf("field check 6.1 does not take %s.png", name)
		}
	}
}

// fieldSections are the field checklist's release sections, in order, each
// with its checks by bold title. Master roadmap §8.13 puts "## 6. v0.6 …"
// to "## 9. v0.9 …" before "## Results"; a release adds its section here
// with its own list, and each section ends at the next "## " heading.
var fieldSections = []struct {
	heading string
	items   []string
}{
	{"## 6. v0.6 chat alerts and watching", v06Items},
}

// v06Items are the v0.6 field checks, by their bold title: the real
// hardware runs of v0.6 §7.4 (1-7), the standard-user runs of §7.5, the
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
	// §7.4 item 7, mDNS on a real network, run on both (M2's F14 types)
	"A phone shows as a phone.", "Macs, Apple TVs and HomePods keep their types.", "The connect wizard finds bubbles by its mDNS name.",
	// M9's and M12's checks with a live agent
	"Claude Code explains the problems.", "Claude checks the chat alerts.", "Claude Desktop sees the window.",
	// §7.6
	"Release candidate scan.", "Release build scan.",
}

// item is one checklist entry: "- [ ] **Title.** Steps: …" followed by its
// "Date: … · OS: … · Result: …" line.
var (
	itemLine   = regexp.MustCompile(`^- \[( |x)\] \*\*([^*]+)\*\* (.*)$`)
	resultLine = regexp.MustCompile(`^  Date:(.*)· OS:(.*)· Result:(.*)$`)
)

// fieldSection is the text of the section under heading, up to the next
// "## " heading (a "### " subsection stays in it), and whether the section
// is there before "## Results".
func fieldSection(doc, heading string) (string, bool) {
	at, results := strings.Index(doc, "\n"+heading+"\n"), strings.Index(doc, "\n## Results\n")
	if at < 0 || results < at {
		return "", false
	}
	body := doc[at+len(heading)+2:]
	if end := strings.Index(body, "\n## "); end >= 0 {
		body = body[:end]
	}
	return body, true
}

// TestFieldChecklist: each release section holds exactly its checks, each
// with its steps and a Date, OS and Result line, and an item is ticked only
// with its date, OS and result filled in (v0.6 §6 M13 acceptance).
func TestFieldChecklist(t *testing.T) {
	doc := read(t, "docs/field-checklist.md")
	for _, sec := range fieldSections {
		body, ok := fieldSection(doc, sec.heading)
		if !ok {
			t.Errorf("docs/field-checklist.md needs %q before \"## Results\"", sec.heading)
			continue
		}
		lines := strings.Split(body, "\n")
		seen := map[string]bool{}
		for i, line := range lines {
			m := itemLine.FindStringSubmatch(line)
			if m == nil {
				if strings.HasPrefix(line, "- [") {
					t.Errorf("%s: %q is not a check with a bold title", sec.heading, line)
				}
				continue
			}
			ticked, title, rest := m[1] == "x", m[2], m[3]
			if seen[title] {
				t.Errorf("%s: %q is listed twice", sec.heading, title)
			}
			seen[title] = true
			if !strings.HasPrefix(rest, "Steps: ") || len(rest) < 80 {
				t.Errorf("%s: %q has no steps (\"Steps: …\" with what to do and what to see)", sec.heading, title)
			}
			if i+1 >= len(lines) {
				t.Errorf("%s: %q has no Date, OS and Result line", sec.heading, title)
				continue
			}
			r := resultLine.FindStringSubmatch(lines[i+1])
			if r == nil {
				t.Errorf("%s: %q: the next line is %q, want \"  Date: · OS: · Result:\"", sec.heading, title, lines[i+1])
				continue
			}
			if ticked && (strings.TrimSpace(r[1]) == "" || strings.TrimSpace(r[2]) == "" || strings.TrimSpace(r[3]) == "") {
				t.Errorf("%s: %q is ticked without its date, OS and result", sec.heading, title)
			}
		}
		for _, want := range sec.items {
			if !seen[want] {
				t.Errorf("%s has no %q check", sec.heading, want)
			}
		}
		if len(seen) != len(sec.items) {
			t.Errorf("%s has %d checks, its list in fieldSections %d: add the new one there", sec.heading, len(seen), len(sec.items))
		}
	}
}

// TestFieldSectionEndsAtNextHeading: a later release's section is not
// counted as part of the one before it, and a "### " subsection is.
func TestFieldSectionEndsAtNextHeading(t *testing.T) {
	doc := "# Checklist\n\n## 6. v0.6 a\n\n### 6.1 b\n- [ ] six\n\n## 7. v0.7 c\n- [ ] seven\n\n## Results\n"
	if body, ok := fieldSection(doc, "## 6. v0.6 a"); !ok || !strings.Contains(body, "six") || strings.Contains(body, "seven") {
		t.Errorf("v0.6 section = %q, %v", body, ok)
	}
	if body, ok := fieldSection(doc, "## 7. v0.7 c"); !ok || !strings.Contains(body, "seven") || strings.Contains(body, "Results") {
		t.Errorf("v0.7 section = %q, %v", body, ok)
	}
	if _, ok := fieldSection("## Results\n\n## 8. v0.8 d\n", "## 8. v0.8 d"); ok {
		t.Error("a section after \"## Results\" counts")
	}
}
