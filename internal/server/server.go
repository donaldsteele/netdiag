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
	"time"

	"netdiag/internal/netinfo"
	"netdiag/internal/scan"
)

// Config configures a Server.
type Config struct {
	Registry   *scan.Registry
	Interfaces func() ([]netinfo.Iface, error) // defaults to netinfo.Interfaces
	Static     fs.FS                           // served at "/"; may be nil
}

// Server is the HTTP server. Create with New, then Listen and Serve.
type Server struct {
	cfg       Config
	keepalive time.Duration

	baseCtx context.Context
	cancel  context.CancelFunc

	mu   sync.Mutex
	port string
	ln   net.Listener

	quit     chan struct{}
	quitOnce sync.Once
}

// New returns a server for cfg.
func New(cfg Config) *Server {
	if cfg.Interfaces == nil {
		cfg.Interfaces = netinfo.Interfaces
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{cfg: cfg, keepalive: 15 * time.Second, baseCtx: ctx, cancel: cancel, quit: make(chan struct{})}
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
	mux.HandleFunc("POST /api/scans", s.handleStart)
	mux.HandleFunc("GET /api/scans/{id}", s.handleGet)
	mux.HandleFunc("GET /api/scans/{id}/events", s.handleEvents)
	mux.HandleFunc("POST /api/scans/{id}/cancel", s.handleCancel)
	mux.HandleFunc("GET /api/scans/{id}/export", s.handleExport)
	mux.HandleFunc("POST /api/quit", s.handleQuit)
	if s.cfg.Static != nil {
		mux.Handle("GET /", http.FileServerFS(s.cfg.Static))
	}
	return s.guard(mux)
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
