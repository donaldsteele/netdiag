package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"netdiag/internal/arp"
	"netdiag/internal/bubbles/bubblestest"
	"netdiag/internal/bubbles/protocol"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/notify"
	"netdiag/internal/problems"
	"netdiag/internal/scan"
	"netdiag/internal/snmpprobe"
	"netdiag/internal/store"
)

// closedSNMP is the printer at 10.0.0.10 of coverSNMP with its cover
// closed: it answers with no alerts.
type closedSNMP struct{}

func (closedSNMP) Probe(_ context.Context, ip, _ string) (*snmpprobe.Result, error) {
	if ip != "10.0.0.10" {
		return nil, snmpprobe.ErrNoResponse
	}
	return &snmpprobe.Result{
		Snmp:    &model.SnmpInfo{Version: "v2c", SysDescr: "Zebra ZD621", SysName: "Front Desk", HrPrinterStatus: "idle"},
		Printer: &model.PrinterInfo{Model: "ZD621"},
	}, nil
}

// keyOrder is routerARP and an SNMP printer at 10.0.0.10 that make a first
// pass learn the printer's MAC (the ARP read) before its SNMP answer, or
// after it: its cover alert then opens under the MAC key, or under the
// "ip:" key (notify.Key). Each waits at most 5 s for the other.
type keyOrder struct {
	macFirst bool
	snmp     scan.SNMPProber
	arpRead  chan struct{}
	answered chan struct{}
	arpOnce  sync.Once
	ansOnce  sync.Once
}

func newKeyOrder(macFirst bool, snmp scan.SNMPProber) *keyOrder {
	return &keyOrder{macFirst: macFirst, snmp: snmp, arpRead: make(chan struct{}), answered: make(chan struct{})}
}

// after waits for ch, then a moment more, so the scan has applied what
// came before it.
func after(ctx context.Context, ch chan struct{}) {
	select {
	case <-ch:
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
	}
	time.Sleep(100 * time.Millisecond)
}

func (k *keyOrder) Table(ctx context.Context) ([]arp.Entry, error) {
	if !k.macFirst {
		after(ctx, k.answered)
	}
	k.arpOnce.Do(func() { close(k.arpRead) })
	return routerARP{}.Table(ctx)
}

func (k *keyOrder) Probe(ctx context.Context, ip, community string) (*snmpprobe.Result, error) {
	if ip == "10.0.0.10" && k.macFirst {
		after(ctx, k.arpRead)
	}
	r, err := k.snmp.Probe(ctx, ip, community)
	if ip == "10.0.0.10" {
		k.ansOnce.Do(func() { close(k.answered) })
	}
	return r, err
}

// runOn is one run of netdiag on the data folder dir, wired as `netdiag
// ui` wires it (one ledger and profile store for every scan, the window's
// team chat manager), its printer answering SNMP with snmp; edit changes
// the scan's dependencies. It returns what Quit needs and a channel that
// gets Serve's answer.
func runOn(t *testing.T, dir string, snmp scan.SNMPProber, edit ...func(*scan.Deps)) (*Server, *store.Store, chan error) {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := problems.OpenLedger(st)
	if err != nil {
		t.Fatal(err)
	}
	d := scan.Deps{
		Pinger: fakePinger{}, ARP: routerARP{}, Ports: fakePorts{}, Web: fakeWeb{},
		SNMP: snmp, DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{},
		Store: st, Settings: st, Ledger: ledger, Profiles: scan.NewProfiles(st),
	}
	for _, fn := range edit {
		fn(&d)
	}
	s := New(Config{Registry: scan.NewRegistry(scan.New(d)), Store: st, Health: fakeHealth{}, Gateway: func() (string, error) { return "10.0.0.1", nil },
		Interfaces:   func() ([]netinfo.Iface, error) { return []netinfo.Iface{{Name: "eth0", IP: "10.0.0.30"}}, nil },
		SessionToken: testToken, Ledger: ledger, Logf: t.Logf})
	if err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- s.Serve(context.Background()) }()
	return s, st, served
}

