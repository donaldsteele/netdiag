package problems

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"netdiag/internal/model"
	"netdiag/internal/store"
)

// countFiles is a store that counts the ledger's writes.
type countFiles struct {
	*store.Store
	writes atomic.Int32
}

func (c *countFiles) WriteFile(name string, data []byte) error {
	c.writes.Add(1)
	return c.Store.WriteFile(name, data)
}

func newFiles(t *testing.T) *countFiles {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &countFiles{Store: st}
}

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func openTest(t *testing.T, files Files) (*Ledger, *fakeClock) {
	t.Helper()
	l, err := OpenLedger(files)
	if err != nil {
		t.Fatal(err)
	}
	c := &fakeClock{t: time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC)}
	l.SetClock(c.now)
	l.delay = time.Hour // tests flush by hand unless they say otherwise
	return l, c
}

const netA, netB = "n-aaaaaaaaaaaa", "n-bbbbbbbbbbbb"

var (
	cover = model.Notice{ID: "n-1", Trigger: model.TriggerPrinterAlert, Severity: "critical", IP: "192.168.68.76",
		MAC: "00:07:4D:11:22:33", Name: "ZD621", Title: "Printer alert", Detail: "Door or cover open",
		Key: "printerAlert|00:07:4D:11:22:33|hrPrinterDetectedErrorState.doorOpen|1a2b3c4d", At: "2026-10-17T09:00:00Z"}
	coverOK = model.Notice{ID: "n-2", Trigger: model.TriggerPrinterAlert, Severity: "info", IP: cover.IP, Name: cover.Name,
		Title: "Printer alert cleared", Key: cover.Key, OK: true, OpenedAt: cover.At, At: "2026-10-17T09:30:00Z"}
	dns = model.Notice{ID: "n-3", Trigger: model.TriggerHealthFailed, Severity: "warn", Title: "Health check failed",
		Key: "healthFailed||dns", At: "2026-10-17T09:05:00Z"}
)

func TestLedgerApplyAndReopen(t *testing.T) {
	files := newFiles(t)
	l, _ := openTest(t, files)
	l.Apply(netA, []model.Notice{cover, dns,
		{Trigger: model.TriggerNewDevice, Key: "newDevice|AA|10.0.0.9"},
		{Trigger: model.TriggerNetdiagLink, Key: "netdiagLink||" + netA}})
	later := cover
	later.At, later.Severity = "2026-10-17T09:10:00Z", "warn"
	l.Apply(netA, []model.Notice{later}) // a fault already open keeps its OpenedAt
	open, fixed := l.Snapshot()
	if len(open) != 2 || len(fixed) != 0 {
		t.Fatalf("open %v fixed %v", open, fixed)
	}
	if e := open[1]; e.Key != cover.Key || e.OpenedAt != cover.At || e.Severity != "warn" || e.Network != netA || e.Detail != cover.Detail {
		t.Fatalf("entry %+v", e)
	}
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
	l2, _ := openTest(t, files)
	got := l2.OpenFor(netA)
	if len(got) != 2 || got[1].Key != cover.Key || got[1].OpenedAt.Format(time.RFC3339) != cover.At || got[1].Detail != cover.Detail || got[1].MAC != cover.MAC {
		t.Fatalf("reopened: %+v", got)
	}
	if got := l2.OpenFor(netB); len(got) != 0 {
		t.Fatalf("other network: %v", got)
	}
	if got := l2.OpenFor(""); len(got) != 0 {
		t.Fatalf("unknown network: %v", got)
	}
	// An OK moves the problem to the fixed list.
	l2.Apply(netA, []model.Notice{coverOK})
	open, fixed = l2.Snapshot()
	if len(open) != 1 || len(fixed) != 1 {
		t.Fatalf("open %v fixed %v", open, fixed)
	}
	if f := fixed[0]; f.Key != cover.Key || f.FixedAt != coverOK.At || f.OpenedAt != cover.At || f.Title != "Printer alert" || f.Network != netA {
		t.Fatalf("fixed %+v", f)
	}
}

// The same key on two networks is two problems.
func TestLedgerKeysPerNetwork(t *testing.T) {
	l, _ := openTest(t, newFiles(t))
	l.Apply(netA, []model.Notice{dns})
	l.Apply(netB, []model.Notice{dns})
	ok := model.Notice{Trigger: dns.Trigger, Key: dns.Key, OK: true, Title: "Health check OK again", At: "2026-10-17T09:20:00Z"}
	l.Apply(netB, []model.Notice{ok})
	if got := l.OpenFor(netA); len(got) != 1 {
		t.Fatalf("A: %v", got)
	}
	if got := l.OpenFor(netB); len(got) != 0 {
		t.Fatalf("B: %v", got)
	}
}

