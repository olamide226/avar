//go:build unix

// LimaProvider is macOS-only (REQ-17.6), and this package's tests are its
// behaviour tests: they assert on identity mount mappings that only a POSIX
// host can have, on golden instance configurations containing POSIX paths, and
// on guest execution driven through real POSIX programs. None of that is a
// question a Windows host can answer, and answering it there with fabricated
// Windows-shaped fixtures would assert something that can never happen.
//
// What the Windows build claims for this package is therefore that it compiles
// — which is what keeps `avr.exe` linkable while a second backend is built
// beside this one — not that a backend which cannot run there passes.

package lima

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/provider"
)

// The argv is the whole of avar's agreement with the backend about what runs
// where, so these tests assert on it rather than on "something was executed":
// it is what proves the guest's working directory, the guest's environment and
// the user's own arguments arrive intact and that no shell is ever involved on
// the way.

const shellMachine = "avr-ubuntu-24.04-arm64"

// guestEnv is a composed policy environment, as internal/envpolicy produces.
func guestEnv() map[string]string {
	return map[string]string{"TERM": "xterm-256color", "LANG": "en_GB.UTF-8"}
}

func TestShellArgv_OneShotCommand_REQ_2_1(t *testing.T) {
	got := shellArgv(shellMachine, provider.ShellOpts{
		Workdir: "/Users/dev/code/app/api",
		Argv:    []string{"npm", "test"},
		Env:     guestEnv(),
	})

	want := []string{
		"shell", "--workdir", "/Users/dev/code/app/api", shellMachine, "--",
		"env", "--", "LANG=en_GB.UTF-8", "TERM=xterm-256color", "npm", "test",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shellArgv:\nwant: %v\ngot:  %v", want, got)
	}
}

// An interactive session has no argv of its own: Lima execs the user's own
// login shell, which is what keeps avar out of the business of knowing which
// shell a distribution installs (REQ-1.1).
func TestShellArgv_InteractiveSession_REQ_1_1(t *testing.T) {
	got := shellArgv(shellMachine, provider.ShellOpts{
		Workdir: "/Users/dev/code/app",
		Env:     guestEnv(),
	})

	want := []string{"shell", "--workdir", "/Users/dev/code/app", shellMachine}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shellArgv:\nwant: %v\ngot:  %v", want, got)
	}
}

// Nothing between the user and the guest re-splits or re-quotes an argument:
// each one the user typed is one element of the argv (REQ-2.5).
func TestShellArgv_PassesGuestArgumentsVerbatim_REQ_2_5(t *testing.T) {
	argv := []string{"git", "commit", "-m", "two words; rm -rf /", "--author", "A <a@b.c>"}

	got := shellArgv(shellMachine, provider.ShellOpts{Workdir: "/w", Argv: argv})

	tail := got[len(got)-len(argv):]
	if !reflect.DeepEqual(tail, argv) {
		t.Errorf("guest arguments were altered:\nwant: %q\ngot:  %q", argv, tail)
	}
}

// A guest command that begins with a dash is a command, not an option of the
// env program avar prefixes it with.
func TestShellArgv_SeparatesEnvironmentFromACommandBeginningWithADash(t *testing.T) {
	got := shellArgv(shellMachine, provider.ShellOpts{
		Workdir: "/w",
		Argv:    []string{"--help"},
		Env:     map[string]string{"TERM": "xterm-256color"},
	})

	want := []string{"shell", "--workdir", "/w", shellMachine, "--", "env", "--", "TERM=xterm-256color", "--help"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shellArgv:\nwant: %v\ngot:  %v", want, got)
	}
}

