// Package problems keeps what is wrong on the local network. This file is
// the open-problem ledger (v0.6 F2b): every problem netdiag reported and
// has not seen fixed, kept in open-problems.json so a restart neither
// repeats a problem nor forgets to close one, plus the problems fixed in the
// last day. One Ledger per process is the file's only writer.
package problems

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"sync"
	"time"

	"netdiag/internal/model"
	"netdiag/internal/notify"
	"netdiag/internal/store"
)

// ledgerSchema is open-problems.json's schemaVersion.
const ledgerSchema = 1

// Ledger limits (v0.6 §4.6).
const (
	// FixedFor is how long a fixed problem stays on the list.
	FixedFor = 24 * time.Hour
	// MaxFixed is how many fixed problems the list keeps.
	MaxFixed = 50
	// OtherNetworkFor is how long an open problem of a network netdiag is
	// not on stays: after that it is dropped (team chat resolves its card).
	OtherNetworkFor = 7 * 24 * time.Hour
	// SaveDelay is the shortest time between two saves: changes in between
	// are written together.
	SaveDelay = 2 * time.Second
	// touchEvery is how stale an open problem's LastSeenAt may get before
	// a scan on its network refreshes it, so watching does not rewrite the
	// file every cycle.
	touchEvery = time.Hour
)

// Chat states of an open problem (Entry.Chat), set by the team chat router.
const (
	ChatPending = "pending"
	ChatPosted  = "posted"
)

// Entry is an open problem.
type Entry struct {
	Key      string `json:"key"`
	Trigger  string `json:"trigger"`
	Severity string `json:"severity"`
	IP       string `json:"ip,omitempty"`
	MAC      string `json:"mac,omitempty"`
	Name     string `json:"name,omitempty"`
	Title    string `json:"title"`
	// Detail is what the OK names (a printer alert's or supply's description,
	// or a health check's name).
	Detail string `json:"detail,omitempty"`
	// Network is the id (model.NetworkID) of the network the problem is on,
	// "" when netdiag could not tell the network apart.
	Network string `json:"network"`
	// OpenedAt is when it was first reported; LastSeenAt when netdiag was
	// last on its network with it open (RFC 3339).
	OpenedAt   string `json:"openedAt"`
	LastSeenAt string `json:"lastSeenAt"`
	// Chat is "", ChatPending or ChatPosted.
	Chat string `json:"chat,omitempty"`
}

// Fixed is a problem fixed in the last FixedFor.
type Fixed struct {
	Key string `json:"key"`
	// Title is the open problem's title ("Health check failed", "Printer
	// offline"): what was fixed. Only a problem the ledger did not have open
	// keeps the OK notice's title.
	Title string `json:"title"`
	// Detail says which one was fixed, as the open problem's Detail does
	// (the check, the printer alert or the supply).
	Detail   string `json:"detail,omitempty"`
	IP       string `json:"ip,omitempty"`
	Name     string `json:"name,omitempty"`
	Network  string `json:"network"`
	OpenedAt string `json:"openedAt,omitempty"`
	FixedAt  string `json:"fixedAt"`
	// Chat is the open problem's team chat state when it was fixed, so the
	// chat router can still tell whether a card waits for its resolve.
	Chat string `json:"chat,omitempty"`
}

// ledgerFile is open-problems.json.
type ledgerFile struct {
	SchemaVersion int     `json:"schemaVersion"`
	Network       string  `json:"network"` // the network of the last change
	UpdatedAt     string  `json:"updatedAt"`
	Open          []Entry `json:"open"`
	Fixed         []Fixed `json:"fixed"`
}

// Files is the part of *store.Store the ledger goes through.
type Files interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte) error
	QuarantineFile(name string) (string, error)
}

// Ledger is the open-problem ledger. It is safe for concurrent use. Apply
// never touches the disk: a save follows at most every SaveDelay, and Flush
// writes what is left (on Quit).
type Ledger struct {
	files Files
	now   func() time.Time
	delay time.Duration

	wmu sync.Mutex // one write at a time, in order

	mu      sync.Mutex
	network string
	open    map[string]*Entry
	fixed   []Fixed
	dirty   bool
	timer   *time.Timer
	onDrop  func([]Entry)
	// unread is why open-problems.json could not be read (or, unreadable,
	// could not be set aside): until a read works, nothing is written over
	// the file, so the problems it holds are not lost.
	unread error
}

