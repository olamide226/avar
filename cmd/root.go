// Package cmd defines avar's command-line surface. It owns all user-facing
// output and delegates every decision to the packages under internal/.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/resolve"
	"github.com/spf13/cobra"
)

// exitUsage is the exit code for a command line avar cannot read: an unknown
// flag, a missing flag value, or an unsupported --arch/--distro value
// (REQ-4.4). It is distinct from 1 so that scripts can tell "avar could not
// understand you" from "the operation failed".
const exitUsage = 2

// ExitCodeError lets any layer choose avar's exit code, which matters because
// avr must exit with the guest's status (REQ-1.7, REQ-2.2).
type ExitCodeError struct {
	Code int
	Err  error
}

func (e *ExitCodeError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitCodeError) Unwrap() error { return e.Err }

// Exit builds an error that terminates avar with the given code, printing
// nothing when err is nil.
func Exit(code int, err error) error { return &ExitCodeError{Code: code, Err: err} }

// NewRootCommand builds the root command tree for an already-parsed
// invocation.
//
// The argv grammar is not cobra's job: internal/cli.Parse has already decided
// whether this invocation is a shell, a guest command, or an avar subcommand,
// because that split must be deterministic and fuzzable (PROP-9) rather than a
// consequence of pflag's behaviour around "--". Cobra renders help and routes
// subcommands; the selector flags below are declared purely so that
// `avr --help` documents them.
func NewRootCommand(version string, inv cli.Invocation, app *App) *cobra.Command {
	root := &cobra.Command{
		Use:   "avr [flags] [--] [command [args...]]",
		Short: "Run your current directory in Linux",
		Long: "avr opens the current directory in a complete Linux environment.\n\n" +
			"With no command it starts an interactive shell. With a command it runs\n" +
			"that command in Linux and exits with its status. The Linux environment\n" +
			"is created on first use; nothing needs configuring.\n\n" +
			"How avr reads a command line:\n" +
			"  avr [flags] [--] [COMMAND [ARGS...]]   no COMMAND opens a shell\n" +
			"  avr [flags] SUBCOMMAND [ARGS...]\n\n" +
			"Flags come first. The first token that is not one of avr's own flags\n" +
			"decides the rest: an avr subcommand if it names one, otherwise the\n" +
			"start of a command to run in Linux, whose own flags avr never reads.\n" +
			"`--` forces the command reading, so `avr -- status` runs the guest's\n" +
			"own `status` rather than avr's.\n\n" +
			commandIndex,
		Example: "  avr                     Interactive Linux shell in the current directory\n" +
			"  avr npm test            Run one command in Linux\n" +
			"  avr npm test --watch    --watch goes to npm, not to avr\n" +
			"  avr --arch amd64 make   Run on x86_64 instead of the host architecture\n" +
			"  avr --distro fedora     Use Fedora instead of Ubuntu\n" +
			"  avr -- status           Run the guest's own `status`, not avr's",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,

		// internal/cli.Parse has already read every flag on the command
		// line, so cobra must not read them again: with parsing left on, a
		// subcommand's flag would be rejected by the root command until the
		// subcommand itself exists.
		DisableFlagParsing: true,

		// cli.Parse only ever reports a name from its own subcommand set,
		// so cobra's unknown-command path is unreachable and its argument
		// check would only get in the way of routing.
		Args: cobra.ArbitraryArgs,

		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatch(cmd.Context(), app, inv)
		},
	}

	// Declared for `avr --help` only: internal/cli owns parsing these
	// (REQ-2.5, REQ-4.5), and Execute never hands raw flags to cobra.
	flags := root.Flags()
	flags.String("arch", "", "guest CPU architecture: arm64 or amd64")
	flags.String("distro", "", "guest distribution: ubuntu, debian or fedora, optionally :version")
	flags.Bool("isolate", false, "use a machine dedicated to this project")
	flags.Bool("shared", false, "use the machine shared by every project, just this once")
	flags.StringSlice("env", nil, "forward host env var to a guest session: NAME or NAME=value (repeatable)")
	flags.String("env-file", "", "file of KEY=value lines to forward to a guest session (.env format)")
	flags.Bool("ssh-agent", false, "forward the host SSH agent socket for a guest session")
	flags.Bool("native-fs", false, "run in a copy of the project on the Linux filesystem, for speed")
	flags.BoolP("help", "h", false, "show how to use avr")
	flags.BoolP("version", "v", false, "show the avr version")

	return root
}

