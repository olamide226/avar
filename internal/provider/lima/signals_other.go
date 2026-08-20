//go:build !unix

package lima

import "os"

// forwardedSignals is empty off Unix. SIGWINCH does not exist there, and the
// two signals that do would have nowhere to be delivered: see relaySignals.
var forwardedSignals []os.Signal

// relaySignals does nothing off Unix, and says so rather than pretending.
//
// The Unix relay works by signalling avar's own process group, which is a
// concept Windows does not have — the nearest thing, a console control event,
// is delivered to a console's attached processes rather than to a group avar is
// a member of, and it carries no signal number to relay. There is nothing
// faithful to do with a SIGWINCH that cannot be raised.
//
// Nothing is lost by that, because this backend does not run here. Lima is
// macOS-only (REQ-17.6) and provider.SupportedHost refuses every other host, so
// this file exists to keep the package compiling on the platform the
// WSL2Provider is being built for, in the same spirit as diskusage_other.go.
// The guest-facing equivalent for Windows is the WSL2Provider's own console
// handling (design §3.6), not this.
func relaySignals() (stop func()) { return func() {} }
