package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/arp"
	"netdiag/internal/health"
	"netdiag/internal/instance"
	"netdiag/internal/mcpserver"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/scan"
	"netdiag/internal/server"
	"netdiag/internal/service"
	"netdiag/internal/snmpprobe"
	"netdiag/internal/store"
)

func TestRunMCPBadFlag(t *testing.T) {
	var out, errb bytes.Buffer
	if c := runMCP([]string{"--bogus"}, strings.NewReader(""), &out, &errb); c != 2 {
		t.Fatalf("code %d", c)
	}
	if c := runMCP([]string{"extra"}, strings.NewReader(""), &out, &errb); c != 2 {
		t.Fatalf("code %d", c)
	}
	if out.Len() != 0 {
		t.Fatal("stdout written")
	}
}

func TestMCPStdioIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "netdiag")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if o, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, o)
	}
	cmd := exec.Command(bin, "mcp", "--store-dir", filepath.Join(dir, "store"))
	in, _ := cmd.StdinPipe()
	outp, _ := cmd.StdoutPipe()
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer cmd.Process.Kill()

	for _, l := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_interfaces","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"prompts/list"}`,
		`{"jsonrpc":"2.0","id":5,"method":"resources/list"}`,
	} {
		if _, err := in.Write([]byte(l + "\n")); err != nil {
			t.Fatal(err)
		}
	}

	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(outp)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	got := map[int]map[string]any{}
	timeout := time.After(20 * time.Second)
	for len(got) < 5 {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stdout closed early; stderr=%s", errb.String())
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(l), &m); err != nil {
				t.Fatalf("non-JSON stdout line: %q", l)
			}
			if m["jsonrpc"] != "2.0" {
				t.Fatalf("bad frame: %q", l)
			}
			if id, ok := m["id"].(float64); ok {
				got[int(id)] = m
			}
		case <-timeout:
			t.Fatalf("timeout; got %d responses; stderr=%s", len(got), errb.String())
		}
	}
	for id, m := range got {
		if m["error"] != nil {
			t.Errorf("id %d error: %v", id, m["error"])
		}
	}
	raw, _ := json.Marshal(got[2]["result"])
	for _, n := range mcpserver.ToolNames {
		if !strings.Contains(string(raw), `"name":"`+n+`"`) {
			t.Errorf("tools/list missing %s", n)
		}
	}

	in.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit: %v; stderr=%s", err, errb.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not exit after stdin close")
	}
}

// ---- netdiag mcp --attach ----

// attachPinger answers for 10.0.0.10 only; nothing leaves this computer.
type attachPinger struct{}

func (attachPinger) Ping(_ context.Context, ip netip.Addr, _ time.Duration) (time.Duration, bool, error) {
	return time.Millisecond, ip.String() == "10.0.0.10", nil
}

// attachNet is the rest of the window's network access, all fake.
type attachNet struct{}

func (attachNet) Table(context.Context) ([]arp.Entry, error)                 { return nil, nil }
func (attachNet) ScanPorts(context.Context, string, []int) []int             { return nil }
func (attachNet) Reachable(context.Context, string, int, time.Duration) bool { return false }
func (attachNet) ProbeAll(context.Context, string, []int) []model.WebService { return nil }
func (attachNet) LookupAddr(context.Context, string) ([]string, error)       { return nil, io.EOF }
func (attachNet) Lookup(string) string                                       { return "" }
func (attachNet) Gateway() (string, error)                                   { return "10.0.0.1", nil }
func (attachNet) Self(string) (model.SelfInfo, error) {
	return model.SelfInfo{IP: "10.0.0.30", Interface: "eth0"}, nil
}
func (attachNet) Probe(context.Context, string, string) (*snmpprobe.Result, error) {
	return nil, snmpprobe.ErrNoResponse
}
func (attachNet) Run(context.Context, health.Input) model.HealthReport {
	return model.HealthReport{CheckedAt: time.Now().UTC().Format(time.RFC3339),
		Checks: []model.HealthCheck{{ID: "gateway", Label: "Gateway", Status: model.HealthOK}}}
}

// testWindow is a netdiag window on 127.0.0.1 (a random port) for this
// test's data folder (NETDIAG_DATA_DIR, so `netdiag mcp` finds it), with
// fake network access and instance.json written as `netdiag ui` writes it.
// agent turns its Agent access on.
func testWindow(t *testing.T, agent bool) (*store.Store, *service.Service, instance.Info) {
	t.Helper()
	_, st := dataDir(t)
	if agent {
		set, err := st.LoadSettings()
		if err != nil {
			t.Fatal(err)
		}
		set.MCP.Enabled = true
		if _, err := st.SaveSettings(set); err != nil {
			t.Fatal(err)
		}
	}
	n := attachNet{}
	deps := scan.Deps{Pinger: attachPinger{}, ARP: n, Ports: n, TCP: n, Web: n, SNMP: n, DNS: n, Vendors: n, Env: n, Health: n,
		Store: st, Settings: st,
		LocalPrefixes: func() ([]netip.Prefix, error) { return []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}, nil }}
	reg := scan.NewRegistry(scan.New(deps))
	ctx, cancel := context.WithCancel(context.Background())
	svc := service.New(service.Config{Registry: reg, Store: st, Health: n, BaseCtx: ctx,
		Interfaces: func() ([]netinfo.Iface, error) {
			return []netinfo.Iface{{Name: "eth0", IP: "10.0.0.30", CIDR: "10.0.0.30/24", IsDefault: true}}, nil
		},
		Gateway: func() (string, error) { return "10.0.0.1", nil }})
	info := instance.Info{PID: os.Getpid(), InstanceID: instance.NewID(), Token: instance.NewToken(), Version: "test"}
	srv := server.New(server.Config{Registry: reg, Store: st, Service: svc, InstanceID: info.InstanceID, SessionToken: info.Token, Version: "test"})
	if err := srv.Listen(0); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the window did not stop")
		}
	})
	info.Port = srv.Port()
	if err := instance.Save(st, info); err != nil {
		t.Fatal(err)
	}
	return st, svc, info
}

// attached runs `netdiag mcp args...` on pipes, connects an MCP client to
// it and returns the session, the exit code channel and stderr.
func attached(t *testing.T, args ...string) (*mcp.ClientSession, chan int, *syncBuf) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errb := &syncBuf{}
	code := make(chan int, 1)
	go func() {
		c := runMCP(args, inR, outW, errb)
		outW.Close()
		code <- c
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
		Connect(ctx, &mcp.IOTransport{Reader: outR, Writer: inW}, nil)
	if err != nil {
		t.Fatalf("connect: %v; stderr=%s", err, errb.String())
	}
	t.Cleanup(func() { cs.Close() })
	return cs, code, errb
}

// getStatus calls get_status and decodes it.
func getStatus(t *testing.T, cs *mcp.ClientSession) mcpserver.StatusOut {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_status", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("get_status: %v %+v", err, res)
	}
	var st mcpserver.StatusOut
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// exited waits for runMCP's exit code.
func exited(t *testing.T, code chan int, errb *syncBuf) int {
	t.Helper()
	select {
	case c := <-code:
		return c
	case <-time.After(10 * time.Second):
		t.Fatalf("netdiag mcp did not exit; stderr=%s", errb.String())
		return -1
	}
}

// With the window open and Agent access on, --attach relays to the
// window's own MCP address: the agent sees the window (thisProcess), the
// scan watching the network and every tool and prompt; closing stdin ends
// it with 0.
func TestMCPAttachSeesWindow(t *testing.T) {
	_, svc, info := testWindow(t, true)
	live, err := svc.Start(model.ScanRequest{CIDR: "10.0.0.0/28", Live: true, LiveIntervalSec: 300}.WithOrigin(model.OriginUI))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { live.Cancel(); <-live.Done() })
	for deadline := time.Now().Add(10 * time.Second); live.State() != model.StateLive; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the window's scan did not start watching: %s", live.State())
		}
	}

	cs, code, errb := attached(t, "--attach")
	st := getStatus(t, cs)
	if st.Transport != "http" || !st.Window.Open || !st.Window.ThisProcess || st.Window.Port != info.Port ||
		st.Watching == nil || st.Watching.ScanID != live.ID || st.Chat.State != "off" || st.Note != "" {
		t.Fatalf("attached get_status = %+v", st)
	}
	tl, err := cs.ListTools(context.Background(), nil)
	if err != nil || len(tl.Tools) != len(mcpserver.ToolNames) {
		t.Fatalf("tools = %v %v", tl, err)
	}
	pl, err := cs.ListPrompts(context.Background(), nil)
	if err != nil || len(pl.Prompts) != len(mcpserver.PromptNames) {
		t.Fatalf("prompts = %v %v", pl, err)
	}
	cs.Close()
	if c := exited(t, code, errb); c != 0 {
		t.Fatalf("exit %d; stderr=%s", c, errb.String())
	}
	if strings.Contains(errb.String(), "--attach:") {
		t.Fatalf("attached, yet said it runs on its own: %s", errb.String())
	}
}

// The window open with Agent access off (its /mcp answers 404): --attach
// runs on its own, and says so on stderr and in get_status's note.
func TestMCPAttachFallsBackAgentOff(t *testing.T) {
	testWindow(t, false)
	cs, code, errb := attached(t, "--attach")
	st := getStatus(t, cs)
	if st.Transport != "stdio" || !st.Window.Open || st.Window.ThisProcess || st.Window.AgentAccess || st.Watching != nil ||
		st.Chat.State != service.ChatUnknown || st.Note != service.NoteAgentOff {
		t.Fatalf("fallback get_status = %+v", st)
	}
	if !strings.Contains(errb.String(), "netdiag mcp --attach: "+service.NoteAgentOff) {
		t.Fatalf("stderr = %q", errb.String())
	}
	cs.Close()
	if c := exited(t, code, errb); c != 0 {
		t.Fatalf("exit %d; stderr=%s", c, errb.String())
	}
}

// No window: no instance.json, or one whose port nothing answers on (a
// window that quit without removing it). --attach runs on its own.
func TestMCPAttachFallsBackNoWindow(t *testing.T) {
	_, st := dataDir(t)
	for _, stale := range []bool{false, true} {
		if stale {
			if err := instance.Save(st, instance.Info{Port: freePort(t), InstanceID: "i-gone", Token: "t"}); err != nil {
				t.Fatal(err)
			}
		}
		cs, code, errb := attached(t, "--attach")
		s := getStatus(t, cs)
		if s.Transport != "stdio" || s.Window.Open || s.Note != service.NoteNoWindow || s.Chat.State != service.ChatUnknown {
			t.Fatalf("stale=%v: get_status = %+v", stale, s)
		}
		if !strings.Contains(errb.String(), "netdiag mcp --attach: "+service.NoteNoWindow) {
			t.Fatalf("stale=%v: stderr = %q", stale, errb.String())
		}
		cs.Close()
		if c := exited(t, code, errb); c != 0 {
			t.Fatalf("exit %d", c)
		}
	}
}

// Turning Agent access off in the window closes the agent's session there:
// the next call gets an error and the attached netdiag mcp exits with 1, so
// the agent is never left talking to a window that no longer allows it.
func TestMCPAttachEndsWhenAgentAccessGoesOff(t *testing.T) {
	st, svc, _ := testWindow(t, true)
	cs, code, errb := attached(t, "--attach")
	if s := getStatus(t, cs); s.Transport != "http" {
		t.Fatalf("not attached: %+v", s)
	}
	set, err := st.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	set.MCP.Enabled = false
	if _, err := svc.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_status", Arguments: map[string]any{}}); err == nil && !res.IsError {
		t.Fatalf("a call after Agent access went off worked: %+v", res)
	}
	if c := exited(t, code, errb); c != 1 || !strings.Contains(errb.String(), "MCP connection ended") {
		t.Fatalf("exit %d; stderr=%s", c, errb.String())
	}
}

// Quitting the window ends the attached netdiag mcp too: the next call gets
// an error answer (never no answer) and it exits with 1.
func TestMCPAttachEndsWhenWindowQuits(t *testing.T) {
	_, _, info := testWindow(t, true)
	cs, code, errb := attached(t, "--attach")
	if s := getStatus(t, cs); s.Transport != "http" {
		t.Fatalf("not attached: %+v", s)
	}
	if c := post(t, "http://127.0.0.1:"+strconv.Itoa(info.Port)+"/api/quit", info.Token); c != 204 {
		t.Fatalf("quit: %d", c)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if running, _ := instance.Running(context.Background(), info); !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the window did not quit")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_status", Arguments: map[string]any{}}); err == nil && !res.IsError {
		t.Fatalf("a call after Quit worked: %+v", res)
	}
	if c := exited(t, code, errb); c != 1 || !strings.Contains(errb.String(), "MCP connection ended") {
		t.Fatalf("exit %d; stderr=%s", c, errb.String())
	}
}

// protoVersion takes the version initialize agreed on and puts it on every
// later request, unless the request already has one.
func TestAttachProtocolVersionHeader(t *testing.T) {
	var got []string
	p := &protoVersion{base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = append(got, r.Header.Get("Mcp-Protocol-Version"))
		return &http.Response{StatusCode: 202, Body: http.NoBody}, nil
	})}
	send := func(h string) {
		req, _ := http.NewRequest("POST", "http://127.0.0.1:1/mcp", nil)
		if h != "" {
			req.Header.Set("Mcp-Protocol-Version", h)
		}
		if _, err := p.RoundTrip(req); err != nil {
			t.Fatal(err)
		}
	}
	id := func(n float64) jsonrpc.ID {
		v, err := jsonrpc.MakeID(n)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	send("")
	p.sent(&jsonrpc.Request{ID: id(1), Method: "tools/list"})
	p.sent(&jsonrpc.Request{ID: id(7), Method: "initialize"})
	p.seen(&jsonrpc.Response{ID: id(1), Result: json.RawMessage(`{"protocolVersion":"wrong"}`)})
	send("")
	p.seen(&jsonrpc.Response{ID: id(7), Result: json.RawMessage(`{"protocolVersion":"2025-06-18"}`)})
	send("")
	send("2025-03-26")
	if want := []string{"", "", "2025-06-18", "2025-03-26"}; !slices.Equal(got, want) {
		t.Fatalf("headers %q, want %q", got, want)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
