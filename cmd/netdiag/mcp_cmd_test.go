package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"netdiag/internal/mcpserver"
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
