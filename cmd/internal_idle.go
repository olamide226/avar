package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/session"
	"github.com/olamide226/avar/internal/state"
	"github.com/olamide226/avar/internal/types"
)

func init() { registerSubcommand("internal", runInternal) }

// launchdLabel and launchdPlist are the macOS per-user agent that invokes
// `avr internal idle-check` every 30 minutes. The plist lives in
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
// every 30 minutes and is designed to be safe to run at any frequency:
// machines with live sessions are never stopped (Property 11), and an idle
// clock only starts when the last session detaches.
//
// An editor window connected to a machine is a session too, though avar holds
// no record of it (REQ-5.10). Only a running machine the check is about to stop
// is asked, which keeps the round trip into a guest off every other machine,
// and a machine the backend cannot answer for is left running.
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
	stoppable := make(map[string]types.MachineStatus, len(statuses))
	for _, s := range statuses {
		if s.State == types.StateRunning || s.State == types.StateStopped {
			stoppable[s.Name] = s
		}
	}

	// A backend that cannot look inside its machines has no editor to report,
	// and the check decides on avar's session records alone, as it always has.
	prober, _ := p.(provider.EditorProber)
	var unanswered []error

	for _, name := range idle {
		m, ok := stoppable[name]
		if !ok {
			continue
		}
		if m.State == types.StateRunning && prober != nil {
			kept, err := keptForEditor(ctx, app, prober, store, m)
			if err != nil {
				unanswered = append(unanswered, err)
			}
			if kept {
				continue
			}
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
	// Nobody is watching a scheduled check. Exiting non-zero is what makes
	// the host scheduler record that a machine was kept for want of an answer.
	return errors.Join(unanswered...)
}

// ensureIdleScheduler asks the host's own scheduler to invoke `avr internal
// idle-check` every thirty minutes, printing a one-time notice the first time it
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
//
// A binary in the temporary directory is never registered. The registration
// names the file and outlives it: a `go test` binary, a helper a test built,
// `go run`, and an avr.exe opened straight out of a zip archive all run from
// there, and each is deleted while the scheduler keeps running it and failing.
// A test binary that did exactly that is why this guard sits here, in front of
// both hosts, and not only behind the App.scheduleIdleCheck seam tests use:
// the seam protects the tests that remember it, and this protects the host
// from the ones that do not.
func ensureIdleScheduler(app *App) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		return
	}
	bin, err := os.Executable()
	if err != nil {
		return
	}
	if inTemporaryDir(bin) {
		fmt.Fprintf(app.Err, "avr: idle auto-stop is not set up, because avr is running from a temporary folder (%s).\n", filepath.Dir(bin))
		fmt.Fprintf(app.Err, "     Install it somewhere permanent and it is set up on the next `avr`.\n")
		return
	}
	wanted, ok := idleCheckWanted(app)
	if !ok {
		return
	}
	switch runtime.GOOS {
	case "darwin":
		ensureLaunchdAgent(app, bin, wanted)
	case "windows":
		ensureScheduledTask(app, bin, wanted)
	}
}

// idleCheckWanted reports whether the user's configuration wants idle stopping
// at all, and whether the configuration could be read to say so.
//
// idle_timeout = "0" turns idle stopping off, and a scheduler entry that runs
// every half hour to stop nothing is a background task with no purpose, so it
// is removed rather than left. A config.toml that cannot be read says nothing
// avar can act on: the registration is left exactly as it is, and the command
// that got here has already named the line.
func idleCheckWanted(app *App) (wanted, ok bool) {
	cfg, err := app.Config()
	if err != nil {
		return false, false
	}
	return session.IdleTimeout(cfg) > 0, true
}

// ensureLaunchdAgent installs a per-user launchd agent. The plist lives in
// ~/Library/LaunchAgents/ so launchd picks it up at the next login; avar loads
// it immediately so no logout is needed.
//
// It costs one read when the agent is already current, and repairs one that
// runs a different binary (see installLaunchdAgent).
func ensureLaunchdAgent(app *App, bin string, wanted bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if !wanted {
		removeLaunchdAgent(app, dir, launchctl)
		return
	}
	installLaunchdAgent(app, dir, bin, launchctl)
}

// removeLaunchdAgent unloads and deletes the agent because idle stopping is
// off. It costs one stat when there is no agent, which is every invocation
// after the first.
//
// Setting a timeout again brings it back: the next `avr` finds no plist and
// installs one, with the notice.
func removeLaunchdAgent(app *App, launchAgentsDir string, launchctl func(args ...string) error) {
	plistPath := filepath.Join(launchAgentsDir, launchdPlist)
	if !fileExists(plistPath) {
		return
	}
	if launchctl("list", launchdLabel) == nil {
		_ = launchctl("unload", plistPath)
	}
	if err := os.Remove(plistPath); err != nil {
		return
	}
	printIdleRemoved(app)
}

