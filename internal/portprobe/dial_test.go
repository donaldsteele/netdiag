package portprobe

import (
	"context"
	"errors"
	"net"
	"testing"
)

// closedPort returns a loopback port with no listener.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

func TestDialRefused(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	c, err := Dial(context.Background(), "127.0.0.1", l.Addr().(*net.TCPAddr).Port, ConnectTimeout)
	if err != nil {
		t.Fatalf("open port: %v", err)
	}
	c.Close()

	_, err = Dial(context.Background(), "127.0.0.1", closedPort(t), ConnectTimeout)
	if !Refused(err) {
		t.Fatalf("closed port: Refused(%v) = false", err)
	}
}

// TestConnectTimeoutUsesFastRefuse: probes longer than fastRefuseMax lose
// fast refusal on Windows.
func TestConnectTimeoutUsesFastRefuse(t *testing.T) {
	if ConnectTimeout > fastRefuseMax {
		t.Fatalf("ConnectTimeout %v > fastRefuseMax %v", ConnectTimeout, fastRefuseMax)
	}
}

func TestRefusedOtherErrors(t *testing.T) {
	for _, err := range []error{nil, context.DeadlineExceeded, errors.New("i/o timeout")} {
		if Refused(err) {
			t.Errorf("Refused(%v) = true", err)
		}
	}
}
