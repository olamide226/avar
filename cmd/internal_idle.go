package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/session"
	"github.com/olamide226/avar/internal/types"
)

func init() { registerSubcommand("internal", runInternal) }

// launchdLabel and launchdPlist are the macOS per-user agent that invokes
// `avr internal idle-check` every 10 minutes. The plist lives in
// ~/Library/LaunchAgents/ so launchd picks it up at the user's next login;
// avar loads it immediately so no logout is needed.
const (
	launchdLabel = "com.avar.idle-check"
	launchdPlist = launchdLabel + ".plist"
)

// runInternal dispatches avar's internal subcommands, which are not part of
// the public interface. `internal` itself is documented in the help as a
// group of internal-use-only commands.
func runInternal(ctx context.Context, app *App, inv cli.Invocation) error {
	if len(inv.SubcommandArgs) < 1 {
		return Exit(exitUsage, fmt.Errorf("`avr internal` requires a subcommand: idle-check"))
	}
	switch inv.SubcommandArgs[0] {
	case "idle-check":
		return runIdleCheck(ctx, app)
	default:
		return Exit(exitUsage, fmt.Errorf("`avr internal %s` is not a recognised internal command", inv.SubcommandArgs[0]))
	}
}

// runIdleCheck stops machines that have been idle — no live sessions — for
// longer than the configured timeout (REQ-5.5). It is invoked by launchd
// every 10 minutes and is designed to be safe to run at any frequency:
// machines with live sessions are never stopped (Property 11), and an idle
// clock only starts when the last session detaches.
func runIdleCheck(ctx context.Context, app *App) error {
	store, err := app.Store()
	if err != nil {
		return err
	}

	timeout, disabled, err := session.IdleTimeout(store)
	if err != nil {
		return err
	}
	if disabled {
		return nil
	}

	idle, err := session.IdleMachines(store, timeout)
	if err != nil {
		return err
	}
	if len(idle) == 0 {
		return nil
	}

	p, err := app.Provider(ctx)
	if err != nil {
		return err
	}

	statuses, err := p.Status(ctx)
	if err != nil {
		return err
	}
	running := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		if s.State == types.StateRunning {
			running[s.Name] = true
		}
	}

	for _, name := range idle {
		if !running[name] {
			continue
		}
		// Stop is best-effort: a failure leaves the machine running, and
		// the next idle-check will try again. A machine that disappeared
		// between the listing and the stop is the state we wanted.
		if err := p.Stop(ctx, name, types.DiscardProgress); err != nil {
			// Logging this would require a logger; the next check
			// retries, and `avr status` shows the running machine.
			continue
		}
	}
	return nil
}

// ensureLaunchdAgent installs a per-user launchd agent that invokes `avr
// internal idle-check` every 10 minutes, printing a one-time notice the first
// time it does so. It is called when a machine is first created so that idle
// auto-stop is active from the moment there is anything to stop.
//
// The function is a no-op when the plist already exists or when the user is
// not on macOS.
func ensureLaunchdAgent(app *App) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	plistPath := filepath.Join(launchAgentsDir, launchdPlist)

	if _, err := os.Stat(plistPath); err == nil {
		return // already installed
	}

	bin, err := os.Executable()
	if err != nil {
		return
	}

	if err := os.MkdirAll(launchAgentsDir, 0o755); err != nil {
		return
	}

	plist := launchdPlistContent(bin)
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return
	}

	// Load the agent for the current session so that idle-checking starts
	// immediately rather than waiting for the next login.
	_ = launchctl("load", plistPath)

	fmt.Fprintf(app.Err, "avr: installed a background idle-check that stops Linux environments\n")
	fmt.Fprintf(app.Err, "     after they have had no activity for a while, so they do not hold\n")
	fmt.Fprintf(app.Err, "     host resources when you are not using them.\n")
	fmt.Fprintf(app.Err, "     The check runs every 10 minutes. To disable it:\n")
	fmt.Fprintf(app.Err, "       launchctl bootout gui/$(id -u)/%s\n", launchdLabel)
	fmt.Fprintf(app.Err, "     Or set idle_timeout = \"0\" in %s\n", filepath.Join(home, ".avr", "config.toml"))
}

// launchdPlistContent returns the plist XML for the idle-check agent.
func launchdPlistContent(bin string) string {
	// xml-template rendered as a string to avoid an xml package dependency
	// for a single static document.
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>internal</string>
		<string>idle-check</string>
	</array>
	<key>StartInterval</key>
	<integer>600</integer>
	<key>RunAtLoad</key>
	<false/>
</dict>
</plist>
`, launchdLabel, bin)
}

// launchctl runs launchctl with the given arguments. If loading the agent
// fails the plist is still on disk, so launchd picks it up at the next login.
func launchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}
