//go:build windows

package health

import (
	"context"
	"time"
)

func readWiFi(ctx context.Context, r *Runner, iface string) (WiFiInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := r.Exec(ctx, "netsh", "wlan", "show", "interfaces")
	if err != nil {
		return WiFiInfo{}, err
	}
	return parseNetshWlan(string(out))
}
