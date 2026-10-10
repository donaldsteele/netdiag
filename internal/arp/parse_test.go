package arp

import (
	"os"
	"reflect"
	"testing"
)

func TestParseWindows(t *testing.T) {
	b, err := os.ReadFile("../../testdata/arp_windows.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{"192.168.1.1", "A4:2B:B0:12:34:56"},
		{"192.168.1.20", "00:1B:A9:AA:BB:CC"},
		{"10.0.0.9", "3C:22:FB:01:02:03"},
	}
	if got := ParseWindows(string(b)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseDarwin(t *testing.T) {
	b, err := os.ReadFile("../../testdata/arp_darwin.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{"192.168.1.1", "A4:2B:B0:12:34:56"},
		{"192.168.1.20", "00:1B:A9:AA:BB:CC"},
		{"192.168.1.50", "3C:22:FB:01:02:03"},
		{"192.168.1.2", "AA:BB:CC:DD:EE:FF"},
	}
	if got := ParseDarwin(string(b)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestNormalizeMAC(t *testing.T) {
	for _, bad := range []string{"", "ff-ff-ff-ff-ff-ff", "00:00:00:00:00:00", "01:00:5e:00:00:fb", "zz:00:00:00:00:01", "1:2:3"} {
		if _, ok := NormalizeMAC(bad); ok {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
