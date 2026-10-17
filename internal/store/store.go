// Package store persists settings and scan history under the user config dir.
package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// EnvDataDir overrides the default data directory.
const EnvDataDir = "NETDIAG_DATA_DIR"

// Store owns the data directory. All disk access is guarded by one mutex.
type Store struct {
	mu      sync.Mutex
	dir     string
	histDir string
	// repaired lists the settings fields the last load reset (F7).
	repaired []string
}

// Data files that other packages own and keep in the data directory through
// ReadFile, WriteFile, RemoveFile and QuarantineFile. Each has one writer
// (v0.6 plan §4.15). Settings and history have their own methods.
const (
	FileInstance       = "instance.json"        // internal/instance
	FileNetworkProfile = "network-profile.json" // scan, through Deps.Profiles
	FileOpenProblems   = "open-problems.json"   // problems.Ledger
	FileBubbles        = "bubbles.json"         // chat.Manager
	FileChatOutbox     = "chat-outbox.json"     // the chat outbox
)

var dataFiles = map[string]bool{
	FileInstance: true, FileNetworkProfile: true, FileOpenProblems: true,
	FileBubbles: true, FileChatOutbox: true,
}

// ErrFileName means a data file name is not on the allowlist.
var ErrFileName = errors.New("store: data file name not allowed")

func dataFile(name string) error {
	if !dataFiles[name] {
		return fmt.Errorf("%w: %q", ErrFileName, name)
	}
	return nil
}

// ReadFile returns the contents of the named data file. A missing file is
// an error that matches fs.ErrNotExist.
func (s *Store) ReadFile(name string) ([]byte, error) {
	if err := dataFile(name); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.ReadFile(filepath.Join(s.dir, name))
}

// WriteFile atomically replaces the named data file (temp file and rename).
// On macOS the file is readable by its owner only (0600).
func (s *Store) WriteFile(name string, data []byte) error {
	if err := dataFile(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeAtomic(filepath.Join(s.dir, name), data)
}

// RemoveFile deletes the named data file. A missing file is not an error.
func (s *Store) RemoveFile(name string) error {
	if err := dataFile(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// QuarantineFile renames an unreadable data file to <name>.bad-<UTC time>,
// so its owner can start again from defaults without losing it. It returns
// the new name.
func (s *Store) QuarantineFile(name string) (string, error) {
	if err := dataFile(name); err != nil {
		return "", err
	}
	bad := name + ".bad-" + time.Now().UTC().Format("20060102T150405Z")
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Rename(filepath.Join(s.dir, name), filepath.Join(s.dir, bad)); err != nil {
		return "", err
	}
	return bad, nil
}

// RemoveQuarantined deletes every copy QuarantineFile set aside for the
// named data file (<name>.bad-*), for an owner that must forget what the
// file held: team chat's Disconnect forgets the token.
func (s *Store) RemoveQuarantined(name string) error {
	if err := dataFile(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), name+".bad-") {
			if err := os.Remove(filepath.Join(s.dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// ResolveDir returns baseDir, else $NETDIAG_DATA_DIR, else UserConfigDir/netdiag.
func ResolveDir(baseDir string) (string, error) {
	if baseDir != "" {
		return baseDir, nil
	}
	if d := os.Getenv(EnvDataDir); d != "" {
		return d, nil
	}
	c, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(c, "netdiag"), nil
}

// Open resolves and creates the data directory.
func Open(baseDir string) (*Store, error) {
	dir, err := ResolveDir(baseDir)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, histDir: filepath.Join(dir, "history")}
	if err := os.MkdirAll(s.histDir, 0o755); err != nil {
		return nil, err
	}
	removeStaleTemps(s.dir, s.histDir)
	return s, nil
}

// staleTempAge is how old a leftover writeAtomic temp file must be before Open
// deletes it; younger ones may belong to another running netdiag.
const staleTempAge = 10 * time.Minute

// removeStaleTemps deletes temp files left by an interrupted writeAtomic.
func removeStaleTemps(dirs ...string) {
	cutoff := time.Now().Add(-staleTempAge)
	for _, d := range dirs {
		ents, _ := os.ReadDir(d)
		for _, e := range ents {
			if e.IsDir() || !strings.HasPrefix(e.Name(), ".tmp-") {
				continue
			}
			if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
				os.Remove(filepath.Join(d, e.Name()))
			}
		}
	}
}

// Dir returns the data directory.
func (s *Store) Dir() string { return s.dir }

var errBadID = errors.New("store: invalid id")

// writeAtomic writes data to path via temp file + rename.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
