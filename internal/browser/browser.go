// Package browser opens a web address in the host's default browser, which is
// what `avr open` does once it knows a port is forwarded (REQ-16.2).
//
// Opening a browser is a side effect on the user's desktop, so it sits behind
// Opener and the command layer is handed one: flow tests substitute a double
// that records the address instead of starting a browser. The package prints
// nothing.
//
// Each host has its own mechanism, selected by build tags as the rest of avar's
// host-specific code is:
//
//   - macOS runs `/usr/bin/open -u <url>`. The -u flag tells open(1) the
//     argument is a URL, so it is never mistaken for a file name.
//   - Windows calls ShellExecuteW with the "open" verb, as github.com/pkg/browser
//     does. No command interpreter is involved, so the address is never parsed
//     by `cmd.exe` — the familiar `cmd /c start <url>` breaks on `&` in a query
//     string and treats a quoted first argument as a window title.
package browser

import (
	"context"
	"fmt"
	"net/url"
)

// Opener opens a web address in the host's default browser.
type Opener interface {
	// Open asks the host to show address in the default browser and returns
	// once the request has been handed over. It does not wait for the page to
	// load, and cannot tell whether anything answered at the address.
	Open(ctx context.Context, address string) error
}

// System returns the host's own Opener.
func System() Opener { return systemOpener{} }

// systemOpener is implemented per host in browser_<host>.go.
type systemOpener struct{}

// Open validates the address and hands it to the host.
func (systemOpener) Open(ctx context.Context, address string) error {
	if err := validate(address); err != nil {
		return err
	}
	if err := openURL(ctx, address); err != nil {
		return fmt.Errorf("opening %s in your browser: %w; open that address in a browser yourself", address, err)
	}
	return nil
}

// validate refuses anything but an absolute http or https address.
//
// avar only ever builds http://localhost:<port>, so this refuses nothing avar
// asks for. It exists because both host mechanisms open whatever they are given
// with whatever application is registered for it: a file path or another scheme
// reaching them by mistake would launch something other than a browser.
func validate(address string) error {
	u, err := url.Parse(address)
	if err != nil {
		return fmt.Errorf("opening %q in your browser: it is not a web address: %w", address, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("opening %q in your browser: only http and https addresses are opened", address)
	}
	return nil
}
