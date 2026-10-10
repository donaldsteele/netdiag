package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"netdiag/internal/model"
)

const settingsFile = "settings.json"

// DefaultSettings returns the built-in defaults.
func DefaultSettings() model.Settings {
	return model.Settings{
		SchemaVersion:    model.SettingsSchemaVersion,
		Community:        model.DefaultCommunity,
		WebServer:        model.SettingsWebServer{ExpectStatus: 200},
		InternetCheckURL: "http://connectivitycheck.gstatic.com/generate_204",
		Alerts: model.SettingsAlerts{
			Sound: true, CooldownSec: 300, SupplyThresholdPct: 15,
			Triggers: model.SettingsTriggers{
				PrinterOffline: true, SupplyLow: true, PrinterAlert: true,
				NewDevice: true, IPConflict: true, HealthFailed: true,
			},
		},
		IgnoredConflictMACs: []string{},
	}
}

func checkURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s must be an http or https URL", name)
	}
	return nil
}

// NormalizeSettings validates s and returns a normalized copy.
func NormalizeSettings(s model.Settings) (model.Settings, error) {
	s.Community = strings.TrimSpace(s.Community)
	if s.Community == "" {
		s.Community = model.DefaultCommunity
	}
	s.WebServer.URL = strings.TrimSpace(s.WebServer.URL)
	if s.WebServer.URL != "" {
		if err := checkURL("webServer.url", s.WebServer.URL); err != nil {
			return s, err
		}
	}
	if s.WebServer.ExpectStatus == 0 {
		s.WebServer.ExpectStatus = 200
	}
	if s.WebServer.ExpectStatus < 100 || s.WebServer.ExpectStatus > 599 {
		return s, errors.New("webServer.expectStatus must be 100-599")
	}
	s.InternetCheckURL = strings.TrimSpace(s.InternetCheckURL)
	if s.InternetCheckURL == "" {
		s.InternetCheckURL = DefaultSettings().InternetCheckURL
	}
	if err := checkURL("internetCheckURL", s.InternetCheckURL); err != nil {
		return s, err
	}
	if s.Alerts.SupplyThresholdPct < 1 || s.Alerts.SupplyThresholdPct > 100 {
		return s, errors.New("alerts.supplyThresholdPct must be 1-100")
	}
	if s.Alerts.CooldownSec < 30 || s.Alerts.CooldownSec > 3600 {
		return s, errors.New("alerts.cooldownSec must be 30-3600")
	}
	seen := map[string]bool{}
	macs := []string{}
	for _, m := range s.IgnoredConflictMACs {
		hw, err := net.ParseMAC(strings.TrimSpace(m))
		if err != nil {
			return s, fmt.Errorf("invalid MAC %q", m)
		}
		n := strings.ToUpper(hw.String())
		if !seen[n] {
			seen[n] = true
			macs = append(macs, n)
		}
	}
	s.IgnoredConflictMACs = macs
	if s.SchemaVersion < model.SettingsSchemaVersion {
		s.SchemaVersion = model.SettingsSchemaVersion
	}
	return s, nil
}

// LoadSettings reads settings.json. Missing, corrupt or invalid files yield
// defaults (missing fields take default values); bad content is never an error.
func (s *Store) LoadSettings() (model.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	def := DefaultSettings()
	data, err := os.ReadFile(filepath.Join(s.dir, settingsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return def, nil
		}
		return def, err
	}
	cur := def
	cur.SchemaVersion = 0
	if err := json.Unmarshal(data, &cur); err != nil {
		return def, nil
	}
	if cur.IgnoredConflictMACs == nil {
		cur.IgnoredConflictMACs = []string{}
	}
	n, err := NormalizeSettings(cur)
	if err != nil {
		return def, nil
	}
	return n, nil
}

// SaveSettings validates, normalizes and atomically stores settings. It
// returns the normalized document.
func (s *Store) SaveSettings(in model.Settings) (model.Settings, error) {
	n, err := NormalizeSettings(in)
	if err != nil {
		return in, err
	}
	data, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return in, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeAtomic(filepath.Join(s.dir, settingsFile), data); err != nil {
		return in, err
	}
	return n, nil
}
