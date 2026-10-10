package netinfo

import "testing"

func names(l []Iface) []string {
	var o []string
	for _, f := range l {
		o = append(o, f.Name)
	}
	return o
}

func TestRank(t *testing.T) {
	in := []Iface{
		{Name: "Ethernet 2", IP: "8.8.4.4"},
		{Name: "vEthernet (WSL)", IP: "172.20.0.1"},
		{Name: "Wi-Fi", IP: "192.168.1.20"},
		{Name: "Ethernet", IP: "10.0.0.5"},
		{Name: "docker0", IP: "172.17.0.1"},
	}
	t.Run("default first, virtual dropped", func(t *testing.T) {
		got := Rank(in, "Ethernet 2")
		want := []string{"Ethernet 2", "Wi-Fi", "Ethernet"}
		g := names(got)
		if len(g) != 3 || g[0] != want[0] || g[1] != want[1] || g[2] != want[2] {
			t.Fatalf("got %v want %v", g, want)
		}
		if !got[0].IsDefault || got[1].IsDefault {
			t.Fatal("IsDefault wrong")
		}
	})
	t.Run("no default, RFC1918 first", func(t *testing.T) {
		got := names(Rank(in, ""))
		if got[len(got)-1] != "Ethernet 2" || len(got) != 3 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("all virtual kept", func(t *testing.T) {
		got := Rank([]Iface{{Name: "VMware Network", IP: "192.168.5.1"}, {Name: "utun3", IP: "10.9.0.1"}}, "")
		if len(got) != 2 {
			t.Fatalf("got %v", names(got))
		}
	})
	t.Run("case insensitive", func(t *testing.T) {
		got := Rank([]Iface{{Name: "VIRTUALBOX Host", IP: "192.168.56.1"}, {Name: "en0", IP: "192.168.1.2"}}, "")
		if len(got) != 1 || got[0].Name != "en0" {
			t.Fatalf("got %v", names(got))
		}
	})
	t.Run("empty", func(t *testing.T) {
		if got := Rank(nil, ""); len(got) != 0 {
			t.Fatal("expected empty")
		}
	})
}