// printIdleRemoved tells the user avar removed its scheduler entry because of
// their setting, on either host.
func printIdleRemoved(app *App) {
	fmt.Fprintf(app.Err, "avr: removed the background idle-check, because idle_timeout = \"0\" turns idle stopping off.\n")
	fmt.Fprintf(app.Err, "     Set a timeout again and your next `avr` puts it back.\n")
}

// installLaunchdAgent is ensureLaunchdAgent with its host dependencies passed
// in: the LaunchAgents directory, the binary the agent should run, and how to
// run launchctl.
//
// The plist is compared with what this binary would write, not merely looked
// for. An agent that runs some other binary (an upgrade that moves avr, or the
// one a test run once installed pointing at a deleted test binary) fails on every run indefinitely, and trusting its
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

// scheduledTaskName is the Windows Task Scheduler entry that runs avar's idle
// check. It is a single name at the root rather than a task in a folder, so
// that removing avar removes exactly one thing and leaves no empty folder
// behind (design §3.8).
const scheduledTaskName = "avar-idle-check"

// scheduledTaskStamp records, inside avar's state directory, what avar last
// did about the Task Scheduler entry for this binary: registered it, or found
// no helper to register. Its presence is the cheap answer to "has this been
// done", and its contents are the cheap answer to "is it still what this avar
// would do". Every stamp an earlier avar wrote matches nothing this one writes,
// which is what replaces the tasks those versions registered.
const scheduledTaskStamp = "idle-task"

// idleCheckMinutes is how often the check runs, on both hosts. It was ten
// minutes until 2026-09-18. On Windows every run then briefly opened a console
// window, and six of those an hour was too many; thirty keeps a forgotten
// environment's extra running time within half an hour of its timeout.
const idleCheckMinutes = 30

// windowlessHelper is the program the scheduled task runs: avar's idle check
// linked as a Windows GUI-subsystem binary (cmd/avrw). avr.exe is a console
// program, and Windows gives a console program started in the user's session a
// console window of its own, so a task running avr.exe put a window on the
// desktop every time it fired. A GUI-subsystem program gets no console.
//
// It ships beside avr.exe in avar's Windows archive and is found there, never
// on PATH: whatever a scheduled task runs, it runs unattended every few
// minutes, and it must be the file avar shipped.
const windowlessHelper = "avrw.exe"

// ensureScheduledTask registers the Windows Task Scheduler entry that runs the
// idle check.
//
// The task runs as the signed-in user with their own token, which is what makes
// it work without elevation: a per-user task needs no administrator, and asking
// for one in order to stop a Linux environment nobody is using would be a poor
// trade. It also means the task can only see the environments that user owns,
// which is the same boundary everything else in avar respects.
//
// That is also why the window is avoided by changing the program rather than
// the logon. A task registered to run whether or not the user is logged on
// runs in no desktop session and would open no window, but it needs the
// user's password stored or an S4U logon, and wsl.exe is reported not to work
// from either (design §3.8 has the evidence).
//
// What it does *not* do is run schtasks on every invocation. This is reached
// from recordMachine, which runs on every `avr`, warm or cold — the macOS branch
// costs one read there and returns. Spawning two subprocesses instead (measured
// at 50–150 ms on this host) would spend a fifth of REQ-17.1's whole latency
// budget re-registering a task that has not changed, and rewrite the task store
// on disk each time.
//
// Every failure is silent, exactly as the launchd path is. Idle auto-stop is a
// convenience; a user who has just asked for a Linux shell should not be given
// an error about a background timer instead.
func ensureScheduledTask(app *App, bin string, wanted bool) {
	store, err := app.Store()
	if err != nil {
		return
	}
	stamp := filepath.Join(store.Root(), scheduledTaskStamp)
	if !wanted {
		removeScheduledTask(app, stamp, runSchtasks)
		return
	}
	installScheduledTask(app, stamp, bin, runSchtasks)
}

