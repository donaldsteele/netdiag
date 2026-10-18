//go:build windows

package store

import (
	"errors"
	"syscall"
)

// errSharingViolation is ERROR_SHARING_VIOLATION, which syscall does not
// name.
const errSharingViolation syscall.Errno = 32

// renameBusy reports whether a rename failed only because another process
// has the file open: Windows does not replace a file that is open without
// FILE_SHARE_DELETE (os.Open opens files without it), nor move one that
// antivirus or the search indexer is reading.
func renameBusy(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) || errors.Is(err, errSharingViolation)
}