// PROP-4: the guest is given exactly the policy's environment. The host's own
// variables are not merged in, and the variables the transport is itself
// capable of forwarding are scrubbed from it and rewritten from the policy, so
// nothing crosses merely because it was exported in the user's shell.
func TestTransportEnv_ScrubsWhatTheTransportCouldForward_PROP_4(t *testing.T) {
	host := []string{
		"PATH=/opt/homebrew/bin:/usr/bin",
		"HOME=/Users/dev",
		"TERM=dumb",
		"COLORTERM=truecolor",
		"LANG=C",
		"LC_ALL=C",
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI",
		"malformed-entry-with-no-equals",
	}

	got := transportEnv(host, map[string]string{"TERM": "xterm-256color", "LANG": "en_GB.UTF-8"})

	want := []string{
		"PATH=/opt/homebrew/bin:/usr/bin",
		"HOME=/Users/dev",
		// The transport keeps the host's credentials because it needs its own
		// environment to work; ssh forwards none of them.
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI",
		"LANG=en_GB.UTF-8",
		"TERM=xterm-256color",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("transportEnv:\nwant: %v\ngot:  %v", want, got)
	}
	for _, entry := range got {
		if strings.HasPrefix(entry, "COLORTERM=") || strings.HasPrefix(entry, "LC_ALL=") {
			t.Errorf("%q survived in the transport's environment, so ssh could forward it into the guest", entry)
		}
	}
}

func TestShellCommand_HonoursTheCallersStreams(t *testing.T) {
	p := newTestProvider(t, newFakeRunner(), newFakeRecords(ownedRecord(shellMachine)))
	var out, errs bytes.Buffer
	in := strings.NewReader("input")

	cmd, relay := p.shellCommand(context.Background(), shellMachine, provider.ShellOpts{
		Workdir: "/w",
		Argv:    []string{"cat"},
		Stdin:   in,
		Stdout:  &out,
		Stderr:  &errs,
	})

	if cmd.Stdin != io.Reader(in) {
		t.Error("the guest's standard input was not the caller's")
	}
	if cmd.Stdout != io.Writer(&out) || cmd.Stderr != io.Writer(&errs) {
		t.Error("the guest's output streams were not the caller's")
	}
	if relay != nil {
		t.Error("a stream that is not a terminal was relayed through a pipe for no reason")
		relay.close()
	}
}

func TestShellCommand_InheritsTheProcessStreamsByDefault(t *testing.T) {
	p := newTestProvider(t, newFakeRunner(), newFakeRecords(ownedRecord(shellMachine)))

	cmd, relay := p.shellCommand(context.Background(), shellMachine, provider.ShellOpts{
		Workdir: "/w",
		TTY:     true,
	})
	defer relay.close()

	if cmd.Stdin != io.Reader(os.Stdin) || cmd.Stderr != io.Writer(os.Stderr) {
		t.Error("an attached session did not inherit avar's own streams")
	}
	if relay != nil {
		t.Error("an interactive session had its output relayed, which would hide the terminal from the transport")
	}
}

// PROP-8: a pseudo-terminal is allocated if and only if one was asked for.
// Lima decides that by looking at its own standard output, so when no terminal
// was asked for and the transport would have inherited one, avar replaces it
// with a pipe. Without this, a pipeline feeding avar would get a guest
// pseudo-terminal that echoes its input back.
func TestShellCommand_HidesTheTerminalWhenNoneWasAskedFor_PROP_8(t *testing.T) {
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("this test needs a controlling terminal to hide: %v", err)
	}
	defer func() { _ = tty.Close() }()

	p := newTestProvider(t, newFakeRunner(), newFakeRecords(ownedRecord(shellMachine)))

	cmd, relay := p.shellCommand(context.Background(), shellMachine, provider.ShellOpts{
		Workdir: "/w",
		Argv:    []string{"ls"},
		Stdout:  tty,
	})
	if relay == nil {
		t.Fatal("a terminal was left on the transport's standard output, so it would allocate a guest pseudo-terminal that was not asked for")
	}
	defer relay.close()

	if cmd.Stdout == io.Writer(tty) {
		t.Error("the transport was given the terminal itself rather than the relay's pipe")
	}
}

