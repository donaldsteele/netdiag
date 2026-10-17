package portprobe

import (
	"context"
	"errors"
	"net"
	"sync"
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

// fakeDial records how many dials run at once.
type fakeDial struct {
	mu        sync.Mutex
	now, peak int
	calls     int
}

func (f *fakeDial) dial(ctx context.Context, _ string, _ int, _ time.Duration) (net.Conn, error) {
	f.mu.Lock()
	f.now++
	f.calls++
	f.peak = max(f.peak, f.now)
	f.mu.Unlock()
	select {
	case <-time.After(20 * time.Millisecond):
	case <-ctx.Done():
	}
	f.mu.Lock()
	f.now--
	f.mu.Unlock()
	return nil, errors.New("closed")
}

func useDial(t *testing.T, f func(context.Context, string, int, time.Duration) (net.Conn, error)) {
	t.Helper()
	old := dialPort
	dialPort = f
	t.Cleanup(func() { dialPort = old })
}

func TestScanPortsBoundsConcurrency(t *testing.T) {
	f := &fakeDial{}
	useDial(t, f.dial)
	ports := make([]int, 64)
	for i := range ports {
		ports[i] = 1000 + i
	}
	if got := ScanPorts(context.Background(), "192.0.2.1", ports); len(got) != 0 {
		t.Fatalf("open = %v", got)
	}
	if f.calls != len(ports) {
		t.Fatalf("dialled %d of %d ports", f.calls, len(ports))
	}
	if f.peak > PortWorkers || f.peak < 2 {
		t.Fatalf("peak concurrent dials = %d, want 2..%d", f.peak, PortWorkers)
	}
}

func TestScanPortsStopsWhenCancelled(t *testing.T) {
	f := &fakeDial{}
	useDial(t, f.dial)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ports := make([]int, 64)
	for i := range ports {
		ports[i] = 1000 + i
	}
	ScanPorts(ctx, "192.0.2.1", ports)
	if f.calls != 0 {
		t.Fatalf("cancelled scan still dialled %d ports", f.calls)
	}
}

func TestSchemeFor(t *testing.T) {
	if SchemeFor(80) != "http" || SchemeFor(8443) != "https" || SchemeFor(9100) != "" {
		t.Fatal("bad scheme map")
	}
}
