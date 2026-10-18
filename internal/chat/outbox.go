package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"sync"
	"time"

	"netdiag/internal/bubbles/protocol"
	"netdiag/internal/problems"
	"netdiag/internal/store"
)

// The outbox (v0.6 4.9) keeps what netdiag still has to send to team chat
// in chat-outbox.json, so an alert survives the chat server being away and
// netdiag being quit. One sender works through it in order (Manager.send).

// Outbox operations.
const (
	OpPost    = "post"    // POST /alerts
	OpResolve = "resolve" // POST /alerts/resolve, by key
	OpMessage = "message" // POST /channels/{id}/messages
)

// Outbox limits (v0.6 4.9).
const (
	// PostTTL is how long a post waits: an alert older than this is no news.
	PostTTL = 60 * time.Minute
	// ResolveTTL is how long a resolve waits: a card stays open until it
	// goes, so it waits long.
	ResolveTTL = 7 * 24 * time.Hour
	// MessageTTL is how long a message waits.
	MessageTTL = 60 * time.Minute
	// MaxOutbox is how many items the outbox keeps; the oldest notes (info)
	// go first, then the oldest items.
	MaxOutbox = 500
	// outboxSaveDelay is the shortest time between two saves.
	outboxSaveDelay = time.Second
)

// outboxSchema is chat-outbox.json's schemaVersion.
const outboxSchema = 1

// Item is one thing waiting to be sent. The alert's channel is filled in
// when it is sent, so a Change channel applies to what still waits.
type Item struct {
	ID      string `json:"id"`
	Op      string `json:"op"`
	Network string `json:"network"` // the problem's network (model.NetworkID)
	Key     string `json:"key,omitempty"`
	// Alert is a post's alert, without its channel.
	Alert *protocol.AlertInput `json:"alert,omitempty"`
	// Text is a resolve's note or a message's body.
	Text string `json:"text,omitempty"`
	// Update marks a post for a key whose card is open in chat already: a
	// resolve after it closes that card instead of dropping both (D17).
	Update bool `json:"update,omitempty"`
	// Moved marks a post that already moved its card out of another
	// channel once (bubbles' 409 "already open in another channel").
	Moved      bool      `json:"moved,omitempty"`
	EnqueuedAt time.Time `json:"enqueuedAt"`
	Attempts   int       `json:"attempts"`
	NextAt     time.Time `json:"nextAt"`
	ExpiresAt  time.Time `json:"expiresAt"`

	held    bool // left by a previous run: waits for release
	sending bool // the sender has it
}

// outboxFile is chat-outbox.json.
type outboxFile struct {
	SchemaVersion int     `json:"schemaVersion"`
	Items         []*Item `json:"items"`
}

// outbox is the queue. It is safe for concurrent use. mark tells the
// ledger the chat state of a problem (problems.ChatPending, ChatPosted or
// ""); it is called with o.mu held, so it must not call back into the
// outbox.
type outbox struct {
	files     DataFiles // nil: memory only
	now       func() time.Time
	mark      func(network, key, state string)
	saveDelay time.Duration
	wake      chan struct{}

	wmu sync.Mutex // one save at a time

	mu       sync.Mutex
	items    []*Item
	seq      int
	dropped  int
	lastSent time.Time
	dirty    bool
	timer    *time.Timer
}

func newOutbox(files DataFiles, now func() time.Time, mark func(network, key, state string)) *outbox {
	if mark == nil {
		mark = func(string, string, string) {}
	}
	return &outbox{files: files, now: now, mark: mark, saveDelay: outboxSaveDelay, wake: make(chan struct{}, 1)}
}

