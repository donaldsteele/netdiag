package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"netdiag/internal/model"
)

func writeSettings(t *testing.T, s *Store, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.Dir(), settingsFile), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A settings.json written by v0.5.6 with supplyThresholdPct 0 (outside
// 1-100) loads with 15 and keeps every other field. Before F7 the whole file
// was replaced with defaults.
func TestLoadV056SettingsRepairsThreshold(t *testing.T) {
	s := open(t)
	data, err := os.ReadFile(filepath.Join("testdata", "settings-v0.5.6.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeSettings(t, s, string(data))
	got, repaired, err := s.LoadSettingsRepaired()
	if err != nil {
		t.Fatal(err)
	}
	want := model.Settings{
		SchemaVersion: 1, Community: "frontdesk", PrinterStatusPollSec: 12,
		WebServer:        model.SettingsWebServer{URL: "http://192.168.1.10/status", MustContain: "Welcome", ExpectStatus: 204},
		InternetCheckURL: "http://example.test/generate_204",
		Alerts: model.SettingsAlerts{
			Sound: false, Desktop: true, CooldownSec: 600, SupplyThresholdPct: 15,
			Triggers: model.SettingsTriggers{PrinterOffline: true, DeviceOffline: true, SupplyLow: false,
				PrinterAlert: true, NewDevice: false, IPConflict: true, HealthFailed: false},
		},
		IgnoredConflictMACs: []string{"AA:BB:CC:DD:EE:01", "AA:BB:CC:DD:EE:02"},
		MCP:                 model.SettingsMCP{Enabled: true},
		Chat:                model.SettingsChat{Enabled: false, MinSeverity: "warning"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded\n%+v\nwant\n%+v", got, want)
	}
	if !reflect.DeepEqual(repaired, []string{"alerts.supplyThresholdPct"}) {
		t.Fatalf("repaired = %v", repaired)
	}
	if r := s.Repaired(); !reflect.DeepEqual(r, repaired) {
		t.Fatalf("store remembers %v", r)
	}
	// Saving writes a valid file, so nothing is reported any more.
	if _, err := s.SaveSettings(got); err != nil {
		t.Fatal(err)
	}
	if r := s.Repaired(); r != nil {
		t.Fatalf("after save: %v", r)
	}
	if back, r, _ := s.LoadSettingsRepaired(); r != nil || !reflect.DeepEqual(back, want) {
		t.Fatalf("reload: %v %+v", r, back)
	}
}

// validSettings has a non-default value in every field, so a test can see
// that a repair touched only the field it should.
func validSettings() model.Settings {
	return model.Settings{
		SchemaVersion: 1, Community: "c0mm", PrinterStatusPollSec: 30,
		WebServer: model.SettingsWebServer{URL: "https://nas.local/", MustContain: "ok", ExpectStatus: 301,
			TrustedCert: strings.Repeat("ab", 32)},
		InternetCheckURL: "https://example.test/ping",
		Alerts: model.SettingsAlerts{Sound: false, Desktop: true, CooldownSec: 90, SupplyThresholdPct: 40,
			Triggers: model.SettingsTriggers{DeviceOffline: true, HealthFailed: true}},
		IgnoredConflictMACs: []string{"AA:BB:CC:DD:EE:FF"},
		MCP:                 model.SettingsMCP{Enabled: true},
		Chat:                model.SettingsChat{Enabled: true, MinSeverity: "critical"},
	}
}

func TestRepairSettingsPerField(t *testing.T) {
	def := DefaultSettings()
	cases := []struct {
		path  string
		bad   func(*model.Settings)
		reset func(*model.Settings)
	}{
		{"printerStatusPollSec", func(s *model.Settings) { s.PrinterStatusPollSec = 1 },
			func(s *model.Settings) { s.PrinterStatusPollSec = 5 }},
		{"printerStatusPollSec", func(s *model.Settings) { s.PrinterStatusPollSec = 301 },
			func(s *model.Settings) { s.PrinterStatusPollSec = 5 }},
		{"webServer.url", func(s *model.Settings) { s.WebServer.URL = "ftp://nas.local/" },
			func(s *model.Settings) { s.WebServer.URL = "" }},
		{"webServer.expectStatus", func(s *model.Settings) { s.WebServer.ExpectStatus = 99 },
			func(s *model.Settings) { s.WebServer.ExpectStatus = 200 }},
		{"webServer.expectStatus", func(s *model.Settings) { s.WebServer.ExpectStatus = 600 },
			func(s *model.Settings) { s.WebServer.ExpectStatus = 200 }},
		{"webServer.trustedCert", func(s *model.Settings) { s.WebServer.TrustedCert = "abc" },
			func(s *model.Settings) { s.WebServer.TrustedCert = "" }},
		{"internetCheckURL", func(s *model.Settings) { s.InternetCheckURL = "not a url" },
			func(s *model.Settings) { s.InternetCheckURL = def.InternetCheckURL }},
		{"alerts.supplyThresholdPct", func(s *model.Settings) { s.Alerts.SupplyThresholdPct = 0 },
			func(s *model.Settings) { s.Alerts.SupplyThresholdPct = 15 }},
		{"alerts.supplyThresholdPct", func(s *model.Settings) { s.Alerts.SupplyThresholdPct = 101 },
			func(s *model.Settings) { s.Alerts.SupplyThresholdPct = 15 }},
		{"alerts.cooldownSec", func(s *model.Settings) { s.Alerts.CooldownSec = 29 },
			func(s *model.Settings) { s.Alerts.CooldownSec = 300 }},
		{"alerts.cooldownSec", func(s *model.Settings) { s.Alerts.CooldownSec = 3601 },
			func(s *model.Settings) { s.Alerts.CooldownSec = 300 }},
		{"ignoredConflictMACs", func(s *model.Settings) { s.IgnoredConflictMACs = []string{"zz", "AA:BB:CC:DD:EE:FF", "nope"} },
			func(s *model.Settings) { s.IgnoredConflictMACs = []string{"AA:BB:CC:DD:EE:FF"} }},
		{"chat.minSeverity", func(s *model.Settings) { s.Chat.MinSeverity = "loud" },
			func(s *model.Settings) { s.Chat.MinSeverity = "warning" }},
	}
	for _, c := range cases {
		in := validSettings()
		c.bad(&in)
		if _, err := NormalizeSettings(in); err == nil {
			t.Errorf("%s: strict normalize accepted the bad value", c.path)
		}
		got, paths := RepairSettings(in)
		want := validSettings()
		c.reset(&want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: repaired\n%+v\nwant\n%+v", c.path, got, want)
		}
		if !reflect.DeepEqual(paths, []string{c.path}) {
			t.Errorf("%s: paths %v", c.path, paths)
		}
		if _, err := NormalizeSettings(got); err != nil {
			t.Errorf("%s: repaired settings are still invalid: %v", c.path, err)
		}
		if !slices.Contains(RepairPaths(), c.path) {
			t.Errorf("%s: not in RepairPaths", c.path)
		}
	}
}

// RepairPaths covers every settings field and object, by JSON name, in
// field order, and leaves out schemaVersion.
func TestRepairPaths(t *testing.T) {
	got := RepairPaths()
	want := []string{"community", "printerStatusPollSec",
		"webServer", "webServer.url", "webServer.mustContain", "webServer.expectStatus", "webServer.trustedCert",
		"internetCheckURL", "noInternet",
		"alerts", "alerts.sound", "alerts.desktop", "alerts.cooldownSec", "alerts.supplyThresholdPct",
		"alerts.triggers", "alerts.triggers.printerOffline", "alerts.triggers.deviceOffline",
		"alerts.triggers.supplyLow", "alerts.triggers.printerAlert", "alerts.triggers.newDevice",
		"alerts.triggers.ipConflict", "alerts.triggers.healthFailed",
		"ignoredConflictMACs", "mcp", "mcp.enabled", "chat", "chat.enabled", "chat.minSeverity"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RepairPaths\n%v\nwant\n%v", got, want)
	}
}

func TestRepairSettingsValidAndBlank(t *testing.T) {
	v := validSettings()
	got, paths := RepairSettings(v)
	if paths != nil || !reflect.DeepEqual(got, v) {
		t.Fatalf("valid settings changed: %v %+v", paths, got)
	}
	// Blank and zero values mean "the default" and are not repairs.
	blank := v
	blank.Community, blank.PrinterStatusPollSec, blank.InternetCheckURL = "  ", 0, ""
	blank.WebServer.ExpectStatus, blank.Chat.MinSeverity = 0, ""
	got, paths = RepairSettings(blank)
	if paths != nil || got.Community != "public" || got.PrinterStatusPollSec != 5 ||
		got.InternetCheckURL != DefaultSettings().InternetCheckURL || got.WebServer.ExpectStatus != 200 ||
		got.Chat.MinSeverity != "warning" {
		t.Fatalf("blank: %v %+v", paths, got)
	}
	// Several bad fields are each reset, in field order.
	many := v
	many.PrinterStatusPollSec, many.Alerts.SupplyThresholdPct, many.Chat.MinSeverity = 999, -3, "x"
	got, paths = RepairSettings(many)
	if want := []string{"printerStatusPollSec", "alerts.supplyThresholdPct", "chat.minSeverity"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths %v", paths)
	}
	if got.Community != v.Community || got.Alerts.CooldownSec != v.Alerts.CooldownSec || !got.Chat.Enabled {
		t.Fatalf("other fields changed: %+v", got)
	}
}

func TestSettingsChatDefaults(t *testing.T) {
	d := DefaultSettings()
	if d.Chat.Enabled || d.Chat.MinSeverity != model.ChatSeverityWarning {
		t.Fatalf("default chat %+v", d.Chat)
	}
	s := open(t)
	writeSettings(t, s, `{"schemaVersion":1,"community":"x"}`) // written before chat existed
	got, repaired, err := s.LoadSettingsRepaired()
	if err != nil || repaired != nil || got.Chat != d.Chat {
		t.Fatalf("old file: %+v %v %v", got.Chat, repaired, err)
	}
	in := got
	in.Chat = model.SettingsChat{Enabled: true, MinSeverity: " CRITICAL "}
	out, err := s.SaveSettings(in)
	if err != nil || out.Chat.MinSeverity != "critical" || !out.Chat.Enabled {
		t.Fatalf("save: %+v %v", out.Chat, err)
	}
	in.Chat.MinSeverity = "loud"
	if _, err := s.SaveSettings(in); err == nil || !strings.Contains(err.Error(), "chat.minSeverity") {
		t.Fatalf("strict save accepted a bad level: %v", err)
	}
	// A client that sends no chat block (zero value) gets the default level.
	in.Chat = model.SettingsChat{}
	if out, err := s.SaveSettings(in); err != nil || out.Chat.MinSeverity != "warning" {
		t.Fatalf("zero chat: %+v %v", out.Chat, err)
	}
	for _, sev := range []string{"info", "warning", "critical"} {
		if !model.ValidChatSeverity(sev) {
			t.Errorf("%s not valid", sev)
		}
	}
	if model.ValidChatSeverity("warn") || model.ValidChatSeverity("") {
		t.Error("warn or blank accepted")
	}
	b, _ := json.Marshal(d)
	if !strings.Contains(string(b), `"chat":{"enabled":false,"minSeverity":"warning"}`) {
		t.Fatalf("json: %s", b)
	}
}

// The "This network has no internet" switch (v0.6-D15) is off by default,
// off for a file written before it existed, kept on save, and a value of
// the wrong type is reset and reported.
func TestSettingsNoInternet(t *testing.T) {
	if DefaultSettings().NoInternet {
		t.Fatal("on by default")
	}
	s := open(t)
	writeSettings(t, s, `{"schemaVersion":1,"community":"x"}`)
	got, repaired, err := s.LoadSettingsRepaired()
	if err != nil || repaired != nil || got.NoInternet {
		t.Fatalf("old file: %v %v %v", got.NoInternet, repaired, err)
	}
	got.NoInternet = true
	if out, err := s.SaveSettings(got); err != nil || !out.NoInternet {
		t.Fatalf("save: %v %v", out.NoInternet, err)
	}
	if again, err := s.LoadSettings(); err != nil || !again.NoInternet {
		t.Fatalf("reload: %v %v", again.NoInternet, err)
	}
	writeSettings(t, s, `{"schemaVersion":1,"noInternet":"yes"}`)
	got, repaired, err = s.LoadSettingsRepaired()
	if err != nil || got.NoInternet || !slices.Equal(repaired, []string{"noInternet"}) {
		t.Fatalf("wrong type: %v %v %v", got.NoInternet, repaired, err)
	}
}

func TestLoadSettingsWrongTypeAndUnreadable(t *testing.T) {
	s := open(t)
	writeSettings(t, s, `{"community":"keep","alerts":{"cooldownSec":"60","supplyThresholdPct":20},"mcp":{"enabled":true}}`)
	got, repaired, err := s.LoadSettingsRepaired()
	if err != nil {
		t.Fatal(err)
	}
	if got.Alerts.CooldownSec != 300 || got.Alerts.SupplyThresholdPct != 20 || got.Community != "keep" || !got.MCP.Enabled {
		t.Fatalf("wrong type: %+v", got)
	}
	if !reflect.DeepEqual(repaired, []string{"alerts.cooldownSec"}) {
		t.Fatalf("repaired %v", repaired)
	}
	// Every value of the wrong type is reported, not only the first one
	// json names: objects too, keys in another case, in field order. A bad
	// schemaVersion is put right without a word.
	writeSettings(t, s, `{"schemaVersion":"1","community":"keep","webServer":5,`+
		`"alerts":{"cooldownSec":"60","supplyThresholdPct":"20","Sound":"yes","triggers":[true]},`+
		`"mcp":{"enabled":true},"chat":{"enabled":"on","minSeverity":"loud"}}`)
	got, repaired, err = s.LoadSettingsRepaired()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"webServer", "alerts.sound", "alerts.cooldownSec", "alerts.supplyThresholdPct",
		"alerts.triggers", "chat.enabled", "chat.minSeverity"}
	if !reflect.DeepEqual(repaired, want) {
		t.Fatalf("repaired %v, want %v", repaired, want)
	}
	exp := DefaultSettings()
	exp.Community, exp.MCP.Enabled = "keep", true
	if !reflect.DeepEqual(got, exp) {
		t.Fatalf("several wrong types:\n%+v\nwant\n%+v", got, exp)
	}
	// A wrong-typed MAC list element is reported once.
	writeSettings(t, s, `{"ignoredConflictMACs":[1,"aa:bb:cc:dd:ee:ff"]}`)
	got, repaired, _ = s.LoadSettingsRepaired()
	if !reflect.DeepEqual(repaired, []string{"ignoredConflictMACs"}) || !reflect.DeepEqual(got.IgnoredConflictMACs, []string{"AA:BB:CC:DD:EE:FF"}) {
		t.Fatalf("mac list: %v %v", repaired, got.IgnoredConflictMACs)
	}
	// Unreadable JSON, or a document that is not an object, still means defaults.
	for _, bad := range []string{`{{{`, `[]`, `"x"`, ``} {
		writeSettings(t, s, bad)
		got, repaired, err = s.LoadSettingsRepaired()
		if err != nil || repaired != nil || !reflect.DeepEqual(got, DefaultSettings()) {
			t.Fatalf("%q: %+v %v %v", bad, got, repaired, err)
		}
	}
	if s.Repaired() != nil {
		t.Fatal("unreadable file reported as repaired")
	}
}

func TestDataFiles(t *testing.T) {
	s := open(t)
	names := []string{FileInstance, FileNetworkProfile, FileOpenProblems, FileBubbles, FileChatOutbox}
	for _, n := range names {
		if _, err := s.ReadFile(n); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s missing: %v", n, err)
		}
		if err := s.WriteFile(n, []byte(`{"schemaVersion":1,"name":"`+n+`"}`)); err != nil {
			t.Fatal(err)
		}
		if err := s.WriteFile(n, []byte(`{"schemaVersion":1,"v":2}`)); err != nil {
			t.Fatal(err)
		}
		b, err := s.ReadFile(n)
		if err != nil || string(b) != `{"schemaVersion":1,"v":2}` {
			t.Fatalf("%s: %q %v", n, b, err)
		}
		if runtime.GOOS != "windows" {
			if fi, err := os.Stat(filepath.Join(s.Dir(), n)); err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("%s mode %v %v", n, fi.Mode(), err)
			}
		}
	}
	bad, err := s.QuarantineFile(FileOpenProblems)
	if err != nil || !strings.HasPrefix(bad, FileOpenProblems+".bad-") {
		t.Fatalf("quarantine: %q %v", bad, err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), bad)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadFile(FileOpenProblems); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("quarantined file still there: %v", err)
	}
	// RemoveQuarantined deletes only the named file's quarantined copies.
	os.WriteFile(filepath.Join(s.Dir(), FileOpenProblems+".bad-20261017T000000Z"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(s.Dir(), FileBubbles+".bad-20261017T000000Z"), []byte("x"), 0o600)
	if err := s.RemoveQuarantined(FileOpenProblems); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(s.Dir(), FileOpenProblems+".bad-*")); len(left) != 0 {
		t.Fatalf("quarantined copies left: %v", left)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), FileBubbles+".bad-20261017T000000Z")); err != nil {
		t.Fatalf("another file's quarantined copy was removed: %v", err)
	}
	if _, err := s.ReadFile(FileBubbles); err != nil {
		t.Fatalf("the data file itself was removed: %v", err)
	}
	if err := s.RemoveQuarantined(FileOpenProblems); err != nil {
		t.Fatalf("nothing to remove: %v", err)
	}
	if err := s.RemoveFile(FileBubbles); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveFile(FileBubbles); err != nil {
		t.Fatalf("removing a missing file: %v", err)
	}
	if _, err := s.ReadFile(FileBubbles); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("removed file still there: %v", err)
	}
	// Only the listed names: never settings, history, paths or other case.
	os.WriteFile(filepath.Join(s.Dir(), "outside.json"), []byte("x"), 0o644)
	for _, n := range []string{"", settingsFile, "history/x.json", "../outside.json", "..\\outside.json",
		"outside.json", "BUBBLES.JSON", "bubbles.json ", "./bubbles.json", filepath.Join(s.Dir(), FileBubbles), bad} {
		if _, err := s.ReadFile(n); !errors.Is(err, ErrFileName) {
			t.Errorf("read %q: %v", n, err)
		}
		if err := s.WriteFile(n, []byte("x")); !errors.Is(err, ErrFileName) {
			t.Errorf("write %q: %v", n, err)
		}
		if err := s.RemoveFile(n); !errors.Is(err, ErrFileName) {
			t.Errorf("remove %q: %v", n, err)
		}
		if _, err := s.QuarantineFile(n); !errors.Is(err, ErrFileName) {
			t.Errorf("quarantine %q: %v", n, err)
		}
		if err := s.RemoveQuarantined(n); !errors.Is(err, ErrFileName) {
			t.Errorf("remove quarantined %q: %v", n, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(s.Dir(), "outside.json")); string(b) != "x" {
		t.Fatal("a file outside the allowlist was changed")
	}
	if _, err := s.LoadSettings(); err != nil {
		t.Fatal(err)
	}
}
