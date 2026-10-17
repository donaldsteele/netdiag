// Package server implements the loopback-only HTTP/SSE API and static UI.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

	"netdiag/internal/mcpserver"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
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
}

// New returns a server for cfg.
func New(cfg Config) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	svc := cfg.Service
	if svc == nil {
		svc = service.New(service.Config{Registry: cfg.Registry, Store: cfg.Store, Health: cfg.Health,
			Interfaces: cfg.Interfaces, Gateway: cfg.Gateway, BaseCtx: ctx})
	}
	if cfg.Remote == nil {
		cfg.Remote = remote.System{}
	}
	if cfg.OpenOSPage == nil {
		cfg.OpenOSPage = remote.OpenOSPage
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = newInstanceID()
	}
	s := &Server{cfg: cfg, svc: svc, keepalive: 15 * time.Second, baseCtx: ctx, cancel: cancel, quit: make(chan struct{}),
		instanceID: cfg.InstanceID, startedAt: time.Now().UTC().Format(time.RFC3339)}
	s.mcpSrv = mcpserver.New(svc, mcpserver.Options{Transport: "http", Version: cfg.Version})
	s.mcpHandler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcpSrv },
		&mcp.StreamableHTTPOptions{MaxRequestBodyBytes: 1 << 20})
	if svc.HasStore() {
		if set, err := svc.LoadSettings(); err == nil {
			s.mcpOn.Store(set.MCP.Enabled)
		}
	}
	svc.OnSettingsSaved(s.settingsSaved)
	return s
}

// newInstanceID returns a random id for one run of netdiag.
func newInstanceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "i-" + hex.EncodeToString(b[:])
}

// version is the netdiag version reported to clients ("dev" when unset).
func (s *Server) version() string {
	if s.cfg.Version == "" {
		return "dev"
	}
	return s.cfg.Version
}

// settingsSaved updates the MCP runtime gate; disabling closes live sessions.
func (s *Server) settingsSaved(set model.Settings) {
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

// URL returns the base URL; valid after Listen.
func (s *Server) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return "http://127.0.0.1:" + s.port
}

// Handler returns the guarded routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/interfaces", s.handleInterfaces)
	mux.HandleFunc("GET /api/identity", s.handleIdentity)
	mux.HandleFunc("POST /api/scans", s.handleStart)
	mux.HandleFunc("GET /api/scans/running", s.handleRunning)
	mux.HandleFunc("GET /api/scans/{id}", s.handleGet)
	mux.HandleFunc("GET /api/scans/{id}/events", s.handleEvents)
	mux.HandleFunc("POST /api/scans/{id}/cancel", s.handleCancel)
	mux.HandleFunc("GET /api/scans/{id}/export", s.handleExport)
	mux.HandleFunc("POST /api/quit", s.handleQuit)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handlePutSettings)
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("POST /api/health/run", s.handleHealthRun)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("GET /api/history/{id}", s.handleHistoryGet)
	mux.HandleFunc("DELETE /api/history/{id}", s.handleHistoryDelete)
	mux.HandleFunc("GET /api/compare", s.handleCompare)
	mux.HandleFunc("GET /api/compare/export", s.handleCompareExport)
	mux.HandleFunc("POST /api/conflicts/ignore", s.handleIgnore)
	mux.HandleFunc("GET /api/remote", s.handleRemoteCaps)
	mux.HandleFunc("POST /api/scans/{id}/remote", s.handleRemoteOpen)
	mux.HandleFunc("GET /api/scans/{id}/remote.rdp", s.handleRemoteRDPFile)
	mux.HandleFunc("POST /api/os/open", s.handleOSOpen)
	for _, m := range []string{"GET", "POST", "DELETE"} {
		mux.Handle(m+" /mcp", s.mcpGate(s.mcpHandler))
	}
	if s.cfg.Static != nil {
		mux.Handle("GET /", http.FileServerFS(s.cfg.Static))
	}
	return s.guard(mux)
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
// called, then cancels running scans and shuts down gracefully.
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
		return err
	case <-ctx.Done():
	case <-s.quit:
	}
	s.cancel() // cancels running scans and open SSE streams
	s.waitScans(5 * time.Second)
	sctx, c := context.WithTimeout(context.Background(), 3*time.Second)
	defer c()
	if err := hs.Shutdown(sctx); err != nil {
		_ = hs.Close()
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server: %w", err)
	}
	return nil
}
