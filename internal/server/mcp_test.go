package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpDisabledMsg = "MCP is disabled; enable it in Settings > Agent access"

func isDisabledBody(b string) bool {
	var m map[string]string
	return json.Unmarshal([]byte(b), &m) == nil && m["error"] == mcpDisabledMsg
}

const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`

func setMCP(t *testing.T, base string, on bool) {
	t.Helper()
	_, body := doj(t, "GET", base+"/api/settings", "", "")
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	m["mcp"] = map[string]any{"enabled": on}
	b, _ := json.Marshal(m)
	resp, out := doj(t, "PUT", base+"/api/settings", string(b), "application/json")
	if resp.StatusCode != 200 {
		t.Fatalf("PUT settings %d %s", resp.StatusCode, out)
	}
}

func mcpConnect(t *testing.T, base string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: base + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callJSON(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("%s: tool error %+v", name, res.Content[0].(*mcp.TextContent).Text)
	}
	b, _ := json.Marshal(res.StructuredContent)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func TestMCPDisabledByDefault(t *testing.T) {
	_, base, _ := startStore(t)
	resp, body := doj(t, "POST", base+"/mcp", initBody, "application/json")
	if resp.StatusCode != 404 || !isDisabledBody(body) {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
}

func TestMCPEnableAndUse(t *testing.T) {
	_, base, _ := startStore(t)
	setMCP(t, base, true)
	_, body := doj(t, "GET", base+"/api/settings", "", "")
	var m map[string]any
	_ = json.Unmarshal([]byte(body), &m)
	if m["mcpUrl"] != base+"/mcp" || m["community"] == nil {
		t.Fatalf("settings = %s", body)
	}
	cs := mcpConnect(t, base)
	tl, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil || len(tl.Tools) != 16 {
		t.Fatalf("tools %v %v", tl, err)
	}
	gs := callJSON(t, cs, "get_settings", nil)
	if s := gs["community"]; s != nil && s != "***" {
		t.Fatalf("community not redacted: %v", gs)
	}
	// get_status over the window's /mcp speaks for the window itself, on
	// the port the page is on, with Agent access on.
	st := callJSON(t, cs, "get_status", nil)
	w, _ := st["window"].(map[string]any)
	u, _ := url.Parse(base)
	if w["open"] != true || w["thisProcess"] != true || w["agentAccess"] != true || fmt.Sprint(w["port"]) != u.Port() || st["transport"] != "http" {
		t.Fatalf("get_status = %v", st)
	}
	if b, _ := json.Marshal(st); strings.Contains(string(b), "bub_") {
		t.Fatalf("get_status holds a chat token: %s", b)
	}
	if lp := callJSON(t, cs, "list_problems", nil); lp["source"] == nil {
		t.Fatalf("list_problems = %v", lp)
	}
}

func TestMCPScansShared(t *testing.T) {
	_, base, _ := startStore(t)
	setMCP(t, base, true)
	cs := mcpConnect(t, base)
	st := callJSON(t, cs, "start_scan", map[string]any{"cidr": "10.0.0.0/28", "waitSec": 20})
	id, _ := st["scanId"].(string)
	if id == "" {
		t.Fatalf("start_scan = %v", st)
	}
	resp, body := doj(t, "GET", base+"/api/scans/"+id, "", "")
	if resp.StatusCode != 200 {
		t.Fatalf("REST get %d %s", resp.StatusCode, body)
	}
	// Scan started via REST is visible via MCP.
	resp, body = doj(t, "POST", base+"/api/scans", `{"cidr":"10.0.0.0/28"}`, "application/json")
	if resp.StatusCode >= 300 {
		t.Fatalf("REST start %d %s", resp.StatusCode, body)
	}
	var started struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &started)
	if started.ID == "" {
		t.Fatalf("no id in %s", body)
	}
	g := callJSON(t, cs, "get_scan", map[string]any{"scanId": started.ID})
	if g["scanId"] != started.ID {
		t.Fatalf("get_scan = %v", g)
	}
}

func TestMCPLiveOverHTTP(t *testing.T) {
	_, base, _ := startStore(t)
	setMCP(t, base, true)
	cs := mcpConnect(t, base)
	st := callJSON(t, cs, "start_scan", map[string]any{"cidr": "10.0.0.0/28", "live": true, "waitSec": 0})
	id, _ := st["scanId"].(string)
	if id == "" {
		t.Fatalf("live start = %v", st)
	}
	callJSON(t, cs, "cancel_scan", map[string]any{"scanId": id})
}

func TestMCPDisableClosesSessions(t *testing.T) {
	_, base, _ := startStore(t)
	setMCP(t, base, true)
	cs := mcpConnect(t, base)
	callJSON(t, cs, "get_settings", nil)
	setMCP(t, base, false)
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_settings"})
		if err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("call still succeeds after disable")
		}
		time.Sleep(50 * time.Millisecond)
	}
	resp, body := doj(t, "POST", base+"/mcp", initBody, "application/json")
	if resp.StatusCode != 404 || !isDisabledBody(body) {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
}

func TestMCPHostAndOrigin(t *testing.T) {
	_, base, _ := startStore(t)
	setMCP(t, base, true)
	port := base[strings.LastIndex(base, ":")+1:]
	do := func(host, origin string) int {
		req, _ := http.NewRequest("POST", base+"/mcp", strings.NewReader(initBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if host != "" {
			req.Host = host
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := do("evil.example.com", ""); c != 403 {
		t.Fatalf("evil host = %d", c)
	}
	if c := do("", "http://evil.example.com"); c != 403 {
		t.Fatalf("evil origin = %d", c)
	}
	if c := do("", "http://127.0.0.1:"+port); c != 200 {
		t.Fatalf("own origin = %d", c)
	}
	if c := do("", ""); c != 200 {
		t.Fatalf("no origin = %d", c)
	}
}

func TestMCPNoStoreDisabled(t *testing.T) {
	s := New(Config{})
	if err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = s.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	resp, body := doj(t, "POST", s.URL()+"/mcp", initBody, "application/json")
	if resp.StatusCode != 404 || !isDisabledBody(body) {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
}
