package discovery

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

// TestWindowsPingLoopback checks the production pinger on Windows: the raw
// ICMP socket of pro-bing's privileged mode opens and gets an echo reply
// from 127.0.0.1. Run from a non-elevated shell it also shows that no
// administrator rights are needed.
func TestWindowsPingLoopback(t *testing.T) {
	if !privileged {
		t.Fatal("Windows needs pro-bing's privileged mode; its UDP mode does not exist there")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rtt, ok, err := NewPinger().Ping(ctx, netip.MustParseAddr("127.0.0.1"), 2*time.Second)
	if err != nil {
		t.Fatalf("ping 127.0.0.1: %v", err)
	}
	if !ok {
		t.Fatal("no echo reply from 127.0.0.1")
	}
	t.Logf("127.0.0.1 answered in %v", rtt)
}