// commandIndex is part of the root help rather than a Cobra command tree:
// internal/cli owns argv parsing so that a guest command can never be mistaken
// for an avar command. Keep this list limited to the public interface; internal
// scheduler commands remain deliberately undocumented.
const commandIndex = `Management commands:
  status                         Show every Linux environment avar manages
  stop [--all]                   Stop this environment, or every avar environment
  snapshot [name]                List snapshots, or capture one named snapshot
  restore <name>                 Restore this environment from a snapshot
  reset [--yes]                  Recreate this environment from a clean OS
  isolate [on|off [--yes]]       Show or change this project's isolation default
  sync [--to-host|--to-guest]    Review or apply changes between a project's
                                 host copy and its Linux-native one
  destroy [--all|--orphaned] [--yes]
                                 Remove environments after confirmation
  code                           Open this project in VS Code over Remote-SSH
  help [command]                 Show general or command-specific help
  version                        Print the avr version

Run "avr help <command>" or "avr <command> --help" for command-specific help.`

type commandHelp struct {
	usage       string
	description string
	flags       string
}

// publicCommandHelp describes the subcommands that have handlers today. It is
// intentionally alongside the root help: a public command must have both a
// registered handler and user-facing usage before it is added here.
var publicCommandHelp = map[string]commandHelp{
	"code": {
		usage:       "avr [selector flags] code",
		description: "Open the current project in VS Code attached to its Linux environment.",
	},
	"destroy": {
		usage:       "avr [selector flags] destroy [--all | --orphaned] [--yes]",
		description: "Remove the selected environment, all avar environments, or isolated environments whose projects no longer exist. Host project files are never removed; confirmation is required unless --yes is supplied.",
		flags:       "  --all        remove every Linux environment avar manages\n  --orphaned   remove isolated environments whose project directory is gone\n  --yes        skip the confirmation prompt",
	},
	"isolate": {
		usage:       "avr isolate [on | off [--yes]]",
		description: "Show whether this project defaults to its own environment, or change that default. Turning isolation off offers to delete the isolated environment.",
		flags:       "  --yes   with `avr isolate off`, delete the isolated environment without asking",
	},
	"reset": {
		usage:       "avr [selector flags] reset [--yes]",
		description: "Recreate the selected environment from a clean OS. Host project files are never changed; confirmation is required unless --yes is supplied.",
		flags:       "  --yes   skip the confirmation prompt",
	},
	"restore": {
		usage:       "avr [selector flags] restore <name>",
		description: "Restore the selected environment from a named snapshot. Snapshots are available only in environments whose backend supports them.",
	},
	"snapshot": {
		usage:       "avr [selector flags] snapshot [name]",
		description: "List snapshots for the selected environment, or capture one with a name. Snapshots are available only in environments whose backend supports them.",
	},
	"status": {
		usage:       "avr status",
		description: "Show every Linux environment avar manages, including state, resources, sessions, and forwarded-port diagnostics.",
	},
	"stop": {
		usage:       "avr [selector flags] stop [--all]",
		description: "Stop the selected environment, or every Linux environment avar manages.",
		flags:       "  --all   stop every Linux environment avar manages",
	},
	"sync": {
		usage:       "avr [selector flags] sync [--to-host | --to-guest] [--yes]",
		description: "Show what differs between this project's host copy and the Linux-native copy `avr --native-fs` keeps, and apply one side's changes to the other. With no direction it changes nothing. Files both copies changed are reported and never overwritten.\n\nNote that `sync` is an avar command, so it does not reach the guest: if your project has a script called `sync`, or you want the guest's own sync(1), run `avr -- sync`.",
		flags:       "  --to-host    apply the Linux copy's changes to the host copy\n  --to-guest   apply the host copy's changes to the Linux copy\n  --yes        skip the confirmation prompt",
	},
	"version": {
		usage:       "avr version",
		description: "Print the avr version. Also available as `avr --version`.",
	},
}

