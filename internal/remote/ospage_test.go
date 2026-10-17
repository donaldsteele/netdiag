package remote

import (
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"

	"netdiag/internal/model"
)

// TestOSPagesFixed: the list holds exactly the pages the advice uses, each
// an ms-settings: link with no room for anything a request could add.
func TestOSPagesFixed(t *testing.T) {
	var names []string
	for name, target := range osPages {
		names = append(names, name)
		if !strings.HasPrefix(target, "ms-settings:") || strings.ContainsAny(target, " /\\&|\"'") {
			t.Errorf("%s opens %q", name, target)
		}
	}
	if slices.Sort(names); !slices.Equal(names, []string{model.OSPageLocation}) {
		t.Fatalf("pages %v", names)
	}
	if !KnownOSPage("location") {
		t.Fatal("location is not known")
	}
	for _, bad := range []string{"", "Location", "printers", "ms-settings:privacy-location", "location ", "../location", "http://x/"} {
		if KnownOSPage(bad) {
			t.Errorf("KnownOSPage(%q)", bad)
		}
		if err := OpenOSPage(bad); !errors.Is(err, ErrUnknownOSPage) {
			t.Errorf("OpenOSPage(%q) = %v", bad, err)
		}
	}
}

// Off Windows, every page is unsupported (the route answers 501). On
// Windows the real page is not opened by tests: it would start Settings.
func TestOSPageUnsupportedOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("would open the Settings app")
	}
	if err := OpenOSPage(model.OSPageLocation); !errors.Is(err, ErrOSPageUnsupported) {
		t.Fatalf("got %v", err)
	}
}
