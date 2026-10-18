package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"netdiag/internal/model"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func scan(id string, n int, cidr string) model.ScanResult {
	return model.ScanResult{ID: id, StartedAt: fmt.Sprintf("2026-01-01T10:%02d:00Z", n),
		State: model.StateDone, Request: model.ScanRequest{CIDR: cidr, Live: n%2 == 0},
		Stats: model.Stats{Alive: n}}
}

func TestEnvDir(t *testing.T) {
	d := t.TempDir()
	t.Setenv(EnvDataDir, d)
	s, err := Open("")
	if err != nil || s.Dir() != d {
		t.Fatalf("%v %v", s, err)
	}
}

func TestOpenRemovesStaleTemps(t *testing.T) {
	d := t.TempDir()
	hist := filepath.Join(d, "history")
	os.MkdirAll(hist, 0o755)
	old, fresh := filepath.Join(hist, ".tmp-1"), filepath.Join(hist, ".tmp-2")
	os.WriteFile(old, []byte("x"), 0o644)
	os.WriteFile(fresh, []byte("x"), 0o644)
	past := time.Now().Add(-time.Hour)
	os.Chtimes(old, past, past)
	if _, err := Open(d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("stale temp kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("fresh temp removed")
	}
}

func TestSettings(t *testing.T) {
	s := open(t)
	d, _ := s.LoadSettings()
	if d.Community != "public" || d.Alerts.CooldownSec != 300 || !d.Alerts.Triggers.NewDevice {
		t.Fatalf("defaults %+v", d)
	}
	os.WriteFile(filepath.Join(s.Dir(), "settings.json"), []byte(`{"community":"x"}`), 0o644)
	d, _ = s.LoadSettings()
	if d.Community != "x" || !d.Alerts.Sound || d.SchemaVersion != 1 || d.PrinterStatusPollSec != 5 {
		t.Fatalf("partial %+v", d)
	}
	d.IgnoredConflictMACs = []string{"aa-bb-cc-dd-ee-ff", "AA:BB:CC:DD:EE:FF"}
	d.WebServer.URL = "http://x.local/"
	out, err := s.SaveSettings(d)
	if err != nil || len(out.IgnoredConflictMACs) != 1 || out.IgnoredConflictMACs[0] != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("%+v %v", out, err)
	}
	back, _ := s.LoadSettings()
	if back.WebServer.URL != "http://x.local/" {
		t.Fatal("not persisted")
	}
	z := back
	z.PrinterStatusPollSec = 0 // older clients omit it
	if out, err := s.SaveSettings(z); err != nil || out.PrinterStatusPollSec != 5 {
		t.Fatalf("zero poll interval: %+v %v", out.PrinterStatusPollSec, err)
	}
	bad := []func(*model.Settings){
		func(x *model.Settings) { x.WebServer.URL = "ftp://x" },
		func(x *model.Settings) { x.Alerts.SupplyThresholdPct = 0 },
		func(x *model.Settings) { x.Alerts.SupplyThresholdPct = 101 },
		func(x *model.Settings) { x.Alerts.CooldownSec = 29 },
		func(x *model.Settings) { x.Alerts.CooldownSec = 3601 },
		func(x *model.Settings) { x.PrinterStatusPollSec = 1 },
		func(x *model.Settings) { x.PrinterStatusPollSec = 301 },
		func(x *model.Settings) { x.InternetCheckURL = "nope" },
		func(x *model.Settings) { x.IgnoredConflictMACs = []string{"zz"} },
		func(x *model.Settings) { x.WebServer.TrustedCert = "abc" },
		func(x *model.Settings) { x.WebServer.TrustedCert = strings.Repeat("g", 64) },
		func(x *model.Settings) { x.WebServer.TrustedCert = strings.Repeat("a", 66) },
	}
	for i, f := range bad {
		c := back
		f(&c)
		if _, err := s.SaveSettings(c); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	os.WriteFile(filepath.Join(s.Dir(), "settings.json"), []byte(`{{{`), 0o644)
	d, err = s.LoadSettings()
	if err != nil || d.Community != "public" {
		t.Fatal("corrupt")
	}
	hi := DefaultSettings()
	hi.SchemaVersion = 5
	o, _ := s.SaveSettings(hi)
	if o.SchemaVersion != 5 {
		t.Fatal("lowered")
	}
}

func TestSettingsTrustedCert(t *testing.T) {
	s := open(t)
	d := DefaultSettings()
	if d.WebServer.TrustedCert != "" {
		t.Fatal("trusted by default")
	}
	fp := strings.Repeat("ab", 32)
	colons := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	for _, in := range []string{fp, strings.ToUpper(fp), " " + colons + " ", strings.ReplaceAll(colons, ":", " ")} {
		d.WebServer.TrustedCert = in
		out, err := s.SaveSettings(d)
		if err != nil || out.WebServer.TrustedCert != fp {
			t.Fatalf("%q: %q %v", in, out.WebServer.TrustedCert, err)
		}
	}
	if back, _ := s.LoadSettings(); back.WebServer.TrustedCert != fp {
		t.Fatalf("not persisted: %q", back.WebServer.TrustedCert)
	}
	d.WebServer.TrustedCert = "  "
	if out, err := s.SaveSettings(d); err != nil || out.WebServer.TrustedCert != "" {
		t.Fatalf("forget: %q %v", out.WebServer.TrustedCert, err)
	}
}

func TestHistory(t *testing.T) {
	s := open(t)
	for i := 0; i < 25; i++ {
		cidr := "10.0.0.0/24"
		if i%5 == 0 {
			cidr = "10.1.0.0/24"
		}
		if err := s.SaveScan(scan(fmt.Sprintf("s%02d", i), i, cidr)); err != nil {
			t.Fatal(err)
		}
	}
	l, _ := s.ListHistory()
	if len(l) != Retention || l[0].ID != "s24" || l[19].ID != "s05" {
		t.Fatalf("%d %v", len(l), l[0].ID)
	}
	if !l[0].Live || l[1].Live {
		t.Fatal("live flag")
	}
	r, err := s.LoadScan("s10")
	if err != nil || r.Stats.Alive != 10 {
		t.Fatal(err)
	}
	if _, err := s.LoadScan("s00"); err != ErrNotFound {
		t.Fatal("expected not found")
	}
	if _, err := s.LoadScan("../x"); err != ErrNotFound {
		t.Fatal("traversal")
	}
	p, ok, _ := s.Previous("10.0.0.0/24", "s24")
	if !ok || p.ID != "s23" {
		t.Fatalf("prev %v", p)
	}
	p, ok, _ = s.Previous("10.1.0.0/24", "s10")
	if !ok || p.ID != "s05" {
		t.Fatalf("prev cidr %v", p)
	}
	p, ok, _ = s.Previous("10.0.0.0/24", "running-id")
	if !ok || p.ID != "s24" {
		t.Fatalf("prev running %v", p)
	}
	if _, ok, _ := s.Previous("10.1.0.0/24", "s05"); ok {
		t.Fatal("no older")
	}
	r.Stats.Alive = 99
	s.SaveScan(r)
	l, _ = s.ListHistory()
	if len(l) != Retention {
		t.Fatal("dup")
	}
	if err := s.DeleteScan("s10"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteScan("s10"); err != ErrNotFound {
		t.Fatal("double delete")
	}
	fs, _ := os.ReadDir(filepath.Join(s.Dir(), "history"))
	b, _ := os.ReadFile(filepath.Join(s.Dir(), "history", fs[0].Name()))
	if !strings.Contains(string(b), `"schemaVersion":2`) {
		t.Fatal("schema")
	}
	os.WriteFile(filepath.Join(s.Dir(), "history", "20990101T000000Z-bad.json"), []byte("xx"), 0o644)
	l, err = s.ListHistory()
	if err != nil || len(l) != Retention {
		t.Fatalf("corrupt %d %v", len(l), err)
	}
	if l[0].ID != "bad" || l[0].Error != UnreadableError || l[1].Error != "" {
		t.Fatalf("corrupt entry %+v %+v", l[0], l[1])
	}
	if err := s.DeleteScan("bad"); err != nil {
		t.Fatalf("delete corrupt: %v", err)
	}
}

// LatestScan is the newest saved scan that reads, past a damaged newer file
// (the Problems right now card reads it every minute, so it does not decode
// the whole history).
func TestLatestScan(t *testing.T) {
	s := open(t)
	if _, ok := s.LatestScan(); ok {
		t.Fatal("empty history has a latest scan")
	}
	for i := 0; i < 3; i++ {
		if err := s.SaveScan(scan(fmt.Sprintf("s%02d", i), i, "10.0.0.0/24")); err != nil {
			t.Fatal(err)
		}
	}
	if r, ok := s.LatestScan(); !ok || r.ID != "s02" {
		t.Fatalf("latest %v %v", r.ID, ok)
	}
	os.WriteFile(filepath.Join(s.Dir(), "history", "20990101T000000Z-bad.json"), []byte("xx"), 0o644)
	if r, ok := s.LatestScan(); !ok || r.ID != "s02" {
		t.Fatalf("latest past a damaged file %v %v", r.ID, ok)
	}
}

func TestOversizeTruncatesHosts(t *testing.T) {
	s := open(t)
	r := scan("huge", 1, "c")
	pad := strings.Repeat("y", 4096)
	for i := 0; i < 2000; i++ { // ~8 MB of hosts
		r.Hosts = append(r.Hosts, model.Host{IP: fmt.Sprintf("10.%d.%d.1", i/250, i%250), Hostname: pad})
	}
	if err := s.SaveScan(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadScan("huge")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got.Hosts); n == 0 || n >= 2000 {
		t.Fatalf("hosts kept = %d", n)
	}
	if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "of 2000 hosts") {
		t.Fatalf("warnings = %v", got.Warnings)
	}
	fs, _ := os.ReadDir(filepath.Join(s.Dir(), "history"))
	if info, _ := fs[0].Info(); info.Size() > MaxScanBytes {
		t.Fatalf("file size %d", info.Size())
	}
	if len(r.Warnings) != 0 || len(r.Hosts) != 2000 {
		t.Fatal("caller's scan was mutated")
	}
}

func TestCapAndBadID(t *testing.T) {
	s := open(t)
	r := scan("big", 1, "c")
	r.Warnings = []string{strings.Repeat("x", MaxScanBytes+1)}
	if err := s.SaveScan(r); err != ErrTooLarge {
		t.Fatal(err)
	}
	if err := s.SaveScan(scan("../evil", 1, "c")); err == nil {
		t.Fatal("bad id")
	}
}

func TestConcurrent(t *testing.T) {
	s := open(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.SaveScan(scan(fmt.Sprintf("c%d", i), i, "c"))
			s.SaveSettings(DefaultSettings())
			s.ListHistory()
			s.LoadSettings()
		}(i)
	}
	wg.Wait()
}

