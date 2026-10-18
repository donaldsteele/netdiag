package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// A previous run's items wait until release; a post whose problem is not
// open any more is dropped then, the rest go.
func TestOutboxHeldUntilRelease(t *testing.T) {
	st, dir := newStore(t)
	o, _, _ := testOutbox(st)
	o.Post("n", "open", post("open"), false)
	o.Post("n", "fixed", post("fixed"), false)
	o.Resolve("n", "card", "Fixed")
	if err := o.save(); err != nil {
		t.Fatal(err)
	}
	o2, _, marks := testOutbox(st)
	if err := o2.load(); err != nil {
		t.Fatal(err)
	}
	if it, _ := o2.next(); it != nil || !o2.holding() {
		t.Fatalf("not held: %+v", it)
	}
	o2.release(func(network, key string) bool { return key == "open" })
	var keys []string
	for _, it := range o2.items {
		keys = append(keys, it.Op+" "+it.Key)
	}
	if strings.Join(keys, ",") != "post open,resolve card" || marks["n fixed"] != "" || o2.holding() {
		t.Fatalf("after release %v marks %v", keys, marks)
	}
	// New ids follow the loaded ones.
	o2.Message("n", "hi")
	if o2.items[len(o2.items)-1].ID != "4" {
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

// The alert keeps within bubbles' limits, links only to the device's own
// page, and never re-notifies (4.9 "Notice to alert").
func TestAlertContentLimits(t *testing.T) {
	long := strings.Repeat("é", 300)
	n := model.Notice{Trigger: model.TriggerSupplyLow, Severity: "warn", IP: "192.168.1.50", Name: long, Title: "Supply low",
		Message: strings.Repeat("m", 5000), At: "2026-10-17T10:30:00Z", Key: "supplyLow|AA|x", Detail: long,
		Advice: &model.Advice{Steps: []string{"one", "two", "three", "four"}}}
	h := &model.Host{IP: "192.168.1.50", Vendor: long, Web: []model.WebService{{URL: "http://192.168.1.99/"}, {URL: "ftp://192.168.1.50/"}}}
	in := alertFor(n, h, "")
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
	text := alertFor(short, nil, "").Text
	if text != "Black toner is at 12%.\n\nWhat to try:\n1. one\n2. two\n3. three" {
		t.Fatalf("text %q", text)
	}
	if at := alertFor(short, nil, "").OccurredAt; *at != short.OpenedAt {
		t.Fatalf("occurred_at %s", *at)
	}
	h.Web = append(h.Web, model.WebService{URL: "https://192.168.1.50:8443/"})
	if l := alertFor(n, h, "").Link; l == nil || *l != "https://192.168.1.50:8443/" {
		t.Fatalf("link %v", l)
	}

	hc := model.Notice{Trigger: model.TriggerHealthFailed, Severity: "critical", Title: "Health check failed",
		Message: "Router check failed.", Key: "healthFailed||gateway"}
	if in := alertFor(hc, nil, ""); in.Title != "Health check failed: Router" || in.Severity != protocol.SeverityCritical {
		t.Fatalf("health %+v", in)
	}
	cf := model.Notice{Trigger: model.TriggerIPConflict, Severity: "warn", IP: "192.168.1.50", Title: "Network conflict", Key: "ipConflict|mac_flap|192.168.1.50"}
	in = alertFor(cf, nil, "c-123")
	if in.Title != "Network conflict: 192.168.1.50" || in.Fields[len(in.Fields)-1] != (protocol.AlertField{Name: "Conflict id", Value: "c-123", Short: true}) {
		t.Fatalf("conflict %+v", in)
	}
	if got := downFor(125 * time.Minute); got != "2 h 5 min" {
		t.Fatal(got)
	}
	if !sends("critical", "") || sends("info", "warning") || !sends("warn", "warning") || sends("warn", "critical") {
		t.Fatal("sends")
	}
}
