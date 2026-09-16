package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/editor"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/types"
)

// Flow tests for the editor commands: the real command code against the
// in-process FakeProvider, with a stand-in for the editor's launcher on PATH.
//
// The stand-in is this test binary under the editor's name. When it is run
// with fakeEditorLogEnv set it records the arguments it was given and exits,
// before the testing package parses a single flag — so a launcher flag such as
// Zed's `--wsl` reaches it intact. That makes these tests about what avar
// actually executes, found by PATH lookup the way a user's shell would find it,
// rather than about a slice a function returns.
//
// What they cannot show is that Cursor or Zed accept those arguments. That
// needs the editors themselves; see the editor package's tests for the part of
// the contract that can be checked without them (the SSH host resolving through
// real OpenSSH) and the pull request for what remains to be checked by hand.

const fakeEditorLogEnv = "AVR_TEST_FAKE_EDITOR_LOG"

func TestMain(m *testing.M) {
	if log := os.Getenv(fakeEditorLogEnv); log != "" {
		if err := os.WriteFile(log, []byte(strings.Join(os.Args[1:], "\n")), 0o600); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// editorTest is one editor command's environment: a Fake, an App wired to it,
// a PATH holding only the stand-in launcher, and a home directory of its own
// so that the user's real ~/.ssh/config is neither read nor offered an edit.
type editorTest struct {
	*testApp
	f       *fake.Fake
	machine string
	log     string
}

func newEditorTest(t *testing.T, launchers ...string) *editorTest {
	t.Helper()
	f := fake.New()
	app := newTestApp(t, f)
	app.Stdin = strings.NewReader("") // a prompt, if one were shown, is declined

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// Creating an environment installs the idle-check agent unless its plist
	// is already in ~/Library/LaunchAgents. With HOME moved, an empty one here
	// is what keeps these tests from running launchctl against the real
	// session.
	agents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatalf("creating a stand-in LaunchAgents directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agents, launchdPlist), nil, 0o644); err != nil {
		t.Fatalf("creating a stand-in idle-check agent: %v", err)
	}

	bin := t.TempDir()
	for _, name := range launchers {
		installFakeLauncher(t, bin, name)
	}
	t.Setenv("PATH", bin)

	log := filepath.Join(t.TempDir(), "argv")
	t.Setenv(fakeEditorLogEnv, log)

	target, err := app.Resolve(editorInvocation("zed"))
	if err != nil {
		t.Fatalf("resolving the target environment: %v", err)
	}
	return &editorTest{testApp: app, f: f, machine: target.MachineName, log: log}
}

// installFakeLauncher puts this test binary on PATH under an editor's name.
func installFakeLauncher(t *testing.T, dir, name string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("finding the test binary: %v", err)
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dst := filepath.Join(dir, name)
	if err := os.Link(self, dst); err == nil {
		return
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatalf("opening the test binary: %v", err)
	}
	defer src.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatalf("creating the stand-in launcher: %v", err)
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		t.Fatalf("copying the stand-in launcher: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("writing the stand-in launcher: %v", err)
	}
}

func editorInvocation(name string, args ...string) cli.Invocation {
	return cli.Invocation{Mode: cli.ModeSubcommand, Subcommand: name, SubcommandArgs: args}
}

// run dispatches an editor command the way Execute does.
func (e *editorTest) run(t *testing.T, inv cli.Invocation) error {
	t.Helper()
	return dispatch(context.Background(), e.App, inv)
}

// launched returns the arguments the launcher on PATH was run with, and
// whether it was run at all.
func (e *editorTest) launched(t *testing.T) ([]string, bool) {
	t.Helper()
	data, err := os.ReadFile(e.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("reading what the launcher was run with: %v", err)
	}
	return strings.Split(string(data), "\n"), true
}

// guestCwd is the directory the Fake maps this project's working directory to.
func (e *editorTest) guestCwd(t *testing.T) string {
	t.Helper()
	target, err := e.Resolve(editorInvocation("zed"))
	if err != nil {
		t.Fatalf("resolving the target environment: %v", err)
	}
	_, cwd, err := e.f.MapProjectPath(target.Project.ID, target.Project.Path, target.HostCwd)
	if err != nil {
		t.Fatalf("mapping the project: %v", err)
	}
	return cwd
}

func (e *editorTest) sshConfig(t *testing.T) string {
	t.Helper()
	config, err := editor.ReadHostConfig(e.store.SSHDir())
	if err != nil {
		t.Fatalf("reading avar's SSH configuration: %v", err)
	}
	return config
}

// limaStanza is what the Lima backend's EditorTarget hands back: Lima 2.2.0's
// generated ssh.config with its Host line rewritten to the machine name.
func limaStanza(machine string) string {
	return "Host " + machine + `
  IdentityFile "/Users/dev/.lima/_config/user"
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  User dev
  Hostname 127.0.0.1
  Port 59003`
}

func (e *editorTest) seedSSHTarget(t *testing.T) {
	t.Helper()
	seedMachine(t, e.f, e.machine, ubuntu(), types.KindShared)
	e.f.SetEditorTarget(e.machine, provider.EditorTarget{
		Authority: "ssh-remote+" + e.machine,
		SSHConfig: limaStanza(e.machine),
	})
}

func (e *editorTest) seedWSLTarget(t *testing.T) {
	t.Helper()
	seedMachine(t, e.f, e.machine, ubuntu(), types.KindShared)
	e.f.SetEditorTarget(e.machine, provider.EditorTarget{Authority: "wsl+" + e.machine})
}

// On macOS `avr zed` opens the project through Zed's SSH remote development,
// naming the host by the alias in avar's SSH configuration — so the user, port
// and key live in that stanza and nowhere on a command line.
func TestZed_OpensTheProjectOverSSHByHostAlias_REQ_13_6(t *testing.T) {
	e := newEditorTest(t, "zed")
	e.seedSSHTarget(t)

	if err := e.run(t, editorInvocation("zed")); err != nil {
		t.Fatalf("avr zed: %v", err)
	}

	f := e.f
	f.AssertCalled(t, fake.OpEnsureMachine)
	if call := f.AssertCalled(t, fake.OpEditorTarget); call.Machine != e.machine {
		t.Errorf("asked for an editor target on %s, want %s", call.Machine, e.machine)
	}

	argv, ok := e.launched(t)
	if !ok {
		t.Fatal("`avr zed` did not run the zed launcher")
	}
	if len(argv) != 1 {
		t.Fatalf("zed was run with %q, want one ssh:// URL", argv)
	}
	u, err := url.Parse(argv[0])
	if err != nil {
		t.Fatalf("zed was given %q, which is not a URL: %v", argv[0], err)
	}
	if u.Scheme != "ssh" || u.Host != e.machine || u.User != nil || u.Port() != "" {
		t.Errorf("zed was given %q, want ssh://%s with no user or port, so ssh takes them from avar's stanza", argv[0], e.machine)
	}
	if want := e.guestCwd(t); u.Path != want {
		t.Errorf("zed was asked to open %q, want the project's guest path %q", u.Path, want)
	}

	// The alias only resolves if avar wrote the stanza it names.
	if config := e.sshConfig(t); !strings.Contains(config, "Host "+e.machine) {
		t.Errorf("avar's SSH configuration has no entry for %s:\n%s", e.machine, config)
	}
	// The Include is explained in Zed's name — as guidance when stdin is not a
	// terminal, as a question when it is — and, since the answer here is no,
	// not written into a file avar does not own.
	include := editor.IncludeLine(editor.ConfigPath(e.store.SSHDir()))
	if stderr := e.err.String(); !strings.Contains(stderr, "avr: Zed ") || !strings.Contains(stderr, include) {
		t.Errorf("`avr zed` did not explain, in Zed's name, the %q line it needs:\n%s", include, stderr)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".ssh", "config")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("`avr zed` touched the user's SSH configuration without consent (stat: %v)", err)
	}
	if !strings.Contains(e.stdout(), "in Zed on") {
		t.Errorf("`avr zed` did not say what it opened:\n%s", e.stdout())
	}
}

// On Windows `avr zed` opens the project through Zed's WSL connection to the
// selected distribution, and avar writes no SSH material for it.
func TestZed_OpensTheProjectInWSLWithoutSSH_REQ_13_6_REQ_18_10(t *testing.T) {
	e := newEditorTest(t, "zed")
	e.seedWSLTarget(t)

	if err := e.run(t, editorInvocation("zed")); err != nil {
		t.Fatalf("avr zed: %v", err)
	}

	argv, ok := e.launched(t)
	if !ok {
		t.Fatal("`avr zed` did not run the zed launcher")
	}
	if want := []string{"--wsl", e.machine, e.guestCwd(t)}; !slices.Equal(argv, want) {
		t.Errorf("zed was run with %q, want %q", argv, want)
	}
	if config := e.sshConfig(t); config != "" {
		t.Errorf("`avr zed` wrote SSH configuration for a WSL target:\n%s", config)
	}
	if strings.Contains(e.err.String(), "Include") {
		t.Errorf("`avr zed` raised the SSH Include for a WSL target:\n%s", e.err.String())
	}
}

// `avr cursor` hands Cursor exactly what `avr code` hands VS Code, on either
// kind of target, and prepares the same SSH configuration where there is one.
func TestCursor_OpensTheSameTargetAsCode_REQ_13_5(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seed    func(*editorTest, *testing.T)
		wantSSH bool
	}{
		{name: "ssh", seed: (*editorTest).seedSSHTarget, wantSSH: true},
		{name: "wsl", seed: (*editorTest).seedWSLTarget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launches := map[string][]string{}
			for _, command := range []string{"code", "cursor"} {
				e := newEditorTest(t, command)
				tc.seed(e, t)
				if err := e.run(t, editorInvocation(command)); err != nil {
					t.Fatalf("avr %s: %v", command, err)
				}
				argv, ok := e.launched(t)
				if !ok {
					t.Fatalf("`avr %s` did not run its launcher", command)
				}
				if len(argv) != 3 {
					t.Fatalf("`avr %s` ran its launcher with %q, want --remote <authority> <path>", command, argv)
				}
				launches[command] = argv

				if got := e.sshConfig(t) != ""; got != tc.wantSSH {
					t.Errorf("`avr %s` wrote SSH configuration: %t, want %t", command, got, tc.wantSSH)
				}
			}

			authority := launches["code"][1]
			if want := []string{"--remote", authority, launches["code"][2]}; !slices.Equal(launches["cursor"], want) {
				t.Errorf("cursor was run with %q, want what code was run with, %q", launches["cursor"], launches["code"])
			}
			if !strings.HasPrefix(authority, tc.name) {
				t.Errorf("the authority %q is not the %s target that was programmed", authority, tc.name)
			}
		})
	}
}