// OpenLedger reads open-problems.json. It always returns a usable ledger:
// a missing file starts it empty, and an unreadable one is set aside as
// <name>.bad-<time> (the error says so) and it starts empty too. A file
// that could not be read at all (a sharing violation, access denied) or
// could not be set aside is never written over: the ledger keeps its
// changes in memory, reads the file again before each save and adds what
// it holds.
func OpenLedger(files Files) (*Ledger, error) {
	l := &Ledger{files: files, now: time.Now, delay: SaveDelay, open: map[string]*Entry{}}
	f, bad, err := l.read()
	if err != nil {
		l.unread = err
		return l, err
	}
	if f != nil {
		l.merge(*f)
	}
	if bad != "" {
		return l, fmt.Errorf("problems: %s was unreadable and is now %s", store.FileOpenProblems, bad)
	}
	return l, nil
}

// read reads open-problems.json without l.mu: the file (nil when there is
// none), where an unreadable one was set aside, or why it could not be read
// or set aside.
func (l *Ledger) read() (*ledgerFile, string, error) {
	data, err := l.files.ReadFile(store.FileOpenProblems)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("problems: %s could not be read: %w", store.FileOpenProblems, err)
	}
	var f ledgerFile
	if err := json.Unmarshal(data, &f); err != nil || f.SchemaVersion < 1 {
		bad, qerr := l.files.QuarantineFile(store.FileOpenProblems)
		if qerr != nil {
			return nil, "", fmt.Errorf("problems: %s is unreadable and could not be set aside: %v", store.FileOpenProblems, qerr)
		}
		return nil, bad, nil
	}
	return &f, "", nil
}

// merge adds the file f's problems to the ledger. What the ledger already
// has wins: a problem it has open, or fixed since it started, is not taken
// from the file. l.mu must be held (or l not shared yet).
func (l *Ledger) merge(f ledgerFile) {
	if l.network == "" {
		l.network = f.Network
	}
	fixed := map[string]bool{}
	for _, x := range l.fixed {
		fixed[slot(x.Network, x.Key)] = true
	}
	for i := range f.Open {
		e := f.Open[i]
		k := slot(e.Network, e.Key)
		if e.Key == "" || l.open[k] != nil || fixed[k] {
			continue
		}
		l.open[k] = &e
	}
	all := append(append([]Fixed(nil), f.Fixed...), l.fixed...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].FixedAt < all[j].FixedAt })
	l.fixed = all
}

// SetClock replaces the clock (tests).
func (l *Ledger) SetClock(now func() time.Time) {
	l.mu.Lock()
	l.now = now
	l.mu.Unlock()
}

// OnDrop sets the function told about the open problems dropped because
// netdiag has not been on their network for OtherNetworkFor, so team chat
// can resolve their cards (v0.6 M11). It is called without the ledger's
// lock held.
func (l *Ledger) OnDrop(fn func([]Entry)) {
	l.mu.Lock()
	l.onDrop = fn
	l.mu.Unlock()
}

// kept reports whether trigger's problems go into the ledger: new devices
// have nothing to fix, and the storm guard's problem is about this run of
// netdiag only.
func kept(trigger string) bool {
	switch trigger {
	case model.TriggerNewDevice, model.TriggerNetdiagLink, model.TriggerChatTest:
		return false
	}
	return true
}

// Apply records the notices of one scan step on network: a fault opens its
// problem (one already open keeps its OpenedAt), an OK moves it to the fixed
// list. It also marks network's open problems as seen and drops the old
// ones. It never blocks on the disk.
func (l *Ledger) Apply(network string, ns []model.Notice) {
	l.mu.Lock()
	now := l.now().UTC()
	at := now.Format(time.RFC3339)
	changed := network != l.network
	l.network = network
	for _, n := range ns {
		if !kept(n.Trigger) || n.Key == "" {
			continue
		}
		if n.OK {
			e := l.open[slot(network, n.Key)]
			delete(l.open, slot(network, n.Key))
			fx := Fixed{Key: n.Key, Title: n.Title, Detail: n.Detail, IP: n.IP, Name: n.Name, Network: network,
				OpenedAt: n.OpenedAt, FixedAt: stamp(n.At, at)}
			if e != nil {
				fx.Title, fx.Network, fx.Chat = e.Title, e.Network, e.Chat
				if e.Detail != "" {
					fx.Detail = e.Detail
				}
				if fx.OpenedAt == "" {
					fx.OpenedAt = e.OpenedAt
				}
				if fx.IP == "" {
					fx.IP, fx.Name = e.IP, e.Name
				}
			}
			l.fixed = append(l.fixed, fx)
			changed = true
			continue
		}
		if e := l.open[slot(network, n.Key)]; e != nil {
			e.Severity, e.LastSeenAt = n.Severity, at
			changed = true
			continue
		}
		l.open[slot(network, n.Key)] = &Entry{Key: n.Key, Trigger: n.Trigger, Severity: n.Severity, IP: n.IP, MAC: n.MAC,
			Name: n.Name, Title: n.Title, Detail: n.Detail, Network: network,
			OpenedAt: stamp(n.At, at), LastSeenAt: at}
		changed = true
	}
	for _, e := range l.open {
		if e.Network == network && network != "" && olderThan(e.LastSeenAt, now, touchEvery) {
			e.LastSeenAt = at
			changed = true
		}
	}
	dropped, pruned := l.prune(now)
	if changed || pruned {
		l.changed()
	}
	fn := l.onDrop
	l.mu.Unlock()
	if fn != nil && len(dropped) > 0 {
		fn(dropped)
	}
}