func TestStdoutRelay_PassesEveryByteOn(t *testing.T) {
	var out bytes.Buffer
	relay, err := newStdoutRelay(&out)
	if err != nil {
		t.Fatalf("newStdoutRelay: %v", err)
	}

	// Stand in for the child: write through the end the transport would hold,
	// then let start close avar's own copy so the copy sees end of file.
	go func() {
		_, _ = relay.writer.WriteString("hello guest\n")
		relay.start()
	}()

	if err := relay.wait(); err != nil {
		t.Fatalf("relay.wait: %v", err)
	}
	if out.String() != "hello guest\n" {
		t.Errorf("relayed %q, want %q", out.String(), "hello guest\n")
	}
}

func TestShell_RefusesAnInteractiveSessionWithRedirectedStreams(t *testing.T) {
	p := newTestProvider(t, newFakeRunner(), newFakeRecords(ownedRecord(shellMachine)))

	_, err := p.Shell(context.Background(), shellMachine, provider.ShellOpts{
		Workdir: "/w",
		TTY:     true,
		Stdout:  &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("a pseudo-terminal and a redirected stream were accepted together")
	}
	if !strings.Contains(err.Error(), "redirected") {
		t.Errorf("error %q does not explain the contradiction", err)
	}
}

func TestShell_RequiresAGuestWorkingDirectory_PROP_1(t *testing.T) {
	p := newTestProvider(t, newFakeRunner(), newFakeRecords(ownedRecord(shellMachine)))

	_, err := p.Shell(context.Background(), shellMachine, provider.ShellOpts{Argv: []string{"pwd"}})
	if err == nil {
		t.Fatal("a session was accepted with no guest working directory, so the guest would start somewhere Lima chose")
	}
}

// PROP-6: a machine avar has no record of is untouchable, whatever its name
// looks like, and refusing costs no subprocess at all.
func TestShell_RefusesAMachineAvarDoesNotOwn_PROP_6(t *testing.T) {
	cases := map[string]string{
		"a machine the user created": "default",
		"an unrecorded avr- machine": "avr-not-in-avars-records",
	}
	for name, machine := range cases {
		t.Run(name, func(t *testing.T) {
			runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
			p := newTestProvider(t, runner, newFakeRecords(ownedRecord(shellMachine)))

			_, err := p.Shell(context.Background(), machine, provider.ShellOpts{Workdir: "/w", Argv: []string{"ls"}})
			if !errors.Is(err, provider.ErrNotOwned) {
				t.Fatalf("Shell on %s returned %v, want ErrNotOwned", machine, err)
			}
			if got := runner.limactlArgvs(); len(got) != 0 {
				t.Errorf("refusing a machine avar does not own still ran %v", got)
			}
		})
	}
}

func TestShell_ReportsAMachineThatIsNotRunning(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-fedora-42-amd64")))

	_, err := p.Shell(context.Background(), "avr-fedora-42-amd64", provider.ShellOpts{Workdir: "/w", Argv: []string{"ls"}})
	if !errors.Is(err, provider.ErrMachineNotRunning) {
		t.Fatalf("Shell returned %v, want ErrMachineNotRunning", err)
	}
	assertArgvs(t, runner.limactlArgvs(), []string{"limactl list --json"})
}

func TestShell_ReportsAMachineThatDoesNotExist(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-empty.json"))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord(shellMachine)))

	_, err := p.Shell(context.Background(), shellMachine, provider.ShellOpts{Workdir: "/w", Argv: []string{"ls"}})
	if !errors.Is(err, provider.ErrMachineNotFound) {
		t.Fatalf("Shell returned %v, want ErrMachineNotFound", err)
	}
}

// PROP-3: a guest process that ran and exited non-zero is a success for Shell,
// and its status is reported unchanged. Only a failure to run the command at
// all is an error.
func TestExitStatus_ReportsTheGuestsOwnStatus_PROP_3(t *testing.T) {
	cases := []struct {
		name    string
		script  string
		want    int
		wantErr bool
	}{
		{name: "success", script: "exit 0", want: 0},
		{name: "one", script: "exit 1", want: 1},
		{name: "forty-two", script: "exit 42", want: 42},
		{name: "the highest status a process can report", script: "exit 255", want: 255},
		{name: "killed by SIGTERM", script: "kill -TERM $$", want: 128 + 15},
		{name: "killed by SIGINT", script: "kill -INT $$", want: 128 + 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			waitErr := exec.Command("/bin/sh", "-c", tc.script).Run()

			got, err := exitStatus(shellMachine, waitErr)
			if err != nil {
				t.Fatalf("exitStatus returned an error for a guest status: %v", err)
			}
			if got != tc.want {
				t.Errorf("exit status = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestExitStatus_AFailureToRunAtAllIsAnError(t *testing.T) {
	// Not an ExitError: the transport never ran, so there is no guest status to
	// report and reporting one would invent a result.
	_, err := exitStatus(shellMachine, errors.New("fork/exec /opt/homebrew/bin/limactl: no such file or directory"))
	if err == nil {
		t.Fatal("a transport that never ran was reported as a guest exit status")
	}
	if !strings.Contains(err.Error(), shellMachine) {
		t.Errorf("error %q does not name the machine avar was working on", err)
	}
}

// The flag has to reach the transport, not merely be recorded on the options.
// An earlier version set ShellOpts.ForwardSSHAgent and no backend ever read it,
// so `avr --ssh-agent` produced a session with no agent and said nothing.
func TestShellCommand_ForwardsTheAgentWhenAsked_REQ_12_3(t *testing.T) {
	p := newTestProvider(t, newFakeRunner(), newFakeRecords(ownedRecord(shellMachine)))

	cmd, relay := p.shellCommand(context.Background(), shellMachine, provider.ShellOpts{
		Workdir:         "/w",
		Argv:            []string{"true"},
		ForwardSSHAgent: true,
	})
	if relay != nil {
		relay.close()
	}

	ssh := lastValueOf(cmd.Env, "SSH")
	if ssh == "" {
		// Deliberately not printing cmd.Env: it is the developer's real
		// environment, and a failing test is not a reason to put their
		// secrets in CI output.
		t.Fatal("nothing in the transport environment asks ssh to forward the agent (no SSH= entry)")
	}
	if !strings.Contains(ssh, "-A") {
		t.Errorf("SSH=%q does not request agent forwarding", ssh)
	}
	// Lima multiplexes over a persistent control socket, so a reused
	// connection ignores -A entirely. Without this the flag is a no-op on the
	// second and every later invocation.
	if !strings.Contains(ssh, "ControlPath=none") {
		t.Errorf("SSH=%q reuses Lima's control socket, so -A would be ignored", ssh)
	}
}

// The default must stay agentless (REQ-9.2): the agent is a credential, and it
// crosses only when the user asks for it that invocation.
func TestShellCommand_DoesNotForwardTheAgentByDefault_REQ_9_2(t *testing.T) {
	p := newTestProvider(t, newFakeRunner(), newFakeRecords(ownedRecord(shellMachine)))

	cmd, relay := p.shellCommand(context.Background(), shellMachine, provider.ShellOpts{
		Workdir: "/w",
		Argv:    []string{"true"},
	})
	if relay != nil {
		relay.close()
	}

	if ssh := lastValueOf(cmd.Env, "SSH"); strings.Contains(ssh, "-A") {
		t.Errorf("the agent was forwarded without being asked for: SSH=%q", ssh)
	}
}

// lastValueOf returns the value of the final NAME= entry in an environment,
// which is the one exec applies when a name appears more than once.
func lastValueOf(env []string, name string) string {
	value := ""
	for _, entry := range env {
		if got, rest, ok := strings.Cut(entry, "="); ok && got == name {
			value = rest
		}
	}
	return value
}
