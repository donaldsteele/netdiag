package netinfo

import (
	"bufio"
	"errors"
	"net/netip"
	"strings"
)

// parseDarwinRoute parses the output of `route -n get default`.
func parseDarwinRoute(out string) (Route, error) {
	var rt Route
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "gateway":
			if a, err := netip.ParseAddr(v); err == nil {
				rt.Gateway = a
			}
		case "interface":
			rt.IfaceName = v
		}
	}
	if rt.IfaceName == "" && !rt.Gateway.IsValid() {
		return Route{}, errors.New("no default route found")
	}
	return rt, nil
}
