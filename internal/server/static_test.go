package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"netdiag/internal/store"
	"netdiag/web"
)

func TestStaticAssetsServed(t *testing.T) {
	s := New(Config{Static: web.FS()})
	if err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for _, p := range []string{"/", "/app.js", "/styles.css", "/vendor/alpine.min.js"} {
		req := httptest.NewRequest("GET", p, nil)
		req.Host = strings.TrimPrefix(s.URL(), "http://")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", p, rec.Code)
		}
		b, _ := io.ReadAll(rec.Body)
		if len(b) == 0 {
			t.Errorf("%s: empty body", p)
		}
		if p == "/" && !strings.Contains(string(b), "vendor/alpine.min.js") {
			t.Errorf("index missing alpine script tag")
		}
	}
}

func TestNoExternalReferences(t *testing.T) {
	re := regexp.MustCompile(`(?i)(src|href)\s*=\s*["']?(https?:)?//`)
	b, err := readAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if re.Match(b) {
		t.Errorf("index.html references external resource")
	}
	js, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`https?://`).Match(js) {
		t.Errorf("app.js contains absolute URL")
	}
}

func readAsset(name string) ([]byte, error) {
	f, err := web.FS().Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func TestLiveModeUI(t *testing.T) {
	html, err := readAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`type="checkbox" id="live-mode"`, `x-model="live"`, "Stop live", ">NEW<", `aria-live="polite"`, "liveText()"} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html missing %q", want)
		}
	}
	for _, want := range []string{"{id: 'new', label: 'New'}", "localStorage", "'New device: '", "on('live'", "on('snapshot'"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js missing %q", want)
		}
	}
}

func TestOfflineUI(t *testing.T) {
	html, err := readAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{">OFFLINE<", "offline: isOffline(h)", "offlineFor(h)"} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html missing %q", want)
		}
	}
	for _, want := range []string{"{id: 'offline', label: 'Offline'}", "on('presence'", "'Device went offline: '",
		"'Device back online: '", "' offline'", "if (this.isOffline(h)) return true;"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js missing %q", want)
		}
	}
}

func TestV02UI(t *testing.T) {
	html, err := readAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`id="health"`, `aria-labelledby="health-h"`, "Recheck", `<dialog id="settings-dialog"`,
		`id="alerts"`, `aria-label="Alerts"`, `id="tab-history"`, ">History<", "CONFLICT", "Ignore this MAC",
		"Enable desktop alerts", "Export diff CSV", `role="alert"`} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html missing %q", want)
		}
	}
	for _, want := range []string{"{id: 'conflicts', label: 'Conflicts'}", "on('notice'", "on('health'", "on('conflict'",
		"n.replay", "Health: ", "Notification.requestPermission", "/api/history", "/api/compare", "/api/conflicts/ignore", "/api/health/run"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js missing %q", want)
		}
	}
	// Desktop permission must only be requested from a click handler.
	if strings.Count(string(js), "requestPermission") != 1 {
		t.Errorf("expected exactly one requestPermission call")
	}
}

// Notice ids restart at n-1 for every scan, so the UI must dedupe per scan
// and reset its seen set when a new scan starts. Otherwise a second scan's
// alerts are silently dropped.
func TestNoticeDedupeScopedToScan(t *testing.T) {
	js, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	for _, want := range []string{
		"onNotice(n, scanId)",
		"const key = (scanId || '') + ':' + n.id;",
		"this.seenNotices[key]",
		"on('notice', n => this.onNotice(n, id));",
		"this.seenNotices = {};",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("app.js missing %q", want)
		}
	}
	if strings.Contains(src, "this.seenNotices[n.id]") {
		t.Errorf("app.js still dedupes on bare notice id")
	}
}

// An OK notice turns its fault's banner (same key) into a Fixed banner with
// a shape and a word, never beeps, and replaces the desktop notification
// with the same tag, silently (v0.6-D8). The banner and the notification
// keep the OK message, which names what was fixed.
func TestFixedBanners(t *testing.T) {
	js, _ := readAsset("app.js")
	html, _ := readAsset("index.html")
	src := string(js)
	for _, want := range []string{
		"if (n.ok) { this.onFixed(n, key); return; }",
		"x.key === n.key && !x.ok",
		"'Fixed: '",
		"tag: n.key || n.id, silent: true",
		"body: (n.message ? n.message + ' ' : '') + 'Fixed '",
		"return this.fixedText(b) + (b.message ? ' ' + b.message : '');",
		"(was down ",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("app.js missing %q", want)
		}
	}
	i := strings.Index(src, "onFixed(n, bid) {")
	j := strings.Index(src, "fixedText(n) {")
	if i < 0 || j < i || strings.Contains(src[i:j], "beep(") {
		t.Errorf("onFixed must not beep")
	}
	for _, want := range []string{"sevIcon(b.ok ? 'ok' : b.severity)", "bannerLead(b)", "bannerText(b)", "b.ok ? 'sev-ok'"} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html missing %q", want)
		}
	}
}

