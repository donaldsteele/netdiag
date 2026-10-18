package mcpserver

import (
	"context"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// allowList is the authority ceiling: reads plus start_scan, cancel_scan,
// run_health_check and probe_host. Changing it is a deliberate decision.
var allowList = []string{
	"list_interfaces", "start_scan", "get_scan", "cancel_scan", "list_devices",
	"get_device", "get_events", "get_health", "run_health_check", "probe_host",
	"list_history", "compare_scans", "get_settings",
}

func TestToolSetMatchesAllowList(t *testing.T) {
	e := setup(t, "http", true, fakePinger{})
	res, err := e.cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tl := range res.Tools {
		got = append(got, tl.Name)
	}
	slices.Sort(got)
	want := slices.Clone(allowList)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("registered tools %v != allow-list %v", got, want)
	}
	if !slices.Equal(slices.Sorted(slices.Values(ToolNames)), want) {
		t.Fatalf("ToolNames drifted from allow-list")
	}
}

func TestNoMutatingCallsInMCPLayer(t *testing.T) {
	// OpenOSPage( (v0.6 M7): agents never open this computer's settings pages.
	// ReplaceLive: agents never replace a scan that is watching the network
	// (F9); only the window's Restart watching does. The team chat actions
	// (v0.6 M10): agents never connect or disconnect team chat, choose its
	// channel or send test alerts (chat.Manager's StartPairing,
	// ConnectString, SetChannel, SendTest and Disconnect). Nor do they post
	// to team chat themselves or push its outbox: v0.6 M11's Enqueue, as
	// the registry names it, and Try now's TryNow.
	forbidden := []string{"SaveSettings(", "DeleteScan(", "IgnoreMAC(", "handleQuit", "Quit(", "OpenOSPage(", "ReplaceLive",
		"StartPairing(", "ConnectString(", "Disconnect(", "SendTest(", "SetChannel(", "Enqueue(", "TryNow("}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range entries {
		n := en.Name()
		if en.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range forbidden {
			if strings.Contains(string(b), f) {
				t.Errorf("%s contains forbidden call %q", n, f)
			}
		}
	}
}

// actionPackages act on the world (team chat, the chat token, the
// single-instance file, remote sessions). mcpserver must not import them,
// so agents can only read their state through the service (v0.6 plan
// §4.14, master roadmap §8.11).
var actionPackages = []string{
	"netdiag/internal/bubbles",
	"netdiag/internal/chat",
	"netdiag/internal/secret",
	"netdiag/internal/instance",
	"netdiag/internal/remote",
}

func TestMCPImportsNoActionPackages(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, n := range files {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, n, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			for _, banned := range actionPackages {
				if p == banned || strings.HasPrefix(p, banned+"/") {
					t.Errorf("%s imports %s; agents may only read its state through the service", n, p)
				}
			}
		}
	}
}

func TestSDKImportIsolated(t *testing.T) {
	root := filepath.Join("..", "..")
	allowed := []string{
		filepath.Join(root, "internal", "mcpserver"),
		filepath.Join(root, "internal", "server"), // HTTP mount (T7)
		filepath.Join(root, "cmd", "netdiag"),     // stdio command (T6)
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Like the go tool, skip directories starting with "." or "_"
			// (.git, .claude/worktrees with other checkouts of this repo).
			n := d.Name()
			if p != root && (strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_")) || n == "node_modules" || n == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		for _, a := range allowed {
			if strings.HasPrefix(p, a+string(filepath.Separator)) {
				return nil
			}
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "github.com/modelcontextprotocol/go-sdk") {
			t.Errorf("%s imports the MCP SDK outside the allowed packages", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
