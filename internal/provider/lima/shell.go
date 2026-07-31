package lima

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/olamide226/avar/internal/provider"
	"golang.org/x/term"
)

// Attaching a guest session is the one operation where avar is not a program
// that runs another program and reports on it: it is a piece of plumbing
// between the user's terminal and a process inside Linux, and its job is to be
// invisible. Three things follow, and they are what the rest of this file is
// about.
//
// The guest's exit status is data, not failure. A command that ran and exited
// non-zero is a *successful* Shell call which reports that code, so `avr false`
// exits 1 and `avr sh -c 'exit 42'` exits 42 (REQ-1.7, REQ-2.2, PROP-3). An
// error is reserved for not being able to run the command at all.
//
// Terminal behaviour is inherited rather than emulated. The transport is given
// the real file descriptors and shares avar's process group, so an interactive
// session is a foreground job like any other: ssh puts the local terminal into
// raw mode and Ctrl-C, Ctrl-Z and Ctrl-D travel to the guest as data rather
// than acting on avr (REQ-3.3), and a window resize reaches the guest's
// pseudo-terminal without avar being in the path at all (REQ-3.1). avar adds no
// buffer of its own between the two ends (REQ-2.3, REQ-3.4).
//
// The environment the guest sees is built, never inherited. Only the policy's
// variables are passed, and the transport's own environment is scrubbed of the
// variables ssh is capable of forwarding, so nothing crosses the boundary
// merely because it was exported in the user's shell (REQ-9.1, PROP-4).

// limactlShell is the limactl subcommand that runs something in a guest.
const limactlShell = "shell"

// guestEnvCommand is the guest program avar prefixes a command with in order to
// set the policy's variables. It is part of coreutils, so it is present on
// every distribution in avar's matrix, and it takes its assignments as
// arguments — which means no value can turn into shell syntax on the way in.
const guestEnvCommand = "env"

// transportForwardable names the host variables the transport itself is able to
// put into the guest: TERM travels in ssh's pseudo-terminal request, COLORTERM
// is named in the SendEnv option Lima adds, and LANG/LC_* are in the SendEnv
// list that ships in the system ssh configuration on macOS.
//
// They are removed from the environment limactl runs in and replaced by the
// policy's own values, so that what the transport is able to forward is exactly
// what policy allows rather than whatever the user's shell happened to export
// (PROP-4). Anything not on this list cannot cross: ssh forwards no other
// variable, and avar never passes --preserve-env, which is Lima's own opt-in
// for propagating the host environment wholesale.
var transportForwardable = []string{"TERM", "COLORTERM", "LANG"}

// sshAgentOverride turns on SSH agent forwarding for one invocation (REQ-12.3).
//
// `limactl shell` has no agent flag, but it documents $SSH as the way to
// substitute the ssh command it runs, so the forwarding request goes there.
// The socket itself needs nothing from avar: ssh runs on the host and reads the
// host's own SSH_AUTH_SOCK, which transportEnv leaves in place because it is
// not something the transport can put into the guest by itself.
//
// Disabling connection multiplexing is what actually makes it work, and is not
// optional. Lima's ssh configuration sets ControlMaster/ControlPersist, so a
// second `limactl shell` reuses the connection the first one opened; `-A` on
// the later invocation is then silently ignored, because agent forwarding is
// negotiated when the master connection is established. Verified against Lima
// 2.2.0: with multiplexing left on, SSH_AUTH_SOCK is empty in the guest.
//
// The cost is a fresh TCP+SSH handshake for an agent-forwarded session, which
// is why this is applied only when the flag is given and never to the warm path
// (REQ-17.1).
const sshAgentOverride = `SSH=ssh -A -o ControlMaster=no -o ControlPath=none`

// forwardedSignals are the signals avar passes on to the guest.
//
// SIGINT and SIGTERM are Requirement 2.4. SIGWINCH is Requirement 3.1: it
// already reaches the transport directly, because the terminal driver signals
// the whole foreground process group, and relaying it as well costs nothing and
// keeps the guarantee from depending on avar and the transport sharing a group.
var forwardedSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGWINCH}

