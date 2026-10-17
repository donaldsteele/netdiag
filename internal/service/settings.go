package service

import (
	"net"
	"strings"

	"netdiag/internal/model"
	"netdiag/internal/store"
)

// LoadSettings returns persisted settings.
func (s *Service) LoadSettings() (model.Settings, error) {
	if err := s.needStore(); err != nil {
		return model.Settings{}, err
	}
	return s.cfg.Store.LoadSettings()
}

// RedactedSettings returns settings with the SNMP community hidden.
func (s *Service) RedactedSettings() (model.Settings, error) {
	set, err := s.LoadSettings()
	if err != nil {
		return set, err
	}
	return RedactSettings(set), nil
}

// RedactSettings returns a copy of s with the SNMP community hidden.
func RedactSettings(s model.Settings) model.Settings {
	if s.Community != "" {
		s.Community = model.Redacted
	}
	return s
}

// SaveSettings validates, persists and live-applies settings.
func (s *Service) SaveSettings(in model.Settings) (model.Settings, error) {
	if err := s.needStore(); err != nil {
		return model.Settings{}, err
	}
	norm, err := store.NormalizeSettings(in)
	if err != nil {
		return norm, &BadRequestError{Msg: err.Error()}
	}
	out, err := s.cfg.Store.SaveSettings(norm)
	if err != nil {
		return out, err
	}
	if sc, ok := s.cfg.Registry.Running(); ok {
		sc.ApplySettings(out)
	}
	if s.cfg.OnSettingsSaved != nil {
		s.cfg.OnSettingsSaved(out)
	}
	return out, nil
}

// SetOnSettingsSaved installs the post-save hook. Call before serving.
func (s *Service) SetOnSettingsSaved(f func(model.Settings)) { s.cfg.OnSettingsSaved = f }

// IgnoreMAC adds a MAC to the ignored-conflict list.
func (s *Service) IgnoreMAC(mac string) error {
	if err := s.needStore(); err != nil {
		return err
	}
	hw, err := net.ParseMAC(strings.TrimSpace(mac))
	if err != nil {
		return &BadRequestError{Msg: "invalid mac"}
	}
	m := strings.ToUpper(hw.String())
	set, err := s.cfg.Store.LoadSettings()
	if err != nil {
		return err
	}
	found := false
	for _, x := range set.IgnoredConflictMACs {
		if strings.EqualFold(x, m) {
			found = true
		}
	}
	if !found {
		set.IgnoredConflictMACs = append(set.IgnoredConflictMACs, m)
	}
	_, err = s.SaveSettings(set)
	return err
}
