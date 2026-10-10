package scan

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"netdiag/internal/model"
	"netdiag/internal/snmpprobe"
)

func probeDeps() Deps {
	d := fakeDeps()
	d.LocalPrefixes = func() ([]netip.Prefix, error) {
		return []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}, nil
	}
	return d
}

func TestProbeHostHappy(t *testing.T) {
	d := probeDeps()
	d.Ports = fakePorts{"10.0.0.10": {80, 9100}}
	o := New(d)
	h, err := o.ProbeHost(context.Background(), "10.0.0.10", "")
	if err != nil {
		t.Fatal(err)
	}
	if !h.Alive || len(h.OpenPorts) != 2 || h.Snmp == nil || h.Printer == nil || len(h.Web) == 0 {
		t.Fatalf("incomplete host: %+v", h)
	}
	if h.DeviceType == "" {
		t.Fatalf("no device type: %+v", h)
	}
	h, err = o.ProbeHost(context.Background(), "10.0.0.1", "")
	if err != nil || h.Hostname != "router.lan" || h.MAC != "00:11:22:33:44:01" {
		t.Fatalf("gateway host: %+v err=%v", h, err)
	}
}

func TestProbeHostNoResponse(t *testing.T) {
	h, err := New(probeDeps()).ProbeHost(context.Background(), "10.0.0.99", "")
	if err != nil || h.Alive || h.IP != "10.0.0.99" {
		t.Fatalf("got %+v err=%v", h, err)
	}
}

func TestProbeHostRejects(t *testing.T) {
	o := New(probeDeps())
	if _, err := o.ProbeHost(context.Background(), "8.8.8.8", ""); !errors.Is(err, ErrNotLocal) {
		t.Fatalf("err = %v, want ErrNotLocal", err)
	}
	for _, bad := range []string{"", "nope", "::1", "10.0.0.256"} {
		if _, err := o.ProbeHost(context.Background(), bad, ""); err == nil || errors.Is(err, ErrNotLocal) {
			t.Fatalf("%q: err = %v", bad, err)
		}
	}
}

func TestProbeHostCancelled(t *testing.T) {
	d := probeDeps()
	d.Pinger = fakePinger{block: true}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := New(d).ProbeHost(ctx, "10.0.0.10", "")
	if err == nil || time.Since(start) > 900*time.Millisecond {
		t.Fatalf("err=%v elapsed=%v", err, time.Since(start))
	}
	ctx2, c2 := context.WithCancel(context.Background())
	c2()
	if _, err := New(d).ProbeHost(ctx2, "10.0.0.10", ""); err == nil {
		t.Fatal("expected error for pre-cancelled ctx")
	}
}

func TestProbeHostDoesNotAffectScan(t *testing.T) {
	d := probeDeps()
	d.Pinger = fakePinger{alive: map[string]bool{"10.0.0.10": true, "10.0.0.11": true}}
	d.SNMP = fakeSNMP{"10.0.0.10": d.SNMP.(fakeSNMP)["10.0.0.10"], "10.0.0.77": &snmpprobe.Result{Snmp: &model.SnmpInfo{Version: "v2c"}}}
	o := New(d)
	s, err := o.Start(context.Background(), model.ScanRequest{CIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.ProbeHost(context.Background(), "10.0.0.77", ""); err != nil {
		t.Fatal(err)
	}
	res := s.Wait()
	for _, h := range res.Hosts {
		if h.IP == "10.0.0.77" {
			t.Fatalf("probe leaked into scan: %+v", h)
		}
	}
}