// Shell runs opts.Argv in the guest, or attaches an interactive login shell
// when it is empty, and reports the status the guest process finished with.
//
// The machine must already be running: this creates and starts nothing, so a
// caller that skipped EnsureMachine gets ErrMachineNotFound or
// ErrMachineNotRunning rather than a machine started as a side effect of
// wanting a shell.
//
// The returned exit code is the guest's own. It is an error only when the
// command could not be run at all — the transport failed, the context was
// cancelled — because a shell that turned `grep -q` finding nothing into an
// avar failure would be unusable in a script (PROP-3).
func (p *Provider) Shell(ctx context.Context, machine string, opts provider.ShellOpts) (int, error) {
	// Running a command inside a machine is as consequential as anything avar
	// does to one, so it takes the full ownership check: the avr- prefix and
	// avar's own record (REQ-5.4, PROP-6).
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return 0, err
	}
	if err := checkShellOpts(machine, opts); err != nil {
		return 0, err
	}

	inst, err := p.newView().require(ctx, machine)
	if err != nil {
		return 0, err
	}
	if !inst.running() {
		return 0, fmt.Errorf("%w: %s is %s; avar starts a machine before entering it, so reaching this means EnsureMachine was skipped",
			provider.ErrMachineNotRunning, machine, inst.state())
	}

	cmd, relay := p.shellCommand(ctx, machine, opts)
	return runShell(ctx, machine, cmd, relay)
}

// checkShellOpts refuses the two ways one execution can be self-contradictory,
// before anything is started.
func checkShellOpts(machine string, opts provider.ShellOpts) error {
	if strings.TrimSpace(opts.Workdir) == "" {
		// Without one, Lima chooses a working directory of its own from the
		// host's current directory, which is precisely the guess avar exists to
		// replace (REQ-1.1, REQ-6.6, PROP-1).
		return fmt.Errorf("running a command in machine %s: no guest working directory was given; it comes from MapProjectPath", machine)
	}
	if opts.TTY && (opts.Stdin != nil || opts.Stdout != nil || opts.Stderr != nil) {
		// A pseudo-terminal is bound to the real terminal's file descriptors,
		// so honouring both would mean silently ignoring one of them.
		return fmt.Errorf("running a command in machine %s: an interactive session cannot have its streams redirected", machine)
	}
	return nil
}

// shellCommand builds the process that carries out one guest execution,
// together with the relay its standard output may need.
//
// It starts nothing. Everything that decides what the guest sees — the argv,
// the environment, which end of each stream the transport is given — is settled
// here, so that it can be asserted in a test without a virtual machine, which
// is the only way the environment boundary is checkable at all.
func (p *Provider) shellCommand(ctx context.Context, machine string, opts provider.ShellOpts) (*exec.Cmd, *stdoutRelay) {
	cmd := exec.CommandContext(ctx, p.limactl, shellArgv(machine, opts)...)
	cmd.Env = transportEnv(os.Environ(), opts.Env)
	if opts.ForwardSSHAgent {
		cmd.Env = append(cmd.Env, sshAgentOverride)
	}

	cmd.Stdin = opts.Stdin
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	cmd.Stderr = opts.Stderr
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}

	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}

	// Lima decides whether to ask ssh for a guest pseudo-terminal by looking at
	// its own standard output, so that is the one lever avar has over PTY
	// allocation, and honouring `PTY iff opts.TTY` (PROP-8) means using it. When
	// no terminal was asked for but the transport would inherit one — a
	// pipeline feeding avar, with output still on screen — its standard output
	// is replaced by a pipe that avar copies onward. Without that, ssh would
	// allocate a pseudo-terminal whose line discipline echoes piped input back
	// and rewrites newlines, which is visible corruption rather than a subtlety.
	if !opts.TTY && isTerminal(stdout) {
		if relay, err := newStdoutRelay(stdout); err == nil {
			cmd.Stdout = relay.writer
			return cmd, relay
		}
		// A pipe avar cannot create is not worth failing the session over: the
		// guest still runs, on a pseudo-terminal it did not need.
	}
	cmd.Stdout = stdout
	return cmd, nil
}

// shellArgv is the limactl command line for one guest execution.
//
// The guest command is passed after "--" as separate arguments, never as a
// string: avar re-splits nothing and quotes nothing, so `avr git commit -m 'two
// words'` reaches the guest as the three arguments the user typed (REQ-2.5).
// Lima escapes each one for the guest's login shell itself.
func shellArgv(machine string, opts provider.ShellOpts) []string {
	argv := []string{limactlShell, "--workdir", opts.Workdir, machine}
	if len(opts.Argv) == 0 {
		// No command: Lima execs the user's own login shell, which is what
		// REQ-1.1 asks for and what keeps avar out of the business of knowing
		// which shell a distribution installs.
		return argv
	}
	argv = append(argv, "--")
	return append(argv, guestArgv(opts)...)
}

