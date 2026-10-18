package advice

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"netdiag/internal/model"
)

var oses = []string{"windows", "darwin", "linux"}

// statusFor is a status each reason can come with.
func statusFor(id, reason string) string {
	switch reason {
	case model.ReasonTCPOnly:
		return model.HealthOK
	case model.ReasonTimeout, model.ReasonError, model.ReasonNoGateway, model.ReasonPingError, model.ReasonHiddenLocation:
		return model.HealthUnavailable
	case model.ReasonNoLease, model.ReasonNotConnected, model.ReasonNotSet:
		return model.HealthInfo
	}
	if id == "internet" {
		return model.HealthInfo
	}
	return model.HealthWarn
}

func check(id, reason string, data map[string]any) model.HealthCheck {
	d := map[string]any{"reason": reason}
	for k, v := range data {
		d[k] = v
	}
	return model.HealthCheck{ID: id, Status: statusFor(id, reason), Detail: "raw detail for " + id, LatencyMs: 182, Data: d}
}

// every returns every piece of advice the catalogue can give, by a name
// for the failure message.
func every() map[string]*model.Advice {
	out := map[string]*model.Advice{}
	x := func(goos string) Ctx {
		return Ctx{GOOS: goos, Gateway: "192.168.1.1", Hosts: map[string]model.Host{
			"192.168.1.50": {IP: "192.168.1.50", MACHistory: []model.MACObservation{{MAC: "A"}, {MAC: "B"}, {MAC: "A"}}},
			"192.168.1.60": {IP: "192.168.1.60", MACHistory: []model.MACObservation{{MAC: "A"}, {MAC: "B"}}},
		}}
	}
	for _, goos := range oses {
		for id, reasons := range Reasons() {
			for _, r := range reasons {
				for _, data := range []map[string]any{nil, {"noInternet": true, "status": 503, "signalPct": 32,
					"pingError": true, "certSHA256": strings.Repeat("ab", 32)}} {
					name := goos + " " + id + " " + r
					if data != nil {
						name += " with data"
					}
					out[name] = Health(check(id, r, data), x(goos))
				}
			}
		}
		for _, tr := range Triggers() {
			for _, code := range []string{"", "hrPrinterDetectedErrorState.doorOpen", "hrPrinterDetectedErrorState.jammed",
				"hrPrinterDetectedErrorState.noPaper", "hrPrinterDetectedErrorState.noToner", "hrPrinterDetectedErrorState.lowPaper",
				"hrPrinterDetectedErrorState.lowToner", "hrPrinterDetectedErrorState.offline", "media-jam-error", "cover-open-warning",
				"marker-supply-low-report", "paused", "hrDeviceStatus.down"} {
				if tr != model.TriggerPrinterAlert && code != "" {
					continue
				}
				n := model.Notice{Trigger: tr, IP: "192.168.1.50", Code: code, Detail: "Black Toner", Message: "raw message"}
				out[goos+" notice "+tr+" "+code] = Notice(n, nil, x(goos))
			}
		}
		// The storm guard's notice says why it holds (v0.6 M6).
		for _, reason := range []string{model.HeldLink, model.HeldGateway, model.HeldNetworkChanged} {
			n := model.Notice{Trigger: model.TriggerNetdiagLink, Code: reason, Message: "raw message"}
			out[goos+" notice netdiagLink "+reason] = Notice(n, nil, x(goos))
		}
		for _, kind := range ConflictKinds() {
			for _, ip := range []string{"192.168.1.50", "192.168.1.60"} {
				c := model.Conflict{Kind: kind, IPs: []string{ip}, Name: "printer-2", Detail: "raw conflict detail"}
				out[goos+" conflict "+kind+" "+ip] = Conflict(c, x(goos))
			}
		}
	}
	out["printer without status"] = PrinterNoStatus(model.Host{IP: "192.168.1.9", IsPrinter: true,
		Web: []model.WebService{{URL: "http://192.168.1.9/"}}})
	return out
}

