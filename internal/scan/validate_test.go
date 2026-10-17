package scan

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"netdiag/internal/model"
)

func TestValidatePorts(t *testing.T) {
	seq := func(n int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = 1000 + i
		}
		return out
	}
	ok := []struct {
		in, want []int
	}{
		{nil, model.DefaultPorts},
		{[]int{}, model.DefaultPorts},
		{[]int{443, 80, 443, 80, 9100}, []int{443, 80, 9100}},
		{[]int{1, 65535}, []int{1, 65535}},
		{seq(MaxPorts), seq(MaxPorts)},
		{append(seq(MaxPorts), seq(MaxPorts)...), seq(MaxPorts)}, // duplicates do not count
	}
	for _, c := range ok {
		in := slices.Clone(c.in)
		req, _, err := Validate(model.ScanRequest{CIDR: "10.0.0.1", Ports: in})
		if err != nil || !slices.Equal(req.Ports, c.want) {
			t.Errorf("Validate(%v) = %v, %v; want %v", c.in, req.Ports, err, c.want)
		}
		if !slices.Equal(in, c.in) {
			t.Errorf("Validate changed the caller's ports: %v", in)
		}
	}
	req, _, _ := Validate(model.ScanRequest{CIDR: "10.0.0.1"})
	req.Ports[0] = 1
	if model.DefaultPorts[0] == 1 {
		t.Fatal("default ports shared with the request")
	}

	bad := []struct {
		in   []int
		want string
	}{
		{[]int{80, 0}, "0 is not a port number"},
		{[]int{-1}, "-1 is not a port number"},
		{[]int{65536}, "65536 is not a port number"},
		{seq(MaxPorts + 1), "too many ports"},
	}
	for _, c := range bad {
		_, _, err := Validate(model.ScanRequest{CIDR: "10.0.0.1", Ports: c.in})
		if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Validate(%d ports) error = %v, want %q", len(c.in), err, c.want)
		}
	}
}

// A request with bad ports never stops a running live scan.
func TestBadPortsRejectedBeforeStart(t *testing.T) {
	reg := NewRegistry(New(fakeDeps()))
	if _, err := reg.Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24", Ports: []int{70000}}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := reg.Running(); ok {
		t.Fatal("a scan started")
	}
}
