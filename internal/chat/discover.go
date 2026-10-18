package chat

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"netdiag/internal/bubbles"
	"netdiag/internal/mdns"
)

// ServiceType is the DNS-SD type a bubbles server announces.
const ServiceType = "_bubbles._tcp"

// discoverWindow is how long the wizard looks for chat servers.
const discoverWindow = 3 * time.Second

// Found is a chat server on the local network: what its mDNS announcement
// says, or what the bubbles tray app on this computer uses.
type Found struct {
	Name        string `json:"name"`
	Address     string `json:"address"` // its IPv4 address (or .local name)
	Port        int    `json:"port"`    // HTTPS
	OnboardPort int    `json:"onboardPort"`
	// FPPrefix is the TXT "fp" key: the first 16 hex digits of the CA
	// fingerprint. It is only a hint: the person compares the fingerprint
	// itself, and the page never shows this as a check (critic #13).
	FPPrefix string `json:"fpPrefix,omitempty"`
}

// Discovery is the answer of POST /api/bubbles/discover.
type Discovery struct {
	Servers    []Found `json:"servers"`
	TrayServer *Found  `json:"trayServer"`
}

// BrowseFunc is mdns.Browse; tests replace it.
type BrowseFunc func(ctx context.Context, iface string, services []string, window time.Duration) ([]mdns.Record, error)

// Discover looks for chat servers for about 3 s with netdiag's own mDNS on
// iface ("" is the default route), and reads the server the bubbles tray
// app on this computer uses. Only the tray's server address and CA
// fingerprint are read; its sign-in token lives in the system keychain and
// is never touched. Discoveries run one at a time.
func (m *Manager) Discover(ctx context.Context, iface string) (Discovery, error) {
	m.discoverMu.Lock()
	defer m.discoverMu.Unlock()
	out := Discovery{Servers: []Found{}}
	recs, err := m.opt.Browse(ctx, iface, []string{ServiceType}, discoverWindow)
	if err != nil && ctx.Err() != nil {
		return out, ctx.Err()
	}
	out.Servers = append(out.Servers, foundIn(recs)...)
	slices.SortFunc(out.Servers, func(a, b Found) int { return strings.Compare(a.Name+a.Address, b.Name+b.Address) })
	m.mu.Lock()
	m.found = slices.Clone(out.Servers)
	m.mu.Unlock()
	if tr, ok := m.trayServer(ctx); ok {
		out.TrayServer = &tr
	}
	return out, nil
}

func netJoin(ip string, port int) string { return ip + ":" + strconv.Itoa(port) }

// trayConfig is the part of the bubbles tray's config.json netdiag reads
// (bubbles tray/internal/core/config.go). The device token is not in it.
type trayConfig struct {
	ServerURL     string `json:"server_url"`
	OnboardingURL string `json:"onboarding_url"`
	CAFingerprint string `json:"ca_fingerprint"`
}

// TrayConfigPath is where the bubbles tray app keeps its config:
// <user config dir>/bubbles-tray/config.json.
func TrayConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bubbles-tray", "config.json"), nil
}

// trayServer is the chat server the bubbles tray app on this computer is
// signed in to, with its name when the server answers within 2 s (the
// tray pinned its CA, so the pin is checked).
func (m *Manager) trayServer(ctx context.Context) (Found, bool) {
	if m.opt.TrayConfig == nil {
		return Found{}, false
	}
	p, err := m.opt.TrayConfig()
	if err != nil {
		return Found{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			m.opt.Logf("team chat: could not read the bubbles tray config: %v", err)
		}
		return Found{}, false
	}
	var c trayConfig
	if json.Unmarshal(b, &c) != nil {
		return Found{}, false
	}
	t, err := targetOf(c.ServerURL, c.OnboardingURL)
	if err != nil {
		return Found{}, false
	}
	f := Found{Name: t.Host, Address: t.Host, Port: t.Port, OnboardPort: t.OnboardPort}
	if fp, err := bubbles.NormalizeFingerprint(c.CAFingerprint); err == nil {
		f.FPPrefix = fp[len("sha256:") : len("sha256:")+16]
		sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if name := serverName(sctx, t, fp); name != "" {
			f.Name = name
		}
	}
	return f, true
}

// serverName is the instance name the server reports (GET /status), or
// "" when it does not answer with the pinned CA.
func serverName(ctx context.Context, t Target, fp string) string {
	c, err := bubbles.New(bubbles.Config{BaseURL: t.BaseURL(), CAFingerprint: fp})
	if err != nil {
		return ""
	}
	st, err := c.Status(ctx)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(st.InstanceName)
}