// installScheduledTask is ensureScheduledTask with its host dependencies passed
// in: the stamp file, the running binary, and how to run schtasks.
func installScheduledTask(app *App, stamp, bin string, schtasks func(args ...string) error) {
	recorded, err := os.ReadFile(stamp)
	found := err == nil
	registered := scheduledTaskStampContent(bin)

	// The warm path, and the whole of it: one read, no look for the helper,
	// no subprocess. A task the user deleted is as settled as a current one.
	if found && (string(recorded) == registered || string(recorded) == removedByUserStamp) {
		return
	}

	helper, ok := windowlessHelperBeside(bin)
	if !ok {
		withoutWindowlessHelper(app, stamp, bin, recorded, found, schtasks)
		return
	}

	// Replacing a registration — avr.exe moved, or an older avar made it — is
	// the one time avar asks whether the task is still there. The notice told
	// the user that deleting it turns the check off, so one that is gone was
	// deleted on purpose, and /Create /F would quietly undo that.
	if found && replacesRegistration(recorded, bin) && schtasks("/Query", "/TN", scheduledTaskName) != nil {
		_ = state.WriteFileAtomic(stamp, []byte(removedByUserStamp))
		return
	}

	// The command is one string because Task Scheduler stores it as one; the
	// path is quoted inside it so a path containing a space stays one argument
	// when the scheduler runs it. The helper takes no arguments. /F overwrites,
	// which is what replaces a task left by a previous install, including one
	// that runs avr.exe directly.
	if err := schtasks(
		"/Create",
		"/TN", scheduledTaskName,
		"/TR", fmt.Sprintf(`"%s"`, helper),
		"/SC", "MINUTE",
		"/MO", strconv.Itoa(idleCheckMinutes),
		"/F",
	); err != nil {
		return
	}

	// The stamp is written only after the task exists, so a failed
	// registration is retried next time rather than remembered as done.
	if err := state.WriteFileAtomic(stamp, []byte(registered)); err != nil {
		return
	}

	// Replacing a task is silent: this user has read the notice already. It is
	// shown when there was no task before, including when avar itself had
	// taken it away.
	if found && replacesRegistration(recorded, bin) {
		return
	}
	printIdleNotice(app, fmt.Sprintf("schtasks /Delete /TN %s /F", scheduledTaskName))
}

// withoutWindowlessHelper is what happens when avrw.exe is not beside avr.exe:
// nothing is registered, and the user is told once.
//
// Registering avr.exe itself instead would keep idle auto-stop working at the
// price of a window that takes the keyboard focus from whatever the user is
// typing into, every time the check runs, for as long as the environment
// exists. An environment left running costs memory until `avr stop`; that is
// the smaller cost, and the one the user can end. A task an earlier avar
// registered is deleted for the same reason: it runs avr.exe directly.
//
// The deletion is best-effort. If it fails, the old task keeps running as it
// did before, and the stamp still records that the user has been told, so a
// failure does not cost a subprocess on every invocation.
func withoutWindowlessHelper(app *App, stamp, bin string, recorded []byte, found bool, schtasks func(args ...string) error) {
	missing := helperMissingStampContent(bin)
	if found && string(recorded) == missing {
		return
	}
	if found {
		_ = schtasks("/Delete", "/TN", scheduledTaskName, "/F")
	}
	if err := state.WriteFileAtomic(stamp, []byte(missing)); err != nil {
		return
	}
	name := filepath.Base(bin)
	fmt.Fprintf(app.Err, "avr: idle auto-stop is off: %s is not in the same folder as %s.\n", windowlessHelper, name)
	fmt.Fprintf(app.Err, "     %s runs the background idle check without opening a window,\n", windowlessHelper)
	fmt.Fprintf(app.Err, "     and it ships beside %s in avar's Windows download. Put it next to\n", name)
	fmt.Fprintf(app.Err, "     %s and your next `avr` turns idle auto-stop on. Until then, stop\n", name)
	fmt.Fprintf(app.Err, "     environments you are not using with `avr stop`.\n")
}

// scheduledTaskStampContent is what the stamp records once the task runs the
// helper beside bin every idleCheckMinutes minutes.
func scheduledTaskStampContent(bin string) string {
	return fmt.Sprintf("%s\nevery %d minutes\nthrough %s\n", bin, idleCheckMinutes, windowlessHelper)
}

// helperMissingStampContent is what the stamp records once avar has told the
// user that bin has no helper beside it.
func helperMissingStampContent(bin string) string {
	return fmt.Sprintf("%s\nnot registered: no %s\n", bin, windowlessHelper)
}

// removedByUserStamp records that the task avar registered was found deleted.
// It names no binary: the user turned the check off, not one copy of avr.
// Deleting the stamp file undoes it, at the next `avr`.
const removedByUserStamp = "not registered: removed by the user\n"

// idleDisabledStamp records that avar removed the task because idle_timeout
// is "0". A timeout set again registers it at the next `avr`.
const idleDisabledStamp = "not registered: idle_timeout is 0\n"

