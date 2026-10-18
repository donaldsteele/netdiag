package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"netdiag/internal/bubbles/bubblestest"
	"netdiag/internal/bubbles/protocol"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
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

// runOn is one run of netdiag on the data folder dir, wired as `netdiag
// ui` wires it (one ledger and profile store for every scan, the window's
// team chat manager), its printer answering SNMP with snmp. It returns
// what Quit needs and a channel that gets Serve's answer.
func runOn(t *testing.T, dir string, snmp scan.SNMPProber) (*Server, *store.Store, chan error) {
	t.Helper()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := problems.OpenLedger(st)
	if err != nil {
		t.Fatal(err)
	}
	reg := scan.NewRegistry(scan.New(scan.Deps{
		Pinger: fakePinger{}, ARP: routerARP{}, Ports: fakePorts{}, Web: fakeWeb{},
		SNMP: snmp, DNS: fakeDNS{}, Vendors: fakeVendors{}, Env: fakeEnv{},
		Store: st, Settings: st, Ledger: ledger, Profiles: scan.NewProfiles(st),
	}))
	s := New(Config{Registry: reg, Store: st, Health: fakeHealth{}, Gateway: func() (string, error) { return "10.0.0.1", nil },
		Interfaces:   func() ([]netinfo.Iface, error) { return []netinfo.Iface{{Name: "eth0", IP: "10.0.0.30"}}, nil },
		SessionToken: testToken, Ledger: ledger, Logf: t.Logf})
	if err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- s.Serve(context.Background()) }()
	return s, st, served
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

// The v0.6 restart acceptance end to end, with real scans and the bubbles
// v0.2.0 fake: the printer's cover opens while netdiag watches, and one
// card goes to chat; Quit saves the ledger and the outbox; the cover is
// closed while netdiag is closed; the next run's first pass finds it
// fixed and resolves the same card (the same dedupe_key) once, and posts
// nothing new.
func TestFixedWhileClosedResolvesTheCardAfterRestart(t *testing.T) {
	f := bubblestest.New(t)
	dir := t.TempDir()
	const watch = `{"cidr":"10.0.0.0/28","live":true,"liveIntervalSec":5}`

	s, st, served := runOn(t, dir, coverSNMP{})
	pairChat(t, s.chat, f)
	_, b := doj(t, "GET", s.URL()+"/api/settings", "", "")
	set := decode[map[string]any](t, b)
	set["chat"] = map[string]any{"enabled": true, "minSeverity": "warning"}
	sb, _ := json.Marshal(set)
	if r, b := doj(t, "PUT", s.URL()+"/api/settings", string(sb), "application/json"); r.StatusCode != 200 {
		t.Fatalf("save settings: %d %s", r.StatusCode, b)
	}
	waitFor(t, "the connected message", func() bool { return len(f.Messages()) == 1 })
	if r, b := doj(t, "POST", s.URL()+"/api/scans", watch, "application/json"); r.StatusCode != 202 {
		t.Fatalf("start live: %d %s", r.StatusCode, b)
	}
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
	s, st, served = runOn(t, dir, closedSNMP{})
	defer quit(t, s, served)
	if r, b := doj(t, "POST", s.URL()+"/api/scans", watch, "application/json"); r.StatusCode != 202 {
		t.Fatalf("start live after the restart: %d %s", r.StatusCode, b)
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
}
