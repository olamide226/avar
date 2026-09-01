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
// Adding a host means adding a row, not a branch somewhere in cmd/. Each row is
// a claim that avar can construct that backend on that host, so a row is added
// in the change that makes the claim true and not before: an entry for a
// provider avar cannot build would turn a clear "unsupported host" message into
// a failure much further in.
func providerForHost(goos string) (types.ProviderID, bool) {
	switch goos {
	case "darwin":
		return types.ProviderLima, true
	case "windows":
		return types.ProviderWSL2, true
	default:
		return "", false
	}
}

// HostProviderID returns the backend for the host avar is running on.
//
// It fails before any dependency work, so an unsupported host is told plainly
// that avar does not support it rather than being sent to install a runtime and
// discovering the same thing more slowly (Req 17.6).
func HostProviderID() (types.ProviderID, error) {
	return providerIDFor(runtime.GOOS)
}

func providerIDFor(goos string) (types.ProviderID, error) {
	id, ok := providerForHost(goos)
	if !ok {
		return "", fmt.Errorf(
			"avr does not support %s: it runs on macOS, where it uses Lima, "+
				"and on Windows, where it uses WSL 2", hostName(goos))
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
