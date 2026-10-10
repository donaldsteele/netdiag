//go:build windows

package health

import (
	"context"
	"time"
)

func readDHCP(ctx context.Context, r *Runner, iface, ip string) (DHCPInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := r.Exec(ctx, "ipconfig", "/all")
	if err != nil {
		return DHCPInfo{}, err
	}
	return parseIpconfigAll(string(out), ip)
}
