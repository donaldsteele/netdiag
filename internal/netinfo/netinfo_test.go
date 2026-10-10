package netinfo

import (
	"errors"
	"testing"
)

func TestParseTarget(t *testing.T) {
	tests := []struct {
		in      string
		n       int
		first   string
		last    string
		wantErr error
		anyErr  bool
	}{
		{in: "192.168.1.0/24", n: 254, first: "192.168.1.1", last: "192.168.1.254"},
		{in: "192.168.1.5/32", n: 1, first: "192.168.1.5", last: "192.168.1.5"},
		{in: "192.168.1.4/31", n: 2, first: "192.168.1.4", last: "192.168.1.5"},
		{in: "10.0.0.0/30", n: 2, first: "10.0.0.1", last: "10.0.0.2"},
		{in: "10.0.0.0/22", n: 1022, first: "10.0.0.1", last: "10.0.3.254"},
		{in: "10.0.0.0/21", wantErr: ErrRangeTooLarge},
		{in: "10.0.0.0/8", wantErr: ErrRangeTooLarge},
		{in: "192.168.1.10-192.168.1.50", n: 41, first: "192.168.1.10", last: "192.168.1.50"},
		{in: "192.168.1.10-50", n: 41, first: "192.168.1.10", last: "192.168.1.50"},
		{in: "192.168.1.7-7", n: 1, first: "192.168.1.7", last: "192.168.1.7"},
		{in: "10.0.0.0-10.0.4.0", wantErr: ErrRangeTooLarge},
		{in: "192.168.1.50-10", anyErr: true},
		{in: "192.168.1.50-192.168.1.10", anyErr: true},
		{in: "192.168.1.1", n: 1, first: "192.168.1.1", last: "192.168.1.1"},
		{in: " 192.168.1.1 ", n: 1, first: "192.168.1.1", last: "192.168.1.1"},
		{in: "", anyErr: true},
		{in: "garbage", anyErr: true},
		{in: "300.1.1.1", anyErr: true},
		{in: "192.168.1.1-300", anyErr: true},
		{in: "::1", anyErr: true},
		{in: "fe80::/64", anyErr: true},
		{in: "::1-::5", anyErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseTarget(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if tc.anyErr {
				if err == nil {
					t.Fatalf("expected error, got %d addrs", len(got))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.n {
				t.Fatalf("len = %d, want %d", len(got), tc.n)
			}
			if got[0].String() != tc.first || got[len(got)-1].String() != tc.last {
				t.Fatalf("bounds %s..%s, want %s..%s", got[0], got[len(got)-1], tc.first, tc.last)
			}
			for i := 1; i < len(got); i++ {
				if got[i-1].Compare(got[i]) >= 0 {
					t.Fatal("not sorted/unique")
				}
			}
		})
	}
}

func TestSuggestTarget(t *testing.T) {
	tests := []struct {
		f     Iface
		want  string
		trunc bool
	}{
		{Iface{IP: "192.168.1.20", CIDR: "192.168.1.0/24"}, "192.168.1.0/24", false},
		{Iface{IP: "10.0.1.5", CIDR: "10.0.0.0/22"}, "10.0.1.0/24", true},
		{Iface{IP: "10.2.7.9", CIDR: "10.0.0.0/8"}, "10.2.7.0/24", true},
		{Iface{IP: "172.16.40.3", CIDR: "172.16.32.0/20"}, "172.16.40.0/24", true},
		{Iface{IP: "192.168.1.20", CIDR: "192.168.1.16/28"}, "192.168.1.16/28", false},
	}
	for _, tc := range tests {
		got, tr := SuggestTarget(tc.f)
		if got != tc.want || tr != tc.trunc {
			t.Errorf("%v: got %s,%v want %s,%v", tc.f, got, tr, tc.want, tc.trunc)
		}
	}
}

func TestInterfacesSmoke(t *testing.T) {
	if _, err := Interfaces(); err != nil {
		t.Fatal(err)
	}
}
