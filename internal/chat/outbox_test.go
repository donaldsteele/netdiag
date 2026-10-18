package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"netdiag/internal/bubbles/protocol"
	"netdiag/internal/model"
	"netdiag/internal/problems"
	"netdiag/internal/store"
)

// testOutbox is an outbox on a clock the test moves, recording what it
// tells the ledger.
func testOutbox(files DataFiles) (*outbox, *time.Time, map[string]string) {
	now := t0
	marks := map[string]string{}
	o := newOutbox(files, func() time.Time { return now }, func(network, key, state string) { marks[network+" "+key] = state })
	o.saveDelay = time.Hour // the test saves
	return o, &now, marks
}

func post(title string) protocol.AlertInput {
	return protocol.AlertInput{Severity: protocol.SeverityWarning, Title: title, DedupeKey: "k"}
}

// Coalescing per key (4.9): a newer post replaces one that waits; a
// resolve after a post never delivered drops both (D17); a resolve after
// a post that only updates an open card drops the post and closes the
// card; a newer resolve replaces the note of one that waits.
func TestOutboxCoalescing(t *testing.T) {
	o, _, marks := testOutbox(nil)
	o.Post("n", "a", post("one"), false)
	o.Post("n", "a", post("two"), false)
	if len(o.items) != 1 || o.items[0].Alert.Title != "two" || marks["n a"] != problems.ChatPending {
		t.Fatalf("items %+v marks %v", o.items, marks)
	}
	o.Resolve("n", "a", "Fixed")
	if len(o.items) != 0 || marks["n a"] != "" {
		t.Fatalf("D17: items %+v marks %v", o.items, marks)
	}

	o.Post("n", "b", post("update"), true)
	o.Resolve("n", "b", "Fixed at 10:42.")
	if len(o.items) != 1 || o.items[0].Op != OpResolve || o.items[0].Text != "Fixed at 10:42." {
		t.Fatalf("update then resolve: %+v", o.items)
	}
	o.Resolve("n", "b", "Fixed at 10:43.")
	if len(o.items) != 1 || o.items[0].Text != "Fixed at 10:43." {
		t.Fatalf("two resolves: %+v", o.items)
	}

	// A move (resolve, then post) that is fixed before it went: the card
	// is closed with the fixed note and not posted again.
	o.Resolve("n", "c", "Moved to #ops.")
	o.Post("n", "c", post("moved"), false)
	o.Resolve("n", "c", "Fixed at 10:44.")
	var ops []string
	for _, it := range o.items {
		if it.Key == "c" {
			ops = append(ops, it.Op+" "+it.Text)
		}
	}
	if len(ops) != 1 || ops[0] != "resolve Fixed at 10:44." {
		t.Fatalf("move then fixed: %v", ops)
	}

	// Two networks keep their own problems under one key.
	o.Post("n1", "d", post("one"), false)
	o.Post("n2", "d", post("two"), false)
	o.Resolve("n1", "d", "Fixed")
	if i := o.pendingPost("n2", "d"); i < 0 || o.pendingPost("n1", "d") >= 0 {
		t.Fatalf("networks mixed: %+v", o.items)
	}
}

// What waits too long is dropped: posts after 60 minutes, resolves after
// 7 days; at most 500 items, notes first.
func TestOutboxTTLAndLimit(t *testing.T) {
	o, now, marks := testOutbox(nil)
	o.Post("n", "a", post("a"), false)
	o.Resolve("n", "b", "Fixed")
	*now = now.Add(PostTTL + time.Second)
	it, _ := o.next()
	if it == nil || it.Op != OpResolve || marks["n a"] != "" || o.dropped != 1 {
		t.Fatalf("after an hour: %+v marks %v dropped %d", it, marks, o.dropped)
	}
	o.done(it, sentRetry, time.Second)
	*now = now.Add(ResolveTTL)
	if it, _ := o.next(); it != nil || len(o.items) != 0 || o.dropped != 2 {
		t.Fatalf("after 7 days: %+v", o.items)
	}

	o2, _, _ := testOutbox(nil)
	o2.Message("n", "connected")
	info := post("note")
	info.Severity = protocol.SeverityInfo
	o2.Post("n", "info", info, false)
	for i := range MaxOutbox {
		o2.Post("n", fmt.Sprint("k", i), post("w"), false)
	}
	if len(o2.items) != MaxOutbox || o2.items[0].Key != "k0" || o2.dropped != 2 {
		t.Fatalf("limit: %d items, first %+v, dropped %d", len(o2.items), o2.items[0], o2.dropped)
	}
}

