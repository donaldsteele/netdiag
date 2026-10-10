//go:build windows

package netinfo

import (
	"context"
	"os/exec"
	"time"
)

// DefaultRoute returns the default IPv4 route via `route print`.
func DefaultRoute() (Route, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "route", "print", "-4", "0.0.0.0").Output()
	if err != nil {
		return Route{}, err
	}
	return parseWindowsRoute(string(out))
}
