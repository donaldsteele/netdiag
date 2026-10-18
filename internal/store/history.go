package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"netdiag/internal/devtype"
	"netdiag/internal/model"
)

const (
	// Retention is the number of newest scans kept.
	Retention = 20
	// MaxScanBytes is the per-file size cap.
	MaxScanBytes = 5 << 20
)

// ErrNotFound is returned when a history id does not exist.
var ErrNotFound = errors.New("store: not found")

// ErrTooLarge is returned when a scan exceeds MaxScanBytes even with no hosts.
var ErrTooLarge = errors.New("store: scan exceeds size cap")

// UnreadableError is the HistoryEntry.Error value for a corrupt history file.
const UnreadableError = "unreadable"

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func compactTime(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t = time.Now()
	}
	return t.UTC().Format("20060102T150405Z")
}

// fileInfo is a parsed history filename.
type fileInfo struct {
	name, stamp, id string
}

// files lists history files, newest first.
func (s *Store) files() []fileInfo {
	ents, err := os.ReadDir(s.histDir)
	if err != nil {
		return nil
	}
	var out []fileInfo
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".json") || strings.HasPrefix(n, ".") {
			continue
		}
		base := strings.TrimSuffix(n, ".json")
		i := strings.Index(base, "-")
		if i <= 0 || i == len(base)-1 {
			continue
		}
		out = append(out, fileInfo{n, base[:i], base[i+1:]})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].stamp != out[b].stamp {
			return out[a].stamp > out[b].stamp
		}
		return out[a].id > out[b].id
	})
	return out
}

func entryOf(r model.ScanResult) model.HistoryEntry {
	return model.HistoryEntry{
		ID: r.ID, CIDR: r.Request.CIDR, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		State: r.State, Live: r.Request.Live, Stats: r.Stats,
	}
}

func (s *Store) readScan(f fileInfo) (model.ScanResult, error) {
	data, err := os.ReadFile(filepath.Join(s.histDir, f.name))
	if err != nil {
		return model.ScanResult{}, err
	}
	var r model.ScanResult
	if err := json.Unmarshal(data, &r); err != nil {
		return r, err
	}
	// Files saved before v0.5.8 hold the SNMP community in clear. The file
	// is rewritten without it once; if that fails, it is still hidden in
	// what is handed out, and the next read tries again.
	if c := r.Request.Community; c != "" && c != model.Redacted {
		if err := s.redactFile(f, data); err != nil {
			log.Printf("store: could not hide the SNMP community in history file %s: %v", f.name, err)
		}
	}
	r = model.RedactScan(r)
	if r.ID == "" {
		r.ID = f.id
	}
	// Back-fill device types for scans saved before v0.4.
	for i := range r.Hosts {
		if h := &r.Hosts[i]; h.DeviceType == "" {
			t, why := devtype.Classify(*h, r.Gateway)
			h.DeviceType, h.DeviceTypeReason = string(t), why
		}
	}
	return r, nil
}

// rewriteFile replaces a history file; tests swap it to make it fail.
var rewriteFile = writeAtomic

