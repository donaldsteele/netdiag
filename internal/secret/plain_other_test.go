//go:build !windows

package secret

import (
	"encoding/base64"
	"errors"
	"testing"
)

func TestPlainIsMarkedNone(t *testing.T) {
	s, err := Seal([]byte(token))
	if err != nil {
		t.Fatal(err)
	}
	if s.Protected != ProtectedNone {
		t.Fatalf("protected %q", s.Protected)
	}
	if b, _ := base64.StdEncoding.DecodeString(s.Data); string(b) != token {
		t.Fatalf("data %q", s.Data)
	}
}

// A file from a Windows PC ("dpapi") does not open here.
func TestPlainRefusesDPAPIBlob(t *testing.T) {
	_, err := Open(Sealed{Protected: ProtectedDPAPI, Data: base64.StdEncoding.EncodeToString([]byte("blob"))})
	if !errors.Is(err, ErrCannotOpen) {
		t.Fatalf("err = %v", err)
	}
}