// The advice UI (v0.6 M7): health cards say what a result means with the raw
// detail under Technical details, banners have What to try and Show device,
// the drawer shows the conflict pair (with the makers the server names) and
// the printer-without-status block, and Settings has the "This network has
// no internet" switch, kept per network. Links come only from server data
// (no URL literal in app.js, TestNoExternalReferences). Only a mac_flap can
// read "Conflict". Once the health checks are fine, the Health panel's line
// says "Everything looks fine" only when nothing the page lists is a problem
// by the Problems card's rules (v0.6-D27); otherwise it speaks for the
// health checks alone.
func TestAdviceUI(t *testing.T) {
	js, _ := readAsset("app.js")
	html, _ := readAsset("index.html")
	for _, want := range []string{
		"<strong>What this means:</strong>", "<summary>Technical details</summary>", "What to try<span",
		"Show device<span", `class="plain cpair"`, `id="printer-nostatus"`, `id="set-nointernet"`,
		"This network has no internet</label>", `x-model="noInternetHere" :disabled="!healthNetwork()"`,
		`id="health-fine"`, `<span x-text="healthFineText()"></span>`,
		"conflictSeen(c) ? 'Conflict' : 'Possible conflict'", `:href="linkAction(a) ? a.target : null"`,
		`rel="noopener noreferrer"`,
	} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html missing %q", want)
		}
	}
	for _, want := range []string{
		"this.api('POST', '/api/os/open', {page: page})", "a.kind !== 'osSettings' || this.isWindows()",
		"/^https?:/i.test(a.target || '')", "noInternetNetworks: 'This network has no internet'",
		"return this.alternations(hist) >= 2;", "if (c.kind !== 'mac_flap') return false;",
		"noInternet: 'set-nointernet'", "vendors[mac] ||", "this.health && this.health.network",
		"'Everything looks fine' : 'All health checks look fine'", "if (!this.healthFine()) return false;",
		"on('deviceOffline', false)",
	} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js missing %q", want)
		}
	}
}

// Banner identity must be unique across scans (scan id + notice id), used as
// the x-for key and for dismiss.
func TestBannerIdentityUniqueAcrossScans(t *testing.T) {
	js, _ := readAsset("app.js")
	html, _ := readAsset("index.html")
	for _, want := range []string{"{bid: key}", "b.bid !== bid"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js missing %q", want)
		}
	}
	for _, want := range []string{`:key="b.bid"`, "dismiss(b.bid)"} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html missing %q", want)
		}
	}
	if strings.Contains(string(html), `:key="b.id"`) || strings.Contains(string(html), "dismiss(b.id)") {
		t.Errorf("index.html still keys banners on notice id")
	}
}

