package oui

import "testing"

func TestLookup(t *testing.T) {
	for _, m := range []string{"E8:0A:B9:11:22:33", "e8-0a-b9-11-22-33", "E80AB9112233", "E80A.B911.2233"} {
		if v := Lookup(m); v == "" {
			t.Errorf("Lookup(%q) empty", m)
		}
	}
	for _, m := range []string{"", "zz:zz:zz:00:00:00", "12"} {
		if v := Lookup(m); v != "" {
			t.Errorf("Lookup(%q)=%q want empty", m, v)
		}
	}
	if len(db) < 10000 {
		t.Errorf("db too small: %d", len(db))
	}
}
