// Package chat connects netdiag to a bubbles team chat server. This file
// keeps the saved connection in bubbles.json; chat.Manager (v0.6 M10b) is
// its one writer. The app token lives only sealed in that file (DPAPI on
// Windows, internal/secret) and only in memory otherwise: it never goes
// into settings, an API or MCP answer, a log line or an export.
package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"netdiag/internal/bubbles"
	"netdiag/internal/secret"
	"netdiag/internal/store"
)

// connectionSchema is bubbles.json's schemaVersion.
const connectionSchema = 1

// tokenPrefixLen is how much of a token bubbles shows in Admin > Apps.
const tokenPrefixLen = 8

// Server is the chat server netdiag paired with: its name, its HTTPS and
// onboarding addresses, and the CA fingerprint the person confirmed.
type Server struct {
	Name          string `json:"name"`
	BaseURL       string `json:"baseURL"`
	OnboardingURL string `json:"onboardingURL"`
	CAFingerprint string `json:"caFingerprint"`
}

// Channel is the channel alerts go to.
type Channel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Connection is the saved connection to team chat. The token is
// unexported and Connection prints without it, so marshalling or logging
// a Connection never shows it; only Token returns it.
type Connection struct {
	Server   Server    `json:"server"`
	AppName  string    `json:"appName"`
	Channel  Channel   `json:"channel"`
	Scopes   []string  `json:"scopes"`
	PairedAt time.Time `json:"pairedAt"`
	token    string
}

// NewConnection returns a connection that carries token.
func NewConnection(s Server, appName string, scopes []string, token string, pairedAt time.Time) Connection {
	return Connection{Server: s, AppName: appName, Scopes: scopes, PairedAt: pairedAt, token: token}
}

// Token is the app token (bub_...), or "" when it could not be opened.
func (c Connection) Token() string { return c.token }

// TokenPrefix is the start of the token that bubbles shows in Admin > Apps,
// so the person can tell which app is netdiag's.
func (c Connection) TokenPrefix() string {
	if len(c.token) < tokenPrefixLen {
		return ""
	}
	return c.token[:tokenPrefixLen]
}

// String describes the connection without the token.
func (c Connection) String() string {
	return fmt.Sprintf("team chat %s (%s), channel %s", c.Server.Name, c.Server.BaseURL, c.Channel.Name)
}

// GoString is String, so %#v does not print the token either.
func (c Connection) GoString() string { return c.String() }

// ClientConfig is the bubbles client configuration for this connection.
func (c Connection) ClientConfig() bubbles.Config {
	return bubbles.Config{BaseURL: c.Server.BaseURL, Token: c.token, CAFingerprint: c.Server.CAFingerprint}
}

// connectionFile is bubbles.json on disk (v0.6 plan §4.15).
type connectionFile struct {
	SchemaVersion int           `json:"schemaVersion"`
	Server        Server        `json:"server"`
	AppName       string        `json:"appName"`
	Channel       Channel       `json:"channel"`
	Scopes        []string      `json:"scopes"`
	Token         secret.Sealed `json:"token"`
	PairedAt      time.Time     `json:"pairedAt"`
}

// DataFiles is the part of *store.Store that bubbles.json goes through.
type DataFiles interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte) error
	RemoveFile(name string) error
	QuarantineFile(name string) (string, error)
}

var (
	// ErrNotConnected means there is no usable bubbles.json.
	ErrNotConnected = errors.New("chat: not connected to team chat")
	// ErrNeedsReconnect means bubbles.json names a server but its token
	// cannot be opened here (the file came from another Windows user or
	// computer, or is damaged). LoadConnection still returns the server,
	// pin and channel, so Reconnect can reuse them.
	ErrNeedsReconnect = errors.New("chat: team chat needs reconnecting")
)

// LoadConnection reads bubbles.json. A missing file is ErrNotConnected. An
// unreadable one is renamed <name>.bad-<time> and is ErrNotConnected too.
// A token that cannot be opened is ErrNeedsReconnect, with the rest of the
// connection returned.
func LoadConnection(files DataFiles) (Connection, error) {
	data, err := files.ReadFile(store.FileBubbles)
	if errors.Is(err, fs.ErrNotExist) {
		return Connection{}, ErrNotConnected
	}
	if err != nil {
		return Connection{}, err
	}
	var f connectionFile
	if err := json.Unmarshal(data, &f); err != nil || !validFile(f) {
		bad, qerr := files.QuarantineFile(store.FileBubbles)
		if qerr != nil {
			return Connection{}, fmt.Errorf("%w: %s is unreadable and could not be set aside: %v", ErrNotConnected, store.FileBubbles, qerr)
		}
		return Connection{}, fmt.Errorf("%w: %s was unreadable and is now %s", ErrNotConnected, store.FileBubbles, bad)
	}
	c := Connection{Server: f.Server, AppName: f.AppName, Channel: f.Channel, Scopes: f.Scopes, PairedAt: f.PairedAt}
	tok, err := secret.Open(f.Token)
	if err != nil {
		return c, fmt.Errorf("%w: %w", ErrNeedsReconnect, err)
	}
	c.token = string(tok)
	return c, nil
}

// validFile reports whether f names a server netdiag can talk to.
func validFile(f connectionFile) bool {
	if f.SchemaVersion != connectionSchema || !strings.HasPrefix(f.Server.BaseURL, "https://") {
		return false
	}
	_, err := bubbles.NormalizeFingerprint(f.Server.CAFingerprint)
	return err == nil
}

// SaveConnection writes c to bubbles.json with its token sealed.
func SaveConnection(files DataFiles, c Connection) error {
	if c.token == "" {
		return errors.New("chat: a connection without a token cannot be saved")
	}
	f := connectionFile{SchemaVersion: connectionSchema, Server: c.Server, AppName: c.AppName,
		Channel: c.Channel, Scopes: c.Scopes, PairedAt: c.PairedAt.UTC()}
	if !validFile(f) {
		return fmt.Errorf("chat: the server address %q or fingerprint is not valid", c.Server.BaseURL)
	}
	sealed, err := secret.Seal([]byte(c.token))
	if err != nil {
		return err
	}
	f.Token = sealed
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return files.WriteFile(store.FileBubbles, data)
}

// RemoveConnection deletes bubbles.json (Disconnect). A missing file is not
// an error.
func RemoveConnection(files DataFiles) error {
	return files.RemoveFile(store.FileBubbles)
}