// Fonts must be served as font/woff2, stay within the embedded font budget,
// and the theme script must be a non-deferred <head> script so the saved
// theme applies before first paint.
func TestFontsAndThemeUI(t *testing.T) {
	s := New(Config{Static: web.FS()})
	if err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	fonts := []string{"Inter-Variable-latin.woff2", "JetBrainsMono-latin-400.woff2", "JetBrainsMono-latin-700.woff2"}
	total := 0
	for _, f := range fonts {
		p := "/vendor/fonts/" + f
		req := httptest.NewRequest("GET", p, nil)
		req.Host = strings.TrimPrefix(s.URL(), "http://")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", p, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "font/woff2" {
			t.Errorf("%s: Content-Type %q, want font/woff2", p, ct)
		}
		b, err := readAsset("vendor/fonts/" + f)
		if err != nil {
			t.Fatal(err)
		}
		total += len(b)
	}
	for _, f := range []string{"OFL-Inter.txt", "OFL-JetBrainsMono.txt"} {
		if _, err := readAsset("vendor/fonts/" + f); err != nil {
			t.Errorf("missing license %s: %v", f, err)
		}
	}
	if total > 460800 {
		t.Errorf("embedded fonts total %d bytes, budget 460800", total)
	}

	html, err := readAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	head := string(html)
	themeTag := `<script src="theme.js"></script>`
	if !strings.Contains(head, themeTag) {
		t.Errorf("index.html missing non-deferred %q", themeTag)
	}
	if strings.Index(head, themeTag) > strings.Index(head, `<link rel="stylesheet" href="styles.css">`) {
		t.Errorf("theme.js must load before styles.css in <head>")
	}
	for _, want := range []string{`data-theme="dark"`, `themebtn`, `:aria-pressed="theme === 'light'"`, `toggleTheme()`, "Light theme"} {
		if !strings.Contains(head, want) {
			t.Errorf("index.html missing %q", want)
		}
	}

	js, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"toggleTheme() {", "'netdiag.theme'"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js missing %q", want)
		}
	}

	css, err := readAsset("styles.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"@font-face", "font-display:swap", ":root[data-theme=light]", "--font-mono:", "--accent:#c2410c"} {
		if !strings.Contains(string(css), want) {
			t.Errorf("styles.css missing %q", want)
		}
	}

	th, err := readAsset("theme.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(th), "try") || !strings.Contains(string(th), "'netdiag.theme'") {
		t.Errorf("theme.js must read netdiag.theme inside try/catch")
	}

	for _, name := range []string{"styles.css", "theme.js"} {
		b, err := readAsset(name)
		if err != nil {
			t.Fatal(err)
		}
		if regexp.MustCompile(`https?://`).Match(b) {
			t.Errorf("%s contains absolute URL", name)
		}
	}
}

func TestAppShellUI(t *testing.T) {
	html, err := readAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<aside class="sidebar"`, `aria-label="Main"`, `id="tab-devices"`, `id="tab-health"`,
		`id="tab-activity"`, `id="tab-history"`, `id="nav-settings"`, `class="menubtn"`,
		`:aria-expanded="navOpen"`, `class="topbar bar"`, `id="activity"`, `class="menu"`,
		`view === 'devices'`, `view === 'health'`, `view === 'history'`,
	} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html missing shell hook %q", want)
		}
	}
	js, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'netdiag.view'", "views: ['devices', 'health', 'activity', 'history']"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js missing %q", want)
		}
	}
}

func TestDeviceDrawerUI(t *testing.T) {
	html, err := readAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`id="drawer"`, `role="dialog" aria-modal="false" aria-labelledby="drawer-h"`, `id="host-search"`,
		`class="rowbtn mono"`, `Close details`, `Copy IP address`, `icons.svg#i-`,
	} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html missing drawer hook %q", want)
		}
	}
	js, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"openDrawer(ip)", "closeDrawer(noFocus)", "drawerHost()", "hostText(h)"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js missing drawer hook %q", want)
		}
	}
	svg, err := readAsset("icons.svg")
	if err != nil || !strings.Contains(string(svg), `id="i-printer"`) {
		t.Errorf("icons.svg missing or lacks printer symbol: %v", err)
	}
}

// Sprite budget and reference checks for web/icons.svg (T3).
const iconSpriteMaxBytes = 40960

var iconRefRe = regexp.MustCompile(`icons\.svg#(i-[a-z0-9-]+)`)

