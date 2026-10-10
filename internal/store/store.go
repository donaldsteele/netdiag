// Package store persists settings and scan history under the user config dir.
package store

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// EnvDataDir overrides the default data directory.
const EnvDataDir = "NETDIAG_DATA_DIR"

// Store owns the data directory. All disk access is guarded by one mutex.
type Store struct {
	mu      sync.Mutex
	dir     string
	histDir string
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
	return s, nil
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
