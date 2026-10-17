//go:build windows

package secret

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestDPAPIHidesTheSecret(t *testing.T) {
	s, err := Seal([]byte(token))
	if err != nil {
		t.Fatal(err)
	}
	if s.Protected != ProtectedDPAPI {
		t.Fatalf("protected %q", s.Protected)
	}
	raw, _ := base64.StdEncoding.DecodeString(s.Data)
	if bytes.Contains(raw, []byte(token)) || strings.Contains(s.Data, base64.StdEncoding.EncodeToString([]byte(token))) {
		t.Fatal("the sealed data holds the token in clear")
	}
	// Two seals of the same secret differ (DPAPI salts every blob).
	s2, err := Seal([]byte(token))
	if err != nil {
		t.Fatal(err)
	}
	if s2.Data == s.Data {
		t.Fatal("two seals gave the same blob")
	}
}

func mustCannotOpen(t *testing.T, raw []byte) {
	t.Helper()
	got, err := Open(Sealed{Protected: ProtectedDPAPI, Data: base64.StdEncoding.EncodeToString(raw)})
	if !errors.Is(err, ErrCannotOpen) || got != nil {
		t.Fatalf("Open = %q, %v; want ErrCannotOpen", got, err)
	}
	t.Log(err)
}

func TestDPAPITamperedBlobFails(t *testing.T) {
	s, err := Seal([]byte(token))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(s.Data)
	raw[len(raw)-5] ^= 0x40 // inside the signature at the end
	mustCannotOpen(t, raw)
}

// A blob from another user or another computer names a DPAPI master key
// this user does not have. The master key GUID sits at bytes 24-39 of the
// blob (after dwVersion, guidProvider and dwMasterKeyVersion); a random one
// stands in for a bubbles.json copied from another account.
func TestDPAPIBlobFromAnotherUserFails(t *testing.T) {
	s, err := Seal([]byte(token))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(s.Data)
	if len(raw) < 40 {
		t.Fatalf("blob too short: %d bytes", len(raw))
	}
	_, _ = rand.Read(raw[24:40])
	mustCannotOpen(t, raw)
}

// Another program running as this user, sealing with other entropy, cannot
// hand netdiag a blob it opens, nor open netdiag's.
func TestDPAPIOtherEntropyFails(t *testing.T) {
	raw, err := sealWith([]byte(token), []byte("someone-else"))
	if err != nil {
		t.Fatal(err)
	}
	mustCannotOpen(t, raw)
}

// A file written on a Mac ("none") does not open on Windows: the token
// there was never sealed for this user.
func TestDPAPIRefusesPlainSecret(t *testing.T) {
	_, err := Open(Sealed{Protected: ProtectedNone, Data: base64.StdEncoding.EncodeToString([]byte(token))})
	if !errors.Is(err, ErrCannotOpen) {
		t.Fatalf("err = %v", err)
	}
}
