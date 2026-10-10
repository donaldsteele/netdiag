//go:build !windows && !darwin

package netinfo

import "errors"

// DefaultRoute is unsupported on this platform.
func DefaultRoute() (Route, error) {
	return Route{}, errors.New("default route detection not supported on this OS")
}
