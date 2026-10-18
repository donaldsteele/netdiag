package store

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Antivirus or the search indexer holding the new file for a moment makes
// the rename fail with access denied or a sharing violation (once seen in
// TestTwoStoresSameDir on the Windows CI runner): the save tries again
// and succeeds.
func TestReplaceFileRetriesBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileOpenProblems)
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := failRename(t, syscall.ERROR_ACCESS_DENIED, errSharingViolation, syscall.ERROR_ACCESS_DENIED)
	if err := writeAtomic(path, []byte("new")); err != nil {
		t.Fatalf("save: %v", err)
	}
	if *calls != 4 {
		t.Fatalf("renamed %d times, want 4", *calls)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "new" {
		t.Fatalf("after the save: %q %v", b, err)
	}
}

// A file that stays busy fails the save after renameTries tries.
func TestReplaceFileGivesUpWhenBusy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileOpenProblems)
	busy := make([]error, renameTries+1)
	for i := range busy {
		busy[i] = syscall.ERROR_ACCESS_DENIED
	}
	calls := failRename(t, busy...)
	if err := writeAtomic(path, []byte("new")); !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		t.Fatalf("save = %v, want access denied", err)
	}
	if *calls != renameTries {
		t.Fatalf("renamed %d times, want %d", *calls, renameTries)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("left %v", ents)
	}
}
