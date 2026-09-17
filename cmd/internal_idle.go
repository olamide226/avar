package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

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
//
// A config.toml it cannot read stops nothing. Nobody is watching a scheduled
// check, so it cannot ask, and there is no safe guess: the default timeout
// would stop the environments of a user whose broken line was idle_timeout =
// "0". It exits non-zero, which the host scheduler records as the last result,
// and the next interactive `avr` names the line (REQ-17.7). Auto-stop resumes
// on the first check after the file is fixed.
func runIdleCheck(ctx context.Context, app *App) error {
	store, err := app.Store()
	if err != nil {
		return err
	}
	cfg, err := app.Config()
	if err != nil {
		return fmt.Errorf("idle check stopped nothing, because avar's configuration cannot be read: %w", err)
	}

	idle, err := session.IdleMachines(store, session.IdleTimeout(cfg))
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
	// A stopped environment is asked to stop as well. Stop converges on
	// stopped rather than performing a shutdown, and a backend can leave
	// processes running after the machine itself has stopped: converging is
	// what releases them, and without it only an explicit `avr stop` ever
	// would. A machine coming up, broken, or in a state avar has no word for is
	// left alone, as `avr stop --all` leaves it.
	stoppable := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		if s.State == types.StateRunning || s.State == types.StateStopped {
			stoppable[s.Name] = true
		}
	}

	for _, name := range idle {
		if !stoppable[name] {
			continue
		}
		// Stop is best-effort: a failure leaves the machine running, and
		// the next idle-check will try again. A machine that disappeared
		// between the listing and the stop is the state we wanted.
		if err := p.Stop(ctx, name, stopProgress(app.Err)); err != nil {
			// Logging this would require a logger; the next check
			// retries, and `avr status` shows the running machine.
			continue
		}
	}
	return nil
}

// ensureIdleScheduler asks the host's own scheduler to invoke `avr internal
// idle-check` every ten minutes, printing a one-time notice the first time it
// does so.
//
// It is called when an environment is first created, so idle auto-stop is active
// from the moment there is anything to stop, and it is the whole of avar's
// answer to running work in the background: there is no avar daemon on either
// host, only a scheduled invocation of avar itself (REQ-5.5, design §1).
//
// The scheduler is the only part that differs. Everything it schedules — which
// environments are idle, what the timeout is, whether a session is attached — is
// the same code on both, which is why the branch is here and not inside the
// feature.
func ensureIdleScheduler(app *App) {
	switch runtime.GOOS {
	case "darwin":
		ensureLaunchdAgent(app)
	case "windows":
		ensureScheduledTask(app)
	}
}

// ensureLaunchdAgent installs a per-user launchd agent. The plist lives in
// ~/Library/LaunchAgents/ so launchd picks it up at the next login; avar loads
// it immediately so no logout is needed.
//
// It costs one read when the agent is already current, and repairs one that
// runs a different binary (see installLaunchdAgent).
func ensureLaunchdAgent(app *App) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	bin, err := os.Executable()
	if err != nil {
		return
	}
	installLaunchdAgent(app, filepath.Join(home, "Library", "LaunchAgents"), bin, launchctl)
}

// installLaunchdAgent is ensureLaunchdAgent with its host dependencies passed
// in: the LaunchAgents directory, the binary the agent should run, and how to
// run launchctl.
//
// The plist is compared with what this binary would write, not merely looked
// for. An agent that runs some other binary (an upgrade that moves avr, or the
// one a test run once installed pointing at a deleted test binary) fails every ten minutes indefinitely, and trusting its
// mere existence meant idle auto-stop silently never ran again. The warm path
// is still one read: this runs on every `avr` (REQ-17.1).
func installLaunchdAgent(app *App, launchAgentsDir, bin string, launchctl func(args ...string) error) {
	plistPath := filepath.Join(launchAgentsDir, launchdPlist)
	plist := launchdPlistContent(bin)

	current, err := os.ReadFile(plistPath)
	if err == nil {
		if string(current) == plist {
			return
		}
		repairLaunchdAgent(plistPath, plist, launchctl)
		return
	}

	if err := os.MkdirAll(launchAgentsDir, 0o755); err != nil {
		return
	}
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return
	}

	// Load the agent for the current session so that idle-checking starts
	// immediately rather than waiting for the next login.
	_ = launchctl("load", plistPath)

	printIdleNotice(app, "launchctl bootout gui/$(id -u)/"+launchdLabel)
}

// repairLaunchdAgent points an existing agent at this binary.
//
// It is reloaded only if it is loaded now. An agent that is not loaded was
// unloaded by the user, and correcting the file must not turn back on
// something they turned off. The notice is not repeated either: whoever has
// this plist has already been told once.
func repairLaunchdAgent(plistPath, plist string, launchctl func(args ...string) error) {
	loaded := launchctl("list", launchdLabel) == nil
	if loaded {
		_ = launchctl("unload", plistPath)
	}
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return
	}
	if loaded {
		_ = launchctl("load", plistPath)
	}
}

