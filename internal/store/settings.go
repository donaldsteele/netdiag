package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"netdiag/internal/model"
)

const settingsFile = "settings.json"

// DefaultPrinterStatusPollSec is the default live-mode printer status poll.
const DefaultPrinterStatusPollSec = 5

// DefaultSettings returns the built-in defaults.
func DefaultSettings() model.Settings {
	return model.Settings{
		SchemaVersion:        model.SettingsSchemaVersion,
		Community:            model.DefaultCommunity,
		PrinterStatusPollSec: DefaultPrinterStatusPollSec,
		WebServer:            model.SettingsWebServer{ExpectStatus: 200},
		InternetCheckURL:     "http://connectivitycheck.gstatic.com/generate_204",
		Alerts: model.SettingsAlerts{
			Sound: true, CooldownSec: 300, SupplyThresholdPct: 15,
			Triggers: model.SettingsTriggers{
				PrinterOffline: true, SupplyLow: true, PrinterAlert: true,
				NewDevice: true, IPConflict: true, HealthFailed: true,
			},
		},
		NoInternetNetworks:  []string{},
		IgnoredConflictMACs: []string{},
		Chat:                model.SettingsChat{MinSeverity: model.ChatSeverityWarning},
	}
}

func checkURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s must be an http or https URL", name)
	}
	return nil
}

// NormalizeSettings validates s and returns a normalized copy. It is strict:
// the first invalid field is an error that names it (PUT /api/settings).
func NormalizeSettings(s model.Settings) (model.Settings, error) {
	n, _, err := normalize(s, false)
	return n, err
}

// RepairSettings is the lenient NormalizeSettings used for settings read
// from disk: each invalid field is reset to its default on its own, and
// every other field is kept. It returns the normalized settings and the
// JSON paths it reset (such as "alerts.supplyThresholdPct"), in field order.
// Blank and zero values that mean "the default" (a blank community, a zero
// poll interval) are normalized as on save and are not reported.
func RepairSettings(s model.Settings) (model.Settings, []string) {
	n, fixed, _ := normalize(s, true)
	return n, fixed
}

