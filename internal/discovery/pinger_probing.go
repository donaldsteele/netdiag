package discovery

import (
	"context"
	"net/netip"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

// ProbingPinger implements Pinger using pro-bing.
type ProbingPinger struct{}

// NewPinger returns the production Pinger.
func NewPinger() Pinger { return ProbingPinger{} }

// Ping sends one echo request to ip.
func (ProbingPinger) Ping(ctx context.Context, ip netip.Addr, timeout time.Duration) (time.Duration, bool, error) {
	p, err := probing.NewPinger(ip.String())
	if err != nil {
		return 0, false, err
	}
	p.Count = 1
	p.Timeout = timeout
	p.SetPrivileged(privileged)
	if err := p.RunWithContext(ctx); err != nil {
		if ctx.Err() != nil {
			return 0, false, nil
		}
		return 0, false, err
	}
	st := p.Statistics()
	if st.PacketsRecv < 1 {
		return 0, false, nil
	}
	return st.MinRtt, true, nil
}
