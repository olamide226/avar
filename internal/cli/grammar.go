// Package cli holds avar's argv grammar: the single authority that decides how
// a command line splits into avar's own flags, an avar subcommand, and a guest
// command.
//
// The split is a pure function of argv so that it is deterministic and
// fuzzable, and so that no part of it can be perturbed by the flag-parsing
// behaviour of the command framework above it. The package performs no I/O,
// prints nothing, and keeps no global state.
//
// The grammar is:
//
//	avr [selector flags] [--] [COMMAND [ARGS...]]     no COMMAND -> interactive shell
//	avr [selector flags] SUBCOMMAND [subcommand flags/args]
package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/olamide226/avar/internal/types"
)

// Mode is how avar interprets an argv once the selector flags are consumed.
type Mode int

const (
	// ModeShell is `avr` with no command: open an interactive guest shell.
	ModeShell Mode = iota
	// ModeGuestCommand is `avr <command> [args...]`: run one command in the
	// guest and exit with its status.
	ModeGuestCommand
	// ModeSubcommand is `avr <subcommand> [...]`: an avar operation that
	// runs on the host.
	ModeSubcommand
)

// String renders the mode for diagnostics and test failure messages.
func (m Mode) String() string {
	switch m {
	case ModeShell:
		return "shell"
	case ModeGuestCommand:
		return "guest-command"
	case ModeSubcommand:
		return "subcommand"
	default:
		return fmt.Sprintf("mode(%d)", int(m))
	}
}

// Selector holds the environment-selecting flags exactly as the user gave
// them, validated but not defaulted. A zero-valued field means "not
// specified"; supplying defaults and the distro/version matrix is the
// resolver's job, not the grammar's.
type Selector struct {
	// Arch is the value of --arch, or "" if it was not given.
	Arch types.Arch
	// Distro is the name part of --distro, or "" if it was not given.
	Distro types.Distro
	// DistroVersion is the ":<version>" part of --distro, or "" if the user
	// gave a bare distro name and wants its pinned default version.
	DistroVersion string
	// Isolate requests this project's own machine (--isolate).
	Isolate bool
	// Shared requests the shared machine for this invocation (--shared).
	Shared bool
}

// Invocation is the fully resolved reading of an argv.
type Invocation struct {
	// Selector carries the environment-selecting flags, which apply in
	// every mode.
	Selector Selector

	Mode Mode

	// Subcommand is the avar subcommand name when Mode is ModeSubcommand,
	// and SubcommandArgs is everything after it, unparsed: avar's own
	// subcommands parse their own flags.
	Subcommand     string
	SubcommandArgs []string

	// Guest is the guest command and its arguments when Mode is
	// ModeGuestCommand, verbatim and in order. avar never interprets these
	// tokens.
	Guest []string

	// Env holds the raw values of --env flags: either "NAME" (forward from host)
	// or "NAME=value" (set explicitly). Each flag adds one entry; the flag is
	// repeatable (REQ-12.1).
	Env []string

	// EnvFile is the value of --env-file: a path to a file whose KEY=value lines
	// are forwarded to the guest, one line per variable (REQ-12.2).
	EnvFile string

	// SSHAgent requests that the host SSH agent socket be forwarded into the guest
	// for this invocation only (REQ-12.3).
	SSHAgent bool

	// Help and Version record an avar-position --help/-h or --version/-v.
	// After the mode-deciding token those flags belong to the guest command
	// or to the subcommand, so they never set these fields.
	Help    bool
	Version bool
}

// subcommands is the complete set of names that win over a guest command of
// the same name (REQ-2.6). Names for unimplemented commands are present from
// the start deliberately: the split a user sees must not change as commands
// land, or a project script named `snapshot` would silently stop working.
var subcommands = []string{
	"code",
	"destroy",
	"help",
	"internal",
	"isolate",
	"reset",
	"restore",
	"snapshot",
	"status",
	"stop",
	"version",
}

