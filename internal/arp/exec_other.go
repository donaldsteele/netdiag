//go:build !windows && !darwin

package arp

var command = []string{"arp", "-an"}

func parse(s string) []Entry { return ParseDarwin(s) }
