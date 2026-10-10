// Package discovery implements the ICMP sweep stage.
package discovery

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults from the locked scanning limits.
//
// DefaultRate and DefaultBurst pace the sweep with a token bucket. They were
// 2000/s with burst 256 in v0.1; v0.2 lowered them to 500/s and burst 64
// because cheap travel routers drop or stall under bursts of ICMP. A /24
// (254 hosts, default retries) still completes in about one second at the
// default rate, and a /22 stays well inside the 10s budget per subnet.
const (
	DefaultWorkers = 256
	DefaultTimeout = time.Second
	DefaultRetries = 1
	DefaultRate    = 500 // probes per second (token bucket refill)
	DefaultBurst   = 64  // token bucket capacity: probes allowed back-to-back
)

// Pinger sends a single echo request. ok is false when no reply arrived
// within timeout; err is reserved for local failures (socket errors).
type Pinger interface {
	Ping(ctx context.Context, ip netip.Addr, timeout time.Duration) (rtt time.Duration, ok bool, err error)
}

// Result is one responding host.
type Result struct {
	IP  netip.Addr
	RTT time.Duration
}

// Config controls a sweep. Zero values select defaults.
type Config struct {
	Pinger     Pinger
	Workers    int
	Timeout    time.Duration
	Retries    int // extra attempts after the first; negative means none
	Rate       int // probes/second; 0 selects DefaultRate, negative disables pacing
	OnAlive    func(Result)
	OnProgress func(done, total int) // called after each target completes
}

// Sweep pings all targets and invokes OnAlive for each responder as soon as
// it replies. Callbacks may run concurrently. It returns ctx.Err() if
// cancelled, otherwise the first local pinger error (if every attempt on
// some host failed locally), otherwise nil.
func Sweep(ctx context.Context, targets []netip.Addr, cfg Config) error {
	if cfg.Pinger == nil {
		return errors.New("discovery: nil Pinger")
	}
	workers := cfg.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	retries := cfg.Retries
	if retries == 0 {
		retries = DefaultRetries
	} else if retries < 0 {
		retries = 0
	}
	rate := cfg.Rate
	if rate == 0 {
		rate = DefaultRate
	}
	var tb *bucket
	if rate > 0 {
		tb = newBucket(rate, DefaultBurst)
	}

	total := len(targets)
	jobs := make(chan netip.Addr)
	var done atomic.Int64
	var errOnce sync.Once
	var firstErr error
	var wg sync.WaitGroup

	if workers > total {
		workers = total
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip := range jobs {
				var lastErr error
				for attempt := 0; attempt <= retries; attempt++ {
					if tb != nil && tb.wait(ctx) != nil {
						break
					}
					rtt, ok, err := cfg.Pinger.Ping(ctx, ip, timeout)
					if ctx.Err() != nil {
						break
					}
					if err != nil {
						lastErr = err
						continue
					}
					lastErr = nil
					if ok {
						if cfg.OnAlive != nil {
							cfg.OnAlive(Result{IP: ip, RTT: rtt})
						}
						break
					}
				}
				if lastErr != nil {
					errOnce.Do(func() { firstErr = lastErr })
				}
				if ctx.Err() == nil && cfg.OnProgress != nil {
					cfg.OnProgress(int(done.Add(1)), total)
				}
			}
		}()
	}

feed:
	for _, ip := range targets {
		select {
		case jobs <- ip:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	return firstErr
}

// bucket is a simple token bucket.
type bucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newBucket(rate, burst int) *bucket {
	if burst < 1 {
		burst = 1
	}
	return &bucket{rate: float64(rate), burst: float64(burst), tokens: float64(burst), last: time.Now()}
}

func (b *bucket) wait(ctx context.Context) error {
	for {
		b.mu.Lock()
		now := time.Now()
		b.tokens += now.Sub(b.last).Seconds() * b.rate
		if b.tokens > b.burst {
			b.tokens = b.burst
		}
		b.last = now
		if b.tokens >= 1 {
			b.tokens--
			b.mu.Unlock()
			return ctx.Err()
		}
		need := time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
		b.mu.Unlock()
		if need < time.Millisecond {
			need = time.Millisecond
		}
		t := time.NewTimer(need)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}