// Subcommands returns avar's subcommand names in sorted order.
func Subcommands() []string { return slices.Clone(subcommands) }

// IsSubcommand reports whether name is an avar subcommand rather than a guest
// command.
func IsSubcommand(name string) bool { return slices.Contains(subcommands, name) }

// flagKind distinguishes flags that consume the following token from flags
// that do not.
type flagKind int

const (
	// flagBool takes no value: --isolate, or --isolate=true.
	flagBool flagKind = iota
	// flagValue takes a value: --arch amd64, or --arch=amd64.
	flagValue
)

// flagSpec describes one avar-position flag. The table below is the whole
// definition of what avar will parse for itself; adding a flag is adding a
// row, which is what keeps this grammar auditable against the spec.
type flagSpec struct {
	// names holds the flag's long form first, then any short aliases.
	names []string
	kind  flagKind
	// apply records the flag on inv. For flagBool the value is the parsed
	// boolean rendered back as a string; for flagValue it is the raw
	// user-supplied text.
	apply func(inv *Invocation, value string) error
}

// avarFlags are the flags avar consumes before the mode-deciding token.
var avarFlags = []flagSpec{
	{
		names: []string{"--arch"},
		kind:  flagValue,
		apply: func(inv *Invocation, value string) error {
			arch, err := types.ParseArch(value)
			if err != nil {
				return fmt.Errorf("invalid --arch value: %w", err)
			}
			inv.Selector.Arch = arch
			return nil
		},
	},
	{
		names: []string{"--distro"},
		kind:  flagValue,
		apply: func(inv *Invocation, value string) error {
			distro, version, err := parseDistro(value)
			if err != nil {
				return fmt.Errorf("invalid --distro value: %w", err)
			}
			inv.Selector.Distro = distro
			inv.Selector.DistroVersion = version
			return nil
		},
	},
	{
		names: []string{"--isolate"},
		kind:  flagBool,
		apply: func(inv *Invocation, value string) error {
			inv.Selector.Isolate = value == "true"
			return nil
		},
	},
	{
		names: []string{"--shared"},
		kind:  flagBool,
		apply: func(inv *Invocation, value string) error {
			inv.Selector.Shared = value == "true"
			return nil
		},
	},
	{
		names: []string{"--env"},
		kind:  flagValue,
		apply: func(inv *Invocation, value string) error {
			inv.Env = append(inv.Env, value)
			return nil
		},
	},
	{
		names: []string{"--env-file"},
		kind:  flagValue,
		apply: func(inv *Invocation, value string) error {
			inv.EnvFile = value
			return nil
		},
	},
	{
		names: []string{"--ssh-agent"},
		kind:  flagBool,
		apply: func(inv *Invocation, value string) error {
			inv.SSHAgent = value == "true"
			return nil
		},
	},
	{
		names: []string{"--help", "-h"},
		kind:  flagBool,
		apply: func(inv *Invocation, value string) error {
			inv.Help = value == "true"
			return nil
		},
	},
	{
		names: []string{"--version", "-v"},
		kind:  flagBool,
		apply: func(inv *Invocation, value string) error {
			inv.Version = value == "true"
			return nil
		},
	},
}

// lookupFlag finds the spec for a flag name, which is the token with any
// "=value" suffix already removed.
func lookupFlag(name string) (flagSpec, bool) {
	for _, spec := range avarFlags {
		if slices.Contains(spec.names, name) {
			return spec, true
		}
	}
	return flagSpec{}, false
}

// supportedDistros is the distribution *name* set. Which versions each name
// offers, and their pinned defaults, live in the resolver's matrix; the
// grammar only rejects names that can never resolve.
var supportedDistros = []types.Distro{
	types.DistroUbuntu,
	types.DistroDebian,
	types.DistroFedora,
}

