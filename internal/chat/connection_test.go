package chat

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"netdiag/internal/secret"
	"netdiag/internal/store"
)

const testToken = "bub_s3cr3tTOKENabcdefghijklmnopqrstuvwxyz0123456789ABCDEF"

var testServer = Server{
	Name:          "Office",
	BaseURL:       "https://192.168.1.20:28225",
	OnboardingURL: "http://192.168.1.20:28226",
	CAFingerprint: "sha256:" + strings.Repeat("3f2a9c10", 8),
}

func newStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st, dir
}

func testConnection() Connection {
	c := NewConnection(testServer, "netdiag", []string{"alerts:write", "channels:read", "messages:write", "files:write"},
		testToken, time.Date(2026, 10, 17, 10, 40, 0, 0, time.UTC))
	c.Channel = Channel{ID: "C1", Name: "netdiag-alerts"}
	return c
}

func readFile(t *testing.T, dir string) connectionFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, store.FileBubbles))
	if err != nil {
		t.Fatal(err)
	}
	var f connectionFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func writeFile(t *testing.T, dir string, f connectionFile) {
	t.Helper()
	b, _ := json.Marshal(f)
	if err := os.WriteFile(filepath.Join(dir, store.FileBubbles), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionRoundTrip(t *testing.T) {
	st, dir := newStore(t)
	want := testConnection()
	if err := SaveConnection(st, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConnection(st)
	if err != nil {
		t.Fatal(err)
	}
	if got.Token() != testToken || got.Server != want.Server || got.Channel != want.Channel || got.AppName != "netdiag" ||
		strings.Join(got.Scopes, ",") != strings.Join(want.Scopes, ",") || !got.PairedAt.Equal(want.PairedAt) {
		t.Fatalf("loaded %#v (token %q)", got, got.Token())
	}
	if got.TokenPrefix() != "bub_s3cr" {
		t.Fatalf("prefix %q", got.TokenPrefix())
	}
	cfg := got.ClientConfig()
	if cfg.BaseURL != testServer.BaseURL || cfg.Token != testToken || cfg.CAFingerprint != testServer.CAFingerprint {
		t.Fatalf("client config %+v", cfg)
	}
	f := readFile(t, dir)
	if f.SchemaVersion != 1 || f.Server != testServer || f.Channel.Name != "netdiag-alerts" {
		t.Fatalf("file %+v", f)
	}
}

// The acceptance check: the token exists only sealed, in bubbles.json.
func TestTokenOnlySealedInBubblesJSON(t *testing.T) {
	st, dir := newStore(t)
	if err := SaveConnection(st, testConnection()); err != nil {
		t.Fatal(err)
	}
	f := readFile(t, dir)
	switch runtime.GOOS {
	case "windows":
		if f.Token.Protected != secret.ProtectedDPAPI {
			t.Fatalf("token protected as %q, want dpapi", f.Token.Protected)
		}
	default:
		if f.Token.Protected != secret.ProtectedNone {
			t.Fatalf("token protected as %q, want none", f.Token.Protected)
		}
		info, err := os.Stat(filepath.Join(dir, store.FileBubbles))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("bubbles.json mode %v, %v; want 0600", info.Mode().Perm(), err)
		}
	}
	// Nothing in the data folder holds the token in clear (on Windows not
	// even bubbles.json), and nothing but bubbles.json was written.
	encoded := base64.StdEncoding.EncodeToString([]byte(testToken))
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if d.Name() != store.FileBubbles {
			t.Errorf("unexpected file %s", p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if runtime.GOOS == "windows" && (strings.Contains(string(b), testToken) || strings.Contains(string(b), encoded)) {
			t.Errorf("%s holds the token", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Marshalling or printing a Connection, the way an API answer, an MCP
// result or a log line would, never shows the token.
func TestConnectionNeverPrintsToken(t *testing.T) {
	c := testConnection()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{string(b), fmt.Sprint(c), fmt.Sprintf("%v %+v %#v %s", c, c, c, c), fmt.Sprintf("%+v", &c), c.String()} {
		if strings.Contains(s, "s3cr3t") {
			t.Fatalf("token shown in %q", s)
		}
	}
	if !strings.Contains(string(b), `"baseURL":"https://192.168.1.20:28225"`) {
		t.Fatalf("json %s", b)
	}
}

func TestLoadMissingIsNotConnected(t *testing.T) {
	st, _ := newStore(t)
	if _, err := LoadConnection(st); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("err = %v", err)
	}
	if err := RemoveConnection(st); err != nil {
		t.Fatalf("remove missing: %v", err)
	}
}

func TestRemoveConnection(t *testing.T) {
	st, dir := newStore(t)
	if err := SaveConnection(st, testConnection()); err != nil {
		t.Fatal(err)
	}
	if err := RemoveConnection(st); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, store.FileBubbles)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("bubbles.json still there: %v", err)
	}
}

// An unreadable bubbles.json is set aside as bubbles.json.bad-<time> and
// netdiag starts again not connected (v0.6 plan §4.15).
func TestLoadUnreadableIsQuarantined(t *testing.T) {
	good := connectionFile{SchemaVersion: 1, Server: testServer, Token: secret.Sealed{Protected: "dpapi", Data: "AAAA"}}
	for name, content := range map[string]string{
		"not json":         "{nope",
		"no schema":        `{"server":{"baseURL":"https://192.168.1.20:28225"}}`,
		"newer schema":     mustJSON(t, func() connectionFile { f := good; f.SchemaVersion = 2; return f }()),
		"plain http":       mustJSON(t, func() connectionFile { f := good; f.Server.BaseURL = "http://192.168.1.20:28225"; return f }()),
		"bad fingerprint":  mustJSON(t, func() connectionFile { f := good; f.Server.CAFingerprint = "abcd"; return f }()),
		"wrong JSON types": `{"schemaVersion":"1","server":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			st, dir := newStore(t)
			if err := os.WriteFile(filepath.Join(dir, store.FileBubbles), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConnection(st); !errors.Is(err, ErrNotConnected) {
				t.Fatalf("err = %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, store.FileBubbles)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatal("the unreadable file was left in place")
			}
			bad, _ := filepath.Glob(filepath.Join(dir, store.FileBubbles+".bad-*"))
			if len(bad) != 1 {
				t.Fatalf("quarantined files %v", bad)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A bubbles.json copied from another Windows user or computer keeps the
// server, pin and channel but cannot give the token: "needs reconnecting".
// On Windows the copy is simulated by a DPAPI blob naming a master key this
// user does not have; elsewhere by a file written on Windows.
func TestCopiedFileNeedsReconnect(t *testing.T) {
	st, dir := newStore(t)
	if err := SaveConnection(st, testConnection()); err != nil {
		t.Fatal(err)
	}
	f := readFile(t, dir)
	if runtime.GOOS == "windows" {
		raw, _ := base64.StdEncoding.DecodeString(f.Token.Data)
		_, _ = rand.Read(raw[24:40]) // the DPAPI master key GUID
		f.Token.Data = base64.StdEncoding.EncodeToString(raw)
	} else {
		f.Token.Protected = secret.ProtectedDPAPI
	}
	writeFile(t, dir, f)
	c, err := LoadConnection(st)
	if !errors.Is(err, ErrNeedsReconnect) || !errors.Is(err, secret.ErrCannotOpen) {
		t.Fatalf("err = %v, want ErrNeedsReconnect", err)
	}
	if c.Token() != "" || c.Server != testServer || c.Channel.ID != "C1" {
		t.Fatalf("connection %#v (token %q)", c, c.Token())
	}
	if _, err := os.Stat(filepath.Join(dir, store.FileBubbles)); err != nil {
		t.Fatalf("bubbles.json must stay for Reconnect: %v", err)
	}
}

// The token has no place in settings.json: no settings field, at any depth,
// is a token (v0.6 plan §4.15).
func TestSettingsHaveNoTokenField(t *testing.T) {
	var walk func(reflect.Type, string)
	walk = func(ty reflect.Type, path string) {
		for ty.Kind() == reflect.Pointer || ty.Kind() == reflect.Slice || ty.Kind() == reflect.Map {
			ty = ty.Elem()
		}
		if ty.Kind() != reflect.Struct {
			return
		}
		for i := range ty.NumField() {
			fl := ty.Field(i)
			name := strings.ToLower(fl.Name + " " + fl.Tag.Get("json"))
			if strings.Contains(name, "token") {
				t.Errorf("settings field %s%s looks like a token", path, fl.Name)
			}
			walk(fl.Type, path+fl.Name+".")
		}
	}
	walk(reflect.TypeOf(store.DefaultSettings()), "")
}

func TestSaveRefusesIncompleteConnection(t *testing.T) {
	st, dir := newStore(t)
	noToken := testConnection()
	noToken.token = ""
	badURL := testConnection()
	badURL.Server.BaseURL = "http://192.168.1.20:28225"
	for name, c := range map[string]Connection{"no token": noToken, "plain http": badURL} {
		if err := SaveConnection(st, c); err == nil {
			t.Errorf("%s: saved", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, store.FileBubbles)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a file was written")
	}
}
