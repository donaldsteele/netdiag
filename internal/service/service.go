// Package service holds the business logic shared by the REST handlers and
// the MCP layer.
package service

import (
	"context"
	"errors"
	"sync"

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
}

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
// (Quit): the open-problem ledger's last changes. Call it after the scans
// have stopped.
func (s *Service) Close() error {
	if s.cfg.Ledger == nil {
		return nil
	}
	return s.cfg.Ledger.Flush()
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