// normalize holds the one set of settings rules. Strict mode stops at the
// first invalid field; repair mode resets it and records its path, so the
// two modes never disagree about what is valid.
func normalize(s model.Settings, repair bool) (model.Settings, []string, error) {
	def := DefaultSettings()
	var fixed []string
	// bad handles an invalid field: an error in strict mode, else the
	// field is reset and its path remembered.
	bad := func(path string, err error, reset func()) error {
		if !repair {
			return err
		}
		reset()
		fixed = append(fixed, path)
		return nil
	}
	s.Community = strings.TrimSpace(s.Community)
	if s.Community == "" {
		s.Community = model.DefaultCommunity
	}
	if s.PrinterStatusPollSec == 0 {
		s.PrinterStatusPollSec = DefaultPrinterStatusPollSec
	}
	if s.PrinterStatusPollSec < 2 || s.PrinterStatusPollSec > 300 {
		if err := bad("printerStatusPollSec", errors.New("printerStatusPollSec must be 2-300"),
			func() { s.PrinterStatusPollSec = DefaultPrinterStatusPollSec }); err != nil {
			return s, nil, err
		}
	}
	s.WebServer.URL = strings.TrimSpace(s.WebServer.URL)
	if s.WebServer.URL != "" {
		if err := checkURL("webServer.url", s.WebServer.URL); err != nil {
			if err := bad("webServer.url", err, func() { s.WebServer.URL = "" }); err != nil {
				return s, nil, err
			}
		}
	}
	if s.WebServer.ExpectStatus == 0 {
		s.WebServer.ExpectStatus = 200
	}
	if s.WebServer.ExpectStatus < 100 || s.WebServer.ExpectStatus > 599 {
		if err := bad("webServer.expectStatus", errors.New("webServer.expectStatus must be 100-599"),
			func() { s.WebServer.ExpectStatus = def.WebServer.ExpectStatus }); err != nil {
			return s, nil, err
		}
	}
	if s.WebServer.TrustedCert = strings.TrimSpace(s.WebServer.TrustedCert); s.WebServer.TrustedCert != "" {
		if fp, ok := model.NormalizeFingerprint(s.WebServer.TrustedCert); ok {
			s.WebServer.TrustedCert = fp
		} else if err := bad("webServer.trustedCert",
			errors.New("webServer.trustedCert must be a SHA-256 certificate fingerprint (64 hex digits)"),
			func() { s.WebServer.TrustedCert = "" }); err != nil {
			return s, nil, err
		}
	}
	s.InternetCheckURL = strings.TrimSpace(s.InternetCheckURL)
	if s.InternetCheckURL == "" {
		s.InternetCheckURL = def.InternetCheckURL
	}
	if err := checkURL("internetCheckURL", s.InternetCheckURL); err != nil {
		if err := bad("internetCheckURL", err, func() { s.InternetCheckURL = def.InternetCheckURL }); err != nil {
			return s, nil, err
		}
	}
	// The networks without internet on purpose (v0.6-D15): network ids,
	// each once, the newest MaxNoInternetNetworks kept.
	nets := []string{}
	badNet := false
	for _, id := range s.NoInternetNetworks {
		id = strings.ToLower(strings.TrimSpace(id))
		if !model.ValidNetworkID(id) {
			if !repair {
				return s, nil, fmt.Errorf("noInternetNetworks: %q is not a network id", id)
			}
			badNet = true
			continue
		}
		nets = slices.DeleteFunc(nets, func(x string) bool { return x == id })
		nets = append(nets, id)
	}
	if len(nets) > model.MaxNoInternetNetworks {
		nets = nets[len(nets)-model.MaxNoInternetNetworks:]
	}
	s.NoInternetNetworks = nets
	if badNet {
		fixed = append(fixed, "noInternetNetworks")
	}
	if s.Alerts.SupplyThresholdPct < 1 || s.Alerts.SupplyThresholdPct > 100 {
		if err := bad("alerts.supplyThresholdPct", errors.New("alerts.supplyThresholdPct must be 1-100"),
			func() { s.Alerts.SupplyThresholdPct = def.Alerts.SupplyThresholdPct }); err != nil {
			return s, nil, err
		}
	}
	if s.Alerts.CooldownSec < 30 || s.Alerts.CooldownSec > 3600 {
		if err := bad("alerts.cooldownSec", errors.New("alerts.cooldownSec must be 30-3600"),
			func() { s.Alerts.CooldownSec = def.Alerts.CooldownSec }); err != nil {
			return s, nil, err
		}
	}
	seen := map[string]bool{}
	macs := []string{}
	dropped := false
	for _, m := range s.IgnoredConflictMACs {
		hw, err := net.ParseMAC(strings.TrimSpace(m))
		if err != nil {
			if !repair {
				return s, nil, fmt.Errorf("invalid MAC %q", m)
			}
			dropped = true
			continue
		}
		n := strings.ToUpper(hw.String())
		if !seen[n] {
			seen[n] = true
			macs = append(macs, n)
		}
	}
	s.IgnoredConflictMACs = macs
	if dropped {
		fixed = append(fixed, "ignoredConflictMACs")
	}
	s.Chat.MinSeverity = strings.ToLower(strings.TrimSpace(s.Chat.MinSeverity))
	if s.Chat.MinSeverity == "" {
		s.Chat.MinSeverity = def.Chat.MinSeverity
	}
	if !model.ValidChatSeverity(s.Chat.MinSeverity) {
		if err := bad("chat.minSeverity", errors.New("chat.minSeverity must be info, warning or critical"),
			func() { s.Chat.MinSeverity = def.Chat.MinSeverity }); err != nil {
			return s, nil, err
		}
	}
	if s.SchemaVersion < model.SettingsSchemaVersion {
		s.SchemaVersion = model.SettingsSchemaVersion
	}
	return s, fixed, nil
}

// LoadSettings reads settings.json. A missing or unreadable file yields
// defaults, missing fields take their default values, and each invalid field
// is reset to its default on its own (RepairSettings); bad content is never
// an error. Repaired reports the fields the load reset.
func (s *Store) LoadSettings() (model.Settings, error) {
	set, _, err := s.LoadSettingsRepaired()
	return set, err
}

// LoadSettingsRepaired is LoadSettings that also returns the JSON paths of
// the fields it reset, or nil when every field was valid.
func (s *Store) LoadSettingsRepaired() (model.Settings, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set, fixed, err := s.readSettings()
	s.repaired = fixed
	return set, append([]string(nil), fixed...), err
}

