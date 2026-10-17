//go:build windows

package secret

import (
	"slices"
	"unsafe"

	"golang.org/x/sys/windows"
)

const protection = ProtectedDPAPI

// entropy is DPAPI's optional extra secret: another program running as the
// same user can open the blob only if it knows this value too.
var entropy = []byte("netdiag-bubbles-v1")

func blob(b []byte) *windows.DataBlob {
	return &windows.DataBlob{Size: uint32(len(b)), Data: unsafe.SliceData(b)}
}

// seal is CryptProtectData for the current user, never showing a prompt.
func seal(plain []byte) ([]byte, error) {
	return sealWith(plain, entropy)
}

func sealWith(plain, ent []byte) ([]byte, error) {
	var out windows.DataBlob
	if err := windows.CryptProtectData(blob(plain), nil, blob(ent), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	return takeBlob(&out), nil
}

// open is CryptUnprotectData. It fails for a blob sealed by another user,
// on another computer, with other entropy, or changed in any way.
func open(sealed []byte) ([]byte, error) {
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(blob(sealed), nil, blob(entropy), 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	return takeBlob(&out), nil
}

// takeBlob copies a DPAPI output blob into Go memory and frees it.
func takeBlob(b *windows.DataBlob) []byte {
	if b.Data == nil {
		return nil
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(b.Data)))
	return slices.Clone(unsafe.Slice(b.Data, b.Size))
}
