// Package server implements the loopback-only HTTP/SSE API and static UI.
package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"netdiag/internal/chat"
	"netdiag/internal/instance"
	"netdiag/internal/mcpserver"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/problems"
	"netdiag/internal/remote"
	"netdiag/internal/scan"
	"netdiag/internal/service"
	"netdiag/internal/store"
)

// Config configures a Server.
type Config struct {
	Registry   *scan.Registry
	Interfaces func() ([]netinfo.Iface, error) // defaults to netinfo.Interfaces
	Static     fs.FS                           // served at "/"; may be nil
	Store      *store.Store                    // settings + history; nil makes those endpoints return 503
	Health     scan.HealthChecker              // defaults to health.NewRunner()
	Gateway    func() (string, error)          // defaults to the OS default route
	Service    *service.Service                // optional; built from the fields above when nil
	Version    string                          // reported to MCP clients; default "dev"
	Remote     remote.Opener                   // opens remote desktop and screen sharing; defaults to remote.System
	// OpenOSPage opens a page of this computer's settings from the fixed
	// list (POST /api/os/open); defaults to remote.OpenOSPage.
	OpenOSPage func(page string) error
	// InstanceID names this run of netdiag in GET /api/identity, so a page
	// can tell a restarted netdiag from the one it was opened by. A random
	// id is made when it is empty.
	InstanceID string
	// SessionToken is this launch's token (instance.json): every POST, PUT
	// and DELETE under /api/, and the reads in sessionReads, must carry it in
	// X-Netdiag-Session (v0.6 4.1). A random token nobody knows is made
	// when it is empty, so a server is never left open by mistake.
	SessionToken string
	// Ledger is the open-problem ledger the registry's scans write; the
	// service saves it when netdiag quits. nil: none.
	Ledger *problems.Ledger
	// Chat is the team chat (bubbles) connection. When it is nil and Store
	// is set, New makes one that keeps bubbles.json in the Store's folder;
	// without a Store the team chat routes answer 503.
	Chat *chat.Manager
	// Logf receives the team chat manager New makes its diagnostics (a
	// file it could not read or save; never a token). nil: none.
	Logf func(format string, args ...any)
}

// Server is the HTTP server. Create with New, then Listen and Serve.
type Server struct {
	cfg       Config
	svc       *service.Service
	keepalive time.Duration

	baseCtx context.Context
	cancel  context.CancelFunc

	mu   sync.Mutex
	port string
	ln   net.Listener

	mcpSrv     *mcp.Server
	mcpHandler http.Handler
	mcpOn      atomic.Bool

	quit     chan struct{}
	quitOnce sync.Once

	instanceID string // GET /api/identity
	startedAt  string // RFC 3339
	token      string // X-Netdiag-Session

	chat *chat.Manager // nil without a data store
}

// New returns a server for cfg.
func New(cfg Config) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	if cfg.Chat == nil && cfg.Store != nil {
		var l chat.Ledger
		if cfg.Ledger != nil {
			l = cfg.Ledger
		}
		cfg.Chat = chat.NewManager(chat.Options{Files: cfg.Store, Ledger: l, Logf: cfg.Logf})
	}
	svc := cfg.Service
	if svc == nil {
		svc = service.New(service.Config{Registry: cfg.Registry, Store: cfg.Store, Health: cfg.Health,
			Interfaces: cfg.Interfaces, Gateway: cfg.Gateway, BaseCtx: ctx, Ledger: cfg.Ledger, Chat: cfg.Chat})
	}
	if cfg.Remote == nil {
		cfg.Remote = remote.System{}
	}
	if cfg.OpenOSPage == nil {
		cfg.OpenOSPage = remote.OpenOSPage
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = instance.NewID()
	}
	if cfg.SessionToken == "" {
		cfg.SessionToken = instance.NewToken()
	}
	s := &Server{cfg: cfg, svc: svc, keepalive: 15 * time.Second, baseCtx: ctx, cancel: cancel, quit: make(chan struct{}),
		instanceID: cfg.InstanceID, startedAt: time.Now().UTC().Format(time.RFC3339), token: cfg.SessionToken, chat: cfg.Chat}
	s.mcpSrv = mcpserver.New(svc, mcpserver.Options{Transport: "http", Version: cfg.Version})
	s.mcpHandler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcpSrv },
		&mcp.StreamableHTTPOptions{MaxRequestBodyBytes: 1 << 20})
	if svc.HasStore() {
		if set, err := svc.LoadSettings(); err == nil {
			s.mcpOn.Store(set.MCP.Enabled)
			if s.chat != nil {
				s.chat.ApplySettings(set.Chat)
			}
		}
	}
	svc.OnSettingsSaved(s.settingsSaved)
	// MCP get_status over this server's /mcp speaks for the window.
	svc.SetWindow(s.Port)
	return s
}