// A missing launcher is reported before any environment is started or
// provisioned, naming the command and how to install it.
func TestEditorCommands_MissingLauncherFailsBeforeStartingAnything_REQ_13_7(t *testing.T) {
	for _, command := range []string{"code", "cursor", "zed"} {
		t.Run(command, func(t *testing.T) {
			e := newEditorTest(t) // no launchers on PATH
			seedMachine(t, e.f, e.machine, ubuntu(), types.KindShared)
			e.f.SetMachineState(e.machine, types.StateStopped)
			e.f.Reset()

			err := e.run(t, editorInvocation(command))
			if err == nil {
				t.Fatalf("`avr %s` succeeded with no `%s` on PATH", command, command)
			}
			if !strings.Contains(err.Error(), "`"+command+"` command was not found on your PATH") {
				t.Errorf("the error does not name the missing command: %v", err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "install") {
				t.Errorf("the error does not say how to install the command: %v", err)
			}
			if ops := e.f.Ops(); len(ops) != 0 {
				t.Errorf("`avr %s` reached the backend before finding its launcher missing: %v", command, ops)
			}
			e.f.AssertMachineState(t, e.machine, types.StateStopped)
		})
	}
}

// An editor that cannot connect to the kind of environment the backend
// describes says so, and leaves nothing behind: no launcher run, no SSH
// configuration written, no Include line proposed.
func TestZed_RefusesATargetItCannotReach_REQ_13_8(t *testing.T) {
	e := newEditorTest(t, "zed")
	seedMachine(t, e.f, e.machine, ubuntu(), types.KindShared)
	// A backend reached some other way, which happens still to carry SSH
	// material: refusing must come before any of it is written.
	e.f.SetEditorTarget(e.machine, provider.EditorTarget{
		Authority: "orbstack+" + e.machine,
		SSHConfig: limaStanza(e.machine),
	})

	err := e.run(t, editorInvocation("zed"))
	if !errors.Is(err, editor.ErrUnsupportedTarget) {
		t.Fatalf("`avr zed` on an unreachable target: got %v, want ErrUnsupportedTarget", err)
	}
	for _, want := range []string{"Zed cannot open", "avr code"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), e.machine) {
		t.Errorf("the refusal shows the user a machine name (REQ-1.5): %v", err)
	}
	if argv, ran := e.launched(t); ran {
		t.Errorf("zed was run with %q for a target it cannot reach", argv)
	}
	if config := e.sshConfig(t); config != "" {
		t.Errorf("SSH configuration was written for a refused editor:\n%s", config)
	}
	if strings.Contains(e.err.String(), "Include") {
		t.Errorf("an Include line was proposed for a refused editor:\n%s", e.err.String())
	}
}

