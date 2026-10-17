// Package secret protects small secrets kept in the data folder: the team
// chat token in bubbles.json. On Windows a secret is sealed with DPAPI for
// the signed-in user, so a copy of the file is useless to another user or
// on another computer. Elsewhere it is stored as it is and relies on the
// owner-only (0600) file; the macOS Keychain comes after code signing
// (v0.6-D5).
package secret

import (
	"encoding/base64"
	"errors"
	"fmt"
)

// Protection names, the Sealed.Protected values.
const (
	ProtectedDPAPI = "dpapi" // Windows: DPAPI, current user
	ProtectedNone  = "none"  // elsewhere: the plain bytes in an owner-only file
)

// Sealed is a protected secret as it is saved: the protection used and the
// sealed bytes in base64.
type Sealed struct {
	Protected string `json:"protected"`
	Data      string `json:"data"`
}

// ErrCannotOpen means a saved secret cannot be opened here: it was sealed
// by another user or on another computer, with another protection, or it
// is damaged. The owner should ask for a new secret (for team chat: "needs
// reconnecting").
var ErrCannotOpen = errors.New("secret: the saved secret cannot be opened by this user on this computer")

// errEmpty means there is nothing to seal.
var errEmpty = errors.New("secret: nothing to seal")

// Seal protects plain for the current user on this computer.
func Seal(plain []byte) (Sealed, error) {
	if len(plain) == 0 {
		return Sealed{}, errEmpty
	}
	b, err := seal(plain)
	if err != nil {
		return Sealed{}, fmt.Errorf("secret: seal: %w", err)
	}
	return Sealed{Protected: protection, Data: base64.StdEncoding.EncodeToString(b)}, nil
}

// Open returns the secret s holds. Every failure matches ErrCannotOpen.
func Open(s Sealed) ([]byte, error) {
	if s.Protected != protection {
		return nil, fmt.Errorf("%w: it is protected as %q, this computer uses %q", ErrCannotOpen, s.Protected, protection)
	}
	b, err := base64.StdEncoding.DecodeString(s.Data)
	if err != nil || len(b) == 0 {
		return nil, fmt.Errorf("%w: the saved data is damaged", ErrCannotOpen)
	}
	plain, err := open(b)
	if err != nil || len(plain) == 0 {
		return nil, fmt.Errorf("%w: %v", ErrCannotOpen, err)
	}
	return plain, nil
}
