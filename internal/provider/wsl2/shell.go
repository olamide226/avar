package wsl2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/olamide226/avar/internal/provider"
)

// Attaching a guest session is the one operation where avar is not a program
// that runs another program and reports on it: it is plumbing between the user's
// console and a process inside Linux, and its job is to be invisible. Three
// things follow, and they are what the rest of this file is about.
//
// The guest's exit status is data, not failure. A command that ran and exited
// non-zero is a *successful* Shell call reporting that code, so `avr false` exits
// 1 and `avr sh -c 'exit 42'` exits 42 (REQ-1.7, REQ-2.2, REQ-18.8, PROP-3). An
// error is reserved for not being able to run the command at all.
//
// Console behaviour is inherited rather than emulated. wsl.exe is given avar's
// own standard handles and shares its console, so an interactive session is a
// foreground job like any other: Ctrl-C reaches the guest as a console control
// event that Windows delivers to every process attached to the console, and a
// window resize reaches the guest's pseudo-terminal without avar being in the
// path at all. avar adds no buffer of its own between the two ends (REQ-2.3,
// REQ-3.1, REQ-3.4). What avar does do is decline to die of that Ctrl-C itself,
// so that it survives to report the status the guest exited with — see
// internal/provider/wsl2/console_windows.go.
//
// The environment the guest sees is built, never inherited. WSL has its own
// mechanism for carrying variables across the boundary, WSLENV, and avar uses it
// as an allowlist rather than working around it: exactly the policy's variables
// are named there and no others, so nothing crosses merely because it was
// exported in the user's shell (REQ-9.1, REQ-12.4, PROP-4). Windows PATH
// injection is already off in avar's distributions, closing the other door
// interop opens.

// wslEnvVar is WSL's own host-to-guest variable list.
//
// Its format is NAME/flags pairs separated by colons; the /u flag means "pass
// this from Windows into WSL". Setting it to exactly the policy's names is what
// makes the boundary an allowlist: a variable not named here cannot cross, and
// avar never leaves the user's own WSLENV in place to be inherited.
const wslEnvVar = "WSLENV"

// Shell runs opts.Argv in the guest, or attaches an interactive login shell when
// it is empty, and reports the status the guest process finished with.
//
// The environment must already be running: this creates and starts nothing, so a
// caller that skipped EnsureMachine gets ErrMachineNotFound or
// ErrMachineNotRunning rather than a distribution started as a side effect of
// wanting a shell.
//
// The returned exit code is the guest's own. It is an error only when the
// command could not be run at all, because a shell that turned `grep -q` finding
// nothing into an avar failure would be unusable in a script (PROP-3).
func (p *Provider) Shell(ctx context.Context, machine string, opts provider.ShellOpts) (int, error) {
	// Running a command inside an environment is as consequential as anything
	// avar does to one, so it takes the full ownership check (REQ-18.7, PROP-6).
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return 0, err
	}
	if err := checkShellOpts(machine, opts); err != nil {
		return 0, err
	}

	d, err := p.newView().require(ctx, machine)
	if err != nil {
		return 0, err
	}
	if d.WSLVersion == 1 {
		return 0, newWSL1Error(machine)
	}
	if !d.Running {
		return 0, fmt.Errorf("%w: %s is stopped; avar starts an environment before entering it, so reaching this means EnsureMachine was skipped",
			provider.ErrMachineNotRunning, machine)
	}

	cmd := p.shellCommand(ctx, machine, opts)
	return runShell(ctx, machine, cmd)
}

// checkShellOpts refuses the two ways one execution can be self-contradictory,
// before anything is started.
func checkShellOpts(machine string, opts provider.ShellOpts) error {
	if strings.TrimSpace(opts.Workdir) == "" {
		// Without one, wsl.exe chooses a working directory of its own from the
		// Windows current directory — and translates it, so the guest would
		// start under /mnt/c, a path avar's distributions do not even have
		// mounted (REQ-1.1, REQ-6.6, REQ-18.5, PROP-1).
		return fmt.Errorf("running a command in environment %s: no guest working directory was given; it comes from MapProjectPath", machine)
	}
	if !strings.HasPrefix(opts.Workdir, "/") {
		// --cd interprets an argument that does not begin with / as a Windows
		// path and translates it, so a host path passed here would silently
		// start the process somewhere else rather than failing (REQ-18.5).
		return fmt.Errorf("running a command in environment %s: %q is not an absolute Linux path; the guest working directory comes from MapProjectPath", machine, opts.Workdir)
	}
	if opts.TTY && (opts.Stdin != nil || opts.Stdout != nil || opts.Stderr != nil) {
		// An interactive session is bound to the real console's handles, so
		// honouring both would mean silently ignoring one of them.
		return fmt.Errorf("running a command in environment %s: an interactive session cannot have its streams redirected", machine)
	}
	return nil
}