// load reads chat-outbox.json. Every item it holds waits until release:
// only a problem still open after the first pass is posted (4.9). An
// unreadable file is set aside as <name>.bad-<time>.
func (o *outbox) load() error {
	if o.files == nil {
		return nil
	}
	data, err := o.files.ReadFile(store.FileChatOutbox)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("chat: %s could not be read: %w", store.FileChatOutbox, err)
	}
	var f outboxFile
	if err := json.Unmarshal(data, &f); err != nil || f.SchemaVersion < 1 {
		bad, qerr := o.files.QuarantineFile(store.FileChatOutbox)
		if qerr != nil {
			return fmt.Errorf("chat: %s is unreadable and could not be set aside: %v", store.FileChatOutbox, qerr)
		}
		return fmt.Errorf("chat: %s was unreadable and is now %s", store.FileChatOutbox, bad)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, it := range f.Items {
		if it == nil || it.ID == "" || (it.Op == OpPost && it.Alert == nil) {
			continue
		}
		it.held = true
		o.items = append(o.items, it)
		if n, err := strconv.Atoi(it.ID); err == nil && n > o.seq {
			o.seq = n
		}
	}
	return nil
}

// kick wakes the sender.
func (o *outbox) kick() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// add appends a new item; o.mu must be held.
func (o *outbox) addLocked(it *Item, ttl time.Duration) {
	now := o.now()
	o.seq++
	it.ID = strconv.Itoa(o.seq)
	it.EnqueuedAt, it.NextAt, it.ExpiresAt = now, now, now.Add(ttl)
	o.items = append(o.items, it)
	o.limitLocked()
	o.changedLocked()
}

// pendingPost is the last post for key on network that the sender does not
// have, or -1. o.mu must be held.
func (o *outbox) pendingPost(network, key string) int {
	for i := len(o.items) - 1; i >= 0; i-- {
		it := o.items[i]
		if it.Op == OpPost && it.Network == network && it.Key == key && !it.sending {
			return i
		}
	}
	return -1
}

// pendingResolve is the last resolve for key on network that the sender
// does not have, or -1. o.mu must be held.
func (o *outbox) pendingResolve(network, key string) int {
	for i := len(o.items) - 1; i >= 0; i-- {
		it := o.items[i]
		if it.Op == OpResolve && it.Network == network && it.Key == key && !it.sending {
			return i
		}
	}
	return -1
}

// Post queues the alert for key on network. A newer post replaces one that
// still waits. update says the problem's card is open in chat already.
func (o *outbox) Post(network, key string, in protocol.AlertInput, update bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if i := o.pendingPost(network, key); i >= 0 {
		it := o.items[i]
		it.Alert, it.Update = &in, it.Update || update
		it.ExpiresAt = o.now().Add(PostTTL)
		o.changedLocked()
		o.kick()
		return
	}
	o.addLocked(&Item{Op: OpPost, Network: network, Key: key, Alert: &in, Update: update}, PostTTL)
	if !update {
		o.mark(network, key, problems.ChatPending)
	}
	o.kick()
}

// Resolve queues closing the card of key on network with text. A resolve
// that follows a post never delivered drops both (D17): nobody was told,
// so nobody is told it is fixed. A post that only updated an open card is
// dropped, and the card is closed.
func (o *outbox) Resolve(network, key, text string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if i := o.pendingPost(network, key); i >= 0 {
		p := o.items[i]
		o.items = slices.Delete(o.items, i, i+1)
		o.changedLocked()
		if !p.Update {
			if j := o.pendingResolve(network, key); j >= 0 {
				// The post was to follow a resolve (a card moved to
				// another channel): the card is closed with this note.
				o.items[j].Text = text
				return
			}
			o.mark(network, key, "")
			return
		}
	}
	if j := o.pendingResolve(network, key); j >= 0 {
		o.items[j].Text = text
		o.items[j].ExpiresAt = o.now().Add(ResolveTTL)
		o.changedLocked()
		return
	}
	o.addLocked(&Item{Op: OpResolve, Network: network, Key: key, Text: text}, ResolveTTL)
	o.kick()
}

// Message queues a message to the channel.
func (o *outbox) Message(network, text string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.addLocked(&Item{Op: OpMessage, Network: network, Text: text}, MessageTTL)
	o.kick()
}