// Selector flags pick the environment the editor opens, as they do for every
// other command.
func TestEditorCommands_RespectSelectorFlags_REQ_13_9(t *testing.T) {
	for _, command := range []string{"cursor", "zed"} {
		t.Run(command, func(t *testing.T) {
			e := newEditorTest(t, command)
			inv, err := cli.Parse([]string{"--distro", "fedora", command})
			if err != nil {
				t.Fatalf("parsing: %v", err)
			}
			fedora, err := e.Resolve(inv)
			if err != nil {
				t.Fatalf("resolving --distro fedora: %v", err)
			}
			if fedora.MachineName == e.machine {
				t.Fatalf("--distro fedora resolved to the default machine %s; the test proves nothing", e.machine)
			}
			// A target both editors can open, so what is asserted is only which
			// environment was chosen.
			e.f.SetEditorTarget(fedora.MachineName, provider.EditorTarget{Authority: "wsl+" + fedora.MachineName})

			if err := e.run(t, inv); err != nil {
				t.Fatalf("avr --distro fedora %s: %v", command, err)
			}
			if call := e.f.AssertCalled(t, fake.OpEditorTarget); call.Machine != fedora.MachineName {
				t.Errorf("`avr --distro fedora %s` opened %s, want %s", command, call.Machine, fedora.MachineName)
			}
		})
	}
}

func TestEditorCommands_RejectArguments_REQ_13_9(t *testing.T) {
	for _, command := range []string{"cursor", "zed"} {
		e := newEditorTest(t, command)
		err := e.run(t, editorInvocation(command, "main.go"))
		var exit *ExitCodeError
		if !errors.As(err, &exit) || exit.Code != exitUsage {
			t.Errorf("`avr %s main.go`: got %v, want a usage error", command, err)
		}
		if ops := e.f.Ops(); len(ops) != 0 {
			t.Errorf("`avr %s main.go` reached the backend: %v", command, ops)
		}
	}
}

// The help for each editor command names `avr -- <name>` as the way to run a
// program of the same name in Linux.
func TestEditorCommandHelp_NamesTheEscapeHatch_REQ_13_9(t *testing.T) {
	for _, command := range []string{"cursor", "zed"} {
		var out bytes.Buffer
		if err := writeCommandHelp(&out, command); err != nil {
			t.Fatalf("help for %s: %v", command, err)
		}
		if !strings.Contains(out.String(), "avr -- "+command) {
			t.Errorf("`avr help %s` does not mention `avr -- %s`:\n%s", command, command, out.String())
		}
	}
}