// Items go in order, and one waiting for its retry holds back the rest.
func TestOutboxOrderAndRetry(t *testing.T) {
	o, now, marks := testOutbox(nil)
	o.Post("n", "a", post("a"), false)
	o.Post("n", "b", post("b"), false)
	it, _ := o.next()
	if it.Key != "a" {
		t.Fatalf("first %+v", it)
	}
	if again, _ := o.next(); again != nil {
		t.Fatal("two at once")
	}
	o.done(it, sentRetry, 10*time.Second)
	if x, wait := o.next(); x != nil || wait != 10*time.Second {
		t.Fatalf("during the wait: %+v %v", x, wait)
	}
	*now = now.Add(10 * time.Second)
	it, _ = o.next()
	if it.Key != "a" || it.Attempts != 2 {
		t.Fatalf("retry %+v", it)
	}
	o.done(it, sentOK, 0)
	if marks["n a"] != problems.ChatPosted {
		t.Fatalf("marks %v", marks)
	}
	// A resolve delivered while a newer post waits: the post makes a new
	// card, so the problem is pending again.
	o.Resolve("n", "a", "Moved")
	o.Post("n", "a", post("again"), false)
	it, _ = o.next() // b
	o.done(it, sentOK, 0)
	it, _ = o.next() // the resolve of a
	if it.Op != OpResolve {
		t.Fatalf("got %+v", it)
	}
	o.done(it, sentOK, 0)
	if marks["n a"] != problems.ChatPending || o.items[0].Update {
		t.Fatalf("marks %v items %+v", marks, o.items)
	}
}

// A previous run's items wait until release. The first scan whose first
// pass is reconciled lets the resolves and messages go; the posts go only
// once a live scan of their own network did (only then are alerts sent,
// and the ledger has checked that network again): one whose problem is not
// open any more is dropped then, the rest go.
func TestOutboxHeldUntilRelease(t *testing.T) {
	st, dir := newStore(t)
	o, _, _ := testOutbox(st)
	o.Post("n", "open", post("open"), false)
	o.Post("n", "fixed", post("fixed"), false)
	o.Resolve("n", "card", "Fixed")
	o.Post("office", "printer", post("office"), false)
	if err := o.save(); err != nil {
		t.Fatal(err)
	}
	o2, _, marks := testOutbox(st)
	o2.isOpen = func(network, key string) bool { return key == "open" || key == "printer" }
	if err := o2.load(); err != nil {
		t.Fatal(err)
	}
	if it, _ := o2.next(); it != nil || !o2.holding() {
		t.Fatalf("not held: %+v", it)
	}
	ops := func() string {
		var out []string
		for _, it := range o2.items {
			s := it.Op + " " + it.Key
			if it.held {
				s += " (held)"
			}
			out = append(out, s)
		}
		return strings.Join(out, ",")
	}
	// A one-shot scan of n: the resolve goes, the posts wait.
	o2.release("n", false)
	if got := ops(); got != "post open (held),post fixed (held),resolve card,post printer (held)" {
		t.Fatalf("after a one-shot scan: %s", got)
	}
	// A live scan of n: n's posts go or are dropped; the office's waits.
	o2.release("n", true)
	if state, ok := marks["n fixed"]; ops() != "post open,resolve card,post printer (held)" || !ok || state != "" || !o2.holding() {
		t.Fatalf("after a live scan of n: %s marks %v", ops(), marks)
	}
	o2.release("office", true)
	if got := ops(); got != "post open,resolve card,post printer" || o2.holding() {
		t.Fatalf("after a live scan of the office: %s", got)
	}
	// New ids follow the loaded ones.
	o2.Message("n", "hi")
	if o2.items[len(o2.items)-1].ID != "5" {
		t.Fatalf("id %s", o2.items[len(o2.items)-1].ID)
	}

	// An unreadable file is set aside.
	if err := os.WriteFile(filepath.Join(dir, store.FileChatOutbox), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	o3, _, _ := testOutbox(st)
	if err := o3.load(); err == nil || !strings.Contains(err.Error(), ".bad-") || len(o3.items) != 0 {
		t.Fatalf("unreadable: %v %+v", err, o3.items)
	}
	// An empty outbox removes the file.
	o3.dirty = true
	if err := o3.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, store.FileChatOutbox)); !os.IsNotExist(err) {
		t.Fatalf("file %v", err)
	}
}

