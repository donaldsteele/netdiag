//go:build windows

package arp

var command = []string{"arp", "-a"}

func parse(s string) []Entry { return ParseWindows(s) }