// sendAlerts switches Send alerts to team chat on at the default level, as
// the Settings dialog saves it, and waits for the connected message.
func sendAlerts(t *testing.T, s *Server, f *bubblestest.Server) {
	t.Helper()
	before := len(f.Messages())
	_, b := doj(t, "GET", s.URL()+"/api/settings", "", "")
	set := decode[map[string]any](t, b)
	set["chat"] = map[string]any{"enabled": true, "minSeverity": "warning"}
	sb, _ := json.Marshal(set)
	if r, b := doj(t, "PUT", s.URL()+"/api/settings", string(sb), "application/json"); r.StatusCode != 200 {
		t.Fatalf("save settings: %d %s", r.StatusCode, b)
	}
	waitFor(t, "the connected message", func() bool { return len(f.Messages()) == before+1 })
}

// watchRange is the live scan the restart tests start: the printer's
// 10.0.0.0/28, with no live cycle while a test runs, so whatever the scan
// says after a restart is its first pass's.
const watchRange = `{"cidr":"10.0.0.0/28","live":true,"liveIntervalSec":300}`

// watch starts watchRange and returns the scan once its first pass is
// reconciled.
func watch(t *testing.T, s *Server) *scan.Scan {
	t.Helper()
	r, b := doj(t, "POST", s.URL()+"/api/scans", watchRange, "application/json")
	if r.StatusCode != 202 {
		t.Fatalf("start live: %d %s", r.StatusCode, b)
	}
	sc, ok := s.cfg.Registry.Get(idOf(t, b))
	if !ok {
		t.Fatal("no scan")
	}
	select {
	case <-sc.Reconciled():
	case <-time.After(10 * time.Second):
		t.Fatal("the first pass did not end")
	}
	return sc
}