// parseDistro splits and validates a --distro value of the form
// "name" or "name:version" (REQ-4.2).
func parseDistro(value string) (types.Distro, string, error) {
	name, version, hasVersion := strings.Cut(strings.TrimSpace(value), ":")

	distro := types.Distro(strings.ToLower(name))
	if !slices.Contains(supportedDistros, distro) {
		names := make([]string, 0, len(supportedDistros))
		for _, d := range supportedDistros {
			names = append(names, string(d))
		}
		return "", "", fmt.Errorf("unsupported distribution %q: supported values are %s", name, strings.Join(names, ", "))
	}

	version = strings.TrimSpace(version)
	if hasVersion && version == "" {
		return "", "", fmt.Errorf("%q names no version after the colon: drop the colon to use %s's pinned default version, or name a version after it", value, distro)
	}

	return distro, version, nil
}

// Parse splits argv — the arguments after the program name — into avar's
// selector flags, an avar subcommand, and a guest command.
//
// The rules, in the order they are applied:
//
//  1. Leading tokens matching the avar flag table are avar's own.
//  2. "--" ends avar's flags and forces everything after it to be a guest
//     command, even a token that names an avar subcommand (REQ-2.6).
//  3. Otherwise the first token that is not an avar flag decides the mode: an
//     avar subcommand if it names one, and the start of a guest command if it
//     does not (REQ-2.5).
//  4. Nothing at or after the mode-deciding token is interpreted by avar, so
//     the guest command keeps its own flags (REQ-4.5).
//
// Parse never mutates argv and never retains it.
func Parse(argv []string) (Invocation, error) {
	inv := Invocation{Mode: ModeShell}

	i := 0
	for ; i < len(argv); i++ {
		token := argv[i]

		if token == "--" {
			// Rule 2: force guest interpretation of the remainder.
			// `avr --` on its own still means the interactive shell.
			if rest := argv[i+1:]; len(rest) > 0 {
				inv.Mode = ModeGuestCommand
				inv.Guest = slices.Clone(rest)
			}
			return finish(inv)
		}

		name, value, hasValue := strings.Cut(token, "=")
		spec, isFlag := lookupFlag(name)
		if !isFlag {
			break
		}

		switch {
		case spec.kind == flagBool && !hasValue:
			value = "true"
		case spec.kind == flagBool:
			b, err := strconv.ParseBool(value)
			if err != nil {
				return Invocation{}, fmt.Errorf("%s takes no value: %q is not a boolean", name, value)
			}
			value = strconv.FormatBool(b)
		case spec.kind == flagValue && !hasValue:
			if i+1 >= len(argv) {
				return Invocation{}, fmt.Errorf("%s needs a value", name)
			}
			i++
			value = argv[i]
		}

		if err := spec.apply(&inv, value); err != nil {
			return Invocation{}, err
		}
	}

	if i == len(argv) {
		// Only avar flags: the interactive shell (REQ-1.1).
		return finish(inv)
	}

	// Rule 3: the mode-deciding token.
	if token := argv[i]; IsSubcommand(token) {
		inv.Mode = ModeSubcommand
		inv.Subcommand = token
		inv.SubcommandArgs = slices.Clone(argv[i+1:])
		return finish(inv)
	}

	// A leading dash here is a mistyped avar flag far more often than it is a
	// guest command, and guessing wrong would run something unintended in the
	// guest, so say so and name the escape hatch.
	if token := argv[i]; strings.HasPrefix(token, "-") && token != "-" {
		return Invocation{}, fmt.Errorf("unknown flag %q: run \"avr --help\" for avar's flags, or \"avr -- %s ...\" to pass it to a command in Linux", token, token)
	}

	inv.Mode = ModeGuestCommand
	inv.Guest = slices.Clone(argv[i:])
	return finish(inv)
}

// finish applies the checks that depend on the whole invocation rather than on
// a single token.
func finish(inv Invocation) (Invocation, error) {
	if inv.Selector.Isolate && inv.Selector.Shared {
		return Invocation{}, fmt.Errorf("--isolate and --shared are mutually exclusive: --isolate uses a machine dedicated to this project, --shared uses the machine shared by every project")
	}
	return inv, nil
}
