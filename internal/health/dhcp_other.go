//go:build !windows && !darwin

package health

import (
	"context"
	"errors"
)

func readDHCP(ctx context.Context, r *Runner, iface, ip string) (DHCPInfo, error) {
	return DHCPInfo{}, errors.New("DHCP lease check not supported on this OS")
}