// D17 with the post on its way: the problem is fixed while the sender has
// its post. When that post does not get through, neither it nor its
// resolve goes; when it does, the resolve closes the card; when netdiag is
// closing (the post may have reached the server), both stay for the next
// run.
func TestOutboxFixedWhilePosting(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome int
		want    string // the ops left, then the ledger's state
	}{
		{"fails", sentRetry, " "},
		{"refused", sentDrop, " "},
		{"stopped", sentKeep, " "},
		{"gets through", sentOK, "resolve posted"},
		{"closing", sentClosing, "post,resolve pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, _, marks := testOutbox(nil)
			o.Post("n", "a", post("a"), false)
			it, _ := o.next()
			o.Resolve("n", "a", "Fixed at 10:42.")
			if len(o.items) != 2 || marks["n a"] != problems.ChatPending {
				t.Fatalf("while on its way: %+v marks %v", o.items, marks)
			}
			o.Resolve("n", "a", "Fixed at 10:43.") // a second OK only changes the note
			if len(o.items) != 2 || o.items[1].Text != "Fixed at 10:43." {
				t.Fatalf("second OK: %+v", o.items)
			}
			o.done(it, tc.outcome, time.Second)
			var ops []string
			for _, x := range o.items {
				ops = append(ops, x.Op)
			}
			if got := strings.Join(ops, ",") + " " + marks["n a"]; got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("fixed and found again while on its way", func(t *testing.T) {
		o, _, marks := testOutbox(nil)
		o.Post("n", "a", post("a"), false)
		it, _ := o.next()
		o.Resolve("n", "a", "Fixed")
		o.Post("n", "a", post("again"), false)
		o.done(it, sentRetry, time.Second)
		if len(o.items) != 1 || o.items[0].Alert.Title != "again" || o.items[0].Update || marks["n a"] != problems.ChatPending {
			t.Fatalf("items %+v marks %v", o.items, marks)
		}
	})
	t.Run("an update on its way", func(t *testing.T) {
		o, _, _ := testOutbox(nil)
		o.Post("n", "a", post("a"), true)
		it, _ := o.next()
		o.Resolve("n", "a", "Fixed")
		o.done(it, sentRetry, time.Second)
		if len(o.items) != 2 {
			t.Fatalf("the open card must still close: %+v", o.items)
		}
	})
}

// A new connection is a new app in bubbles with no card of the old one's:
// a post still waiting makes a new card (its problem is pending, and an OK
// before it goes drops both, D17); a problem whose card was posted has no
// chat state any more.
func TestOutboxNewApp(t *testing.T) {
	o, _, marks := testOutbox(nil)
	o.Post("n", "waits", post("update"), true) // updates a card of the old app
	o.newApp([]problems.Entry{{Network: "n", Key: "waits", Chat: problems.ChatPosted}, {Network: "n", Key: "card", Chat: problems.ChatPosted}})
	if marks["n waits"] != problems.ChatPending || marks["n card"] != "" || o.items[0].Update {
		t.Fatalf("marks %v items %+v", marks, o.items)
	}
	o.Resolve("n", "waits", "Fixed")
	if len(o.items) != 0 || marks["n waits"] != "" {
		t.Fatalf("D17 after the new connection: %+v marks %v", o.items, marks)
	}
	// A post merged with one that waits marks its problem pending again.
	o.Post("n", "b", post("one"), false)
	marks["n b"] = ""
	o.Post("n", "b", post("two"), false)
	if marks["n b"] != problems.ChatPending {
		t.Fatalf("coalesced post: %v", marks)
	}
}

// Every message gets a nonce of its own, also across runs (bubbles keeps
// them for good: one seen before is answered with its old message, or
// refused in another channel), and keeps it in the file, so a resend after
// a restart is not posted twice.
func TestOutboxNonces(t *testing.T) {
	st, _ := newStore(t)
	o, _, _ := testOutbox(st)
	o.Message("n", "one")
	o.Message("n", "two")
	a, b := o.items[0].Nonce, o.items[1].Nonce
	if a == b || !strings.HasPrefix(a, "netdiag-") || len(a) != 40 {
		t.Fatalf("nonces %q %q", a, b)
	}
	if err := o.save(); err != nil {
		t.Fatal(err)
	}
	o2, _, _ := testOutbox(st)
	if err := o2.load(); err != nil {
		t.Fatal(err)
	}
	if o2.items[0].Nonce != a || o2.items[1].Nonce != b {
		t.Fatalf("not kept: %+v", o2.items)
	}
	o3, _, _ := testOutbox(nil) // a later run, its first message
	o3.Message("n", "one")
	if n := o3.items[0].Nonce; n == a || n == b || o3.items[0].ID != o.items[0].ID {
		t.Fatalf("a later run's nonce %q (ids %s %s)", n, o3.items[0].ID, o.items[0].ID)
	}
}

// lockedFiles is a data folder whose chat-outbox.json cannot be read while
// locked is set (a virus scanner or a backup holding it).
type lockedFiles struct {
	*store.Store
	locked atomic.Bool
}

func (f *lockedFiles) ReadFile(name string) ([]byte, error) {
	if name == store.FileChatOutbox && f.locked.Load() {
		return nil, errors.New("the file is used by another process")
	}
	return f.Store.ReadFile(name)
}

// A chat-outbox.json that cannot be read at start is never written over:
// a save while it still cannot be read writes nothing and says why; once it
// can be read, its items are added (held, as at start) and saved with the
// new ones.
func TestOutboxUnreadFileKept(t *testing.T) {
	s, dir := newStore(t)
	o, _, _ := testOutbox(s)
	o.Resolve("n", "card", "Fixed")
	if err := o.save(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, store.FileChatOutbox))
	st := &lockedFiles{Store: s}
	st.locked.Store(true)
	o2, _, _ := testOutbox(st)
	if err := o2.load(); err == nil {
		t.Fatal("no error for a file that cannot be read")
	}
	o2.Post("n", "new", post("new"), false)
	if err := o2.save(); err == nil {
		t.Fatal("saved over a file it could not read")
	}
	if after, _ := os.ReadFile(filepath.Join(dir, store.FileChatOutbox)); string(after) != string(before) {
		t.Fatalf("the file changed: %s", after)
	}
	st.locked.Store(false)
	if err := o2.save(); err != nil {
		t.Fatal(err)
	}
	var f outboxFile
	b, _ := os.ReadFile(filepath.Join(dir, store.FileChatOutbox))
	if err := json.Unmarshal(b, &f); err != nil || len(f.Items) != 2 || f.Items[0].Key != "card" || f.Items[1].Key != "new" ||
		f.Items[0].ID == f.Items[1].ID {
		t.Fatalf("saved %s", b)
	}
	if !o2.holding() || !o2.items[0].held || o2.items[1].held {
		t.Fatalf("held %+v", o2.items)
	}
}

