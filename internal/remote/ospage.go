package remote

import (
	"errors"

	"netdiag/internal/model"
)

// osPages are the pages of this computer's own settings that netdiag can
// open (POST /api/os/open), by name. The list is fixed: a request names a
// page, and only the value here is handed to Windows.
//
//   - location: ms-settings:privacy-location, the Windows Location privacy
//     page. Windows hides Wi-Fi details from apps until Location is on.
//
// ShellExecute hands an ms-settings: link to Windows, which starts its own
// Settings app (a packaged app that Windows starts, not a program netdiag
// runs or waits for).
var osPages = map[string]string{
	model.OSPageLocation: "ms-settings:privacy-location",
}

// ErrUnknownOSPage means the page is not one netdiag opens.
var ErrUnknownOSPage = errors.New("unknown settings page")

// ErrOSPageUnsupported means this computer has no such page for netdiag
// to open (every page is Windows only for now).
var ErrOSPageUnsupported = errors.New("netdiag can't open this settings page on this computer")

// KnownOSPage reports whether page is one netdiag can be asked to open.
func KnownOSPage(page string) bool {
	_, ok := osPages[page]
	return ok
}

// OpenOSPage opens the named page of this computer's settings. It returns
// ErrUnknownOSPage for a name not in the list and ErrOSPageUnsupported
// off Windows.
func OpenOSPage(page string) error {
	target, ok := osPages[page]
	if !ok {
		return ErrUnknownOSPage
	}
	return openOSPage(target)
}