func TestReadBackfillsDeviceType(t *testing.T) {
	s := open(t)
	old := `{"id":"old1","schemaVersion":2,"startedAt":"2026-01-01T10:00:00Z","state":"done","request":{"cidr":"10.0.0.0/24"},"gateway":"10.0.0.1","hosts":[{"ip":"10.0.0.1","online":true},{"ip":"10.0.0.9","online":true}]}`
	if err := os.WriteFile(filepath.Join(s.Dir(), "history", "20260101T100000Z-old1.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := s.LoadScan("old1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Hosts[0].DeviceType != "gateway" || r.Hosts[1].DeviceType == "" || r.Hosts[1].DeviceTypeReason == "" {
		t.Fatalf("not back-filled: %+v", r.Hosts)
	}
}

// TestReadNamesOldHealthChecks: a scan an older netdiag saved shows its
// checks by the names this one uses (v0.6-D50); a check netdiag has no
// name for keeps its label.
func TestReadNamesOldHealthChecks(t *testing.T) {
	s := open(t)
	old := `{"id":"old2","schemaVersion":2,"startedAt":"2026-01-01T10:00:00Z","state":"done","request":{"cidr":"10.0.0.0/24"},` +
		`"health":{"checkedAt":"2026-01-01T10:00:05Z","checks":[{"id":"gateway","label":"Gateway reachable","status":"fail"},` +
		`{"id":"dhcp","label":"DHCP lease","status":"ok"},{"id":"dns","label":"DNS resolving","status":"ok"},` +
		`{"id":"internet","label":"Internet reachability","status":"info"},{"id":"custom","label":"Something else","status":"ok"}]}}`
	if err := os.WriteFile(filepath.Join(s.Dir(), "history", "20260101T100000Z-old2.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := s.LoadScan("old2")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range r.Health.Checks {
		got = append(got, c.Label)
	}
	want := []string{"Router", "Getting an address (DHCP)", "Name lookup (DNS)", "Internet", "Something else"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("labels %q, want %q", got, want)
	}
	// A scan without a health report still loads.
	if err := os.WriteFile(filepath.Join(s.Dir(), "history", "20260101T110000Z-old3.json"),
		[]byte(`{"id":"old3","schemaVersion":2,"startedAt":"2026-01-01T11:00:00Z","state":"done","request":{"cidr":"10.0.0.0/24"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if r, err := s.LoadScan("old3"); err != nil || r.Health != nil {
		t.Fatalf("no health: %+v %v", r.Health, err)
	}
}

func TestTwoStoresSameDir(t *testing.T) {
	d := t.TempDir()
	a, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i, s := range []*Store{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if err := s.SaveScan(scan(fmt.Sprintf("w%d-%d", i, j), j, "10.0.0.0/24")); err != nil {
					errs <- err
				}
				if _, err := s.SaveSettings(DefaultSettings()); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	c, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	l, err := c.ListHistory()
	if err != nil || len(l) != 10 {
		t.Fatalf("history %d %v", len(l), err)
	}
	filepath.WalkDir(d, func(p string, e os.DirEntry, _ error) error {
		if e != nil && strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("stale temp %s", p)
		}
		return nil
	})
}

func TestSettingsMCPFlag(t *testing.T) {
	s := open(t)
	os.WriteFile(filepath.Join(s.Dir(), "settings.json"), []byte(`{"schemaVersion":1,"community":"x"}`), 0o644)
	d, err := s.LoadSettings()
	if err != nil || d.MCP.Enabled {
		t.Fatalf("old file must load disabled: %+v %v", d.MCP, err)
	}
	d.MCP.Enabled = true
	if _, err := s.SaveSettings(d); err != nil {
		t.Fatal(err)
	}
	back, _ := s.LoadSettings()
	if !back.MCP.Enabled {
		t.Fatal("mcp.enabled not persisted")
	}
}

// The SNMP community never reaches a history file, and files saved before
// v0.5.8 (which hold it in clear) are redacted when read.
func TestHistoryRedactsCommunity(t *testing.T) {
	const secret = "s3cret-community"
	s := open(t)
	r := scan("c1", 1, "10.0.0.0/24")
	r.Request.Community = secret
	if err := s.SaveScan(r); err != nil {
		t.Fatal(err)
	}
	if r.Request.Community != secret {
		t.Fatal("SaveScan changed the caller's scan")
	}
	files, _ := filepath.Glob(filepath.Join(s.Dir(), "history", "*.json"))
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	data, _ := os.ReadFile(files[0])
	if strings.Contains(string(data), secret) || !strings.Contains(string(data), `"community":"***"`) {
		t.Fatalf("saved file: %s", data)
	}

	old := `{"id":"old2","schemaVersion":2,"startedAt":"2026-01-01T09:00:00Z","state":"done","request":{"cidr":"10.0.0.0/24","community":"` + secret + `"}}`
	if err := os.WriteFile(filepath.Join(s.Dir(), "history", "20260101T090000Z-old2.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadScan("old2")
	if err != nil || got.Request.Community != model.Redacted {
		t.Fatalf("old file read: %q %v", got.Request.Community, err)
	}
}

// A history file saved by v0.5.7 or earlier, with the SNMP community in
// clear, is rewritten without it the first time it is read. Everything
// else in the file is kept, fields this version does not know included.
func TestOldHistoryFileRewritten(t *testing.T) {
	const secret = "s3cret-community"
	s := open(t)
	old := `{"id":"old3","schemaVersion":2,"startedAt":"2026-01-01T09:00:00Z","state":"done",` +
		`"request":{"cidr":"10.0.0.0/24","community":"` + secret + `","liveIntervalSec":30,"newerOption":[1,2]},` +
		`"hosts":[{"ip":"10.0.0.7","alive":true,"deviceType":"printer"}],"stats":{"alive":1},"addedLater":{"x":1.25}}`
	path := filepath.Join(s.Dir(), "history", "20260101T090000Z-old3.json")
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	writes := 0
	rewriteFile = func(p string, b []byte) error { writes++; return writeAtomic(p, b) }
	t.Cleanup(func() { rewriteFile = writeAtomic })

	l, err := s.ListHistory()
	if err != nil || len(l) != 1 || l[0].ID != "old3" || l[0].Error != "" {
		t.Fatalf("list = %+v %v", l, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || writes != 1 {
		t.Fatalf("not rewritten (%d writes): %s", writes, data)
	}
	var before, after map[string]any
	json.Unmarshal([]byte(old), &before)
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatalf("rewritten file does not parse: %v\n%s", err, data)
	}
	before["request"].(map[string]any)["community"] = model.Redacted
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rewrite changed more than the community:\nwant %v\ngot  %v", before, after)
	}
	got, err := s.LoadScan("old3")
	if err != nil || got.Request.Community != model.Redacted || len(got.Hosts) != 1 || got.Request.LiveIntervalSec != 30 {
		t.Fatalf("read back: %+v %v", got, err)
	}
	if writes != 1 {
		t.Fatalf("a redacted file was rewritten again (%d writes)", writes)
	}
	left, _ := filepath.Glob(filepath.Join(s.Dir(), "history", ".tmp-*"))
	if len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

// When the rewrite fails, the file is left exactly as it was, the scan
// still reads with the community hidden, and the next read tries again.
func TestOldHistoryFileRewriteFails(t *testing.T) {
	const secret = "s3cret-community"
	s := open(t)
	old := `{"id":"old4","schemaVersion":2,"startedAt":"2026-01-01T09:00:00Z","state":"done","request":{"cidr":"10.0.0.0/24","community":"` + secret + `"}}`
	path := filepath.Join(s.Dir(), "history", "20260101T090000Z-old4.json")
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	tries := 0
	rewriteFile = func(string, []byte) error { tries++; return errors.New("disk full") }
	t.Cleanup(func() { rewriteFile = writeAtomic })
	for i := 1; i <= 2; i++ {
		got, err := s.LoadScan("old4")
		if err != nil || got.Request.Community != model.Redacted || got.Request.CIDR != "10.0.0.0/24" {
			t.Fatalf("read %d: %+v %v", i, got, err)
		}
		if data, _ := os.ReadFile(path); string(data) != old {
			t.Fatalf("file changed after a failed rewrite: %s", data)
		}
		if tries != i {
			t.Fatalf("read %d: %d tries", i, tries)
		}
	}
	// A file that changed on disk since it was read is not overwritten.
	rewriteFile = writeAtomic
	if err := s.redactFile(fileInfo{name: "20260101T090000Z-old4.json"}, []byte(old+" ")); err == nil {
		t.Fatal("rewrote a file that changed since it was read")
	}
	if data, _ := os.ReadFile(path); string(data) != old {
		t.Fatalf("file changed: %s", data)
	}
}

// TestRedactFileNullRequestDuplicate: a file with the community in "request"
// and a case-variant duplicate "Request": null must not panic.
func TestRedactFileNullRequestDuplicate(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	name := "20260101T090000Z-dup.json"
	data := []byte(`{"id":"dup","request":{"cidr":"10.0.0.0/24","community":"private"},"Request":null,"hosts":[]}`)
	if err := os.WriteFile(filepath.Join(s.histDir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	_ = s.redactFile(fileInfo{name: name}, data)
}

// A reader holding a data file open (on Windows, `netdiag mcp` reading what
// the window saves) delays a save instead of failing it.
func TestWriteAtomicWhileRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileOpenProblems)
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		time.Sleep(40 * time.Millisecond)
		f.Close()
		close(closed)
	}()
	if err := writeAtomic(path, []byte("new")); err != nil {
		t.Fatalf("save while read: %v", err)
	}
	<-closed
	if b, err := os.ReadFile(path); err != nil || string(b) != "new" {
		t.Fatalf("after the save: %q %v", b, err)
	}
}

// A reader that never lets go fails the save after the retries (Windows
// only: elsewhere a rename replaces an open file), and no temp file is
// left behind.
func TestWriteAtomicReaderNeverCloses(t *testing.T) {
	renameWait = time.Millisecond
	t.Cleanup(func() { renameWait = 10 * time.Millisecond })
	dir := t.TempDir()
	path := filepath.Join(dir, FileOpenProblems)
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	err = writeAtomic(path, []byte("new"))
	f.Close()
	if runtime.GOOS == "windows" {
		if err == nil {
			t.Fatal("replaced a file another process holds open")
		}
	} else if err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temp file left: %s", e.Name())
		}
	}
}

// failRename makes the next rename calls fail with errs, one each, then
// rename as usual; it returns how many calls there were. Tests that use it
// run alone (not in parallel): rename is the package's.
func failRename(t *testing.T, errs ...error) *int {
	t.Helper()
	renameWait = time.Millisecond
	calls := 0
	rename = func(from, to string) error {
		calls++
		if calls <= len(errs) {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: errs[calls-1]}
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { rename, renameWait = os.Rename, 10*time.Millisecond })
	return &calls
}

// A rename that fails for any reason but a file held open fails the save
// at once, and no temp file is left behind.
func TestReplaceFileOtherErrorsAreImmediate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileOpenProblems)
	disk := errors.New("the disk is full")
	calls := failRename(t, disk, disk)
	if err := writeAtomic(path, []byte("new")); !errors.Is(err, disk) {
		t.Fatalf("save = %v, want the disk error", err)
	}
	if *calls != 1 {
		t.Fatalf("renamed %d times, want 1", *calls)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 0 {
		t.Fatalf("left %v", ents)
	}
}
