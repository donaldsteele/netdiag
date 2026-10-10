package service

import (
	"errors"

	"netdiag/internal/diff"
	"netdiag/internal/model"
	"netdiag/internal/store"
)

// ListHistory returns saved scans (never nil).
func (s *Service) ListHistory() ([]model.HistoryEntry, error) {
	if err := s.needStore(); err != nil {
		return nil, err
	}
	list, err := s.cfg.Store.ListHistory()
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []model.HistoryEntry{}
	}
	return list, nil
}

// LoadScan loads a saved scan.
func (s *Service) LoadScan(id string) (model.ScanResult, error) {
	if err := s.needStore(); err != nil {
		return model.ScanResult{}, err
	}
	return s.cfg.Store.LoadScan(id)
}

// DeleteScan removes a saved scan.
func (s *Service) DeleteScan(id string) error {
	if err := s.needStore(); err != nil {
		return err
	}
	return s.cfg.Store.DeleteScan(id)
}

func resolveErr(err error, msg string) error {
	if errors.Is(err, store.ErrNotFound) {
		return &NotFoundError{Msg: msg}
	}
	return err
}

// Compare diffs two scans. An empty baseID selects the previous scan of the
// same CIDR.
func (s *Service) Compare(baseID, currentID string) (model.Diff, error) {
	if currentID == "" {
		return model.Diff{}, &BadRequestError{Msg: "current is required"}
	}
	cur, err := s.ResolveScan(currentID)
	if err != nil {
		return model.Diff{}, resolveErr(err, "current scan not found")
	}
	if baseID == "" {
		if s.cfg.Store == nil {
			return model.Diff{}, &NotFoundError{Msg: "no baseline available"}
		}
		e, ok, err := s.cfg.Store.Previous(cur.Request.CIDR, cur.ID)
		if err != nil {
			return model.Diff{}, err
		}
		if !ok {
			return model.Diff{}, &NotFoundError{Msg: "no previous scan of " + cur.Request.CIDR}
		}
		baseID = e.ID
	}
	if baseID == currentID || baseID == cur.ID {
		return model.Diff{}, &BadRequestError{Msg: "base and current must differ"}
	}
	base, err := s.ResolveScan(baseID)
	if err != nil {
		return model.Diff{}, resolveErr(err, "base scan not found")
	}
	return diff.Compare(base, cur), nil
}
