// Package envpolicy decides which host environment variables cross into a
// guest, and with what values.
//
// It is the executable form of avar's central security promise: the guest
// receives nothing from the host that the user did not explicitly grant
// (REQ-9.1, PROP-4). A developer's shell profile is full of things that must
// not leak into Linux — cloud credentials, registry tokens, session cookies —
// and the way to guarantee they do not is to build the guest environment from
// an allowlist rather than to strip a blocklist from the host's. A blocklist
// is wrong the first time somebody invents a new variable name; an allowlist
// is wrong only about variables avar was never asked to carry.
//
// Compose is pure: it reads no process environment, no file, and no clock.
// Everything it needs arrives as an argument, which is what lets the property
// that matters — no variable outside the allowlist ever appears in the result —
// be checked against arbitrary generated host environments rather than against
// the one the test process happens to have.
//
// Phase 1 wires the base allowlist only. Requirement 12's explicit forwarding
// (`--env NAME`, `--env-file PATH`, `--ssh-agent`) is deliberately absent: it
// becomes further fields on Input and further steps in Compose, applied over
// the base, rather than a different shape of function.
package envpolicy

import (
	"os"
	"slices"
	"strings"
)

// DefaultTERM is the terminal type the guest is told about when the host has
// nothing usable to say (REQ-3.2).
//
// The host value is passed through whenever there is one, because it describes
// the terminal the user is actually sitting at. "dumb" and an unset TERM are
// the two cases where passing it through would be actively harmful: a guest
// that believes it is on a dumb terminal renders no colour, and full-screen
// programs — vim, htop, less — refuse to start at all rather than draw
// something unusable.
//
// xterm-256color is the substitute because it is the entry every distribution's
// terminfo database has (ncurses ships it in the base terminfo package on
// Ubuntu, Debian and Fedora alike), it advertises colour, and every terminal
// emulator a macOS developer is plausibly using is compatible with it. A more
// capable guess such as one of the truecolor entries would be absent from a
// minimal guest's terminfo and leave the user with a broken display, which is
// worse than eight bits of colour.
const DefaultTERM = "xterm-256color"

// dumbTERM is the terminal type that means "assume nothing works".
const dumbTERM = "dumb"

// localePrefix marks the locale variables the allowlist admits as a family:
// LC_ALL, LC_CTYPE, LC_TIME and the rest are open-ended by POSIX, so they are
// matched by prefix rather than enumerated.
const localePrefix = "LC_"

// termName is the variable full-screen programs read to decide what the
// terminal can do.
const termName = "TERM"

// baseAllowlist is what crosses into every guest session with no grant from the
// user at all: the terminal type and the locale (REQ-9.1). Both describe the
// terminal the command is being run from rather than anything about the host
// machine, which is what makes them safe to carry when nothing else is.
//
// LC_* joins them by prefix, see allowed.
var baseAllowlist = []string{"LANG", termName}

// Input is everything the policy decides from.
//
// It is a struct rather than a bare argument so that Requirement 12's grants
// become fields — a `--env` list, an `--env-file`'s parsed contents, a
// configured persistent allowlist — without changing the signature or the
// callers that already pass a host environment and nothing else.
type Input struct {
	// Host is the host process environment, as name → value. A nil map is a
	// host that offers nothing, which is a legitimate input rather than an
	// error: the result is then avar's own defaults.
	Host map[string]string
}

// Compose returns the environment for one guest execution.
//
// The result is complete: it is exactly what the provider must put into the
// guest process, with nothing merged in from the host afterwards
// (provider.ShellOpts.Env). TERM is always present, because a session without
// one renders badly enough to look like a bug (REQ-3.2); everything else is
// present only if the host had it.
func Compose(in Input) map[string]string {
	out := make(map[string]string, len(baseAllowlist))
	for name, value := range in.Host {
		if !allowed(name) {
			continue
		}
		out[name] = value
	}
	out[termName] = terminalType(in.Host[termName])
	return out
}

// Allows reports whether a host variable of this name crosses into the guest
// under the base policy. It is the same question Compose answers and is
// exported so that a caller which has to explain a decision — a diagnostic, a
// test asserting the boundary — asks the policy instead of restating it.
func Allows(name string) bool { return allowed(name) }

// allowed reports whether name is in the base allowlist.
//
// A name carrying "=" is refused whatever it is called. Such a name cannot be
// created by a shell, but it can exist in a process environment, and every
// transport that carries an environment carries it as NAME=VALUE text: a name
// containing the separator would be read back as a different variable
// altogether. Refusing it here means no backend has to defend against it.
func allowed(name string) bool {
	if name == "" || strings.Contains(name, "=") {
		return false
	}
	if strings.HasPrefix(name, localePrefix) {
		return true
	}
	return slices.Contains(baseAllowlist, name)
}

// terminalType applies the TERM rule: the host's value, or a colour-capable
// default when the host has nothing usable.
func terminalType(hostTERM string) string {
	term := strings.TrimSpace(hostTERM)
	if term == "" || strings.EqualFold(term, dumbTERM) {
		return DefaultTERM
	}
	return term
}

// HostEnviron snapshots the host process environment in the form Compose
// consumes.
//
// It is the one function here that reads the world, and it exists so that the
// command layer does not have to take os.Environ apart itself. Compose stays
// pure, which is what makes the policy testable against host environments no
// real process would have.
//
// A malformed entry — one with no "=" at all, which execve permits and no
// shell produces — is dropped rather than turned into an empty-valued
// variable, because avar cannot know which of the two the caller meant.
func HostEnviron() map[string]string {
	entries := os.Environ()
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		out[name] = value
	}
	return out
}
