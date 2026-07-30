// Package cmd defines avar's command-line surface. It owns all user-facing
// output and delegates every decision to the packages under internal/.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/olamide226/avar/internal/cli"
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

// subcommandOwner names the task that makes each subcommand real, so that an
// early adopter of a Phase 1 build gets a specific answer instead of an
// unknown-command error. Entries disappear as the commands land.
var subcommandOwner = map[string]string{
	"status":   "task 10 (REQ-5.1)",
	"stop":     "task 10 (REQ-5.2)",
	"snapshot": "task 15 (REQ-10.1)",
	"restore":  "task 15 (REQ-10.2)",
	"reset":    "task 16 (REQ-10.3)",
	"isolate":  "task 17 (REQ-11.3)",
	"code":     "task 19 (REQ-13.1)",
	"internal": "task 20 (REQ-5.5)",
}

// NewRootCommand builds the root command tree for an already-parsed
// invocation.
//
// The argv grammar is not cobra's job: internal/cli.Parse has already decided
// whether this invocation is a shell, a guest command, or an avar subcommand,
// because that split must be deterministic and fuzzable (PROP-9) rather than a
// consequence of pflag's behaviour around "--". Cobra renders help and routes
// subcommands; the selector flags below are declared purely so that
// `avr --help` documents them.
func NewRootCommand(version string, inv cli.Invocation) *cobra.Command {
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
			"own `status` rather than avr's.",
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
			return run(cmd.Context(), inv)
		},
	}

	// Declared for `avr --help` only: internal/cli owns parsing these
	// (REQ-2.5, REQ-4.5), and Execute never hands raw flags to cobra.
	flags := root.Flags()
	flags.String("arch", "", "guest CPU architecture: arm64 or amd64")
	flags.String("distro", "", "guest distribution: ubuntu, debian or fedora, optionally :version")
	flags.Bool("isolate", false, "use a machine dedicated to this project")
	flags.Bool("shared", false, "use the machine shared by every project, just this once")
	flags.BoolP("help", "h", false, "show how to use avr")
	flags.BoolP("version", "v", false, "show the avr version")

	return root
}

// run dispatches a parsed invocation. The shell and one-shot paths are task 8;
// each avar subcommand has its own later task.
func run(_ context.Context, inv cli.Invocation) error {
	switch inv.Mode {
	case cli.ModeShell:
		return errors.New("not implemented yet: the interactive shell lands with task 8 (REQ-1.1)")

	case cli.ModeGuestCommand:
		return fmt.Errorf("not implemented yet: running %q in Linux lands with task 8 (REQ-2.1)", inv.Guest[0])

	case cli.ModeSubcommand:
		owner, ok := subcommandOwner[inv.Subcommand]
		if !ok {
			// help and version are handled before dispatch; anything else
			// reaching here means the grammar and this switch disagree.
			return fmt.Errorf("internal error: no handler for subcommand %q", inv.Subcommand)
		}
		return fmt.Errorf("not implemented yet: `avr %s` lands with %s", inv.Subcommand, owner)

	default:
		return fmt.Errorf("internal error: unknown invocation mode %s", inv.Mode)
	}
}

// Execute runs avar and returns the process exit code. It never calls
// os.Exit so that tests can drive the whole command surface in-process.
func Execute(ctx context.Context, version string, args []string) int {
	inv, err := cli.Parse(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "avr: %v\n", err)
		return exitUsage
	}

	root := NewRootCommand(version, inv)

	// `avr help` and `avr version` are the subcommand spellings of the flags
	// (REQ-2.5 keeps them in the subcommand set so that a guest command of
	// either name still needs `avr -- help`).
	switch {
	case inv.Version || inv.Subcommand == "version":
		fmt.Fprintf(root.OutOrStdout(), "avr %s\n", version)
		return 0
	case inv.Help || inv.Subcommand == "help":
		if err := root.Help(); err != nil {
			fmt.Fprintf(os.Stderr, "avr: %v\n", err)
			return 1
		}
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

	fmt.Fprintf(os.Stderr, "avr: %v\n", err)
	return 1
}
