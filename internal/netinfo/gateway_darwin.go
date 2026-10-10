//go:build darwin

package netinfo

import (
	"context"
	"os/exec"
	"time"
)

// DefaultRoute returns the default IPv4 route via `route -n get default`.
func DefaultRoute() (Route, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "route", "-n", "get", "default").Output()
	if err != nil {
		return Route{}, err
	}
	return parseDarwinRoute(string(out))
}
