//go:build !windows

package secret

import "slices"

// protection is "none": the bytes are stored as they are, and the file that
// holds them is readable by its owner only (0600).
const protection = ProtectedNone

func seal(plain []byte) ([]byte, error) { return slices.Clone(plain), nil }

func open(sealed []byte) ([]byte, error) { return slices.Clone(sealed), nil }
