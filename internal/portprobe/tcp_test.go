package portprobe

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestScanPorts(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	open := l.Addr().(*net.TCPAddr).Port
	// find a closed port
	l2, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l2.Addr().(*net.TCPAddr).Port
	l2.Close()

	start := time.Now()
	got := ScanPorts(context.Background(), "127.0.0.1", []int{closed, open})
	if len(got) != 1 || got[0] != open {
		t.Fatalf("got %v want [%d]", got, open)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("too slow: %v", time.Since(start))
	}
}

func TestScanPortsTimeoutBound(t *testing.T) {
	old := ConnectTimeout
	ConnectTimeout = 200 * time.Millisecond
	defer func() { ConnectTimeout = old }()
	start := time.Now()
	got := ScanPorts(context.Background(), "192.0.2.1", []int{80, 443})
	if len(got) != 0 {
		t.Fatalf("unexpected open %v", got)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestSchemeFor(t *testing.T) {
	if SchemeFor(80) != "http" || SchemeFor(8443) != "https" || SchemeFor(9100) != "" {
		t.Fatal("bad scheme map")
	}
}
