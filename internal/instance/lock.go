package instance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// ErrLocked means another netdiag holds the start lock for this data folder.
var ErrLocked = errors.New("instance: another netdiag is using this data folder")

// Lock is the start lock for one data folder: a named mutex on Windows, a
// flock on <datadir>/instance.lock elsewhere. The netdiag that wins keeps it
// until it exits (the system lets go of it when the process ends, even
// after a crash), so a second netdiag with the same data folder never
// serves alongside it. Neither needs admin rights.
type Lock struct {
	key  string
	once sync.Once
	sys  sysLock
}

var (
	ownedMu sync.Mutex
	owned   = map[string]bool{}
)

// Acquire takes the start lock for dir without waiting. It returns ErrLocked
// when another netdiag (or another Lock in this process) holds it.
func Acquire(dir string) (*Lock, error) {
	key := lockKey(dir)
	s, err := acquire(dir, key)
	if err != nil {
		return nil, err
	}
	ownedMu.Lock()
	owned[key] = true
	ownedMu.Unlock()
	return &Lock{key: key, sys: s}, nil
}

// Release lets go of the lock. It is safe to call more than once.
func (l *Lock) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		ownedMu.Lock()
		delete(owned, l.key)
		ownedMu.Unlock()
		l.sys.release()
	})
}

// Owner reports whether this process holds the start lock for dir: it is
// the netdiag that serves that data folder (v0.8 Keep watching asks).
func Owner(dir string) bool {
	ownedMu.Lock()
	defer ownedMu.Unlock()
	return owned[lockKey(dir)]
}

// lockKey names the data folder in the lock: a hash of its absolute path,
// without case on Windows, where paths ignore it.
func lockKey(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	dir = filepath.Clean(dir)
	if runtime.GOOS == "windows" {
		dir = strings.ToLower(dir)
	}
	sum := sha256.Sum256([]byte(dir))
	return hex.EncodeToString(sum[:8])
}
