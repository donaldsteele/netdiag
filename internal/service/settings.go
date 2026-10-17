package service

import (
	"net"
	"slices"
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

// RedactedSettings returns settings with the SNMP community and any
// password in the web server URL hidden.
func (s *Service) RedactedSettings() (model.Settings, error) {
	set, err := s.LoadSettings()
	if err != nil {
		return set, err
	}
	return RedactSettings(set), nil
}

// RedactSettings returns a copy of s with the SNMP community hidden, and
// the password of a user name and password in the web server URL
// (http://user:pass@host/) masked the way the web server check shows it.
func RedactSettings(s model.Settings) model.Settings {
	if s.Community != "" {
		s.Community = model.Redacted
	}
	s.WebServer.URL = model.RedactURL(s.WebServer.URL)
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
	s.hooksMu.Lock()
	hooks := slices.Clone(s.onSaved)
	s.hooksMu.Unlock()
	for _, f := range hooks {
		f(out)
	}
	return out, nil
}

// OnSettingsSaved adds a hook that is called, in the order added, after
// every successful SaveSettings (the server's MCP gate; later the team chat
// manager). Hooks run on the saving goroutine and must not save settings.
func (s *Service) OnSettingsSaved(f func(model.Settings)) {
	s.hooksMu.Lock()
	defer s.hooksMu.Unlock()
	s.onSaved = append(s.onSaved, f)
}

// LoadSettingsRepaired returns persisted settings and the JSON paths of the
// fields that were not valid and were reset when they were read (F7).
func (s *Service) LoadSettingsRepaired() (model.Settings, []string, error) {
	if err := s.needStore(); err != nil {
		return model.Settings{}, nil, err
	}
	return s.cfg.Store.LoadSettingsRepaired()
}

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