// TestCatalogueComplete: every check and reason, every trigger and every
// conflict kind has advice with a Meaning, at most three steps and only
// known button labels.
func TestCatalogueComplete(t *testing.T) {
	for name, a := range every() {
		if a == nil {
			t.Errorf("%s: no advice", name)
			continue
		}
		if strings.TrimSpace(a.Meaning) == "" {
			t.Errorf("%s: empty Meaning", name)
		}
		if len(a.Steps) > MaxSteps {
			t.Errorf("%s: %d steps", name, len(a.Steps))
		}
		for _, s := range a.Steps {
			if strings.TrimSpace(s) == "" || !strings.HasSuffix(s, ".") {
				t.Errorf("%s: step %q", name, s)
			}
		}
		if !strings.HasSuffix(a.Meaning, ".") {
			t.Errorf("%s: Meaning %q does not end a sentence", name, a.Meaning)
		}
		if name != "printer without status" && !strings.HasPrefix(a.Technical, "raw") {
			t.Errorf("%s: Technical %q, want the raw detail", name, a.Technical)
		}
		for _, act := range a.Actions {
			if !slices.Contains(ActionLabels(), act.Label) {
				t.Errorf("%s: unknown label %q", name, act.Label)
			}
		}
	}
	if a := PrinterNoStatus(model.Host{IsPrinter: true}); a == nil || a.Technical != "" {
		t.Errorf("printer without status: %+v", a)
	}
	// Every reason the health checks use is in Reasons (model.Reason*).
	all := map[string]bool{}
	for _, rs := range Reasons() {
		for _, r := range rs {
			all[r] = true
		}
	}
	for _, r := range []string{model.ReasonTimeout, model.ReasonError, model.ReasonNoReply, model.ReasonTCPOnly,
		model.ReasonSlow, model.ReasonNoGateway, model.ReasonPingError, model.ReasonAPIPA, model.ReasonNoLease,
		model.ReasonLocalMismatch, model.ReasonNXDomain, model.ReasonNoInternet, model.ReasonNoServer, model.ReasonNotSet,
		model.ReasonStatus, model.ReasonContent, model.ReasonRequest, model.ReasonTLS, model.ReasonRedirect,
		model.ReasonInvalid, model.ReasonExpired, model.ReasonPortal, model.ReasonNoResponse, model.ReasonWeak,
		model.ReasonNotConnected, model.ReasonHiddenLocation} {
		if !all[r] {
			t.Errorf("reason %s is in no check's list", r)
		}
	}
}

// wordingRe is the product wording rule (master roadmap §2), applied after
// the product term Wake-on-LAN is removed.
var wordingRe = regexp.MustCompile(`(?i)\b(events?|venues?|LAN)\b`)

// technicalRe lists words a Meaning never uses: they belong in Technical.
var technicalRe = regexp.MustCompile(`(?i)\b(ARP|DHCP|DNS|SNMP|MAC|ICMP|IP address(es)?|subnets?)\b`)

// placeRe lists words that point at a place on one page. The same advice
// is shown in the drawer, on the Problems right now card, in team chat and
// to Claude, where nothing is above or below it.
var placeRe = regexp.MustCompile(`(?i)\b(below|above)\b`)

