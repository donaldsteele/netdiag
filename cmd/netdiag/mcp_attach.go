package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/instance"
	"netdiag/internal/service"
	"netdiag/internal/store"
)

// `netdiag mcp --attach` (v0.6 4.14, D12) relays the agent's stdio to the
// open netdiag window's own MCP address, so the agent sees what the window
// sees: the scan watching the network, its problems and team chat. It never
// gets around the window's consent: the window must be this data folder's
// (instance.json, with the identity proof, v0.6-D33) and its Agent access
// switch must be on; otherwise `netdiag mcp` runs on its own, as without the
// flag, and get_status's note says why.

// attachProbeTimeout bounds the check of the window's MCP address.
const attachProbeTimeout = 2 * time.Second

// findWindow returns the MCP address of the netdiag window that serves st's
// data folder, or "" and the reason this connection runs on its own (the
// note get_status gives then). The session token in instance.json only
// proves the window's identity here: /mcp is not under /api/, so the Agent
// access switch is its consent (v0.6 4.1).
func findWindow(ctx context.Context, st *store.Store) (endpoint, why string) {
	if st == nil {
		return "", service.NoteNoWindow
	}
	info, err := instance.Load(st)
	if err != nil || info.Port == 0 {
		return "", service.NoteNoWindow
	}
	if running, _ := instance.Running(ctx, info); !running {
		return "", service.NoteNoWindow
	}
	endpoint = fmt.Sprintf("http://127.0.0.1:%d/mcp", info.Port)
	// While Agent access is off, every request to /mcp answers 404
	// (mcpGate). A GET without a session asks for nothing: the MCP handler
	// refuses it with 400.
	pctx, cancel := context.WithTimeout(ctx, attachProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", service.NoteNoWindow
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := loopbackClient(nil).Do(req)
	if err != nil {
		return "", service.NoteNoWindow
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", service.NoteAgentOff
	}
	return endpoint, ""
}

// loopbackClient talks to 127.0.0.1 only, never through a proxy. wrap
// wraps its transport (nil: none).
func loopbackClient(wrap func(http.RoundTripper) http.RoundTripper) *http.Client {
	var rt http.RoundTripper = &http.Transport{Proxy: nil, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}
	if wrap != nil {
		rt = wrap(rt)
	}
	return &http.Client{Transport: rt}
}

// runAttached relays between the agent on stdin/stdout and the window's MCP
// address until either side ends. stdin closing is the normal end (0);
// the window going away is an error (1).
func runAttached(ctx context.Context, endpoint string, stdin io.ReadCloser, stdout, stderr io.Writer) int {
	local, err := (&mcp.IOTransport{Reader: stdin, Writer: nopWriteCloser{stdout}}).Connect(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	ver := &protoVersion{}
	remote, err := (&mcp.StreamableClientTransport{Endpoint: endpoint,
		HTTPClient: loopbackClient(func(rt http.RoundTripper) http.RoundTripper { ver.base = rt; return ver })}).Connect(ctx)
	if err != nil {
		local.Close()
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	err = relay(ctx, local, remote, ver)
	remote.Close() // ends the window's session
	local.Close()
	if err != nil && ctx.Err() == nil {
		fmt.Fprintln(stderr, "error: the netdiag window's MCP connection ended:", err)
		return 1
	}
	return 0
}

// relay copies JSON-RPC messages from local (the agent) to remote (the
// window) and back. Each call is sent on its own, as the SDK's client does,
// since a POST answers only when its result (or a first progress note)
// is ready; notifications and responses go in order. A call the window does
// not take gets an error answer, so the agent is not left waiting, and ends
// the relay: the window quit or closed the session. It returns nil when
// local ends (stdin closed), else what stopped it.
//
// The relay never starts the window's stream for messages nobody asked for
// (the SDK client starts it only for its own sessions): netdiag's server
// sends nothing outside a call, as its tools, prompts and resources never
// change while it runs.
func relay(ctx context.Context, local, remote mcp.Connection, ver *protoVersion) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var calls sync.WaitGroup
	// The first end wins; stop never blocks.
	errc := make(chan error, 1)
	stop := func(err error) {
		select {
		case errc <- err:
		default:
		}
	}
	reading := make(chan struct{}) // closed when the agent side stops reading
	go func() {
		defer close(reading)
		for {
			msg, err := local.Read(ctx)
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil
				}
				stop(err)
				return
			}
			if req, ok := msg.(*jsonrpc.Request); ok && req.IsCall() {
				ver.sent(req)
				calls.Add(1)
				go func() {
					defer calls.Done()
					if err := remote.Write(ctx, req); err != nil && ctx.Err() == nil {
						_ = local.Write(ctx, &jsonrpc.Response{ID: req.ID,
							Error: &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "the netdiag window did not take this request: " + err.Error()}})
						// The window quit, or ended this session (Agent
						// access off): stop, so the agent sees it.
						stop(err)
					}
				}()
				continue
			}
			if err := remote.Write(ctx, msg); err != nil {
				stop(err)
				return
			}
		}
	}()
	go func() {
		for {
			msg, err := remote.Read(ctx)
			if err != nil {
				stop(err)
				return
			}
			ver.seen(msg)
			if err := local.Write(ctx, msg); err != nil {
				stop(err)
				return
			}
		}
	}()
	err := <-errc
	cancel() // the calls still on their way stop
	<-reading
	calls.Wait()
	return err
}

// protoVersion adds the MCP-Protocol-Version header the agent and the
// window agreed on in initialize to every later request, as the
// streamable HTTP transport asks (the SDK's client transport does it only
// for sessions of its own client).
type protoVersion struct {
	base http.RoundTripper

	mu      sync.Mutex
	initID  jsonrpc.ID
	version string
}

// sent notes the id of the agent's initialize call.
func (p *protoVersion) sent(req *jsonrpc.Request) {
	if req.Method != "initialize" {
		return
	}
	p.mu.Lock()
	p.initID = req.ID
	p.mu.Unlock()
}

// seen takes the protocol version from the answer to initialize.
func (p *protoVersion) seen(msg jsonrpc.Message) {
	resp, ok := msg.(*jsonrpc.Response)
	if !ok || resp.Error != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initID.IsValid() || resp.ID != p.initID {
		return
	}
	var r struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(resp.Result, &r) == nil && r.ProtocolVersion != "" {
		p.version = r.ProtocolVersion
	}
}

func (p *protoVersion) RoundTrip(req *http.Request) (*http.Response, error) {
	p.mu.Lock()
	v := p.version
	p.mu.Unlock()
	if v != "" && req.Header.Get("Mcp-Protocol-Version") == "" {
		req = req.Clone(req.Context())
		req.Header.Set("Mcp-Protocol-Version", v)
	}
	return p.base.RoundTrip(req)
}
