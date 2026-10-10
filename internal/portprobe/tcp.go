// Package portprobe performs TCP connect probes and HTTP(S) fingerprinting.
package portprobe

import (
	"context"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"
)

// ConnectTimeout bounds each TCP connect attempt.
var ConnectTimeout = 800 * time.Millisecond

// ScanPorts concurrently TCP-connects to ports on ip and returns the sorted open ports.
func ScanPorts(ctx context.Context, ip string, ports []int) []int {
	var mu sync.Mutex
	var wg sync.WaitGroup
	open := []int{}
	for _, p := range ports {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			d := net.Dialer{Timeout: ConnectTimeout}
			c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(p)))
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