func TestWordingLint(t *testing.T) {
	for name, a := range every() {
		if a == nil {
			continue
		}
		texts := append([]string{a.Meaning}, a.Steps...)
		for _, act := range a.Actions {
			texts = append(texts, act.Label)
		}
		for _, s := range texts {
			if m := wordingRe.FindString(strings.ReplaceAll(s, "Wake-on-LAN", "")); m != "" {
				t.Errorf("%s: %q uses %q", name, s, m)
			}
			if m := placeRe.FindString(s); m != "" {
				t.Errorf("%s: %q says %q, which is not true everywhere the advice is shown", name, s, m)
			}
		}
		if m := technicalRe.FindString(a.Meaning); m != "" {
			t.Errorf("%s: Meaning %q uses %q", name, a.Meaning, m)
		}
	}
	// The lint itself.
	for _, bad := range []string{"the event staff", "Events", "a venue", "the LAN"} {
		if !wordingRe.MatchString(bad) {
			t.Errorf("wording lint misses %q", bad)
		}
	}
	if wordingRe.MatchString(strings.ReplaceAll("Use Wake-on-LAN", "Wake-on-LAN", "")) || wordingRe.MatchString("get_events") {
		t.Error("wording lint rejects an allowed term")
	}
	for _, bad := range []string{"its MAC changed", "the DNS server", "an IP address"} {
		if !technicalRe.MatchString(bad) {
			t.Errorf("technical lint misses %q", bad)
		}
	}
	for _, bad := range []string{"Look at the two devices below.", "the steps above"} {
		if !placeRe.MatchString(bad) {
			t.Errorf("place lint misses %q", bad)
		}
	}
}