func writeCommandHelp(w io.Writer, name string) error {
	if name == "help" {
		fmt.Fprintln(w, "Usage:")
		fmt.Fprintln(w, "  avr help [command]")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Show avr's help, or help for one public management command.")
		return nil
	}

	help, ok := publicCommandHelp[name]
	if !ok {
		return fmt.Errorf("unknown command %q; run `avr --help` to see avar's commands", name)
	}

	fmt.Fprintln(w, "Usage:")
	fmt.Fprintf(w, "  %s\n", help.usage)
	fmt.Fprintln(w)
	fmt.Fprintln(w, help.description)
	if help.flags != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Flags:")
		fmt.Fprintln(w, help.flags)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Selector flags such as --arch and --distro must come before the command; run `avr --help` to see them.")
	return nil
}

// helpForInvocation identifies help requests before dispatch. This matters for
// destructive and provisioning commands: `avr reset --help` must describe the
// command, not reset an environment.
func helpForInvocation(inv cli.Invocation) (string, bool, error) {
	if inv.Help {
		return "", true, nil
	}
	if inv.Subcommand == "help" {
		switch len(inv.SubcommandArgs) {
		case 0:
			return "", true, nil
		case 1:
			if inv.SubcommandArgs[0] == "--help" || inv.SubcommandArgs[0] == "-h" {
				return "", true, nil
			}
			return inv.SubcommandArgs[0], true, nil
		default:
			return "", false, fmt.Errorf("`avr help` takes at most one command name, got %q", inv.SubcommandArgs)
		}
	}
	if inv.Mode == cli.ModeSubcommand {
		for _, arg := range inv.SubcommandArgs {
			if arg == "--help" || arg == "-h" {
				return inv.Subcommand, true, nil
			}
		}
	}
	return "", false, nil
}

// Execute runs avar and returns the process exit code. It never calls
// os.Exit so that tests can drive the whole command surface in-process.
func Execute(ctx context.Context, version string, args []string) int {
	inv, err := cli.Parse(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "avr: %v\n", err)
		return exitUsage
	}

	app := newApp(version)
	root := NewRootCommand(version, inv, app)

	// `avr help` and `avr version` are the subcommand spellings of the flags
	// (REQ-2.5 keeps them in the subcommand set so that a guest command of
	// either name still needs `avr -- help`). Help is handled before dispatch so
	// it remains side-effect free for every management command.
	helpName, wantsHelp, helpErr := helpForInvocation(inv)
	if helpErr != nil {
		fmt.Fprintf(os.Stderr, "avr: %v\n", helpErr)
		return exitUsage
	}
	switch {
	case wantsHelp && helpName == "":
		if err := root.Help(); err != nil {
			fmt.Fprintf(os.Stderr, "avr: %v\n", err)
			return 1
		}
		return 0
	case wantsHelp:
		if err := writeCommandHelp(root.OutOrStdout(), helpName); err != nil {
			fmt.Fprintf(os.Stderr, "avr: %v\n", err)
			return exitUsage
		}
		return 0
	case inv.Version || inv.Subcommand == "version":
		fmt.Fprintf(root.OutOrStdout(), "avr %s\n", version)
		return 0
	}

	// Only avar's own subcommands are given to cobra to route; a guest
	// command must never be matched against the command tree.
	if inv.Mode == cli.ModeSubcommand {
		root.SetArgs(append([]string{inv.Subcommand}, inv.SubcommandArgs...))
	} else {
		root.SetArgs(nil)
	}

	err = root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}

	var exit *ExitCodeError
	if errors.As(err, &exit) {
		if exit.Err != nil {
			fmt.Fprintf(os.Stderr, "avr: %v\n", exit.Err)
		}
		return exit.Code
	}

	if errors.Is(err, resolve.ErrUnsupportedEnvironment) {
		fmt.Fprintf(os.Stderr, "avr: %v\n", err)
		return exitUsage
	}

	fmt.Fprintf(os.Stderr, "avr: %v\n", err)
	return 1
}
