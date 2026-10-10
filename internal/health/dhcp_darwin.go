//go:build darwin

package health

import (
	"context"
	"errors"
	"time"
)

func readDHCP(ctx context.Context, r *Runner, iface, ip string) (DHCPInfo, error) {
	if iface == "" {
		return DHCPInfo{}, errors.New("interface name unknown")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := r.Exec(ctx, "ipconfig", "getpacket", iface)
	if err != nil && len(out) == 0 {
		// getpacket prints nothing / exits non-zero when there is no lease.
		return DHCPInfo{IP: ip}, nil
	}
	info := parseGetPacket(string(out))
	if info.IP == "" {
		info.IP = ip
	}
	return info, nil
}