// slot is where an open problem is kept: by network and key, because two
// networks can have a problem under one key (each has a DNS check).
func slot(network, key string) string { return network + "\x00" + key }

// stamp is t, or def when t is empty.
func stamp(t, def string) string {
	if t == "" {
		return def
	}
	return t
}

// olderThan reports whether the RFC 3339 time t is more than d before now
// (an unreadable time counts as old).
func olderThan(t string, now time.Time, d time.Duration) bool {
	tt, err := time.Parse(time.RFC3339, t)
	return err != nil || now.Sub(tt) > d
}

// prune drops fixed problems older than FixedFor beyond the newest MaxFixed,
// and open problems of other networks not seen for OtherNetworkFor. It
// returns the open problems it dropped and whether anything changed. l.mu
// must be held.
func (l *Ledger) prune(now time.Time) ([]Entry, bool) {
	changed := false
	keep := l.fixed[:0]
	for _, f := range l.fixed {
		if olderThan(f.FixedAt, now, FixedFor) {
			changed = true
			continue
		}
		keep = append(keep, f)
	}
	l.fixed = keep
	if n := len(l.fixed) - MaxFixed; n > 0 {
		l.fixed = append([]Fixed(nil), l.fixed[n:]...)
		changed = true
	}
	var dropped []Entry
	for k, e := range l.open {
		if e.Network != l.network && olderThan(e.LastSeenAt, now, OtherNetworkFor) {
			dropped = append(dropped, *e)
			delete(l.open, k)
		}
	}
	sort.Slice(dropped, func(i, j int) bool { return dropped[i].Key < dropped[j].Key })
	return dropped, changed || len(dropped) > 0
}

// changed schedules a save. l.mu must be held.
func (l *Ledger) changed() {
	l.dirty = true
	if l.timer == nil {
		l.timer = time.AfterFunc(l.delay, func() { _ = l.Flush() })
	}
}

// OpenFor returns the open problems of network, for a scan to seed its
// alert engine with (nothing for the unknown network "").
func (l *Ledger) OpenFor(network string) []notify.OpenFault {
	if network == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []notify.OpenFault
	for _, e := range l.open {
		if e.Network != network {
			continue
		}
		opened, _ := time.Parse(time.RFC3339, e.OpenedAt)
		out = append(out, notify.OpenFault{Key: e.Key, Trigger: e.Trigger, Severity: e.Severity, IP: e.IP,
			MAC: e.MAC, Name: e.Name, Title: e.Title, Detail: e.Detail, OpenedAt: opened})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// ReadLedger reads open-problems.json without becoming its writer: the
// open problems (sorted by key) and the fixed ones (oldest first), as
// Snapshot gives them. It is for a netdiag that is not the window's, such
// as `netdiag mcp` over stdio (MCP list_problems): a missing file is no
// problems, and an unreadable one is an error and stays as it is, for the
// window's Ledger to set aside.
func ReadLedger(files interface{ ReadFile(string) ([]byte, error) }) ([]Entry, []Fixed, error) {
	data, err := files.ReadFile(store.FileOpenProblems)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("problems: %s could not be read: %w", store.FileOpenProblems, err)
	}
	var f ledgerFile
	if err := json.Unmarshal(data, &f); err != nil || f.SchemaVersion < 1 {
		return nil, nil, fmt.Errorf("problems: %s is unreadable", store.FileOpenProblems)
	}
	open := make([]Entry, 0, len(f.Open))
	for _, e := range f.Open {
		if e.Key != "" {
			open = append(open, e)
		}
	}
	sort.Slice(open, func(i, j int) bool { return open[i].Key < open[j].Key })
	fixed := append([]Fixed(nil), f.Fixed...)
	sort.SliceStable(fixed, func(i, j int) bool { return fixed[i].FixedAt < fixed[j].FixedAt })
	return open, fixed, nil
}

// Snapshot returns copies of the open problems (sorted by key) and the
// fixed ones (oldest first).
func (l *Ledger) Snapshot() ([]Entry, []Fixed) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.entries(), append([]Fixed(nil), l.fixed...)
}

