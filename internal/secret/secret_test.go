package secret

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const token = "bub_0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOP"

func TestSealOpenRoundTrip(t *testing.T) {
	s, err := Seal([]byte(token))
	if err != nil {
		t.Fatal(err)
	}
	if s.Protected != protection {
		t.Fatalf("protected %q, want %q", s.Protected, protection)
	}
	got, err := Open(s)
	if err != nil || string(got) != token {
		t.Fatalf("Open = %q, %v", got, err)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil || len(m) != 2 || m["protected"] != protection || m["data"] == "" {
		t.Fatalf("saved form %s", raw)
	}
}

func TestSealNothing(t *testing.T) {
	if _, err := Seal(nil); err == nil {
		t.Fatal("sealed an empty secret")
	}
}

// Everything that cannot be opened is ErrCannotOpen, so the owner can say
// "needs reconnecting" without telling the cases apart. A truncated blob is
// tested with DPAPI only (dpapi_windows_test.go): the plain variant has no
// integrity check, so half a token opens as half a token.
func TestOpenFailsCleanly(t *testing.T) {
	good, err := Seal([]byte(token))
	if err != nil {
		t.Fatal(err)
	}
	other := ProtectedNone
	if protection == ProtectedNone {
		other = ProtectedDPAPI
	}
	for name, s := range map[string]Sealed{
		"zero value":       {},
		"empty data":       {Protected: protection},
		"not base64":       {Protected: protection, Data: "%%%"},
		"other protection": {Protected: other, Data: good.Data},
		"unknown":          {Protected: "keychain", Data: good.Data},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Open(s)
			if !errors.Is(err, ErrCannotOpen) || got != nil {
				t.Fatalf("Open = %q, %v; want ErrCannotOpen", got, err)
			}
			if strings.Contains(err.Error(), token) {
				t.Fatal("the error holds the secret")
			}
		})
	}
}