// TestHealthAdvice checks the catalogue's exact wording for the plan's
// cases and that results without a reason, and ok results, get none.
func TestHealthAdvice(t *testing.T) {
	x := Ctx{GOOS: "windows", Gateway: "192.168.1.1"}
	cases := []struct {
		id, reason string
		data       map[string]any
		meaning    string
		kinds      []string
	}{
		{"gateway", model.ReasonNoReply, nil, "This computer can't reach your router.", []string{model.ActionRecheck}},
		{"gateway", model.ReasonTCPOnly, nil, "Your router doesn't answer ping, but its web page answers, so it is working.", []string{model.ActionOpenRouter}},
		{"gateway", model.ReasonTCPOnly, map[string]any{"pingError": true}, "netdiag couldn't send its test message from this computer, but your router's web page answers, so it is working.", []string{model.ActionOpenRouter}},
		{"gateway", model.ReasonSlow, nil, "Your router answers, but slowly (182 ms).", []string{model.ActionRecheck}},
		{"dhcp", model.ReasonAPIPA, nil, "This computer didn't get an address from the router, so it can't talk to other devices.", []string{model.ActionRecheck}},
		{"dns", model.ReasonNoInternet, nil, "Your local network works, but internet names can't be looked up. Usually this means there is no internet connection right now.", []string{model.ActionRecheck, model.ActionOpenSettings}},
		{"dns", model.ReasonNoInternet, map[string]any{"noInternet": true}, "This network is set up without internet, so internet names can't be looked up. That is expected.", []string{model.ActionOpenSettings}},
		{"dns", model.ReasonLocalMismatch, nil, "Your name server works, but this computer asks somewhere else first, for example an old VPN setting.", []string{model.ActionRecheck}},
		{"dns", model.ReasonNXDomain, nil, "The name server answers, but doesn't know the internet check address.", []string{model.ActionOpenSettings}},
		{"dns", model.ReasonNoReply, nil, "No name server answers, so this computer can't look up names.", []string{model.ActionRecheck}},
		{"dns", model.ReasonNoServer, nil, "This computer has no name server set.", nil},
		{"webserver", model.ReasonNotSet, nil, "No web server is set, so this check is skipped.", []string{model.ActionOpenSettings}},
		{"webserver", model.ReasonStatus, map[string]any{"status": float64(503)}, "The web server isn't answering as expected (HTTP 503).", []string{model.ActionOpenSettings, model.ActionRecheck}},
		{"webserver", model.ReasonTLS, map[string]any{"certSHA256": strings.Repeat("ab", 32)}, "The web server's certificate isn't trusted.", nil},
		{"webserver", model.ReasonTLS, nil, "The web server's certificate isn't trusted.", []string{model.ActionOpenSettings}},
		{"internet", model.ReasonPortal, nil, "This network wants you to sign in first, on a Wi-Fi login page.", nil},
		{"internet", model.ReasonNoResponse, nil, "netdiag couldn't reach the internet. Devices on the local network still work.", []string{model.ActionRecheck}},
		{"internet", model.ReasonNoResponse, map[string]any{"noInternet": true}, "This network is set up without internet, so this check is only for information.", []string{model.ActionOpenSettings}},
		{"wifi", model.ReasonWeak, map[string]any{"signalPct": 32}, "The Wi-Fi signal is weak (32%).", []string{model.ActionRecheck}},
		{"wifi", model.ReasonHiddenLocation, nil, "Windows hides Wi-Fi details until Location is turned on. The other checks still work.", []string{model.ActionOSSettings}},
	}
	for _, tc := range cases {
		a := Health(check(tc.id, tc.reason, tc.data), x)
		if a == nil || a.Meaning != tc.meaning {
			t.Errorf("%s %s: %+v", tc.id, tc.reason, a)
			continue
		}
		var kinds []string
		for _, act := range a.Actions {
			kinds = append(kinds, act.Kind)
		}
		if !slices.Equal(kinds, tc.kinds) {
			t.Errorf("%s %s: actions %v, want %v", tc.id, tc.reason, kinds, tc.kinds)
		}
		if a.Technical != "raw detail for "+tc.id {
			t.Errorf("%s %s: Technical %q", tc.id, tc.reason, a.Technical)
		}
	}
	// The no-internet advice opens Settings at the switch; the name and
	// internet check addresses at the Internet check URL; a certificate
	// offered for trust points at the card's Trust this certificate button,
	// and never claims Settings can trust it.
	for _, tc := range []struct {
		c      model.HealthCheck
		target string
	}{
		{check("dns", model.ReasonNoInternet, nil), model.SettingsSectionNoInternet},
		{check("dns", model.ReasonNoInternet, map[string]any{"noInternet": true}), model.SettingsSectionNoInternet},
		{check("internet", model.ReasonNoResponse, map[string]any{"noInternet": true}), model.SettingsSectionNoInternet},
		{check("dns", model.ReasonNXDomain, nil), model.SettingsSectionHealth},
		{check("internet", model.ReasonStatus, nil), model.SettingsSectionHealth},
		{check("webserver", model.ReasonNotSet, nil), model.SettingsSectionWebServer},
	} {
		a := Health(tc.c, x)
		i := slices.IndexFunc(a.Actions, func(a model.AdviceAction) bool { return a.Kind == model.ActionOpenSettings })
		if i < 0 || a.Actions[i].Target != tc.target {
			t.Errorf("%s %s: Open Settings %+v, want target %s", tc.c.ID, model.HealthReason(tc.c), a.Actions, tc.target)
		}
	}
	tls := Health(check("webserver", model.ReasonTLS, map[string]any{"certSHA256": strings.Repeat("ab", 32)}), x)
	if len(tls.Steps) == 0 || !strings.Contains(tls.Steps[0], "press Trust this certificate on the Web server card") {
		t.Errorf("tls: %+v", tls)
	}
	for _, s := range tls.Steps {
		if strings.Contains(s, "Settings") || strings.Contains(s, " here") {
			t.Errorf("tls step %q", s)
		}
	}
	if a := Health(check("wifi", model.ReasonError, nil), Ctx{GOOS: "darwin"}); a == nil ||
		a.Meaning != "macOS doesn't share Wi-Fi details with netdiag. The other checks still work." {
		t.Errorf("macOS Wi-Fi: %+v", a)
	}
	if a := Health(model.HealthCheck{ID: "dns", Status: model.HealthOK, Detail: "resolved"}, x); a != nil {
		t.Errorf("ok check: %+v", a)
	}
	if a := Health(model.HealthCheck{ID: "dns", Status: model.HealthFail, Detail: "old report"}, x); a != nil {
		t.Errorf("a result without a reason (an old saved report): %+v", a)
	}
	if a := Health(check("gateway", "something new", nil), x); a != nil {
		t.Errorf("unknown reason: %+v", a)
	}
}

