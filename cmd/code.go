package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/editor"
	"github.com/olamide226/avar/internal/provider"
)

// The editor commands differ only in the editor they open, so they share one
// flow and each is a registration.
func init() {
	registerSubcommand("code", editorCommand(editor.VSCode))
	registerSubcommand("cursor", editorCommand(editor.Cursor))
	registerSubcommand("zed", editorCommand(editor.Zed))
}

// ensureSSHInclude makes sure the user's own SSH configuration pulls in
// avar's, which is what lets the editor resolve the host avar just wrote
// (REQ-13.1).
//
// avar writes one line into a file it does not own, so it asks first, once —
// after which HasInclude finds it and nothing is asked again. Declining is a
// real answer, not a failure: the editor still opens, and the user is told the
// exact line to add if they change their mind. A non-interactive run is
// treated as a decline, because consent cannot be assumed from silence.
func ensureSSHInclude(app *App, ed editor.Editor, avarConfigPath string) error {
	userConfig, err := editor.UserConfigPath()
	if err != nil {
		return err
	}

	present, err := editor.HasInclude(userConfig, avarConfigPath)
	if err != nil {
		return err
	}
	if present {
		return nil
	}

	line := editor.IncludeLine(avarConfigPath)
	if !stdinIsTerminal() {
		fmt.Fprintf(app.Err, "avr: %s cannot find your Linux environments until %s includes avar's SSH configuration.\n", ed.Name, userConfig)
		fmt.Fprintf(app.Err, "     Add this line to the top of that file:\n\n       %s\n\n", line)
		return nil
	}

	fmt.Fprintf(app.Err, "avr: %s finds hosts through %s, which does not yet include avar's.\n", ed.Name, userConfig)
	fmt.Fprintf(app.Err, "     avar would add one line to the top of it and change nothing else:\n\n       %s\n\n", line)
	if !app.confirmYesNo("      Add it? (y/N) ") {
		fmt.Fprintf(app.Err, "avr: left %s alone. Add the line above yourself when you want `avr %s` to connect.\n", userConfig, ed.Command)
		return nil
	}

	if err := editor.AddInclude(userConfig, avarConfigPath); err != nil {
		return fmt.Errorf("add avar's Include line to %s: %w", userConfig, err)
	}
	fmt.Fprintf(app.Err, "avr: added it. This is asked once.\n")
	return nil
}

// forgetSSHHost drops a destroyed machine's entry from avar's SSH
// configuration, so the file does not accumulate stanzas pointing at guests
// that no longer exist.
//
// Failure is deliberately silent: the machine is already gone, and a stale
// entry is harmless clutter that the next editor command overwrites. Reporting
// it would put an error the user cannot act on in front of a successful
// command.
func forgetSSHHost(app *App, machine string) {
	store, err := app.Store()
	if err != nil {
		return
	}
	_ = editor.RemoveHost(store.SSHDir(), machine)
}

// editorCommand builds the handler for one editor subcommand.
func editorCommand(ed editor.Editor) Handler {
	return func(ctx context.Context, app *App, inv cli.Invocation) error {
		return openInEditor(ctx, app, inv, ed)
	}
}

// openInEditor opens the current project in an editor attached to the target
// Linux environment. It ensures the machine is running, writes avar's SSH
// configuration for it when the backend reaches it over SSH, and hands the
// editor the target the backend describes.
//
// Nothing here knows which editor or which backend it is serving: the backend
// describes the target, the editor turns it into its launcher's arguments.
//
// Selector flags honour the same Environment_Selector as every other
// invocation, so `avr --isolate zed`, `avr --distro fedora cursor`, and
// `avr --arch amd64 code` all resolve to the right machine.
func openInEditor(ctx context.Context, app *App, inv cli.Invocation, ed editor.Editor) error {
	if len(inv.SubcommandArgs) > 0 {
		// The editor commands always open the project at the working
		// directory. Arguments like a file or line number could be useful one
		// day but the grammar for them needs a design, and the behaviour when
		// those arguments are not there needs thinking through.
		return Exit(exitUsage,
			fmt.Errorf("`avr %s` takes no arguments, but got %q; it opens the current project in %s attached to the target Linux environment",
				ed.Command, strings.Join(inv.SubcommandArgs, " "), ed.Name))
	}

	// Look for the launcher before anything slow. Otherwise a first `avr zed`
	// provisions an environment for minutes and only then reports that Zed's
	// command is not installed.
	launcher, err := ed.Locate()
	if err != nil {
		return err
	}

	target, err := app.Resolve(inv)
	if err != nil {
		return err
	}

	p, err := app.Provider(ctx)
	if err != nil {
		return err
	}

	// As on the shell path, --native-fs on a backend with no native workspace
	// is refused before bringing anything up (REQ-14.4).
	if inv.NativeFS {
		if _, err := nativeWorkspacer(p); err != nil {
			return err
		}
	}

	// The project's file is reviewed here as on the shell path, before any
	// machine work, so that the environment an editor opens has the packages
	// the user approved.
	target, err = reviewProjectGrants(app, target)
	if err != nil {
		return err
	}

	// Bring the machine up and make the project visible inside it, exactly
	// as the shell path does. The shared prepareEnvironment gives the editor
	// commands the same auto-provision behaviour as `avr`, so a first-time
	// user who types `avr code` before `avr` gets a working environment.
	_, guestCwd, err := prepareEnvironment(ctx, app, p, target, progressTo(app.Err))
	if err != nil {
		return err
	}
	if err := applyProjectConfig(ctx, app, p, target, guestCwd); err != nil {
		return err
	}

	// `avr --native-fs code` opens the copy on the Linux filesystem, because a
	// flag that is accepted and does nothing is worse than one that is refused
	// — the user believes they got what they asked for (docs/lessons.md,
	// "`--ssh-agent` was accepted, plumbed, and did nothing").
	if inv.NativeFS {
		guestCwd, err = enterNativeWorkspace(ctx, app, p, target, progressTo(app.Err))
		if err != nil {
			return err
		}
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

	// Ask the editor whether it can make this connection before writing
	// anything on its behalf: an editor that cannot reach the environment
	// leaves no SSH configuration and no consent prompt behind.
	args, err := ed.Args(et.Authority, et.GuestPath)
	if err != nil {
		return err
	}

	// Write an avar-owned SSH host entry so that the editor's SSH support can
	// resolve the host. The entry lives in a file that belongs to avar alone;
	// the user's own configuration gains at most one Include line, and only
	// with their consent (REQ-13.3).
	if et.SSHConfig != "" {
		store, err := app.Store()
		if err != nil {
			return err
		}
		sshDir := store.SSHDir()
		if err := editor.WriteHost(sshDir, target.MachineName, et.SSHConfig); err != nil {
			return fmt.Errorf("prepare the SSH configuration for %s: %w", target.Selector.Label(), err)
		}
		if err := ensureSSHInclude(app, ed, editor.ConfigPath(sshDir)); err != nil {
			return err
		}
	}

	if err := ed.Launch(ctx, launcher, args, app.Err); err != nil {
		return err
	}

	fmt.Fprintf(app.Out, "avr: opened %s in %s on %s\n", target.Project.Path, ed.Name, target.Selector.Label())
	return nil
}
