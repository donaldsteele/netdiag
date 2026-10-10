package netinfo

import (
	"bufio"
	"errors"
	"net/netip"
	"strconv"
	"strings"
)

// parseWindowsRoute extracts the lowest-metric default route from the output
// of `route print -4 0.0.0.0`.
func parseWindowsRoute(out string) (Route, error) {
	best := -1
	var rt Route
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || f[0] != "0.0.0.0" || f[1] != "0.0.0.0" {
			continue
		}
		gw, err1 := netip.ParseAddr(f[2])
		ifc, err2 := netip.ParseAddr(f[3])
		m, err3 := strconv.Atoi(f[4])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		if best < 0 || m < best {
			best, rt = m, Route{Gateway: gw, IfaceIP: ifc}
		}
	}
	if best < 0 {
		return Route{}, errors.New("no default route found")
	}
	return rt, nil
}

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
