// Package cmd defines avar's command-line surface. It owns all user-facing
// output and delegates every decision to the packages under internal/.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

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

// NewRootCommand builds the root command tree.
//
// Interspersed flag parsing is disabled so that everything after the first
// non-flag token belongs to the guest command: `avr --arch amd64 npm test
// --watch` must pass `--watch` to npm, not to avr (REQ-2.5).
func NewRootCommand(version string) *cobra.Command {
	root := &cobra.Command{
		Use:   "avr [flags] [--] [command [args...]]",
		Short: "Run your current directory in Linux",
		Long: "avr opens the current directory in a complete Linux environment.\n\n" +
			"With no command it starts an interactive shell. With a command it runs\n" +
			"that command in Linux and exits with its status. The Linux environment\n" +
			"is created on first use; nothing needs configuring.",
		Example: "  avr                     Interactive Linux shell in the current directory\n" +
			"  avr npm test            Run one command in Linux\n" +
			"  avr --arch amd64 make   Run on x86_64 instead of the host architecture\n" +
			"  avr --distro fedora     Use Fedora instead of Ubuntu\n" +
			"  avr -- status           Run the guest's own `status`, not avr's",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("not implemented yet: the shell path lands with REQ-1.1")
		},
	}

	root.Flags().SetInterspersed(false)
	root.SetVersionTemplate("avr {{.Version}}\n")

	return root
}

// Execute runs avar and returns the process exit code. It never calls
// os.Exit so that tests can drive the whole command surface in-process.
func Execute(ctx context.Context, version string, args []string) int {
	root := NewRootCommand(version)
	root.SetArgs(args)

	err := root.ExecuteContext(ctx)
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
