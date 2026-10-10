package service

import (
	"context"
	"errors"

	"netdiag/internal/model"
	"netdiag/internal/scan"
)

// ProbeHost probes a single local address without touching any scan or the
// history. Addresses outside local interface subnets are a BadRequestError.
func (s *Service) ProbeHost(ctx context.Context, ip, community string) (model.Host, error) {
	h, err := s.cfg.Registry.Orchestrator().ProbeHost(ctx, ip, community)
	if errors.Is(err, scan.ErrNotLocal) {
		return h, &BadRequestError{Msg: "ip " + ip + " is not on a local interface subnet"}
	}
	return h, err
}
