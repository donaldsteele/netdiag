//go:build !windows && !darwin

package health

import (
	"context"
	"errors"
)

func readWiFi(ctx context.Context, r *Runner, iface string) (WiFiInfo, error) {
	return WiFiInfo{}, errors.New("wi-fi info not supported on this OS")
}
