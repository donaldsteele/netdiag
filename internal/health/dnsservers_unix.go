//go:build !windows

package health

import (
	"context"
	"os"
)

// readDNSServers lists the nameservers in /etc/resolv.conf. On macOS that
// file is kept up to date with the DNS servers of the primary connection.
func readDNSServers(_ context.Context, _, _ string) ([]string, error) {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseResolvConf(f), nil
}