// release lets the items a previous run left go, once a scan's first pass
// is reconciled: a post whose problem is no longer open is dropped (4.9),
// the rest are sent. isOpen is nil when there is no ledger: then every
// post goes.
func (o *outbox) release(isOpen func(network, key string) bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	keep := o.items[:0]
	changed := false
	for _, it := range o.items {
		if !it.held {
			keep = append(keep, it)
			continue
		}
		changed = true
		if it.Op == OpPost && isOpen != nil && !isOpen(it.Network, it.Key) {
			if !it.Update {
				o.mark(it.Network, it.Key, "")
			}
			continue
		}
		it.held = false
		keep = append(keep, it)
	}
	clear(o.items[len(keep):])
	o.items = keep
	if changed {
		o.changedLocked()
		o.kick()
	}
}

// held reports whether items a previous run left still wait for release.
func (o *outbox) holding() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.ContainsFunc(o.items, func(it *Item) bool { return it.held })
}

// next hands the sender the first item that may go, dropping expired ones
// on the way, or nil and how long to wait for the first one that waits
// (0: nothing waits). Items go in order: one that waits for its retry
// holds back the rest, since they go to the same server.
func (o *outbox) next() (*Item, time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	o.expireLocked(now)
	for _, it := range o.items {
		switch {
		case it.held:
			continue
		case it.sending:
			return nil, 0
		case it.NextAt.After(now):
			return nil, it.NextAt.Sub(now)
		}
		it.sending = true
		it.Attempts++
		return it, 0
	}
	return nil, 0
}

// expireLocked drops items past their time. o.mu must be held.
func (o *outbox) expireLocked(now time.Time) {
	keep := o.items[:0]
	for _, it := range o.items {
		if it.sending || now.Before(it.ExpiresAt) {
			keep = append(keep, it)
			continue
		}
		o.dropLocked(it)
	}
	if len(keep) != len(o.items) {
		clear(o.items[len(keep):])
		o.items = keep
		o.changedLocked()
	}
}

// dropLocked counts an item given up on and tells the ledger a post never
// went. o.mu must be held; the caller removes it.
func (o *outbox) dropLocked(it *Item) {
	o.dropped++
	if it.Op == OpPost && !it.Update && o.pendingPostAfter(it) < 0 {
		o.mark(it.Network, it.Key, "")
	}
}

// pendingPostAfter is another post for it's key, or -1. o.mu must be held.
func (o *outbox) pendingPostAfter(it *Item) int {
	for i, x := range o.items {
		if x != it && x.Op == OpPost && x.Network == it.Network && x.Key == it.Key {
			return i
		}
	}
	return -1
}

// limitLocked keeps at most MaxOutbox items: notes (info alerts and
// messages) go first, oldest first, then the oldest items. o.mu must be
// held.
func (o *outbox) limitLocked() {
	for len(o.items) > MaxOutbox {
		i := slices.IndexFunc(o.items, func(it *Item) bool {
			return !it.sending && (it.Op == OpMessage || (it.Op == OpPost && it.Alert.Severity == protocol.SeverityInfo))
		})
		if i < 0 {
			i = slices.IndexFunc(o.items, func(it *Item) bool { return !it.sending })
		}
		if i < 0 {
			return
		}
		it := o.items[i]
		o.items = slices.Delete(o.items, i, i+1)
		o.dropLocked(it)
	}
}

// Outcomes of a send.
const (
	sentOK    = iota // delivered
	sentRetry        // try again after the wait
	sentDrop         // give up on it (counted as dropped)
	sentGone         // nothing to do any more (not counted)
	sentKeep         // leave it as it is: sending has stopped
)

// done records what became of the item the sender had.
func (o *outbox) done(it *Item, outcome int, wait time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	it.sending = false
	i := slices.Index(o.items, it)
	switch outcome {
	case sentRetry:
		it.NextAt = o.now().Add(wait)
		o.changedLocked()
		return
	case sentKeep:
		it.Attempts--
		return
	}
	if i >= 0 {
		o.items = slices.Delete(o.items, i, i+1)
	}
	o.changedLocked()
	switch outcome {
	case sentOK:
		o.lastSent = o.now()
		switch it.Op {
		case OpPost:
			o.mark(it.Network, it.Key, problems.ChatPosted)
			// A newer post for the key only updates this card now.
			for _, x := range o.items {
				if x.Op == OpPost && x.Network == it.Network && x.Key == it.Key {
					x.Update = true
				}
			}
		case OpResolve:
			o.closedLocked(it)
		}
	case sentGone:
		if it.Op == OpResolve {
			o.closedLocked(it)
		} else if it.Op == OpPost && !it.Update && o.pendingPostAfter(it) < 0 {
			o.mark(it.Network, it.Key, "")
		}
	case sentDrop:
		o.dropLocked(it)
		if it.Op == OpResolve {
			o.closedLocked(it)
		}
	}
}

