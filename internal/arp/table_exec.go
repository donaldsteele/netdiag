//go:build !windows

package arp

import (
	"context"
	"os/exec"
)

// readTable runs `arp -an` and parses its output (incomplete rows skipped).
func readTable(ctx context.Context) ([]Entry, error) {
	out, err := exec.CommandContext(ctx, "arp", "-an").Output()
	if err != nil {
		return nil, err
	}
	return ParseDarwin(string(out)), nil
}
