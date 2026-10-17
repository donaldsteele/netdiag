package remote

// openOSPage hands an ms-settings: link to Windows with ShellExecute, the
// way the Run box would; netdiag does not wait for the Settings app.
func openOSPage(target string) error { return shellExecute(target, "") }