// closedLocked records that the card of a resolve is gone: a post that
// still waits for its key makes a new card. o.mu must be held.
func (o *outbox) closedLocked(it *Item) {
	state := ""
	for _, x := range o.items {
		if x.Op == OpPost && x.Network == it.Network && x.Key == it.Key {
			x.Update = false
			state = problems.ChatPending
		}
	}
	o.mark(it.Network, it.Key, state)
}

// stopPosts drops every post and message that waits (Send alerts to team
// chat was switched off); resolves stay, so open cards still close.
func (o *outbox) stopPosts() {
	o.mu.Lock()
	defer o.mu.Unlock()
	keep := o.items[:0]
	for _, it := range o.items {
		if it.sending || it.Op == OpResolve {
			keep = append(keep, it)
			continue
		}
		if it.Op == OpPost && !it.Update {
			o.mark(it.Network, it.Key, "")
		}
	}
	if len(keep) != len(o.items) {
		clear(o.items[len(keep):])
		o.items = keep
		o.changedLocked()
	}
}

// clearAll forgets everything (Disconnect); the caller removes the file.
func (o *outbox) clearAll() {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, it := range o.items {
		if it.Op == OpPost && !it.Update {
			o.mark(it.Network, it.Key, "")
		}
	}
	o.items, o.dirty = nil, false
	if o.timer != nil {
		o.timer.Stop()
		o.timer = nil
	}
}

// resetWaits lets every item go now (Try now).
func (o *outbox) resetWaits() {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	for _, it := range o.items {
		if it.NextAt.After(now) {
			it.NextAt = now
		}
	}
	o.kick()
}

// stats is what the page shows: items waiting (also those a previous run
// left), items given up on, and when the last one was delivered.
func (o *outbox) stats() (waiting, dropped int, lastSent time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.items), o.dropped, o.lastSent
}

// sendable reports whether an item may go now or soon: one not held,
// waiting at most until deadline.
func (o *outbox) sendable(deadline time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.ContainsFunc(o.items, func(it *Item) bool { return !it.held && (it.sending || !it.NextAt.After(deadline)) })
}

// changedLocked schedules a save. o.mu must be held.
func (o *outbox) changedLocked() {
	o.dirty = true
	if o.files != nil && o.timer == nil {
		o.timer = time.AfterFunc(o.saveDelay, func() { _ = o.save() })
	}
}

// save writes chat-outbox.json now if it changed, or removes it when the
// outbox is empty.
func (o *outbox) save() error {
	if o.files == nil {
		return nil
	}
	o.wmu.Lock()
	defer o.wmu.Unlock()
	o.mu.Lock()
	if o.timer != nil {
		o.timer.Stop()
		o.timer = nil
	}
	if !o.dirty {
		o.mu.Unlock()
		return nil
	}
	f := outboxFile{SchemaVersion: outboxSchema, Items: make([]*Item, 0, len(o.items))}
	for _, it := range o.items {
		c := *it
		f.Items = append(f.Items, &c)
	}
	o.dirty = false
	o.mu.Unlock()
	var err error
	if len(f.Items) == 0 {
		err = o.files.RemoveFile(store.FileChatOutbox)
	} else {
		var data []byte
		if data, err = json.MarshalIndent(f, "", "  "); err == nil {
			err = o.files.WriteFile(store.FileChatOutbox, data)
		}
	}
	if err != nil {
		o.mu.Lock()
		o.dirty = true
		o.mu.Unlock()
	}
	return err
}
