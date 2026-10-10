package arp

import (
	"net"
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
		{IP: "192.168.1.1", MAC: "A4:2B:B0:12:34:56", Iface: "192.168.1.50", Type: "dynamic"},
		{IP: "192.168.1.20", MAC: "00:1B:A9:AA:BB:CC", Iface: "192.168.1.50", Type: "dynamic"},
		{IP: "10.0.0.9", MAC: "3C:22:FB:01:02:03", Iface: "10.0.0.5", Type: "dynamic"},
	}
	if got := ParseWindows(string(b)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// multiRaw is the undeduplicated parse of testdata/arp_windows_multi.txt.
// Index 3 is 192.168.1.1 under the 10.0.0.5 block; index 4 is 10.0.0.1 under
// 10.0.0.5 with a different MAC than under 192.168.1.50 (index 2).
func multiRaw(t *testing.T) []Entry {
	t.Helper()
	b, err := os.ReadFile("../../testdata/arp_windows_multi.txt")
	if err != nil {
		t.Fatal(err)
	}
	return ParseWindows(string(b))
}

func TestParseWindowsMultiKeepsDuplicates(t *testing.T) {
	raw := multiRaw(t)
	if len(raw) != 6 {
		t.Fatalf("raw rows = %d want 6: %v", len(raw), raw)
	}
	if raw[0].IP != "192.168.1.1" || raw[3].IP != "192.168.1.1" {
		t.Fatalf("192.168.1.1 not listed under both blocks: %v", raw)
	}
	if raw[3].Iface != "10.0.0.5" || raw[2].Type != "static" {
		t.Fatalf("iface/type tagging wrong: %v", raw)
	}
}

func TestDedupeMultiAdapter(t *testing.T) {
	raw := multiRaw(t)
	cases := []struct {
		name string
		pref string
		want []Entry
	}{
		{
			name: "preferred block 10.0.0.5 wins",
			pref: "10.0.0.5",
			want: []Entry{
				{IP: "192.168.1.1", MAC: "A4:2B:B0:12:34:56", Iface: "10.0.0.5", Type: "dynamic"},
				{IP: "192.168.1.20", MAC: "00:1B:A9:AA:BB:CC", Iface: "192.168.1.50", Type: "dynamic"},
				{IP: "10.0.0.1", MAC: "00:50:56:CC:DD:02", Iface: "10.0.0.5", Type: "dynamic"},
				{IP: "10.0.0.9", MAC: "3C:22:FB:01:02:03", Iface: "10.0.0.5", Type: "dynamic"},
			},
		},
		{
			name: "preferred block 192.168.1.50 wins, static row kept",
			pref: "192.168.1.50",
			want: []Entry{
				{IP: "192.168.1.1", MAC: "A4:2B:B0:12:34:56", Iface: "192.168.1.50", Type: "dynamic"},
				{IP: "192.168.1.20", MAC: "00:1B:A9:AA:BB:CC", Iface: "192.168.1.50", Type: "dynamic"},
				{IP: "10.0.0.1", MAC: "00:50:56:AA:BB:01", Iface: "192.168.1.50", Type: "static"},
				{IP: "10.0.0.9", MAC: "3C:22:FB:01:02:03", Iface: "10.0.0.5", Type: "dynamic"},
			},
		},
		{
			name: "no preference: first dynamic beats earlier static",
			pref: "",
			want: []Entry{
				{IP: "192.168.1.1", MAC: "A4:2B:B0:12:34:56", Iface: "192.168.1.50", Type: "dynamic"},
				{IP: "192.168.1.20", MAC: "00:1B:A9:AA:BB:CC", Iface: "192.168.1.50", Type: "dynamic"},
				{IP: "10.0.0.1", MAC: "00:50:56:CC:DD:02", Iface: "10.0.0.5", Type: "dynamic"},
				{IP: "10.0.0.9", MAC: "3C:22:FB:01:02:03", Iface: "10.0.0.5", Type: "dynamic"},
			},
		},
		{
			name: "unknown preference falls back like no preference",
			pref: "172.16.9.9",
			want: []Entry{
				{IP: "192.168.1.1", MAC: "A4:2B:B0:12:34:56", Iface: "192.168.1.50", Type: "dynamic"},
				{IP: "192.168.1.20", MAC: "00:1B:A9:AA:BB:CC", Iface: "192.168.1.50", Type: "dynamic"},
				{IP: "10.0.0.1", MAC: "00:50:56:CC:DD:02", Iface: "10.0.0.5", Type: "dynamic"},
				{IP: "10.0.0.9", MAC: "3C:22:FB:01:02:03", Iface: "10.0.0.5", Type: "dynamic"},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Dedupe(raw, c.pref); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v\nwant %v", got, c.want)
			}
		})
	}
}

func TestDedupeFirstWhenNoDynamic(t *testing.T) {
	in := []Entry{
		{IP: "10.1.1.1", MAC: "00:50:56:00:00:01", Iface: "10.1.1.2", Type: "static"},
		{IP: "10.1.1.1", MAC: "00:50:56:00:00:02", Iface: "10.2.2.2", Type: "static"},
	}
	got := Dedupe(in, "")
	if len(got) != 1 || got[0] != in[0] {
		t.Fatalf("got %v want first entry", got)
	}
	if Dedupe(nil, "") != nil {
		t.Fatal("empty input should give nil")
	}
}

func TestDefaultIface(t *testing.T) {
	raw := multiRaw(t)
	orig := localIPv4s
	defer func() { localIPv4s = orig }()

	// Host with a 10.0.0.x address: block 10.0.0.5 shares the /24.
	localIPv4s = func() []net.IP { return []net.IP{net.ParseIP("10.0.0.77").To4()} }
	if got := defaultIface(raw); got != "10.0.0.5" {
		t.Fatalf("defaultIface = %q want 10.0.0.5", got)
	}
	// Host on 192.168.1.x: first block matches.
	localIPv4s = func() []net.IP { return []net.IP{net.ParseIP("192.168.1.9").To4()} }
	if got := defaultIface(raw); got != "192.168.1.50" {
		t.Fatalf("defaultIface = %q want 192.168.1.50", got)
	}
	// No local address in any block's /24: no preference.
	localIPv4s = func() []net.IP { return []net.IP{net.ParseIP("172.16.0.2").To4()} }
	if got := defaultIface(raw); got != "" {
		t.Fatalf("defaultIface = %q want empty", got)
	}
	// Darwin-style rows carry no block.
	localIPv4s = func() []net.IP { return []net.IP{net.ParseIP("192.168.1.9").To4()} }
	if got := defaultIface([]Entry{{IP: "192.168.1.1", MAC: "A4:2B:B0:12:34:56"}}); got != "" {
		t.Fatalf("defaultIface = %q want empty", got)
	}
}

func TestParseDarwin(t *testing.T) {
	b, err := os.ReadFile("../../testdata/arp_darwin.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{IP: "192.168.1.1", MAC: "A4:2B:B0:12:34:56"},
		{IP: "192.168.1.20", MAC: "00:1B:A9:AA:BB:CC"},
		{IP: "192.168.1.50", MAC: "3C:22:FB:01:02:03"},
		{IP: "192.168.1.2", MAC: "AA:BB:CC:DD:EE:FF"},
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
