//go:build !windows

package store

// renameBusy reports whether a rename failed only because another process
// has the file open, which never stops a rename here.
func renameBusy(error) bool { return false }