// replacesRegistration reports whether a stamp describes a task avar
// registered, as opposed to one of the states in which avar registered none.
// Every stamp an earlier avar wrote describes a registration.
func replacesRegistration(recorded []byte, bin string) bool {
	switch string(recorded) {
	case helperMissingStampContent(bin), removedByUserStamp, idleDisabledStamp:
		return false
	}
	return !strings.HasSuffix(string(recorded), fmt.Sprintf("\nnot registered: no %s\n", windowlessHelper))
}

// removeScheduledTask deletes the task because idle stopping is off, and says
// so once. Only a task avar registered is deleted; if the stamp says there is
// none, there is nothing to do, and that costs the one read.
func removeScheduledTask(app *App, stamp string, schtasks func(args ...string) error) {
	recorded, err := os.ReadFile(stamp)
	if err != nil || !replacesRegistration(recorded, "") {
		return
	}
	if err := schtasks("/Delete", "/TN", scheduledTaskName, "/F"); err != nil {
		return
	}
	if err := state.WriteFileAtomic(stamp, []byte(idleDisabledStamp)); err != nil {
		return
	}
	printIdleRemoved(app)
}

// windowlessHelperBeside finds the helper that ships beside the running
// binary.
//
// Both meanings of "beside" are tried, because os.Executable makes no promise
// about which one it returns: "If a symlink was used to start the process,
// depending on the operating system, the result might be the symlink or the
// path it pointed to." winget unpacks the archive into a package folder and
// may link avr.exe onto PATH from another, so the two can differ, and the
// helper is certainly beside the real file.
func windowlessHelperBeside(bin string) (string, bool) {
	candidates := []string{filepath.Join(filepath.Dir(bin), windowlessHelper)}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil && resolved != bin {
		candidates = append(candidates, filepath.Join(filepath.Dir(resolved), windowlessHelper))
	}
	for _, candidate := range candidates {
		if fileExists(candidate) {
			return candidate, true
		}
	}
	return "", false
}

// runSchtasks runs schtasks with the given arguments. Finding it is part of
// running it, so the warm path, which runs no schtasks, does not search PATH
// either.
func runSchtasks(args ...string) error {
	schtasks, err := exec.LookPath("schtasks")
	if err != nil {
		return err
	}
	cmd := exec.Command(schtasks, args...)
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Run()
}

// fileExists reports whether a path is there, treating any error as absent —
// which is the right answer for a marker file whose only job is to say "this has
// happened before".
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// inTemporaryDir reports whether bin lies in any directory this host clears
// out, which is every directory a scheduled job must never point into.
//
// os.TempDir() alone is not that set. On macOS it is $TMPDIR, a per-user
// directory under /var/folders, so a binary built into /private/tmp passed the
// guard, registered itself, and left a job pointing at nothing once it was
// deleted — twice, in this repository's own history (docs/lessons.md). The
// roots below are the ones a build or a scratch directory actually lands in.
func inTemporaryDir(bin string) bool {
	for _, root := range temporaryRoots() {
		if root != "" && withinDir(bin, root) {
			return true
		}
	}
	return false
}

// temporaryRoots is every directory this host treats as temporary.
func temporaryRoots() []string {
	roots := []string{os.TempDir()}
	if runtime.GOOS == "windows" {
		// Windows programs read these in this order, and a service or a
		// different shell can give each a different value.
		return append(roots, os.Getenv("TEMP"), os.Getenv("TMP"), filepath.Join(os.Getenv("SystemRoot"), "Temp"))
	}
	// /private/tmp is what /tmp is on macOS; naming both costs nothing and
	// means neither spelling depends on resolveLinks having succeeded.
	return append(roots, "/tmp", "/private/tmp", "/var/tmp", "/private/var/tmp")
}

// withinDir reports whether path is dir or lies beneath it, comparing the two
// with symbolic links resolved where they can be: on macOS the temporary
// directory is reached through /var, which is itself a link to /private/var.
// Windows paths compare without regard to case, as Windows compares them.
func withinDir(path, dir string) bool {
	path, dir = resolveLinks(path), resolveLinks(dir)
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		rel = strings.ToLower(rel)
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// resolveLinks resolves the links in the longest part of path that exists, and
// keeps the rest as written: a binary about to be registered exists, but the
// same answer is wanted for one that does not.
func resolveLinks(path string) string {
	path = filepath.Clean(path)
	rest := ""
	for {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return filepath.Join(path, rest)
		}
		rest = filepath.Join(filepath.Base(path), rest)
		path = parent
	}
}
