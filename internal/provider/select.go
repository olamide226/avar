package provider

import (
	"fmt"
	"runtime"

	"github.com/olamide226/avar/internal/types"
)

// SupportedHost reports whether avar has a backend for a GOOS value.
//
// Provider choice follows the host and is never a user-visible flag: the whole
// product claim is that the command grammar is identical wherever avr runs, and
// a `--provider` flag would be avar asking the user to know which virtualization
// technology their operating system uses (Req 18.1, Req 18.14).
func SupportedHost(goos string) bool {
	_, ok := providerForHost(goos)
	return ok
}

// providerForHost maps a GOOS value onto the backend that serves it.
//
// Adding a host means adding a row, not a branch somewhere in cmd/. The Windows
// row is deliberately absent until the WSL2Provider exists (Phase 4): claiming a
// provider avar cannot construct would turn a clear "unsupported host" message
// into a failure much further in.
func providerForHost(goos string) (types.ProviderID, bool) {
	switch goos {
	case "darwin":
		return types.ProviderLima, true
	default:
		return "", false
	}
}

// HostProviderID returns the backend for the host avar is running on.
//
// It fails before any dependency work, so an unsupported host is told plainly
// that avar does not support it rather than being sent to install Lima and
// discovering the same thing more slowly (Req 17.6).
func HostProviderID() (types.ProviderID, error) {
	return providerIDFor(runtime.GOOS)
}

func providerIDFor(goos string) (types.ProviderID, error) {
	id, ok := providerForHost(goos)
	if !ok {
		return "", fmt.Errorf(
			"avr does not support %s: it runs on macOS, where it uses Lima. "+
				"Windows support through WSL 2 is planned but not built yet", hostName(goos))
	}
	return id, nil
}

// hostName renders a GOOS value the way a user would name their own machine.
func hostName(goos string) string {
	switch goos {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux hosts"
	default:
		return goos
	}
}