// printIdleNotice is the one-time explanation, shared by both schedulers.
//
// Only the command that turns it off differs between them, so only that is a
// parameter. Written twice, the two copies drift: this is user-facing copy
// explaining a background process somebody did not ask for, and it has to say
// the same thing on both hosts.
func printIdleNotice(app *App, disableCommand string) {
	fmt.Fprintf(app.Err, "avr: installed a background idle-check that stops Linux environments\n")
	fmt.Fprintf(app.Err, "     after they have had no activity for a while, so they do not hold\n")
	fmt.Fprintf(app.Err, "     host resources when you are not using them.\n")
	fmt.Fprintf(app.Err, "     The check runs every %d minutes. To disable it:\n", idleCheckMinutes)
	fmt.Fprintf(app.Err, "       %s\n", disableCommand)
	if store, err := app.Store(); err == nil {
		fmt.Fprintf(app.Err, "     Or set idle_timeout = \"0\" in %s\n", store.ConfigPath())
	}
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
	<integer>%d</integer>
	<key>RunAtLoad</key>
	<false/>
</dict>
</plist>
`, launchdLabel, bin, idleCheckMinutes*60)
}

// launchctl runs launchctl with the given arguments. If loading the agent
// fails the plist is still on disk, so launchd picks it up at the next login.
func launchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// scheduledTaskName is the Windows Task Scheduler entry that invokes `avr
// internal idle-check`. It is a single name at the root rather than a task in a
// folder, so that removing avar removes exactly one thing and leaves no empty
// folder behind (design §3.8).
const scheduledTaskName = "avar-idle-check"

// scheduledTaskStamp records, inside avar's state directory, which binary the
// Task Scheduler entry currently points at. Its presence is the cheap answer to
// "is this already registered", and its contents are the cheap answer to "does
// it still point at me".
const scheduledTaskStamp = "idle-task"

// idleCheckMinutes is how often the check runs, on both hosts. Ten minutes is
// short enough that a forgotten environment is stopped within a coffee break and
// long enough that the check itself is not a background cost.
const idleCheckMinutes = 10

// ensureScheduledTask registers the Windows Task Scheduler entry that invokes
// `avr internal idle-check`.
//
// The task runs as the signed-in user with their own token, which is what makes
// it work without elevation: a per-user task needs no administrator, and asking
// for one in order to stop a Linux environment nobody is using would be a poor
// trade. It also means the task can only see the environments that user owns,
// which is the same boundary everything else in avar respects.
//
// What it does *not* do is run schtasks on every invocation. This is reached
// from recordMachine, which runs on every `avr`, warm or cold — the macOS branch
// costs one stat there and returns. Spawning two subprocesses instead (measured
// at 50–150 ms on this host) would spend a fifth of REQ-17.1's whole latency
// budget re-registering a task that has not changed, and rewrite the task store
// on disk each time.
//
// So the stat is kept, and the file records the binary the task points at. That
// is what lets registration still be corrected after an upgrade moves avr.exe —
// the case the previous unconditional `/Create /F` existed for — without paying
// for it when nothing has moved.
//
// Every failure is silent, exactly as the launchd path is. Idle auto-stop is a
// convenience; a user who has just asked for a Linux shell should not be given
// an error about a background timer instead.
func ensureScheduledTask(app *App) {
	bin, err := os.Executable()
	if err != nil {
		return
	}
	store, err := app.Store()
	if err != nil {
		return
	}
	stamp := filepath.Join(store.Root(), scheduledTaskStamp)

	// The warm path: the task is registered and points at this binary.
	if recorded, err := os.ReadFile(stamp); err == nil && string(recorded) == bin {
		return
	}

	schtasks, err := exec.LookPath("schtasks")
	if err != nil {
		return
	}

	// The command is one string because Task Scheduler stores it as one; the
	// binary path is quoted inside it so a path containing a space stays one
	// argument when the scheduler runs it. /F overwrites, which is what makes
	// this correct a task left by a previous install rather than fail on it.
	action := fmt.Sprintf(`"%s" internal idle-check`, bin)
	create := exec.Command(schtasks,
		"/Create",
		"/TN", scheduledTaskName,
		"/TR", action,
		"/SC", "MINUTE",
		"/MO", strconv.Itoa(idleCheckMinutes),
		"/F",
	)
	create.Stdout, create.Stderr = nil, nil
	if err := create.Run(); err != nil {
		return
	}

	// The stamp is written only after the task exists, so a failed
	// registration is retried next time rather than remembered as done.
	firstTime := !fileExists(stamp)
	if err := os.WriteFile(stamp, []byte(bin), 0o600); err != nil {
		return
	}
	if !firstTime {
		// An upgrade corrects the task silently rather than announcing itself
		// again to somebody who has already read this once.
		return
	}

	printIdleNotice(app, fmt.Sprintf("schtasks /Delete /TN %s /F", scheduledTaskName))
}

// fileExists reports whether a path is there, treating any error as absent —
// which is the right answer for a marker file whose only job is to say "this has
// happened before".
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