// Saves are debounced to one per SaveDelay; Flush writes what is left and
// nothing when nothing changed.
func TestLedgerSaveDebounce(t *testing.T) {
	files := newFiles(t)
	l, _ := openTest(t, files)
	l.delay = 50 * time.Millisecond
	for range 20 {
		l.Apply(netA, []model.Notice{cover, dns})
	}
	deadline := time.Now().Add(2 * time.Second)
	for files.writes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := files.writes.Load(); n != 1 {
		t.Fatalf("writes = %d, want 1", n)
	}
	if err := l.Flush(); err != nil || files.writes.Load() != 1 {
		t.Fatalf("flush with nothing new: %v, writes %d", err, files.writes.Load())
	}
	l.MarkChat(netA, cover.Key, ChatPosted)
	if err := l.Flush(); err != nil || files.writes.Load() != 2 {
		t.Fatalf("flush: %v, writes %d", err, files.writes.Load())
	}
	data, err := os.ReadFile(filepath.Join(files.Dir(), store.FileOpenProblems))
	if err != nil {
		t.Fatal(err)
	}
	var f ledgerFile
	if err := json.Unmarshal(data, &f); err != nil || f.SchemaVersion != 1 || f.Network != netA || len(f.Open) != 2 || f.Open[1].Chat != ChatPosted {
		t.Fatalf("file %s (%v)", data, err)
	}
}

// Fixed problems stay 24 h, at most 50.
func TestLedgerFixedPruning(t *testing.T) {
	l, c := openTest(t, newFiles(t))
	for i := range 60 {
		n := dns
		n.Key = "healthFailed||c" + string(rune('A'+i))
		ok := model.Notice{Trigger: n.Trigger, Key: n.Key, OK: true, At: c.now().Format(time.RFC3339)}
		l.Apply(netA, []model.Notice{n, ok})
	}
	if _, fixed := l.Snapshot(); len(fixed) != MaxFixed || fixed[0].Key != "healthFailed||c"+string(rune('A'+10)) {
		t.Fatalf("fixed %d, first %v", len(fixed), fixed[0])
	}
	c.add(FixedFor + time.Minute)
	l.Apply(netA, nil)
	if _, fixed := l.Snapshot(); len(fixed) != 0 {
		t.Fatalf("after a day: %v", fixed)
	}
}

// Open problems of another network are dropped after 7 days without
// netdiag on it (and OnDrop hears of them); a scan on their own network
// keeps them.
func TestLedgerOtherNetworkDrop(t *testing.T) {
	l, c := openTest(t, newFiles(t))
	var dropped []Entry
	l.OnDrop(func(es []Entry) { dropped = append(dropped, es...) })
	l.Apply(netA, []model.Notice{cover})
	c.add(6 * 24 * time.Hour)
	l.Apply(netB, []model.Notice{dns})
	if open, _ := l.Snapshot(); len(open) != 2 || len(dropped) != 0 {
		t.Fatalf("after 6 days: %v %v", open, dropped)
	}
	l.Apply(netA, nil) // back on A: its problem is seen again
	c.add(3 * 24 * time.Hour)
	l.Apply(netB, nil)
	if open, _ := l.Snapshot(); len(open) != 2 || len(dropped) != 0 {
		t.Fatalf("A seen 3 days ago: %v %v", open, dropped)
	}
	c.add(5 * 24 * time.Hour)
	l.Apply(netB, nil)
	open, _ := l.Snapshot()
	if len(open) != 1 || open[0].Network != netB || len(dropped) != 1 || dropped[0].Key != cover.Key {
		t.Fatalf("A not seen for 8 days: open %v dropped %v", open, dropped)
	}
}