func TestIconSpriteBudgetAndRefs(t *testing.T) {
	sprite, err := readAsset("icons.svg")
	if err != nil {
		t.Fatal(err)
	}
	if len(sprite) > iconSpriteMaxBytes {
		t.Errorf("icons.svg is %d bytes, limit %d", len(sprite), iconSpriteMaxBytes)
	}
	s := string(sprite)
	if !strings.HasPrefix(s, "<!-- Lucide v") {
		t.Errorf("icons.svg missing generated Lucide header comment")
	}
	// Only the SVG namespace URL may appear; no external references.
	if strings.Contains(strings.ReplaceAll(s, `xmlns="http://www.w3.org/2000/svg"`, ""), "http") {
		t.Errorf("icons.svg contains an external URL")
	}
	has := func(id string) bool { return strings.Contains(s, `<symbol id="`+id+`"`) }

	// (b) every literal icons.svg#i-<id> reference must have a symbol.
	for _, name := range []string{"index.html", "app.js"} {
		b, err := readAsset(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range iconRefRe.FindAllStringSubmatch(string(b), -1) {
			if !has(m[1]) {
				t.Errorf("%s references missing sprite symbol %s", name, m[1])
			}
		}
	}

	// (c) ids referenced via the dynamic 'icons.svg#i-' + type prefix.
	dynamic := []string{"i-printer", "i-router", "i-web", "i-computer", "i-device",
		"i-type-gateway", "i-type-printer", "i-type-computer", "i-type-phone", "i-type-tv",
		"i-type-nas", "i-type-camera", "i-type-network", "i-type-iot", "i-type-server",
		"i-type-self", "i-type-unknown",
		// statusIcon/statusShape/checkIcon/sevIcon/kindIcon/tileDefs helpers in app.js.
		"i-circle-check", "i-circle-help", "i-circle-x", "i-triangle-alert", "i-info",
		"i-router", "i-network", "i-book-search", "i-cloud", "i-wifi", "i-server", "i-activity",
		"i-radar", "i-circle-plus", "i-wifi-off", "i-heart-pulse", "i-bell", "i-globe", "i-radio-tower",
		"i-printer", "i-chevron-down", "i-chevron-right",
		// actionIcon (advice buttons, v0.6 M7).
		"i-refresh-cw", "i-settings", "i-monitor", "i-map-pin", "i-external-link"}
	for _, id := range dynamic {
		if !has(id) {
			t.Errorf("icons.svg missing dynamically referenced symbol %s", id)
		}
	}
}

func TestIconLicenseAndVersionsEmbedded(t *testing.T) {
	lic, err := readAsset("vendor/LICENSE-lucide.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(lic))) == 0 {
		t.Errorf("vendor/LICENSE-lucide.txt is empty")
	}
	ver, err := readAsset("vendor/VERSIONS.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ver), "lucide-static") {
		t.Errorf("vendor/VERSIONS.txt does not mention lucide-static")
	}
}

func TestMCPSettingsUI(t *testing.T) {
	b, err := readAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`id="mcp-enabled"`, `role="switch"`, `Agent access (MCP)`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("index.html missing %q", want)
		}
	}
}

// Every path a settings load can list in repaired (store.RepairPaths: each
// field and each object of fields) has a plain label in app.js
// repairedLabels for the "Some settings were not valid and were reset"
// notice, and there is no label for a path that does not exist. So a new
// settings field, or a field that is renamed, cannot ship without one.
func TestSettingsRepairedUI(t *testing.T) {
	html, err := readAsset("index.html")
	if err != nil {
		t.Fatal(err)
	}
	js, err := readAsset("app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `id="settings-repaired"`) {
		t.Error(`index.html missing id="settings-repaired"`)
	}
	if !strings.Contains(string(js), "Some settings were not valid and were reset: ") {
		t.Error("app.js missing the repaired settings notice")
	}
	block := func(name string) string {
		m := regexp.MustCompile(`(?s)\n\s*` + name + `: [{\[](.*?)[}\]],\n`).FindStringSubmatch(string(js))
		if m == nil {
			t.Fatalf("app.js has no %s", name)
		}
		return m[1]
	}
	labels := map[string]string{}
	for _, m := range regexp.MustCompile(`(?:'([\w.]+)'|(\w+)): '([^']*)'`).FindAllStringSubmatch(block("repairedLabels"), -1) {
		labels[m[1]+m[2]] = m[3]
	}
	paths := store.RepairPaths()
	for _, p := range paths {
		if strings.TrimSpace(labels[p]) == "" {
			t.Errorf("app.js repairedLabels has no label for %s", p)
		}
	}
	for p := range labels {
		if !slices.Contains(paths, p) {
			t.Errorf("app.js repairedLabels has %s, which is not a settings path", p)
		}
	}
	for _, m := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(block("repairedOffPage"), -1) {
		if !slices.Contains(paths, m[1]) {
			t.Errorf("app.js repairedOffPage has %s, which is not a settings path", m[1])
		}
	}
	// What RepairSettings resets is a subset of RepairPaths.
	all := store.DefaultSettings()
	all.PrinterStatusPollSec, all.WebServer.URL, all.WebServer.ExpectStatus = 1, "ftp://x", 1
	all.WebServer.TrustedCert, all.InternetCheckURL = "abc", "nope"
	all.Alerts.SupplyThresholdPct, all.Alerts.CooldownSec = 0, 1
	all.IgnoredConflictMACs, all.Chat.MinSeverity = []string{"zz"}, "loud"
	_, fixed := store.RepairSettings(all)
	if len(fixed) != 9 {
		t.Fatalf("repaired paths %v", fixed)
	}
	for _, p := range fixed {
		if !slices.Contains(paths, p) {
			t.Errorf("RepairSettings reports %s, which RepairPaths does not list", p)
		}
	}
}