// InstanceID is the id GET /api/identity reports for this run.
func (s *Server) InstanceID() string { return s.instanceID }

// StartedAt is when this server was made (RFC 3339, UTC), as GET
// /api/identity reports it.
func (s *Server) StartedAt() string { return s.startedAt }

// version is the netdiag version reported to clients ("dev" when unset).
func (s *Server) version() string {
	if s.cfg.Version == "" {
		return "dev"
	}
	return s.cfg.Version
}

// settingsSaved updates the MCP runtime gate (disabling closes live
// sessions) and team chat's Send switch and level.
func (s *Server) settingsSaved(set model.Settings) {
	if s.chat != nil {
		s.chat.ApplySettings(set.Chat)
	}
	was := s.mcpOn.Swap(set.MCP.Enabled)
	if was && !set.MCP.Enabled {
		for ss := range s.mcpSrv.Sessions() {
			_ = ss.Close()
		}
	}
}

// MCPEnabled reports whether the MCP endpoint is currently enabled.
func (s *Server) MCPEnabled() bool { return s.mcpOn.Load() }

// MCPURL returns the MCP endpoint URL; valid after Listen.
func (s *Server) MCPURL() string { return s.URL() + "/mcp" }

// settingsView is the settings JSON plus the read-only mcpUrl and, when the
// file had fields that were not valid and were reset on load, their JSON
// paths in repaired (F7). The page shows a one-time notice for them.
func (s *Server) settingsView(set model.Settings, repaired []string) any {
	return struct {
		model.Settings
		MCPURL   string   `json:"mcpUrl"`
		Repaired []string `json:"repaired,omitempty"`
	}{set, s.MCPURL(), repaired}
}

// Listen binds 127.0.0.1:port (0 picks a random free port).
func (s *Server) Listen(port int) error {
	ln, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	s.mu.Lock()
	s.ln, s.port = ln, p
	s.mu.Unlock()
	return nil
}

// Port returns the port Listen bound; 0 before Listen.
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, _ := strconv.Atoi(s.port)
	return p
}

// URL returns the base URL; valid after Listen.
func (s *Server) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return "http://127.0.0.1:" + s.port
}

// route is one entry of the route table: a ServeMux pattern and its
// handler.
type route struct {
	pattern string
	h       http.Handler
}