// guestArgv prefixes the guest command with the policy environment.
//
// `env -- NAME=value command args...` is how one execution gets exactly the
// variables policy allows and nothing else: the assignments are arguments to a
// program rather than syntax, so no shell parses them.
//
// The "--" comes before the assignments, not after. env stops reading options
// at its first non-option argument, so a "--" placed after the assignments is
// not an option terminator at all — it is the command name, and env fails with
// "env: '--': No such file or directory". Placed first it does the job it is
// there for: `avr -- env --version` runs the guest's env with --version
// instead of printing this env's own version. Both forms verified against the
// guest's GNU coreutils 9.4.
//
// The interactive form has no equivalent, because Lima builds `exec <shell> -l`
// with nowhere to put a prefix. There the policy's terminal type reaches the
// guest through ssh's pseudo-terminal request instead — see transportEnv.
func guestArgv(opts provider.ShellOpts) []string {
	argv := make([]string, 0, len(opts.Env)+len(opts.Argv)+2)
	argv = append(argv, guestEnvCommand, "--")
	for _, name := range sortedNames(opts.Env) {
		argv = append(argv, name+"="+opts.Env[name])
	}
	return append(argv, opts.Argv...)
}

// sortedNames orders an environment's names so that one execution produces one
// argv, which is what makes the argv assertable in a test.
func sortedNames(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// transportEnv is the environment the limactl process itself runs in.
//
// It is the host's, because the transport genuinely needs it — a PATH to find
// ssh with, a HOME to read Lima's own configuration and keys from — and none of
// it reaches the guest: ssh forwards only what it is told to forward. The
// exception is the short list of variables it *can* forward, which is scrubbed
// and then rewritten from the policy, so the guest's terminal type and locale
// are the policy's values rather than whatever the user's shell exported
// (REQ-9.1, PROP-4).
//
// This is also how an interactive session gets its terminal type: with no argv
// there is nowhere to put an `env` prefix, and TERM travels in ssh's
// pseudo-terminal request, taken from the environment ssh itself is running in
// (REQ-3.2).
func transportEnv(hostEnv []string, guest map[string]string) []string {
	out := make([]string, 0, len(hostEnv)+len(guest))
	for _, entry := range hostEnv {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || forwardable(name) {
			continue
		}
		out = append(out, entry)
	}
	for _, name := range sortedNames(guest) {
		if forwardable(name) {
			out = append(out, name+"="+guest[name])
		}
	}
	return out
}

// forwardable reports whether ssh is capable of carrying a variable of this
// name into the guest by itself.
func forwardable(name string) bool {
	if strings.HasPrefix(name, "LC_") {
		return true
	}
	for _, forwarded := range transportForwardable {
		if name == forwarded {
			return true
		}
	}
	return false
}

// isTerminal reports whether a stream is a terminal, which is a question about
// the file descriptor rather than about the writer: anything that is not an
// open file is a pipe or a buffer by construction. The check is a terminal
// ioctl rather than "is a character device", because /dev/null is a character
// device and no terminal at all.
func isTerminal(stream io.Writer) bool {
	file, ok := stream.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

// stdoutRelay carries the transport's standard output onward to where the
// caller wanted it, through a pipe, so that the transport does not see a
// terminal on its own standard output. See shellCommand for why that matters.
type stdoutRelay struct {
	reader *os.File
	writer *os.File
	dst    io.Writer
	done   chan error
}

// newStdoutRelay prepares a relay onto dst.
func newStdoutRelay(dst io.Writer) (*stdoutRelay, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	return &stdoutRelay{reader: reader, writer: writer, dst: dst, done: make(chan error, 1)}, nil
}

// start begins copying, after the child has inherited the writing end.
//
// The parent's copy of that end is closed here rather than at the end of the
// call: while it is open the reader never reaches end of file, so a guest that
// exits would leave avar waiting on output that can no longer arrive.
func (r *stdoutRelay) start() {
	if r == nil {
		return
	}
	_ = r.writer.Close()
	go func() {
		// io.Copy passes each read straight to the destination, so output
		// appears as the guest produces it: the relay adds a copy, never a
		// buffer (REQ-2.3, REQ-3.4).
		_, err := io.Copy(r.dst, r.reader)
		r.done <- err
	}()
}

// wait blocks until every byte the guest wrote has been passed on.
func (r *stdoutRelay) wait() error {
	if r == nil {
		return nil
	}
	err := <-r.done
	_ = r.reader.Close()
	return err
}

// close releases the pipe when the transport never started.
func (r *stdoutRelay) close() {
	if r == nil {
		return
	}
	_ = r.writer.Close()
	_ = r.reader.Close()
}

// runShell runs the transport to completion and turns its outcome into the
// guest's exit status.
func runShell(ctx context.Context, machine string, cmd *exec.Cmd, relay *stdoutRelay) (int, error) {
	if err := cmd.Start(); err != nil {
		relay.close()
		return 0, fmt.Errorf("running a command in machine %s: starting %s: %w", machine, cmd.Path, err)
	}
	relay.start()

	stop := relaySignals()
	defer stop()

	waitErr := cmd.Wait()
	relayErr := relay.wait()

	// Cancellation is checked before the exit status, because a context that
	// was cancelled killed the transport: the status is then avar's own doing
	// and reporting it as the guest's would be a lie.
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("running a command in machine %s: %w", machine, err)
	}
	if relayErr != nil {
		return 0, fmt.Errorf("passing on the output of a command in machine %s: %w", machine, relayErr)
	}
	return exitStatus(machine, waitErr)
}

// exitStatus separates the guest's status from avar's failures.
//
// Anything that is not the transport exiting with a status is a failure to run
// the command at all. A transport that exited with a status hands that status
// straight through, whatever it is: Lima's shell propagates ssh's, and ssh
// propagates the guest's, so the number that arrives here is the number the
// guest process finished with (PROP-3).
//
// A transport killed by a signal is reported the way every shell reports one,
// as 128 plus the signal number, so that a guest interrupted with Ctrl-C leaves
// avr exiting 130 exactly as an interrupted local command would.
func exitStatus(machine string, waitErr error) (int, error) {
	if waitErr == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		return 0, fmt.Errorf("running a command in machine %s: %w", machine, waitErr)
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal()), nil
	}
	return exitErr.ExitCode(), nil
}