// shellCommand builds the process that carries out one guest execution.
//
// It starts nothing. Everything that decides what the guest sees — the argv, the
// environment, which end of each stream wsl.exe is given — is settled here, so
// that it can be asserted in a test without a distribution, which is the only
// way the environment boundary is checkable at all.
func (p *Provider) shellCommand(ctx context.Context, machine string, opts provider.ShellOpts) *exec.Cmd {
	cmd := exec.CommandContext(ctx, p.wsl, p.shellArgv(machine, opts)...)
	cmd.Env = transportEnv(os.Environ(), opts.Env)

	cmd.Stdin = opts.Stdin
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	cmd.Stdout = opts.Stdout
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	cmd.Stderr = opts.Stderr
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	return cmd
}

// shellArgv is the wsl.exe command line for one guest execution.
//
// The guest command is passed after --exec as separate arguments, never as a
// string: avar re-splits nothing and quotes nothing, so `avr git commit -m 'two
// words'` reaches the guest as the three arguments the user typed (REQ-2.5).
// --exec is what makes that true — without it wsl.exe hands the remainder to the
// guest's login shell, which would parse it a second time.
//
// An empty argv omits --exec entirely, which is how the user gets their own
// login shell rather than a shell avar chose for them (REQ-1.1).
func (p *Provider) shellArgv(machine string, opts provider.ShellOpts) []string {
	argv := []string{
		"--distribution", machine,
		"--user", p.guestUser,
		"--cd", opts.Workdir,
	}
	if len(opts.Argv) == 0 {
		return argv
	}
	argv = append(argv, "--exec")
	return append(argv, opts.Argv...)
}

// transportEnv is the environment the wsl.exe process itself runs in.
//
// It is the host's, because wsl.exe genuinely needs it — a SystemRoot to find
// its own libraries, a PATH — and none of it reaches the guest: avar's
// distributions have Windows PATH injection disabled, and WSL carries a variable
// across only when WSLENV names it.
//
// So WSLENV is rewritten from the policy and never inherited. Whatever the user
// had in theirs is dropped, the policy's variables are set to the policy's
// values, and WSLENV names exactly those. A variable outside the grant cannot
// cross, which is the whole of PROP-4 on this backend, and it is enforced by
// WSL's own mechanism rather than by avar hoping nothing else forwards anything.
func transportEnv(hostEnv []string, guest map[string]string) []string {
	out := make([]string, 0, len(hostEnv)+len(guest)+1)
	for _, entry := range hostEnv {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || strings.EqualFold(name, wslEnvVar) || guestNamed(guest, name) {
			continue
		}
		out = append(out, entry)
	}

	names := sortedNames(guest)
	for _, name := range names {
		out = append(out, name+"="+guest[name])
	}
	// An empty grant still sets WSLENV, to an empty value: leaving it unset
	// would let a WSLENV inherited from somewhere avar did not look decide what
	// crosses.
	out = append(out, wslEnvVar+"="+wslEnvList(names))
	return out
}

// wslEnvList renders the names as WSLENV's host-to-guest list.
func wslEnvList(names []string) string {
	if len(names) == 0 {
		return ""
	}
	entries := make([]string, 0, len(names))
	for _, name := range names {
		entries = append(entries, name+"/u")
	}
	return strings.Join(entries, ":")
}

// guestNamed reports whether the policy has its own value for a host variable,
// so the host's copy is dropped rather than appearing twice.
func guestNamed(guest map[string]string, name string) bool {
	for guestName := range guest {
		if strings.EqualFold(guestName, name) {
			return true
		}
	}
	return false
}

// sortedNames orders an environment's names so that one execution produces one
// command line, which is what makes it assertable in a test.
func sortedNames(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// runShell runs wsl.exe to completion and turns its outcome into the guest's
// exit status.
func runShell(ctx context.Context, machine string, cmd *exec.Cmd) (int, error) {
	// Interrupts are held before the child exists, not after. Installing the
	// hold after Start leaves a window in which a Ctrl-C kills avar while
	// wsl.exe is already running — the exact outcome this exists to prevent,
	// and it would orphan the guest process with nobody left to report its
	// status. The window is small and it sits at the moment the command
	// visibly starts, which is when a user is most likely to press Ctrl-C.
	//
	// Holding across a Start that then fails costs nothing: stop() runs on
	// every path out of this function.
	stop := holdInterrupts()
	defer stop()

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("running a command in environment %s: starting %s: %w", machine, cmd.Path, err)
	}

	waitErr := cmd.Wait()

	// Cancellation is checked before the exit status, because a context that
	// was cancelled killed the transport: the status is then avar's own doing
	// and reporting it as the guest's would be a lie.
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("running a command in environment %s: %w", machine, err)
	}
	return exitStatus(machine, waitErr)
}

// exitStatus separates the guest's status from avar's failures.
//
// Anything that is not wsl.exe exiting with a status is a failure to run the
// command at all. A wsl.exe that exited with a status hands that status straight
// through, whatever it is, because wsl.exe propagates the guest process's own
// (PROP-3).
//
// There is no signal case here, and that is a real difference rather than an
// omission. A Windows process has an exit code and nothing else — no signal
// status — so a guest killed by SIGTERM reaches avar as whatever code wsl.exe
// chose to exit with, and inventing 128+n from it would be avar making a number
// up.
func exitStatus(machine string, waitErr error) (int, error) {
	if waitErr == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		return 0, fmt.Errorf("running a command in environment %s: %w", machine, waitErr)
	}
	return exitErr.ExitCode(), nil
}