// openChat is the team chat state open-problems.json gives the open
// problem key, and false when key is not open there.
func openChat(t *testing.T, st *store.Store, key string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(st.Dir(), store.FileOpenProblems))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Open []problems.Entry `json:"open"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for _, e := range f.Open {
		if e.Key == key {
			return e.Chat, true
		}
	}
	return "", false
}

// openKeys is the keys of the ledger's open problems, sorted.
func openKeys(l *problems.Ledger) []string {
	open, _ := l.Snapshot()
	var out []string
	for _, e := range open {
		out = append(out, e.Key)
	}
	slices.Sort(out)
	return out
}

// The v0.6 restart acceptance end to end, with real scans and the bubbles
// v0.2.0 fake: the printer's cover opens while netdiag watches, and one
// card goes to chat; Quit saves the ledger and the outbox; the cover is
// closed while netdiag is closed; the next run's first pass finds it
// fixed and resolves the same card (the same dedupe_key) once, and posts
// nothing new. The next run has no live cycle in the test's time, so the
// resolve is the first pass's: the ledger has the fix once the pass is
// reconciled.
func TestFixedWhileClosedResolvesTheCardAfterRestart(t *testing.T) {
	f := bubblestest.New(t)
	dir := t.TempDir()

	s, st, served := runOn(t, dir, coverSNMP{})
	pairChat(t, s.chat, f)
	sendAlerts(t, s, f)
	watch(t, s)
	waitFor(t, "the cover alert's card", func() bool { return len(f.Posted()) == 1 })
	key := f.Posted()[0].DedupeKey
	if !strings.HasPrefix(key, model.TriggerPrinterAlert+"|") {
		t.Fatalf("posted %+v", f.Posted()[0])
	}
	waitFor(t, "the card's chat state", func() bool { return s.chat.Status().Waiting == 0 })
	quit(t, s, served)
	if c, ok := openChat(t, st, key); !ok || c != problems.ChatPosted {
		t.Fatalf("after Quit the ledger has %s open %v with chat %q", key, ok, c)
	}
	if _, err := os.Stat(filepath.Join(dir, store.FileChatOutbox)); !os.IsNotExist(err) {
		t.Fatalf("outbox left after the card went: %v", err)
	}
	posted, messages := len(f.Posted()), len(f.Messages())

	// The cover was closed while netdiag was closed.
	s, _, served = runOn(t, dir, closedSNMP{})
	defer quit(t, s, served)
	sc := watch(t, s)
	if open := openKeys(s.cfg.Ledger); len(open) != 0 {
		t.Fatalf("the first pass left %v open", open)
	}
	waitFor(t, "the card's resolve", func() bool { return len(f.Resolved()) == 1 })
	if r := f.Resolved()[0]; r.DedupeKey != key || !strings.HasPrefix(r.Text, "Fixed at ") {
		t.Fatalf("resolve %+v, want %s fixed", r, key)
	}
	if m, ok := f.Alert(key); !ok || m.Alert == nil || m.Alert.State != protocol.AlertResolved {
		t.Fatalf("the card is %+v", m)
	}
	// The first pass's burst and anything late have had their time.
	time.Sleep(2500 * time.Millisecond)
	if len(f.Resolved()) != 1 || len(f.Posted()) != posted || len(f.Messages()) != messages {
		t.Fatalf("after the restart: %d resolves, %d posts (was %d), messages %q (was %d)",
			len(f.Resolved()), len(f.Posted()), posted, f.Messages(), messages)
	}
	if v := s.svc.Problems(); len(v.Open) != 0 || len(v.Fixed) != 1 || v.Fixed[0].Key != key {
		t.Fatalf("the card after the restart: open %+v, fixed %+v", v.Open, v.Fixed)
	}
	if ls, ok := sc.LiveStatus(); !ok || ls.Cycle != 0 {
		t.Fatalf("a live cycle ran: %+v", ls)
	}
}

// The other half of the restart acceptance (v0.6 exit criterion 3): the
// cover is still open after the restart. The next run's first pass gives no
// notice, the ledger keeps the one problem under its key, and chat gets
// nothing new. That holds when the two runs opened the alert under the
// printer's two keys: one learned the MAC before the SNMP answer, the other
// after it.
func TestCoverStillOpenAcrossRestartPostsNothingNew(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after bool // macFirst in each run
		id            string
	}{
		{"ip: key before, MAC key after", false, true, "|ip:10.0.0.10|"},
		{"MAC key before, ip: key after", true, false, "|00:07:4D:00:00:10|"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := bubblestest.New(t)
			dir := t.TempDir()
			order := func(macFirst bool) func(*scan.Deps) {
				return func(d *scan.Deps) {
					k := newKeyOrder(macFirst, coverSNMP{})
					d.ARP, d.SNMP = k, k
				}
			}

			s, _, served := runOn(t, dir, nil, order(tc.before))
			pairChat(t, s.chat, f)
			sendAlerts(t, s, f)
			watch(t, s)
			waitFor(t, "the cover alert's card", func() bool { return len(f.Posted()) == 1 })
			key := f.Posted()[0].DedupeKey
			if !strings.HasPrefix(key, model.TriggerPrinterAlert+tc.id) {
				t.Fatalf("the first run posted %s, want its %s key", key, tc.id)
			}
			waitFor(t, "the card's chat state", func() bool { return s.chat.Status().Waiting == 0 })
			quit(t, s, served)
			posted, messages := len(f.Posted()), len(f.Messages())

			s, _, served = runOn(t, dir, nil, order(tc.after))
			defer quit(t, s, served)
			sc := watch(t, s)
			time.Sleep(2500 * time.Millisecond) // the burst and anything late
			if open := openKeys(s.cfg.Ledger); !slices.Equal(open, []string{key}) {
				t.Fatalf("the ledger has %v open, want only %s", open, key)
			}
			if c := s.cfg.Ledger.Chat(sc.Network(), key); c != problems.ChatPosted {
				t.Fatalf("the card's chat state is %q", c)
			}
			for _, n := range sc.Snapshot().Notices {
				if n.Trigger == model.TriggerPrinterAlert {
					t.Fatalf("the first pass gave a notice: %+v", n)
				}
			}
			if len(f.Posted()) != posted || len(f.Resolved()) != 0 || len(f.Messages()) != messages {
				t.Fatalf("after the restart: posted %+v (was %d), %d resolves, messages %q (was %d)",
					f.Posted(), posted, len(f.Resolved()), f.Messages(), messages)
			}
			if v := s.svc.Problems(); len(v.Open) != 1 || v.Open[0].Key != key || len(v.Alerted()) != 1 {
				t.Fatalf("the card after the restart: %+v", v.Open)
			}
		})
	}
}

// The burst at the end of a live scan's first pass posts the ledger's open
// problems the pass checked (v0.6-D41), with real scans: a printer in the
// watched range that was offline before the restart and still does not
// answer (the pass does not find it, so it stays open), and the cover
// alert the pass finds again. It leaves out the one the pass adopted
// unchecked: a printer outside the range, whose chat state stays "".
func TestBurstAfterRestartPostsWhatThePassChecked(t *testing.T) {
	f := bubblestest.New(t)
	dir := t.TempDir()
	network := scan.NewFingerprint("", "10.0.0.1", "00:11:22:33:44:01").ID
	at := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	door := model.Alert{Severity: "critical", Code: "hrPrinterDetectedErrorState.doorOpen", Description: "Door or cover open"}
	front := model.Host{IP: "10.0.0.10", MAC: "00:07:4D:00:00:10"}
	back := model.Host{IP: "10.0.0.12", MAC: "00:07:4D:00:00:12"}
	away := model.Host{IP: "10.0.0.200"}
	cover := model.Notice{Trigger: model.TriggerPrinterAlert, Severity: "critical", IP: front.IP, MAC: front.MAC, Name: "Front Desk",
		Title: "Printer alert", Message: "ZD621 (10.0.0.10): Door or cover open", Code: door.Code, Detail: door.Description,
		Key: notify.Key(model.TriggerPrinterAlert, front, notify.AlertPart(door)), At: at}
	off := model.Notice{Trigger: model.TriggerPrinterOffline, Severity: "warn", IP: back.IP, MAC: back.MAC, Name: "Back Office",
		Title: "Printer offline", Message: "Printer Back Office (10.0.0.12) stopped responding.",
		Key: notify.Key(model.TriggerPrinterOffline, back, ""), At: at}
	gone := model.Notice{Trigger: model.TriggerPrinterOffline, Severity: "warn", IP: away.IP, Title: "Printer offline",
		Message: "Printer 10.0.0.200 stopped responding.", Key: notify.Key(model.TriggerPrinterOffline, away, ""), At: at}
	// Before the restart, a scan of the whole /24 left them open with Send
	// alerts off: none has a card.
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	l, err := problems.OpenLedger(st)
	if err != nil {
		t.Fatal(err)
	}
	l.Apply(network, []model.Notice{cover, off, gone})
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}

	s, _, served := runOn(t, dir, coverSNMP{})
	defer quit(t, s, served)
	pairChat(t, s.chat, f)
	sendAlerts(t, s, f)
	sc := watch(t, s)
	if sc.Network() != network {
		t.Fatalf("the scan watches %q, want %q", sc.Network(), network)
	}
	if got := sc.Adopted(); !slices.Equal(got, []string{gone.Key}) {
		t.Fatalf("adopted %v, want %s", got, gone.Key)
	}
	waitFor(t, "the burst", func() bool { return len(f.Posted()) >= 2 })
	time.Sleep(2500 * time.Millisecond) // anything late
	var keys []string
	for _, in := range f.Posted() {
		keys = append(keys, in.DedupeKey)
	}
	if want := []string{cover.Key, off.Key}; !slices.Equal(keys, want) { // the most severe first
		t.Fatalf("posted %v, want %v", keys, want)
	}
	if c := s.cfg.Ledger.Chat(network, gone.Key); c != "" || !s.cfg.Ledger.IsOpen(network, gone.Key) {
		t.Fatalf("the adopted problem: chat %q, open %v", c, s.cfg.Ledger.IsOpen(network, gone.Key))
	}
	want := []string{cover.Key, off.Key, gone.Key}
	slices.Sort(want)
	if open := openKeys(s.cfg.Ledger); !slices.Equal(open, want) {
		t.Fatalf("the ledger has %v open", open)
	}
}
