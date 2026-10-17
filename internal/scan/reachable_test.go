package scan

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestPortCheckerRefusedMeansUp guards the presence fallback: a host that
// refuses the connection is up. On Windows this needs WSAECONNREFUSED, not
// syscall.ECONNREFUSED.
func TestPortCheckerRefusedMeansUp(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	open := l.Addr().(*net.TCPAddr).Port
	defer l.Close()
	l2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l2.Addr().(*net.TCPAddr).Port
	l2.Close()

	// portprobe.Dial only refuses fast on Windows for dials of 1 s or less.
	if PresenceTCPTimeout > time.Second {
		t.Fatalf("PresenceTCPTimeout %v is above 1 s", PresenceTCPTimeout)
	}
	var pc portChecker
	ctx := context.Background()
	if !pc.Reachable(ctx, "127.0.0.1", open, PresenceTCPTimeout) {
		t.Error("open port: not reachable")
	}
	if !pc.Reachable(ctx, "127.0.0.1", closed, PresenceTCPTimeout) {
		t.Error("refused port: not reachable")
	}
	// Port 9 (discard): transparent web proxies only intercept 80/443.
	if pc.Reachable(ctx, "192.0.2.1", 9, 200*time.Millisecond) {
		t.Error("silent TEST-NET address: reachable")
	}
}