// The alert keeps within bubbles' limits, links only to the device's own
// page, and never re-notifies (4.9 "Notice to alert").
func TestAlertContentLimits(t *testing.T) {
	long := strings.Repeat("é", 300)
	n := model.Notice{Trigger: model.TriggerSupplyLow, Severity: "warn", IP: "192.168.1.50", Name: long, Title: "Supply low",
		Message: strings.Repeat("m", 5000), At: "2026-10-17T10:30:00Z", Key: "supplyLow|AA|x", Detail: long,
		Advice: &model.Advice{Steps: []string{"one", "two", "three", "four"}}}
	h := &model.Host{IP: "192.168.1.50", Vendor: long, Web: []model.WebService{{URL: "http://192.168.1.99/"}, {URL: "ftp://192.168.1.50/"}}}
	in := alertFor(n, h)
	if utf8.RuneCountInString(in.Title) > protocol.MaxAlertTitle || utf8.RuneCountInString(in.Text) > protocol.MaxAlertText ||
		len(in.Fields) > protocol.MaxAlertFields || in.Link != nil || in.RenotifyAfterS == nil || *in.RenotifyAfterS != 0 ||
		in.OccurredAt == nil || *in.OccurredAt != n.At || in.Severity != protocol.SeverityWarning {
		t.Fatalf("alert %+v", in)
	}
	for _, f := range in.Fields {
		if utf8.RuneCountInString(f.Value) > protocol.MaxAlertFieldValue || f.Name == "" {
			t.Fatalf("field %+v", f)
		}
	}
	short := n
	short.Message, short.OpenedAt = "Black toner is at 12%.", "2026-10-17T09:00:00Z"
	text := alertFor(short, nil).Text
	if text != "Black toner is at 12%.\n\nWhat to try:\n1. one\n2. two\n3. three" {
		t.Fatalf("text %q", text)
	}
	if at := alertFor(short, nil).OccurredAt; *at != short.OpenedAt {
		t.Fatalf("occurred_at %s", *at)
	}
	h.Web = append(h.Web, model.WebService{URL: "https://192.168.1.50:8443/"})
	if l := alertFor(n, h).Link; l == nil || *l != "https://192.168.1.50:8443/" {
		t.Fatalf("link %v", l)
	}

	hc := model.Notice{Trigger: model.TriggerHealthFailed, Severity: "critical", Title: "Health check failed",
		Message: "Router check failed.", Key: "healthFailed||gateway"}
	if in := alertFor(hc, nil); in.Title != "Health check failed: Router" || in.Severity != protocol.SeverityCritical {
		t.Fatalf("health %+v", in)
	}
	// Built from the ledger's entry (no notice): the check's label, and
	// the catalogue's steps.
	e := problems.Entry{Key: "healthFailed||webserver", Trigger: model.TriggerHealthFailed, Severity: "warn", Title: "Health check failed",
		Network: "n", OpenedAt: "2026-10-17T09:00:00Z"}
	in = alertForEntry(e, nil)
	check := ""
	for _, f := range in.Fields {
		if f.Name == "Check" {
			check = f.Value
		}
	}
	// The steps say where to look, since chat readers can't open Health.
	if in.Title != "Health check failed: Web server" || check != "Web server" ||
		in.Text != "Web server check failed.\n\nWhat to try:\n1. In netdiag, press Open Health to see what to try." || *in.OccurredAt != e.OpenedAt {
		t.Fatalf("health from the ledger %+v", in)
	}
	ce := problems.Entry{Key: "ipConflict|mac_flap|192.168.1.50", Trigger: model.TriggerIPConflict, Severity: "warn",
		Title: "Network conflict", IP: "192.168.1.50", Network: "n"}
	if in = alertForEntry(ce, nil); in.Text != "Two devices may be trying to use the same address, so they can work badly.\n\n"+
		"What to try:\n1. In netdiag, press Show device to see the two devices." {
		t.Fatalf("conflict from the ledger %q", in.Text)
	}
	pe := problems.Entry{Key: "printerOffline|ip:192.168.1.50|", Trigger: model.TriggerPrinterOffline, Severity: "warn",
		Title: "Printer offline", IP: "192.168.1.50", Name: "Front Desk HP", Network: "n"}
	in = alertForEntry(pe, &model.Host{IP: "192.168.1.50", Vendor: "HP", Web: []model.WebService{{URL: "http://192.168.1.50/"}}})
	if in.Title != "Printer offline: Front Desk HP (192.168.1.50)" || !strings.HasPrefix(in.Text, "The printer stopped answering.\n\nWhat to try:\n1. ") ||
		in.Link == nil || *in.Link != "http://192.168.1.50/" {
		t.Fatalf("printer from the ledger %+v", in)
	}
	// The fields are words for people: "MAC address", and no conflict id.
	cf := model.Notice{Trigger: model.TriggerIPConflict, Severity: "warn", IP: "192.168.1.50", MAC: "AA:BB:CC:00:11:22",
		Title: "Network conflict", Key: "ipConflict|mac_flap|192.168.1.50"}
	in = alertFor(cf, nil)
	names := []string{}
	for _, f := range in.Fields {
		names = append(names, f.Name)
	}
	if in.Title != "Network conflict: 192.168.1.50" || strings.Join(names, ",") != "Address,MAC address" || in.DedupeKey != cf.Key {
		t.Fatalf("conflict %+v", in)
	}
	// From a day on, days (a long hold read "193 h 52 min").
	for d, want := range map[time.Duration]string{30 * time.Second: "less than a minute", 12 * time.Minute: "12 min",
		125 * time.Minute: "2 h 5 min", 120 * time.Minute: "2 h", 180*time.Minute + 30*time.Second: "3 h",
		23*time.Hour + 59*time.Minute: "23 h 59 min", 24 * time.Hour: "1 day", 25*time.Hour + 59*time.Minute: "1 day 1 h",
		193*time.Hour + 52*time.Minute: "8 days 1 h", 8 * 24 * time.Hour: "8 days"} {
		if got := downFor(d); got != want {
			t.Errorf("downFor(%v) = %q, want %q", d, got, want)
		}
	}
	// A mac_flap alert names both devices' MACs (the notice's Detail), not
	// only the MAC answering now, so whoever reserves an address for one
	// of them has them; it stays within the field limit.
	cf.Detail, cf.Message = "24:6F:28:AA:BB:CC, 3C:52:82:11:22:33", "Two devices may be using 192.168.1.50."
	in = alertFor(cf, &model.Host{IP: "192.168.1.50", MAC: "3C:52:82:11:22:33", Vendor: "Hewlett Packard"})
	field := func(in protocol.AlertInput, name string) string {
		for _, f := range in.Fields {
			if f.Name == name {
				return f.Value
			}
		}
		return ""
	}
	if field(in, "MAC addresses") != cf.Detail || field(in, "MAC address") != "" || field(in, "Maker") != "Hewlett Packard" ||
		len(in.Fields) > protocol.MaxAlertFields || !strings.HasPrefix(in.Text, "Two devices may be using 192.168.1.50.") {
		t.Fatalf("conflict MACs %+v", in)
	}
	// One MAC (one device on several addresses) is the MAC address field.
	multi := model.Notice{Trigger: model.TriggerIPConflict, Severity: "info", IP: "192.168.1.30", MAC: "00:1B:A9:AA:00:30",
		Title: "One device uses several addresses", Key: "ipConflict|mac_multi_ip|00:1B:A9:AA:00:30", Detail: "00:1B:A9:AA:00:30"}
	if in := alertFor(multi, nil); field(in, "MAC address") != "00:1B:A9:AA:00:30" || field(in, "MAC addresses") != "" ||
		in.Severity != protocol.SeverityInfo || in.Title != "One device uses several addresses: 192.168.1.30" {
		t.Fatalf("one MAC %+v", in)
	}
	if !sends("critical", "") || sends("info", "warning") || !sends("warn", "warning") || sends("warn", "critical") {
		t.Fatal("sends")
	}
}

