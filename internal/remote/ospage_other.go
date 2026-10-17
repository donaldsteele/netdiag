//go:build !windows

package remote

// openOSPage: every page in osPages is a Windows settings page.
func openOSPage(string) error { return ErrOSPageUnsupported }