// An unreadable file is set aside as .bad-<time> and the ledger starts
// empty; a missing one starts it empty without a word.
func TestLedgerCorruptFile(t *testing.T) {
	files := newFiles(t)
	if l, err := OpenLedger(files); err != nil || len(l.OpenFor(netA)) != 0 {
		t.Fatalf("missing file: %v", err)
	}
	if err := files.Store.WriteFile(store.FileOpenProblems, []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	l, err := OpenLedger(files)
	if err == nil || !strings.Contains(err.Error(), ".bad-") || l == nil {
		t.Fatalf("corrupt file: %v", err)
	}
	if open, fixed := l.Snapshot(); len(open) != 0 || len(fixed) != 0 {
		t.Fatalf("not empty: %v %v", open, fixed)
	}
	ents, _ := os.ReadDir(files.Dir())
	var bad, cur bool
	for _, e := range ents {
		bad = bad || strings.HasPrefix(e.Name(), store.FileOpenProblems+".bad-")
		cur = cur || e.Name() == store.FileOpenProblems
	}
	if !bad || cur {
		t.Fatalf("files %v", ents)
	}
	l.Apply(netA, []model.Notice{cover})
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
	if l2, err := OpenLedger(files); err != nil || len(l2.OpenFor(netA)) != 1 {
		t.Fatalf("after the next save: %v", err)
	}
}

// The team chat state is kept per network and key: an outbox item for
// network A marks A's problem even after netdiag moved to B, and an OK
// keeps the state on the fixed entry, so the router can still resolve a
// posted card.
func TestLedgerChatState(t *testing.T) {
	l, _ := openTest(t, newFiles(t))
	l.Apply(netA, []model.Notice{dns})
	l.Apply(netB, []model.Notice{dns}) // netdiag is on B now
	l.MarkChat(netA, dns.Key, ChatPosted)
	if a, b := l.Chat(netA, dns.Key), l.Chat(netB, dns.Key); a != ChatPosted || b != "" {
		t.Fatalf("chat A %q, B %q", a, b)
	}
	if !l.IsOpen(netA, dns.Key) || !l.IsOpen(netB, dns.Key) || l.IsOpen("n-other", dns.Key) {
		t.Fatal("IsOpen before the OK")
	}
	ok := model.Notice{Trigger: dns.Trigger, Key: dns.Key, OK: true, Title: "Health check OK again", At: "2026-10-17T09:20:00Z"}
	l.Apply(netA, []model.Notice{ok})
	if l.IsOpen(netA, dns.Key) || !l.IsOpen(netB, dns.Key) {
		t.Fatal("IsOpen after the OK on A")
	}
	if got := l.Chat(netA, dns.Key); got != ChatPosted {
		t.Fatalf("after the OK: %q", got)
	}
	_, fixed := l.Snapshot()
	if len(fixed) != 1 || fixed[0].Network != netA || fixed[0].Chat != ChatPosted {
		t.Fatalf("fixed %+v", fixed)
	}
	l.MarkChat(netA, dns.Key, "") // the card was resolved
	if _, fixed := l.Snapshot(); fixed[0].Chat != "" || l.Chat(netA, dns.Key) != "" || l.Chat(netB, dns.Key) != "" {
		t.Fatalf("after the resolve: %+v", fixed)
	}
	if l.Chat(netA, "nothing||here") != "" {
		t.Fatal("unknown key has a state")
	}
}

// flakyFiles is a store whose reads fail while readFails > 0 and whose
// set-aside fails while noSetAside is set.
type flakyFiles struct {
	*countFiles
	readFails  atomic.Int32
	noSetAside atomic.Bool
}

func (f *flakyFiles) ReadFile(name string) ([]byte, error) {
	if f.readFails.Add(-1) >= 0 {
		return nil, errors.New("the process cannot access the file because it is being used by another process")
	}
	return f.countFiles.ReadFile(name)
}

func (f *flakyFiles) QuarantineFile(name string) (string, error) {
	if f.noSetAside.Load() {
		return "", errors.New("access is denied")
	}
	return f.countFiles.QuarantineFile(name)
}

// A file that could not be read (another program had it open) or, unreadable,
// could not be set aside is never written over: the ledger keeps its
// changes, reads the file again before each save and, once it can, adds
// the problems the file holds.
func TestLedgerUnreadableNotOverwritten(t *testing.T) {
	t.Run("read fails", func(t *testing.T) {
		files := &flakyFiles{countFiles: newFiles(t)}
		earlier, _ := openTest(t, files)
		earlier.Apply(netA, []model.Notice{cover})
		if err := earlier.Flush(); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(filepath.Join(files.Dir(), store.FileOpenProblems))
		files.readFails.Store(2) // the start and the first save
		l, err := OpenLedger(files)
		if err == nil || l == nil || !strings.Contains(err.Error(), "could not be read") {
			t.Fatalf("open: %v", err)
		}
		l.Apply(netB, []model.Notice{dns})
		writes := files.writes.Load()
		if err := l.Flush(); err == nil || files.writes.Load() != writes {
			t.Fatalf("save while unreadable: %v, writes %d", err, files.writes.Load()-writes)
		}
		if now, _ := os.ReadFile(filepath.Join(files.Dir(), store.FileOpenProblems)); string(now) != string(before) {
			t.Fatalf("file written over: %s", now)
		}
		if err := l.Flush(); err != nil {
			t.Fatal(err)
		}
		l2, err := OpenLedger(files)
		if err != nil || len(l2.OpenFor(netA)) != 1 || len(l2.OpenFor(netB)) != 1 {
			t.Fatalf("after the read worked: %v, A %v, B %v", err, l2.OpenFor(netA), l2.OpenFor(netB))
		}
	})
	t.Run("cannot set aside", func(t *testing.T) {
		files := &flakyFiles{countFiles: newFiles(t)}
		if err := files.Store.WriteFile(store.FileOpenProblems, []byte("{not json")); err != nil {
			t.Fatal(err)
		}
		files.noSetAside.Store(true)
		l, err := OpenLedger(files)
		if err == nil || !strings.Contains(err.Error(), "could not be set aside") {
			t.Fatalf("open: %v", err)
		}
		l.Apply(netA, []model.Notice{cover})
		if err := l.Flush(); err == nil {
			t.Fatal("saved over a file it could not set aside")
		}
		if now, _ := os.ReadFile(filepath.Join(files.Dir(), store.FileOpenProblems)); string(now) != "{not json" {
			t.Fatalf("file written over: %s", now)
		}
		files.noSetAside.Store(false)
		if err := l.Flush(); err != nil {
			t.Fatal(err)
		}
		ents, _ := os.ReadDir(files.Dir())
		bad := false
		for _, e := range ents {
			bad = bad || strings.HasPrefix(e.Name(), store.FileOpenProblems+".bad-")
		}
		if l2, err := OpenLedger(files); err != nil || len(l2.OpenFor(netA)) != 1 || !bad {
			t.Fatalf("after set aside: %v, bad %v", err, bad)
		}
	})
}

// A problem this run fixed is not taken back from the file it could not
// read at the start.
func TestLedgerMergeKeepsFixed(t *testing.T) {
	files := &flakyFiles{countFiles: newFiles(t)}
	earlier, _ := openTest(t, files)
	earlier.Apply(netA, []model.Notice{cover})
	if err := earlier.Flush(); err != nil {
		t.Fatal(err)
	}
	files.readFails.Store(1)
	l, err := OpenLedger(files)
	if err == nil {
		t.Fatal("the read did not fail")
	}
	l.delay = time.Hour
	l.Apply(netA, []model.Notice{cover, coverOK})
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
	open, fixed := l.Snapshot()
	if len(open) != 0 || len(fixed) != 1 {
		t.Fatalf("open %+v fixed %+v", open, fixed)
	}
}

// busyFiles is a store whose writes fail while writeFails > 0, as a save
// does on Windows while another process reads the file.
type busyFiles struct {
	*countFiles
	writeFails atomic.Int32
}

func (f *busyFiles) WriteFile(name string, data []byte) error {
	if f.writeFails.Add(-1) >= 0 {
		f.writes.Add(1)
		return errors.New("access is denied")
	}
	return f.countFiles.WriteFile(name, data)
}

// A save that fails is tried again on its own: the change reaches the file
// without another change or Flush.
func TestLedgerRetriesFailedSave(t *testing.T) {
	files := &busyFiles{countFiles: newFiles(t)}
	l, _ := openTest(t, files)
	l.delay = 20 * time.Millisecond
	saved := func(what, want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			data, err := os.ReadFile(filepath.Join(files.Dir(), store.FileOpenProblems))
			if err == nil && strings.Contains(string(data), want) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: not saved (writes %d): %s", what, files.writes.Load(), data)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	files.writeFails.Store(2)
	l.Apply(netA, []model.Notice{cover})
	saved("after two failed saves", cover.Key)
	if n := files.writes.Load(); n != 3 {
		t.Errorf("writes = %d, want 2 failed and 1 good", n)
	}
	// Once saved, nothing more is written.
	time.Sleep(60 * time.Millisecond)
	if n := files.writes.Load(); n != 3 {
		t.Errorf("writes after the save = %d", n)
	}
	// A failed Flush (on Quit) schedules a retry too.
	files.writeFails.Store(1)
	l.MarkChat(netA, cover.Key, ChatPosted)
	if err := l.Flush(); err == nil {
		t.Fatal("the save did not fail")
	}
	saved("after a failed Flush", `"chat": "posted"`)
}
