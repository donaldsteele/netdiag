package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

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

func TestSettings(t *testing.T) {
	s := open(t)
	d, _ := s.LoadSettings()
	if d.Community != "public" || d.Alerts.CooldownSec != 300 || !d.Alerts.Triggers.NewDevice {
		t.Fatalf("defaults %+v", d)
	}
	os.WriteFile(filepath.Join(s.Dir(), "settings.json"), []byte(`{"community":"x"}`), 0o644)
	d, _ = s.LoadSettings()
	if d.Community != "x" || !d.Alerts.Sound || d.SchemaVersion != 1 {
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
	bad := []func(*model.Settings){
		func(x *model.Settings) { x.WebServer.URL = "ftp://x" },
		func(x *model.Settings) { x.Alerts.SupplyThresholdPct = 0 },
		func(x *model.Settings) { x.Alerts.SupplyThresholdPct = 101 },
		func(x *model.Settings) { x.Alerts.CooldownSec = 29 },
		func(x *model.Settings) { x.Alerts.CooldownSec = 3601 },
		func(x *model.Settings) { x.InternetCheckURL = "nope" },
		func(x *model.Settings) { x.IgnoredConflictMACs = []string{"zz"} },
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