// Repaired returns the fields the last settings load reset, or nil. A
// successful save clears it, since the saved file is valid.
func (s *Store) Repaired() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.repaired...)
}

// readSettings does the work of LoadSettingsRepaired; s.mu must be held.
func (s *Store) readSettings() (model.Settings, []string, error) {
	def := DefaultSettings()
	data, err := os.ReadFile(filepath.Join(s.dir, settingsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return def, nil, nil
		}
		return def, nil, err
	}
	cur := def
	cur.SchemaVersion = 0
	var fixed []string
	if err := json.Unmarshal(data, &cur); err != nil {
		// A value of the wrong type (such as "cooldownSec": "60") leaves
		// that field at its default and the rest decoded, but json names
		// only the first such value; wrongTypes finds them all. Anything
		// else (bad JSON, a document that is not an object) is unreadable:
		// defaults.
		var te *json.UnmarshalTypeError
		if !errors.As(err, &te) || te.Field == "" {
			return def, nil, nil
		}
		fixed = append(wrongTypes(data), te.Field)
	}
	if cur.IgnoredConflictMACs == nil {
		cur.IgnoredConflictMACs = []string{}
	}
	n, more := RepairSettings(cur)
	return n, repairOrder(append(fixed, more...)), nil
}

// RepairPaths returns, in field order, every JSON path a settings load can
// list in repaired: each field ("alerts.cooldownSec") and each object of
// fields ("alerts", "alerts.triggers"), since an object of the wrong type
// is reset as a whole. schemaVersion is left out: it is not a setting, and
// a bad one is put right without a word. The page has a label for each
// (TestSettingsRepairedUI).
func RepairPaths() []string {
	var out []string
	var walk func(t reflect.Type, prefix string)
	walk = func(t reflect.Type, prefix string) {
		for _, f := range jsonFields(t) {
			p := prefix + f.name
			if p != pathSchemaVersion {
				out = append(out, p)
			}
			if f.typ.Kind() == reflect.Struct {
				walk(f.typ, p+".")
			}
		}
	}
	walk(reflect.TypeFor[model.Settings](), "")
	return out
}

const pathSchemaVersion = "schemaVersion"

// repairOrder sorts repaired paths into field order, drops duplicates and
// drops schemaVersion (see RepairPaths).
func repairOrder(paths []string) []string {
	all := RepairPaths()
	rank := func(p string) int {
		if i := slices.Index(all, p); i >= 0 {
			return i
		}
		return len(all)
	}
	var out []string
	for _, p := range paths {
		if p != pathSchemaVersion && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.SortStableFunc(out, func(a, b string) int { return rank(a) - rank(b) })
	return out
}

// wrongTypes returns the path of every value in a settings document that
// has the wrong JSON type, such as a string for a number or a number for
// an object. json.Unmarshal skips each one, so the field keeps its default.
// Keys match field names as json matches them: exactly, else ignoring case.
func wrongTypes(data []byte) []string {
	var out []string
	var walk func(obj map[string]json.RawMessage, t reflect.Type, prefix string)
	walk = func(obj map[string]json.RawMessage, t reflect.Type, prefix string) {
		for _, f := range jsonFields(t) {
			raw, ok := obj[f.name]
			if !ok {
				for _, k := range slices.Sorted(maps.Keys(obj)) {
					if strings.EqualFold(k, f.name) {
						raw, ok = obj[k], true
						break
					}
				}
			}
			if !ok {
				continue
			}
			p := prefix + f.name
			if f.typ.Kind() == reflect.Struct {
				var sub map[string]json.RawMessage
				if err := json.Unmarshal(raw, &sub); err != nil {
					out = append(out, p)
				} else {
					walk(sub, f.typ, p+".")
				}
				continue
			}
			if err := json.Unmarshal(raw, reflect.New(f.typ).Interface()); err != nil {
				out = append(out, p)
			}
		}
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) == nil {
		walk(top, reflect.TypeFor[model.Settings](), "")
	}
	return out
}

type jsonField struct {
	name string
	typ  reflect.Type
}

// jsonFields lists the exported fields of struct type t with their JSON
// names, in field order.
func jsonFields(t reflect.Type) []jsonField {
	var out []jsonField
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, jsonField{name, f.Type})
	}
	return out
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
	s.repaired = nil
	return n, nil
}
