package discovery

import (
	"context"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakePinger struct {
	alive    map[netip.Addr]bool
	failOnce sync.Map
	calls    atomic.Int64
	delay    time.Duration
}

func (f *fakePinger) Ping(ctx context.Context, ip netip.Addr, _ time.Duration) (time.Duration, bool, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return 0, false, nil
		}
	}
	if _, loaded := f.failOnce.LoadOrStore(ip, true); !loaded && f.alive[ip] && ip.As4()[3]%2 == 0 {
		return 0, false, nil // first attempt lost; retry should find it
	}
	if f.alive[ip] {
		return 3 * time.Millisecond, true, nil
	}
	return 0, false, nil
}

func addrs(n int) []netip.Addr {
	out := make([]netip.Addr, n)
	base := netip.MustParseAddr("10.0.0.0").As4()
	for i := 0; i < n; i++ {
		b := base
		b[2] = byte(i >> 8)
		b[3] = byte(i)
		out[i] = netip.AddrFrom4(b)
	}
	return out
}

func TestSweep1024Fast(t *testing.T) {
	ts := addrs(1024)
	fp := &fakePinger{alive: map[netip.Addr]bool{}}
	for i := 0; i < 1024; i += 10 {
		fp.alive[ts[i]] = true
	}
	var mu sync.Mutex
	got := map[netip.Addr]time.Duration{}
	var lastDone, lastTotal int
	start := time.Now()
	err := Sweep(context.Background(), ts, Config{
		Pinger: fp,
		OnAlive: func(r Result) {
			mu.Lock()
			got[r.IP] = r.RTT
			mu.Unlock()
		},
		OnProgress: func(d, tot int) {
			mu.Lock()
			if d > lastDone {
				lastDone = d
			}
			lastTotal = tot
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("too slow: %v", el)
	}
	if len(got) != len(fp.alive) {
		t.Fatalf("alive %d want %d", len(got), len(fp.alive))
	}
	for ip, rtt := range got {
		if rtt != 3*time.Millisecond || !fp.alive[ip] {
			t.Fatalf("bad result %v %v", ip, rtt)
		}
	}
	if lastDone != 1024 || lastTotal != 1024 {
		t.Fatalf("progress %d/%d", lastDone, lastTotal)
	}
}

func TestSweepCancel(t *testing.T) {
	ts := addrs(1024)
	fp := &fakePinger{alive: map[netip.Addr]bool{}, delay: 5 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() { res <- Sweep(ctx, ts, Config{Pinger: fp}) }()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case err := <-res:
		if err != context.Canceled {
			t.Fatalf("err = %v", err)
		}
		if el := time.Since(start); el > 200*time.Millisecond {
			t.Fatalf("cancel took %v", el)
		}
	case <-time.After(time.Second):
		t.Fatal("sweep did not stop")
	}
}

func TestSweepNilPinger(t *testing.T) {
	if err := Sweep(context.Background(), addrs(1), Config{}); err == nil {
		t.Fatal("want error")
	}
}

func TestSweepEmpty(t *testing.T) {
	if err := Sweep(context.Background(), nil, Config{Pinger: &fakePinger{}}); err != nil {
		t.Fatal(err)
	}
}

func TestPingLoopback(t *testing.T) {
	if os.Getenv("NETDIAG_LIVE") != "1" {
		t.Skip("set NETDIAG_LIVE=1")
	}
	rtt, ok, err := NewPinger().Ping(context.Background(), netip.MustParseAddr("127.0.0.1"), 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	t.Logf("rtt %v", rtt)
}
