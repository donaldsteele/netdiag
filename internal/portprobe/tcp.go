// Package portprobe performs TCP connect probes and HTTP(S) fingerprinting.
package portprobe

import (
	"context"
	"sort"
	"sync"
	"time"
)

// ConnectTimeout bounds each TCP connect attempt.
var ConnectTimeout = 800 * time.Millisecond

// PortWorkers caps how many connection attempts ScanPorts makes to one host
// at the same time.
const PortWorkers = 16

// dialPort is Dial; tests replace it.
var dialPort = Dial

// ScanPorts TCP-connects to ports on ip, at most PortWorkers at a time, and
// returns the sorted open ports. It stops starting new attempts once ctx is
// cancelled.
func ScanPorts(ctx context.Context, ip string, ports []int) []int {
	var mu sync.Mutex
	var wg sync.WaitGroup
	open := []int{}
	slots := make(chan struct{}, PortWorkers)
loop:
	for _, p := range ports {
		if ctx.Err() != nil {
			break
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			break loop
		}
		wg.Add(1)
		go func(p int) {
			defer func() { <-slots; wg.Done() }()
			c, err := dialPort(ctx, ip, p, ConnectTimeout)
			if err != nil {
				return
			}
			c.Close()
			mu.Lock()
			open = append(open, p)
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	sort.Ints(open)
	return open
}

// SchemeFor returns the HTTP scheme to use for a port, or "" if not a web port.
func SchemeFor(port int) string {
	switch port {
	case 80, 8080:
		return "http"
	case 443, 8443:
		return "https"
	}
	return ""
}
