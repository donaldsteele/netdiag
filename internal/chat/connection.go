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
// a Connection never shows it; only Token and ClientConfig return it. The
// token sits behind a pointer: fmt prints a Connection held in another
// struct's unexported field (chat.Manager's) field by field without calling
// String, and then shows the pointer's address, not the token.
type Connection struct {
	Server   Server    `json:"server"`
	AppName  string    `json:"appName"`
	Channel  Channel   `json:"channel"`
	Scopes   []string  `json:"scopes"`
	PairedAt time.Time `json:"pairedAt"`
	token    *string   // nil when there is none; never changed in place
}

// NewConnection returns a connection that carries token.
func NewConnection(s Server, appName string, scopes []string, token string, pairedAt time.Time) Connection {
	return Connection{Server: s, AppName: appName, Scopes: scopes, PairedAt: pairedAt, token: &token}
}

// Token is the app token (bub_...), or "" when it could not be opened.
func (c Connection) Token() string {
	if c.token == nil {
		return ""
	}
	return *c.token
}

// TokenPrefix is the start of the token that bubbles shows in Admin > Apps,
// so the person can tell which app is netdiag's.
func (c Connection) TokenPrefix() string {
	if t := c.Token(); len(t) >= tokenPrefixLen {
		return t[:tokenPrefixLen]
	}
	return ""
}

// String describes the connection without the token.
func (c Connection) String() string {
	return fmt.Sprintf("team chat %s (%s), channel %s", c.Server.Name, c.Server.BaseURL, c.Channel.Name)
}

// GoString is String, so %#v does not print the token either.
func (c Connection) GoString() string { return c.String() }

// ClientConfig is the bubbles client configuration for this connection.
// It carries the token; bubbles.Config prints without it.
func (c Connection) ClientConfig() bubbles.Config {
	return bubbles.Config{BaseURL: c.Server.BaseURL, Token: c.Token(), CAFingerprint: c.Server.CAFingerprint}
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
	RemoveQuarantined(name string) error
}

var (
	// ErrNotConnected means there is no usable bubbles.json.
	ErrNotConnected = errors.New("chat: not connected to team chat")
	// ErrNeedsReconnect means bubbles.json names a server but its token
	// cannot be opened here (the file came from another Windows user or
	// computer, or is damaged), or the file was written by a newer netdiag.
	// LoadConnection still returns the server, pin and channel, so
	// Reconnect can reuse them.
	ErrNeedsReconnect = errors.New("chat: team chat needs reconnecting")
)

// LoadConnection reads bubbles.json. A missing file is ErrNotConnected. An
// unreadable one is set aside as <name>.bad-<time> without its token and is
// ErrNotConnected too. A token that cannot be opened, or a file from a
// newer netdiag, is ErrNeedsReconnect, with the rest of the connection
// returned and the file left in place.
func LoadConnection(files DataFiles) (Connection, error) {
	data, err := files.ReadFile(store.FileBubbles)
	if errors.Is(err, fs.ErrNotExist) {
		return Connection{}, ErrNotConnected
	}
	if err != nil {
		return Connection{}, err
	}
	var f connectionFile
	err = json.Unmarshal(data, &f)
	c := Connection{Server: f.Server, AppName: f.AppName, Channel: f.Channel, Scopes: f.Scopes, PairedAt: f.PairedAt}
	if err == nil && f.SchemaVersion > connectionSchema && validServer(f.Server) {
		// A later netdiag wrote it (netdiag was downgraded): keep the file
		// for that version and offer Reconnect with the same server and pin.
		return c, fmt.Errorf("%w: %s was written by a newer netdiag (schemaVersion %d)", ErrNeedsReconnect, store.FileBubbles, f.SchemaVersion)
	}
	if err != nil || !validFile(f) {
		return Connection{}, setAside(files, data)
	}
	tok, err := secret.Open(f.Token)
	if err != nil {
		return c, fmt.Errorf("%w: %w", ErrNeedsReconnect, err)
	}
	t := string(tok)
	c.token = &t
	return c, nil
}

// setAside renames an unreadable bubbles.json to bubbles.json.bad-<time>
// and returns the ErrNotConnected that says so. When the file is a JSON
// object its token is taken out first, so the copy keeps the server, pin
// and channel for a person to look at but no token. RemoveConnection
// deletes the copies too.
func setAside(files DataFiles, data []byte) error {
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) == nil {
		if _, ok := m["token"]; ok {
			delete(m, "token")
			b, err := json.MarshalIndent(m, "", "  ")
			if err == nil {
				err = files.WriteFile(store.FileBubbles, b)
			}
			if err != nil {
				return fmt.Errorf("%w: %s is unreadable and could not be set aside: %v", ErrNotConnected, store.FileBubbles, err)
			}
		}
	}
	bad, err := files.QuarantineFile(store.FileBubbles)
	if err != nil {
		return fmt.Errorf("%w: %s is unreadable and could not be set aside: %v", ErrNotConnected, store.FileBubbles, err)
	}
	return fmt.Errorf("%w: %s was unreadable and is now %s", ErrNotConnected, store.FileBubbles, bad)
}

// validFile reports whether f is a schemaVersion 1 file that names a
// server netdiag can talk to.
func validFile(f connectionFile) bool {
	return f.SchemaVersion == connectionSchema && validServer(f.Server)
}

// validServer reports whether s has an https address and a CA fingerprint.
func validServer(s Server) bool {
	if !strings.HasPrefix(s.BaseURL, "https://") {
		return false
	}
	_, err := bubbles.NormalizeFingerprint(s.CAFingerprint)
	return err == nil
}

// SaveConnection writes c to bubbles.json with its token sealed.
func SaveConnection(files DataFiles, c Connection) error {
	if c.Token() == "" {
		return errors.New("chat: a connection without a token cannot be saved")
	}
	f := connectionFile{SchemaVersion: connectionSchema, Server: c.Server, AppName: c.AppName,
		Channel: c.Channel, Scopes: c.Scopes, PairedAt: c.PairedAt.UTC()}
	if !validFile(f) {
		return fmt.Errorf("chat: the server address %q or fingerprint is not valid", c.Server.BaseURL)
	}
	sealed, err := secret.Seal([]byte(c.Token()))
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

// RemoveConnection deletes bubbles.json and every copy of it set aside as
// unreadable (Disconnect), so no copy of the token stays in the data
// folder. A missing file is not an error.
func RemoveConnection(files DataFiles) error {
	if err := files.RemoveFile(store.FileBubbles); err != nil {
		return err
	}
	return files.RemoveQuarantined(store.FileBubbles)
}