// redactFile rewrites the history file f, whose content is data, with the
// request's SNMP community replaced by model.Redacted. Only that value
// changes: the JSON is edited as a tree, so fields this version does not
// know are kept. The new content is checked before it is written, the file
// is replaced atomically, and nothing is written when the file changed on
// disk since it was read (another netdiag sharing the folder). The caller
// holds s.mu.
func (s *Store) redactFile(f fileInfo, data []byte) error {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	redacted, _ := json.Marshal(model.Redacted)
	for k, v := range doc {
		if !strings.EqualFold(k, "request") {
			continue
		}
		var req map[string]json.RawMessage
		if err := json.Unmarshal(v, &req); err != nil {
			return err
		}
		if req == nil { // "request": null, e.g. a case-variant duplicate key
			continue
		}
		for rk := range req {
			if strings.EqualFold(rk, "community") {
				delete(req, rk)
			}
		}
		req["community"] = redacted
		b, err := json.Marshal(req)
		if err != nil {
			return err
		}
		doc[k] = b
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	var check model.ScanResult
	if err := json.Unmarshal(out, &check); err != nil {
		return err
	}
	if check.Request.Community != model.Redacted {
		return errors.New("rewritten file still has the community")
	}
	path := filepath.Join(s.histDir, f.name)
	now, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(now, data) {
		return errors.New("the file changed while it was read")
	}
	return rewriteFile(path, out)
}

// SaveScan atomically stores r (replacing any prior save of the same id) and
// applies retention. The file is written with schemaVersion 2 and the SNMP
// community redacted.
func (s *Store) SaveScan(r model.ScanResult) error {
	if !idRe.MatchString(r.ID) || strings.Contains(r.ID, "..") {
		return errBadID
	}
	r = model.RedactScan(r)
	if r.SchemaVersion < model.HistorySchemaVersion {
		r.SchemaVersion = model.HistorySchemaVersion
	}
	data, err := fitScan(r)
	if err != nil {
		return err
	}
	name := compactTime(r.StartedAt) + "-" + r.ID + ".json"
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files() {
		if f.id == r.ID && f.name != name {
			os.Remove(filepath.Join(s.histDir, f.name))
		}
	}
	if err := writeAtomic(filepath.Join(s.histDir, name), data); err != nil {
		return err
	}
	all := s.files()
	for i := Retention; i < len(all); i++ {
		os.Remove(filepath.Join(s.histDir, all[i].name))
	}
	return nil
}

// fitScan marshals r, truncating its host list (with a warning) when the
// encoding exceeds MaxScanBytes.
func fitScan(r model.ScanResult) ([]byte, error) {
	data, err := json.Marshal(r)
	if err != nil || len(data) <= MaxScanBytes {
		return data, err
	}
	all := r.Hosts
	full := len(data)
	// Estimate from the average host size, then shrink until it fits.
	n := int(float64(len(all)) * float64(MaxScanBytes) / float64(full) * 0.95)
	for {
		if n < 0 {
			n = 0
		}
		r.Hosts = all[:n]
		r.Warnings = append(r.Warnings[:len(r.Warnings):len(r.Warnings)],
			fmt.Sprintf("history: saved %d of %d hosts; the full scan exceeded the %d MB file cap", n, len(all), MaxScanBytes>>20))
		if data, err = json.Marshal(r); err != nil {
			return nil, err
		}
		if len(data) <= MaxScanBytes {
			return data, nil
		}
		if n == 0 {
			return nil, ErrTooLarge
		}
		n = n * 9 / 10
	}
}

// ListHistory returns saved scans newest first. A corrupt file is listed as
// {id, error: "unreadable"} so it can still be seen and deleted.
func (s *Store) ListHistory() ([]model.HistoryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []model.HistoryEntry{}
	for _, f := range s.files() {
		r, err := s.readScan(f)
		if err != nil {
			out = append(out, model.HistoryEntry{ID: f.id, Error: UnreadableError})
			continue
		}
		out = append(out, entryOf(r))
	}
	return out, nil
}

// LatestScan returns the newest saved scan that can be read; ok is false
// when there is none. It reads files newest first and stops at the first
// that decodes, so a caller that only wants the newest scan (the Problems
// right now card, read every minute) does not decode the whole history the
// way ListHistory does.
func (s *Store) LatestScan() (model.ScanResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.files() {
		if r, err := s.readScan(f); err == nil {
			return r, true
		}
	}
	return model.ScanResult{}, false
}

func (s *Store) find(id string) (fileInfo, bool) {
	if !idRe.MatchString(id) {
		return fileInfo{}, false
	}
	for _, f := range s.files() {
		if f.id == id {
			return f, true
		}
	}
	return fileInfo{}, false
}

// LoadScan returns the saved scan with the given id (ErrNotFound if absent).
func (s *Store) LoadScan(id string) (model.ScanResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.find(id)
	if !ok {
		return model.ScanResult{}, ErrNotFound
	}
	return s.readScan(f)
}

// DeleteScan removes a saved scan (ErrNotFound if absent).
func (s *Store) DeleteScan(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.find(id)
	if !ok {
		return ErrNotFound
	}
	return os.Remove(filepath.Join(s.histDir, f.name))
}

// Previous returns the newest saved scan with the given cidr that is older
// than beforeID. If beforeID is not in history (e.g. a running scan), the
// newest entry with that cidr is returned. ok is false when none exists.
func (s *Store) Previous(cidr, beforeID string) (model.HistoryEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := s.files()
	start := 0
	for i, f := range files {
		if f.id == beforeID {
			start = i + 1
			break
		}
	}
	for _, f := range files[start:] {
		r, err := s.readScan(f)
		if err != nil || r.Request.CIDR != cidr {
			continue
		}
		return entryOf(r), true, nil
	}
	return model.HistoryEntry{}, false, nil
}
