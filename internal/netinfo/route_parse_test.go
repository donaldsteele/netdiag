package netinfo

import "testing"

const macRoute = `   route to: default
destination: default
       mask: default
    gateway: 192.168.1.1
  interface: en0
      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING,GLOBAL>
`

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