// TestActionTargets: the router page only for a gateway on a local
// network, a device page only on its own IP over http(s), and Windows
// settings pages only on Windows.
func TestActionTargets(t *testing.T) {
	for gw, want := range map[string]string{
		"192.168.1.1": "http://192.168.1.1/", "10.0.0.1": "http://10.0.0.1/", "172.16.5.1": "http://172.16.5.1/",
		"169.254.1.1": "http://169.254.1.1/", "100.64.0.1": "http://100.64.0.1/",
		"8.8.8.8": "", "": "", "router.lan": "", "fe80::1": "", "192.168.1.1/24": "", "127.0.0.1": "",
	} {
		if got := RouterURL(gw); got != want {
			t.Errorf("RouterURL(%q) = %q, want %q", gw, got, want)
		}
	}
	tcp := check("gateway", model.ReasonTCPOnly, nil)
	if a := Health(tcp, Ctx{Gateway: "203.0.113.1"}); a == nil || len(a.Actions) != 0 {
		t.Errorf("public gateway gets a router button: %+v", a)
	}
	if a := Health(tcp, Ctx{Gateway: "192.168.1.1"}); a == nil || len(a.Actions) != 1 || a.Actions[0].Target != "http://192.168.1.1/" {
		t.Errorf("router button: %+v", a)
	}

	host := func(urls ...string) model.Host {
		h := model.Host{IP: "192.168.1.9", IsPrinter: true}
		for _, u := range urls {
			h.Web = append(h.Web, model.WebService{URL: u})
		}
		return h
	}
	for _, tc := range []struct {
		urls []string
		want string
	}{
		{[]string{"http://192.168.1.9/"}, "http://192.168.1.9/"},
		{[]string{"https://192.168.1.9:8443/admin"}, "https://192.168.1.9:8443/admin"},
		{[]string{"http://192.168.1.10/", "https://192.168.1.9/"}, "https://192.168.1.9/"},
		{[]string{"http://printer.local/"}, ""},
		{[]string{"ftp://192.168.1.9/"}, ""},
		{[]string{"javascript:alert(1)"}, ""},
		{[]string{"http://user:pw@192.168.1.9/"}, ""},
		{[]string{"http://192.168.1.10/"}, ""},
		{nil, ""},
	} {
		h := host(tc.urls...)
		if got := DevicePageURL(h); got != tc.want {
			t.Errorf("DevicePageURL(%v) = %q, want %q", tc.urls, got, tc.want)
		}
		a := PrinterNoStatus(h)
		if tc.want == "" && len(a.Actions) != 0 || tc.want != "" && (len(a.Actions) != 1 || a.Actions[0].Target != tc.want ||
			a.Actions[0].Kind != model.ActionOpenDevicePage || a.Actions[0].Label != LabelOpenPage) {
			t.Errorf("%v: actions %+v", tc.urls, a.Actions)
		}
	}

	loc := check("wifi", model.ReasonHiddenLocation, nil)
	for _, goos := range oses {
		a := Health(loc, Ctx{GOOS: goos})
		has := len(a.Actions) == 1 && a.Actions[0].Kind == model.ActionOSSettings && a.Actions[0].Target == model.OSPageLocation
		if has != (goos == "windows") {
			t.Errorf("%s: Location settings button %v", goos, a.Actions)
		}
	}
}

func TestPrinterNoStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    model.Host
		want bool
	}{
		{"no data", model.Host{IsPrinter: true}, true},
		{"not a printer", model.Host{}, false},
		{"snmp", model.Host{IsPrinter: true, Snmp: &model.SnmpInfo{}}, false},
		{"ipp", model.Host{IsPrinter: true, Printer: &model.PrinterInfo{Source: "ipp"}}, false},
		{"snmp+ipp", model.Host{IsPrinter: true, Printer: &model.PrinterInfo{Source: "snmp+ipp"}}, false},
		{"empty printer info", model.Host{IsPrinter: true, Printer: &model.PrinterInfo{}}, true},
	} {
		if got := PrinterNoStatus(tc.h) != nil; got != tc.want {
			t.Errorf("%s: advice %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNoticeAdvice(t *testing.T) {
	h := &model.Host{IP: "192.168.1.10", Printer: &model.PrinterInfo{Supplies: []model.Supply{{Description: "Black Toner", Percent: 12}}}}
	for _, tc := range []struct {
		n       model.Notice
		meaning string
	}{
		{model.Notice{Trigger: model.TriggerPrinterOffline, IP: "192.168.1.10"}, "The printer stopped answering."},
		{model.Notice{Trigger: model.TriggerDeviceOffline, IP: "192.168.1.20", Name: "Front desk PC"}, "Front desk PC stopped answering."},
		{model.Notice{Trigger: model.TriggerDeviceOffline, IP: "192.168.1.20"}, "The device at 192.168.1.20 stopped answering."},
		{model.Notice{Trigger: model.TriggerSupplyLow, IP: "192.168.1.10", Detail: "Black Toner"}, "Black Toner is at 12%."},
		{model.Notice{Trigger: model.TriggerSupplyLow, IP: "192.168.1.10", Detail: "Cyan Ink"}, "Cyan Ink is low."},
		{model.Notice{Trigger: model.TriggerPrinterAlert, IP: "192.168.1.10", Code: "hrPrinterDetectedErrorState.doorOpen"}, "The printer says a door or cover is open."},
		{model.Notice{Trigger: model.TriggerPrinterAlert, IP: "192.168.1.10", Code: "cover-open-error"}, "The printer says a door or cover is open."},
		{model.Notice{Trigger: model.TriggerPrinterAlert, IP: "192.168.1.10", Code: "hrPrinterDetectedErrorState.jammed"}, "The printer has a paper jam."},
		{model.Notice{Trigger: model.TriggerPrinterAlert, IP: "192.168.1.10", Code: "media-empty-warning"}, "The printer is out of paper."},
		{model.Notice{Trigger: model.TriggerPrinterAlert, IP: "192.168.1.10", Code: "hrPrinterDetectedErrorState.noToner"}, "The printer is out of toner or ink."},
		{model.Notice{Trigger: model.TriggerPrinterAlert, IP: "192.168.1.10", Code: "13", Detail: "Fuser over temperature."}, "The printer reports: Fuser over temperature."},
	} {
		a := Notice(tc.n, h, Ctx{})
		if a == nil || a.Meaning != tc.meaning {
			t.Errorf("%+v: %+v", tc.n, a)
			continue
		}
		if tc.n.IP != "" && (len(a.Actions) != 1 || a.Actions[0].Kind != model.ActionShowDevice || a.Actions[0].Target != tc.n.IP) {
			t.Errorf("%s: actions %+v", tc.n.Trigger, a.Actions)
		}
	}
	if a := Notice(model.Notice{Trigger: model.TriggerPrinterOffline, OK: true}, nil, Ctx{}); a != nil {
		t.Errorf("OK notice: %+v", a)
	}
	if a := Notice(model.Notice{Trigger: "somethingNew"}, nil, Ctx{}); a != nil {
		t.Errorf("unknown trigger: %+v", a)
	}
}

func TestConflictAdvice(t *testing.T) {
	flap := model.Conflict{Kind: model.ConflictMACFlap, Severity: "fail", IPs: []string{"192.168.1.50"}, Detail: "raw"}
	hist := func(macs ...string) Ctx {
		h := model.Host{IP: "192.168.1.50"}
		for _, m := range macs {
			h.MACHistory = append(h.MACHistory, model.MACObservation{MAC: m})
		}
		return Ctx{Hosts: map[string]model.Host{h.IP: h}}
	}
	swap := "Two devices are taking turns using 192.168.1.50, so both work badly."
	possible := "Possible conflict: the device answering at 192.168.1.50 changed."
	for _, tc := range []struct {
		name string
		c    model.Conflict
		x    Ctx
		want string
	}{
		{"two alternations", flap, hist("aa", "BB", "AA"), swap},
		{"one change", flap, hist("AA", "BB"), possible},
		{"one change, the same MAC again", flap, hist("AA", "AA", "BB", "BB"), possible},
		{"no history, analyzer saw a swap", flap, Ctx{}, swap},
		{"no history, not a swap", model.Conflict{Kind: model.ConflictMACFlap, Severity: "warn", IPs: []string{"192.168.1.50"}}, Ctx{}, possible},
	} {
		a := Conflict(tc.c, tc.x)
		if a == nil || a.Meaning != tc.want {
			t.Errorf("%s: %+v", tc.name, a)
		}
		if tc.want == swap && (len(a.Actions) != 1 || a.Actions[0].Kind != model.ActionShowDevice) {
			t.Errorf("%s: actions %+v", tc.name, a.Actions)
		}
		if tc.want == possible && len(a.Actions) != 0 {
			t.Errorf("%s: actions %+v", tc.name, a.Actions)
		}
	}
	if a := Conflict(model.Conflict{Kind: model.ConflictDuplicateName, Name: "printer-2"}, Ctx{}); a == nil ||
		a.Meaning != `Two devices use the same name, "printer-2".` {
		t.Errorf("duplicate name: %+v", a)
	}
	if a := Conflict(model.Conflict{Kind: model.ConflictMACMultiIP}, Ctx{}); a == nil ||
		!strings.HasPrefix(a.Meaning, "One device answers for several addresses.") {
		t.Errorf("multi ip: %+v", a)
	}
	if n := Alternations([]model.MACObservation{{MAC: "A"}, {MAC: ""}, {MAC: "B"}, {MAC: "A"}, {MAC: "B"}}); n != 3 {
		t.Errorf("Alternations = %d", n)
	}
}

// TestUITestFixtureAdvice: the UI harness fixtures
// (tools/uitest/fixtures/index.mjs) carry the advice this catalogue gives
// for the same results, word for word, so the advice specs test what
// netdiag really sends.
func TestUITestFixtureAdvice(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "tools", "uitest", "fixtures", "index.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	x := Ctx{GOOS: "windows", Gateway: "192.168.2.1"}
	hc := func(id, status, reason string, data map[string]any) *model.Advice {
		d := map[string]any{"reason": reason}
		for k, v := range data {
			d[k] = v
		}
		return Health(model.HealthCheck{ID: id, Status: status, Data: d}, x)
	}
	printer := &model.Host{IP: "192.168.2.10", Printer: &model.PrinterInfo{Supplies: []model.Supply{{Description: "Black Toner", Percent: 15}}}}
	flapHost := func(alt int) Ctx {
		h := model.Host{IP: "192.168.2.44"}
		for i := 0; i <= alt; i++ {
			h.MACHistory = append(h.MACHistory, model.MACObservation{MAC: []string{"24:6F:28:AA:BB:CC", "3C:52:82:11:22:33"}[i%2]})
		}
		return Ctx{Hosts: map[string]model.Host{h.IP: h}}
	}
	flap := model.Conflict{Kind: model.ConflictMACFlap, Severity: "fail", IPs: []string{"192.168.2.44"}}
	cases := map[string]*model.Advice{
		"gateway no_reply":          hc("gateway", model.HealthFail, model.ReasonNoReply, nil),
		"dhcp apipa":                hc("dhcp", model.HealthFail, model.ReasonAPIPA, nil),
		"dns no_internet":           hc("dns", model.HealthWarn, model.ReasonNoInternet, nil),
		"dns no_internet, switch":   hc("dns", model.HealthInfo, model.ReasonNoInternet, map[string]any{"noInternet": true}),
		"web not_set":               hc("webserver", model.HealthInfo, model.ReasonNotSet, nil),
		"web tls":                   hc("webserver", model.HealthFail, model.ReasonTLS, map[string]any{"certSHA256": strings.Repeat("ab", 32)}),
		"web status":                hc("webserver", model.HealthFail, model.ReasonStatus, map[string]any{"status": 503}),
		"internet no_response":      hc("internet", model.HealthInfo, model.ReasonNoResponse, nil),
		"internet, switch":          hc("internet", model.HealthInfo, model.ReasonNoResponse, map[string]any{"noInternet": true}),
		"wifi 48":                   hc("wifi", model.HealthWarn, model.ReasonWeak, map[string]any{"signalPct": 48}),
		"wifi 32":                   hc("wifi", model.HealthWarn, model.ReasonWeak, map[string]any{"signalPct": 32}),
		"supply low":                Notice(model.Notice{Trigger: model.TriggerSupplyLow, IP: "192.168.2.10", Detail: "Black Toner"}, printer, x),
		"device offline":            Notice(model.Notice{Trigger: model.TriggerDeviceOffline, IP: "192.168.2.200"}, nil, x),
		"mac_multi_ip":              Conflict(model.Conflict{Kind: model.ConflictMACMultiIP, IPs: []string{"192.168.2.30"}}, x),
		"mac_flap, 2 alternations":  Conflict(flap, flapHost(2)),
		"mac_flap, 1 alternation":   Conflict(flap, flapHost(1)),
		"printer without status":    PrinterNoStatus(model.Host{IsPrinter: true}),
		"wifi hidden_location":      hc("wifi", model.HealthUnavailable, model.ReasonHiddenLocation, nil),
		"netdiagLink link":          Notice(model.Notice{Trigger: model.TriggerNetdiagLink, Code: model.HeldLink}, nil, x),
		"netdiagLink other network": Notice(model.Notice{Trigger: model.TriggerNetdiagLink, Code: model.HeldNetworkChanged}, nil, x),
	}
	quoted := func(s string) string {
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(s)
		return strings.TrimSpace(b.String())
	}
	for name, a := range cases {
		if a == nil {
			t.Errorf("%s: no advice", name)
			continue
		}
		for _, s := range append([]string{a.Meaning}, a.Steps...) {
			if !strings.Contains(string(src), quoted(s)) {
				t.Errorf("%s: fixtures lack %s", name, quoted(s))
			}
		}
	}
	// The network the fixture health reports name is a real network id,
	// and the advice buttons that open Settings use known targets.
	m := regexp.MustCompile(`export const NETWORK = '([^']*)'`).FindStringSubmatch(string(src))
	if m == nil || !model.ValidNetworkID(m[1]) {
		t.Errorf("fixture NETWORK %v is not a network id", m)
	}
	for _, sm := range regexp.MustCompile(`settingsAction\('([^']*)'\)`).FindAllStringSubmatch(string(src), -1) {
		if !slices.Contains([]string{model.SettingsSectionWebServer, model.SettingsSectionHealth, model.SettingsSectionNoInternet,
			model.SettingsSectionAlerts, model.SettingsSectionChat}, sm[1]) {
			t.Errorf("fixture Open Settings target %q", sm[1])
		}
	}
}

// TestImportsOnlyModel keeps the package pure: the standard library and
// model, and nothing that talks to the network or starts programs.
func TestImportsOnlyModel(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, n := range files {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, n, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if strings.Contains(p, ".") || strings.HasPrefix(p, "netdiag/") && p != "netdiag/internal/model" ||
				p == "net/http" || p == "os/exec" || p == "os" {
				t.Errorf("%s imports %s", n, p)
			}
		}
	}
}
