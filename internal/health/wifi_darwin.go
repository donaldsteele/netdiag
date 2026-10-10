//go:build darwin

package health

import (
	"context"
	"time"
)

func readWiFi(ctx context.Context, r *Runner, iface string) (WiFiInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := r.Exec(ctx, "system_profiler", "SPAirPortDataType", "-json")
	if err != nil {
		return WiFiInfo{}, err
	}
	return parseSystemProfiler(out, iface)
}
