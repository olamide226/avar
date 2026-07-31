package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/editor"
	"github.com/olamide226/avar/internal/provider"
)

func init() { registerSubcommand("code", runCode) }

// forgetSSHHost drops a destroyed machine's entry from avar's SSH
// configuration, so the file does not accumulate stanzas pointing at guests
// that no longer exist.
//
// Failure is deliberately silent: the machine is already gone, and a stale
// entry is harmless clutter that the next `avr code` overwrites. Reporting it
// would put an error the user cannot act on in front of a successful command.
func forgetSSHHost(app *App, machine string) {
	store, err := app.Store()
	if err != nil {
		return
	}
	_ = editor.RemoveHost(store.SSHDir(), machine)
}

// runCode opens the current project in VS Code attached to the target Linux
// environment (REQ-13.1). It ensures the machine is running, writes avar's
// SSH configuration for it, and launches `code --remote <authority> <path>`.
//
// Selector flags honour the same Environment_Selector as every other
// invocation, so `avr --isolate code`, `avr --distro fedora code`, and
// `avr --arch amd64 code` all resolve to the right machine (REQ-13.4).
func runCode(ctx context.Context, app *App, inv cli.Invocation) error {
	if len(inv.SubcommandArgs) > 0 {
		// `avr code` always opens the project at the working directory.
		// Arguments like a file or line number could be useful one day
		// but the grammar for them needs a design, and the behaviour
		// when those arguments are not there needs thinking through.
		return Exit(exitUsage,
			fmt.Errorf("`avr code` takes no arguments, but got %q; it opens the current project in VS Code attached to the target Linux environment",
				strings.Join(inv.SubcommandArgs, " ")))
	}

	target, err := app.Resolve(inv)
	if err != nil {
		return err
	}

	p, err := app.Provider(ctx)
	if err != nil {
		return err
	}

	// Bring the machine up and make the project visible inside it, exactly
	// as the shell path does. The shared prepareEnvironment gives `avr code`
	// the same auto-provision behaviour as `avr`, so a first-time user who
	// types `avr code` before `avr` gets a working environment.
	guestCwd, err := prepareEnvironment(ctx, app, p, target, progressTo(app.Err))
	if err != nil {
		return err
	}

	// The backend describes how an editor reaches the guest.
	etp, ok := p.(provider.EditorTargetProvider)
	if !ok {
		return fmt.Errorf("the %s backend cannot open an editor on a Linux environment", p.ID())
	}

	et, err := etp.EditorTarget(ctx, target.MachineName, guestCwd)
	if err != nil {
		return fmt.Errorf("prepare the editor connection for %s: %w", target.Selector.Label(), err)
	}

	// Write an avar-owned SSH host entry so that VS Code's Remote-SSH
	// can resolve the authority. Nothing touches the user's ~/.ssh/config
	// — this file belongs to avar alone (REQ-13.3).
	if et.SSHConfig != "" {
		store, err := app.Store()
		if err != nil {
			return err
		}
		if err := editor.WriteHost(store.SSHDir(), target.MachineName, et.SSHConfig); err != nil {
			return fmt.Errorf("prepare the SSH configuration for %s: %w", target.Selector.Label(), err)
		}
		fmt.Fprintf(app.Out, "avr: wrote SSH configuration for %s\n", target.Selector.Label())
	}

	if err := editor.Launch(ctx, et.Authority, et.GuestPath); err != nil {
		return err
	}

	fmt.Fprintf(app.Out, "avr: opened %s in VS Code on %s\n", target.Project.Path, target.Selector.Label())
	return nil
}
