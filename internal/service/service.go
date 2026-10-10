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
	// OnSettingsSaved, if set, is called after every successful SaveSettings.
	OnSettingsSaved func(model.Settings)
}

// Service implements the application operations.
type Service struct {
	cfg Config

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
