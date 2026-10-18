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
	// Detail is what the OK names (a printer alert's or supply's description).
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
	Key      string `json:"key"`
	Title    string `json:"title"`
	IP       string `json:"ip,omitempty"`
	Name     string `json:"name,omitempty"`
	Network  string `json:"network"`
	OpenedAt string `json:"openedAt,omitempty"`
	FixedAt  string `json:"fixedAt"`
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
}

// OpenLedger reads open-problems.json. It always returns a usable ledger:
// a missing file starts it empty, and an unreadable one is set aside as
// <name>.bad-<time> (the error says so) and it starts empty too.
func OpenLedger(files Files) (*Ledger, error) {
	l := &Ledger{files: files, now: time.Now, delay: SaveDelay, open: map[string]*Entry{}}
	data, err := files.ReadFile(store.FileOpenProblems)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	var f ledgerFile
	if err := json.Unmarshal(data, &f); err != nil || f.SchemaVersion < 1 {
		bad, qerr := files.QuarantineFile(store.FileOpenProblems)
		if qerr != nil {
			return l, fmt.Errorf("problems: %s is unreadable and could not be set aside: %v", store.FileOpenProblems, qerr)
		}
		return l, fmt.Errorf("problems: %s was unreadable and is now %s", store.FileOpenProblems, bad)
	}
	l.network = f.Network
	for i := range f.Open {
		if e := f.Open[i]; e.Key != "" {
			l.open[slot(e.Network, e.Key)] = &e
		}
	}
	l.fixed = f.Fixed
	return l, nil
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
			fx := Fixed{Key: n.Key, Title: n.Title, IP: n.IP, Name: n.Name, Network: network,
				OpenedAt: n.OpenedAt, FixedAt: stamp(n.At, at)}
			if e != nil {
				fx.Title, fx.Network = e.Title, e.Network
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

// MarkChat records the team chat state (ChatPending, ChatPosted or "") of
// an open problem on the network of the last Apply.
func (l *Ledger) MarkChat(key, state string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.open[slot(l.network, key)]; e != nil && e.Chat != state {
		e.Chat = state
		l.changed()
	}
}

// Flush writes the ledger now if it changed since the last save (on Quit).
func (l *Ledger) Flush() error {
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
		l.dirty = true // written with the next change or Flush
		l.mu.Unlock()
	}
	return err
}
