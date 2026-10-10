package netinfo

import "testing"

const winRoute = `===========================================================================
Interface List
  8...00 15 5d 01 02 03 ......Hyper-V Virtual Ethernet Adapter
 12...a4 b1 c2 d3 e4 f5 ......Intel(R) Wi-Fi 6 AX201 160MHz
===========================================================================

IPv4 Route Table
===========================================================================
Active Routes:
Network Destination        Netmask          Gateway       Interface  Metric
          0.0.0.0          0.0.0.0      172.20.0.1      172.20.0.5     5000
          0.0.0.0          0.0.0.0      192.168.1.1    192.168.1.50       35
===========================================================================
Persistent Routes:
  None
`

const macRoute = `   route to: default
destination: default
       mask: default
    gateway: 192.168.1.1
  interface: en0
      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING,GLOBAL>
`

func TestParseWindowsRoute(t *testing.T) {
	rt, err := parseWindowsRoute(winRoute)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Gateway.String() != "192.168.1.1" || rt.IfaceIP.String() != "192.168.1.50" {
		t.Fatalf("got %+v", rt)
	}
	if _, err := parseWindowsRoute("Active Routes:\n None\n"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseDarwinRoute(t *testing.T) {
	rt, err := parseDarwinRoute(macRoute)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Gateway.String() != "192.168.1.1" || rt.IfaceName != "en0" {
		t.Fatalf("got %+v", rt)
	}
	if _, err := parseDarwinRoute("route: writing to routing socket: not in table\n"); err == nil {
		t.Fatal("expected error")
	}
}