// routes is the whole route table. Handler serves exactly these, so the
// session guard test can walk every mutating /api route.
func (s *Server) routes() []route {
	f := func(p string, h http.HandlerFunc) route { return route{p, h} }
	rs := []route{
		f("GET /api/interfaces", s.handleInterfaces),
		f("GET /api/identity", s.handleIdentity),
		f("POST /api/scans", s.handleStart),
		f("GET /api/scans/running", s.handleRunning),
		f("GET /api/scans/{id}", s.handleGet),
		f("GET /api/scans/{id}/events", s.handleEvents),
		f("POST /api/scans/{id}/cancel", s.handleCancel),
		f("GET /api/scans/{id}/export", s.handleExport),
		f("POST /api/quit", s.handleQuit),
		f("GET /api/settings", s.handleGetSettings),
		f("PUT /api/settings", s.handlePutSettings),
		f("GET /api/health", s.handleHealth),
		f("GET /api/problems", s.handleProblems),
		f("POST /api/health/run", s.handleHealthRun),
		f("GET /api/history", s.handleHistory),
		f("GET /api/history/{id}", s.handleHistoryGet),
		f("DELETE /api/history/{id}", s.handleHistoryDelete),
		f("GET /api/compare", s.handleCompare),
		f("GET /api/compare/export", s.handleCompareExport),
		f("POST /api/conflicts/ignore", s.handleIgnore),
		f("GET /api/remote", s.handleRemoteCaps),
		f("POST /api/scans/{id}/remote", s.handleRemoteOpen),
		f("GET /api/scans/{id}/remote.rdp", s.handleRemoteRDPFile),
		f("POST /api/os/open", s.handleOSOpen),
		f("GET /api/bubbles", s.handleChatStatus),
		f("DELETE /api/bubbles", s.handleChatDisconnect),
		f("POST /api/bubbles/discover", s.handleChatDiscover),
		f("POST /api/bubbles/inspect", s.handleChatInspect),
		f("POST /api/bubbles/connect", s.handleChatConnect),
		f("POST /api/bubbles/pair", s.handleChatPair),
		f("GET /api/bubbles/pair/status", s.handleChatPairStatus),
		f("DELETE /api/bubbles/pair", s.handleChatPairCancel),
		f("POST /api/bubbles/channel", s.handleChatChannel),
		f("POST /api/bubbles/test", s.handleChatTest),
		f("POST /api/bubbles/retry", s.handleChatRetry),
	}
	for _, m := range []string{"GET", "POST", "DELETE"} {
		rs = append(rs, route{m + " /mcp", s.mcpGate(s.mcpHandler)})
	}
	if s.cfg.Static != nil {
		rs = append(rs, route{"GET /", http.FileServerFS(s.cfg.Static)})
	}
	return rs
}

// Handler returns the guarded routes: the Host and content-type guard, then
// the session guard (guard.go), then the route table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, r := range s.routes() {
		mux.Handle(r.pattern, r.h)
	}
	return s.guard(s.sessionGuard(mux))
}

// waitScans waits up to limit for cancelled scans to finish, so a live scan's
// final history save completes before the process exits.
func (s *Server) waitScans(limit time.Duration) {
	if s.cfg.Registry == nil {
		return
	}
	deadline := time.After(limit)
	for _, sc := range s.cfg.Registry.List() {
		select {
		case <-sc.Done():
		case <-deadline:
			return
		}
	}
}

// Serve serves on the Listen'd socket until ctx is cancelled or /api/quit is
// called, then cancels running scans, saves what the service holds (the
// open-problem ledger) and shuts down gracefully.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if ln == nil {
		return errors.New("server: Listen not called")
	}
	hs := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return s.baseCtx },
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case err := <-errc:
		s.cancel()
		s.cancelPairing()
		s.waitScans(5 * time.Second)
		_ = s.svc.Close()
		s.closeChat()
		return err
	case <-ctx.Done():
	case <-s.quit:
	}
	s.cancel() // cancels running scans and open SSE streams
	s.cancelPairing()
	s.waitScans(5 * time.Second)
	// The service gives the team chat outbox its last 3 s and saves the
	// ledger; then the chat's sender and polling stop.
	closeErr := s.svc.Close()
	s.closeChat()
	sctx, c := context.WithTimeout(context.Background(), 3*time.Second)
	defer c()
	if err := hs.Shutdown(sctx); err != nil {
		_ = hs.Close()
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("server: saving open problems and team chat alerts: %w", closeErr)
	}
	return nil
}

// cancelPairing stops waiting for a pairing approval (netdiag is closing).
func (s *Server) cancelPairing() {
	if s.chat != nil {
		s.chat.CancelPairing()
	}
}

// closeChat stops the team chat sender and polling and saves the outbox.
func (s *Server) closeChat() {
	if s.chat != nil {
		s.chat.Close()
	}
}
