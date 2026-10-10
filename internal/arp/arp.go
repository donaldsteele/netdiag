// Package arp reads the OS ARP cache and parses it into IP/MAC entries.
package arp

import (
	"context"
	"os/exec"
	"time"
)

// Entry is one resolved ARP table row.
type Entry struct {
	IP  string // dotted IPv4
	MAC string // normalized AA:BB:CC:DD:EE:FF
}

const execTimeout = 2 * time.Second

// Table runs the platform ARP command (2s timeout) and returns valid entries.
func Table(ctx context.Context) ([]Entry, error) {
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, command[0], command[1:]...).Output()
	if err != nil {
		return nil, err
	}
	return parse(string(out)), nil
}