// entries returns copies of the open problems sorted by key. l.mu must be
// held.
func (l *Ledger) entries() []Entry {
	out := make([]Entry, 0, len(l.open))
	for _, e := range l.open {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Chat returns the team chat state (ChatPending, ChatPosted or "") of the
// problem key on network: the open one's, else the one fixed last's, so the
// chat router can tell, when it sees an OK, whether a card waits for its
// resolve. A scan names its network with scan.Scan.Network.
func (l *Ledger) Chat(network, key string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.open[slot(network, key)]; e != nil {
		return e.Chat
	}
	if i := l.fixedAt(network, key); i >= 0 {
		return l.fixed[i].Chat
	}
	return ""
}

// IsOpen reports whether the problem key on network is open. The team chat
// outbox asks it for the posts a previous run left: only a problem still
// open after the first pass is posted (v0.6 4.9).
func (l *Ledger) IsOpen(network, key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.open[slot(network, key)] != nil
}

// MarkChat records the team chat state (ChatPending, ChatPosted or "") of
// the problem key on network: the open one, else the one fixed last (its
// card was resolved). network is the problem's own, which an outbox item
// keeps: netdiag may be on another network by the time the item is sent.
func (l *Ledger) MarkChat(network, key, state string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.open[slot(network, key)]; e != nil {
		if e.Chat != state {
			e.Chat = state
			l.changed()
		}
		return
	}
	if i := l.fixedAt(network, key); i >= 0 && l.fixed[i].Chat != state {
		l.fixed[i].Chat = state
		l.changed()
	}
}

// fixedAt is the index of the problem key on network fixed last, or -1.
// l.mu must be held.
func (l *Ledger) fixedAt(network, key string) int {
	for i := len(l.fixed) - 1; i >= 0; i-- {
		if l.fixed[i].Network == network && l.fixed[i].Key == key {
			return i
		}
	}
	return -1
}

// Flush writes the ledger now if it changed since the last save (on Quit).
// When open-problems.json could not be read before, it reads it first and
// adds what it holds; while it still cannot be read, Flush writes nothing
// and says why. A save that fails keeps the changes and schedules another
// one, so they reach the file without waiting for the next change.
func (l *Ledger) Flush() error {
	err := l.flush()
	if err != nil {
		l.mu.Lock()
		if l.dirty {
			l.changed()
		}
		l.mu.Unlock()
	}
	return err
}

// flush is Flush without the retry.
func (l *Ledger) flush() error {
	l.wmu.Lock()
	defer l.wmu.Unlock()
	l.mu.Lock()
	if l.timer != nil {
		l.timer.Stop()
		l.timer = nil
	}
	if !l.dirty {
		l.mu.Unlock()
		return nil
	}
	unread := l.unread != nil
	l.mu.Unlock()
	if unread {
		f, _, err := l.read() // without l.mu: Apply never waits on the disk
		l.mu.Lock()
		if err != nil {
			l.unread = err
			l.mu.Unlock()
			return err
		}
		l.unread = nil
		if f != nil {
			l.merge(*f)
		}
		l.mu.Unlock()
	}
	l.mu.Lock()
	f := ledgerFile{SchemaVersion: ledgerSchema, Network: l.network, UpdatedAt: l.now().UTC().Format(time.RFC3339),
		Open: l.entries(), Fixed: append([]Fixed{}, l.fixed...)}
	l.dirty = false
	l.mu.Unlock()
	data, err := json.MarshalIndent(f, "", "  ")
	if err == nil {
		err = l.files.WriteFile(store.FileOpenProblems, data)
	}
	if err != nil {
		l.mu.Lock()
		l.dirty = true // Flush schedules the next try
		l.mu.Unlock()
	}
	return err
}
