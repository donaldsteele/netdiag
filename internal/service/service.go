// Package service holds the business logic shared by the REST handlers and
// the MCP layer.
package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"netdiag/internal/chat"
	"netdiag/internal/health"
	"netdiag/internal/model"
	"netdiag/internal/netinfo"
	"netdiag/internal/problems"
	"netdiag/internal/scan"
	"netdiag/internal/store"
)

// ErrNoStore means the data store is unavailable.
var ErrNoStore = errors.New("data store unavailable")

// BadRequestError is a caller error with an exact user-facing message.
type BadRequestError struct{ Msg string }

func (e *BadRequestError) Error() string { return e.Msg }

// NotFoundError is a missing-resource error with an exact message.
type NotFoundError struct{ Msg string }

func (e *NotFoundError) Error() string { return e.Msg }

// Config configures a Service. Zero values get production defaults.
type Config struct {
	Registry   *scan.Registry
	Store      *store.Store
	Health     scan.HealthChecker
	Interfaces func() ([]netinfo.Iface, error)
	Gateway    func() (string, error)
	BaseCtx    context.Context
	// Ledger is the open-problem ledger the scans write (the same one as
	// the registry's scan.Deps.Ledger); Close saves it. nil: none.
	Ledger *problems.Ledger
	// Chat is the team chat connection (the one the /api/bubbles routes
	// use): Start puts its alert router on every scan, and Close gives its
	// outbox a last chance to send. nil: no team chat.
	Chat *chat.Manager
}

// chatFlush is how long Close waits for the team chat outbox (v0.6 4.9).
const chatFlush = 3 * time.Second

// Service implements the application operations.
type Service struct {
	cfg Config

	// saveMu makes each settings save (SaveSettings, IgnoreMAC) one step:
	// the write, ApplySettings and the hooks. hooksMu guards onSaved, the
	// OnSettingsSaved hooks in call order.
	saveMu  sync.Mutex
	hooksMu sync.Mutex
	onSaved []func(model.Settings)

	// healthMu guards lastHealth, the report of the last on-demand health
	// run (RunHealth).
	healthMu   sync.Mutex
	lastHealth *model.HealthReport

	// windowMu guards windowPort, the window's port when this service is
	// the netdiag window's (SetWindow).
	windowMu   sync.Mutex
	windowPort func() int

	logsMu   sync.Mutex
	logs     map[string]*EventLog
	logOrder []string
}

// New returns a Service with defaults applied.
func New(cfg Config) *Service {
	if cfg.Interfaces == nil {
		cfg.Interfaces = netinfo.Interfaces
	}
	if cfg.Health == nil {
		cfg.Health = health.NewRunner()
	}
	if cfg.Gateway == nil {
		cfg.Gateway = DefaultGateway
	}
	if cfg.BaseCtx == nil {
		cfg.BaseCtx = context.Background()
	}
	return &Service{cfg: cfg, logs: map[string]*EventLog{}}
}

// Registry returns the scan registry.
func (s *Service) Registry() *scan.Registry { return s.cfg.Registry }

// Close saves what the service still holds in memory before netdiag exits
// (Quit): it gives the team chat outbox up to 3 s to send what waits and
// saves the rest, then saves the open-problem ledger's last changes (the
// outbox marks in it what it posted). Call it after the scans have
// stopped.
func (s *Service) Close() error {
	var errs []error
	if s.cfg.Chat != nil {
		if err := s.cfg.Chat.Flush(chatFlush); err != nil {
			errs = append(errs, err)
		}
	}
	if s.cfg.Ledger != nil {
		if err := s.cfg.Ledger.Flush(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// HasStore reports whether a data store is configured.
func (s *Service) HasStore() bool { return s.cfg.Store != nil }

func (s *Service) needStore() error {
	if s.cfg.Store == nil {
		return ErrNoStore
	}
	return nil
}

// DefaultGateway is the production Config.Gateway.
func DefaultGateway() (string, error) {
	r, err := netinfo.DefaultRoute()
	if err != nil {
		return "", err
	}
	if !r.Gateway.IsValid() {
		return "", errors.New("no default gateway")
	}
	return r.Gateway.String(), nil
}