// relaySignals passes the signals a user can send avar on to the guest, and
// returns a function that stops doing so.
//
// The signal goes to avar's whole process group rather than to the limactl
// child, because the process actually holding the terminal and the guest
// session is ssh, one level further down: killing limactl alone would leave ssh
// attached to the terminal with the guest command still running, which is worse
// than not forwarding at all (REQ-2.4).
//
// This does mean a signal sent to avr reaches anything else sharing its process
// group — the other members of a pipeline it was started in, say. That is the
// same set the terminal driver signals when the user presses Ctrl-C, so the
// behaviour is the one a user of a shell already expects; and there is no
// narrower target available without putting the transport in a process group of
// its own, which would take it out of the terminal's foreground and break the
// interactive session this exists to serve.
//
// Nothing is relayed for the common case at all: a signal raised at the
// terminal is delivered to the foreground process group, which the transport is
// already part of. This is for a signal aimed at avr alone, as a script or a
// supervisor sends one.
func relaySignals() (stop func()) {
	signals := make(chan os.Signal, len(forwardedSignals))
	signal.Notify(signals, forwardedSignals...)

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case sig := <-signals:
				broadcast(signals, sig)
			}
		}
	}()

	return func() {
		signal.Stop(signals)
		close(done)
	}
}

// broadcast delivers sig to every process in avar's process group.
//
// avar's own disposition is set to ignore for the duration, because it is a
// member of that group and must survive to report the guest's exit status. The
// window in which a second signal would be ignored rather than relayed is the
// price of that, and it is preferable to the alternative — a relay that kills
// the process doing the relaying.
func broadcast(signals chan<- os.Signal, sig os.Signal) {
	number, ok := sig.(syscall.Signal)
	if !ok {
		return
	}
	signal.Ignore(sig)
	// A pid of 0 means every process in the caller's own process group.
	_ = syscall.Kill(0, number)
	signal.Notify(signals, sig)
}