// The resolve note says how long the problem lasted, in words that fit
// every problem: low toner, an open cover or an IP conflict was never
// "down" (v0.6-D54, which replaces D52's "was down" and "after").
func TestFixedTextWording(t *testing.T) {
	from := time.Date(2026, 10, 17, 8, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return from.Add(d).Format(time.RFC3339) }
	clock := func(d time.Duration) string { return from.Add(d).Local().Format("15:04") }
	for _, tc := range []struct {
		trigger, msg string
		d            time.Duration
		want         string
	}{
		{model.TriggerPrinterOffline, "Front Desk HP (192.168.1.50) answers again.", 12 * time.Minute, " (lasted 12 min). Front Desk HP (192.168.1.50) answers again."},
		{model.TriggerDeviceOffline, "", 2 * time.Hour, " (lasted 2 h)."},
		{model.TriggerHealthFailed, "Web server check is OK again.", 30 * time.Second, " (lasted less than a minute). Web server check is OK again."},
		{model.TriggerSupplyLow, "Front Desk HP (192.168.1.50) no longer reports the low supply.", 3 * time.Hour, " (lasted 3 h). Front Desk HP (192.168.1.50) no longer reports the low supply."},
		{model.TriggerIPConflict, "The conflict is gone.", 12 * time.Minute, " (lasted 12 min). The conflict is gone."},
		{model.TriggerPrinterAlert, "ZD621: Cover open is gone.", 65 * time.Minute, " (lasted 1 h 5 min). ZD621: Cover open is gone."},
	} {
		n := model.Notice{Trigger: tc.trigger, OK: true, OpenedAt: at(0), At: at(tc.d), Message: tc.msg}
		want := "Fixed at " + clock(tc.d) + tc.want
		if got := fixedText(n, from); got != want {
			t.Errorf("%s: %q, want %q", tc.trigger, got, want)
		}
		if got := fixedText(n, from); strings.Contains(got, " 0 min") || strings.Contains(got, "was down") || strings.Contains(got, "(after ") {
			t.Errorf("%s: %q says 0 min, was down or after", tc.trigger, got)
		}
	}
}
