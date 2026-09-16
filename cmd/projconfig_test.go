package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/projconfig"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/resolve"
	"github.com/olamide226/avar/internal/types"
)

// Flow tests for a project's .avr.toml: the real command code, run from inside
// a real project directory, against the in-process FakeProvider.

// projectTest is a command run from its own project directory, with a Fake and
// an App wired to it.
type projectTest struct {
	*testApp
	f   *fake.Fake
	dir string
}

func newProjectTest(t *testing.T, avrToml string) *projectTest {
	t.Helper()
	f := fake.New()
	// newTestApp already keeps environment creation off the host's scheduler.
	app := newTestApp(t, f)
	app.Stdin = strings.NewReader("")

	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if avrToml != "" {
		if err := os.WriteFile(filepath.Join(dir, projconfig.FileName), []byte(avrToml), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	enterDir(t, dir)
	return &projectTest{testApp: app, f: f, dir: dir}
}

// enterDir makes dir the working directory for the rest of the test. The
// command layer resolves the project from the working directory, as it does for
// a user, so these tests stand where a user would.
func enterDir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(prev); err != nil {
			t.Errorf("restoring the working directory: %v", err)
		}
	})
}

func guestInvocation(argv ...string) cli.Invocation {
	return cli.Invocation{Mode: cli.ModeGuestCommand, Guest: argv}
}

// REQ-15.1: the environment a bare `avr` creates and enters is the one the
// project's file selects.
func TestShell_ProjectConfigSelectsTheEnvironment_REQ_15_1(t *testing.T) {
	pt := newProjectTest(t, "distro = \"fedora\"\n")

	if err := runGuest(context.Background(), pt.App, guestInvocation("true")); err != nil {
		t.Fatalf("avr true: %v", err)
	}

	ensure := pt.f.AssertCalled(t, fake.OpEnsureMachine)
	want := types.EnvironmentSelector{Distro: types.DistroFedora, Version: "43", Arch: types.HostArch()}
	if ensure.Spec.Selector != want {
		t.Errorf("created %s, want the file's %s", ensure.Spec.Selector.Label(), want.Label())
	}
	shell := pt.f.AssertCalled(t, fake.OpShell)
	if shell.Machine != ensure.Spec.Name {
		t.Errorf("attached to %s, want the environment the file selected, %s", shell.Machine, ensure.Spec.Name)
	}
}

// Every command that resolves an environment honours the file, so `avr stop`
// stops the environment `avr` started rather than the default one.
func TestStop_ProjectConfigSelectsTheEnvironment_REQ_15_1(t *testing.T) {
	pt := newProjectTest(t, "distro = \"debian\"\n")
	target, err := pt.Resolve(stopInvocation())
	if err != nil {
		t.Fatal(err)
	}
	if target.Selector.Distro != types.DistroDebian {
		t.Fatalf("resolved %s, want the file's Debian", target.Selector.Label())
	}
	seedMachine(t, pt.f, target.MachineName, target.Selector, types.KindShared)
	seedMachine(t, pt.f, "avr-ubuntu-24.04-"+string(types.HostArch()), ubuntu(), types.KindShared)

	if err := runStop(context.Background(), pt.App, stopInvocation()); err != nil {
		t.Fatalf("avr stop: %v", err)
	}
	if stop := pt.f.AssertCalled(t, fake.OpStop); stop.Machine != target.MachineName {
		t.Errorf("stopped %s, want the file's environment %s", stop.Machine, target.MachineName)
	}
}

// A flag still wins over the file (design §3.11).
func TestShell_FlagsBeatProjectConfig_REQ_15_1(t *testing.T) {
	pt := newProjectTest(t, "distro = \"fedora\"\n")

	inv := guestInvocation("true")
	inv.Selector.Distro = types.DistroUbuntu
	if err := runGuest(context.Background(), pt.App, inv); err != nil {
		t.Fatalf("avr --distro ubuntu true: %v", err)
	}
	if got := pt.f.AssertCalled(t, fake.OpEnsureMachine).Spec.Selector.Distro; got != types.DistroUbuntu {
		t.Errorf("created %s, want the flag's ubuntu", got)
	}
}

// A file avar cannot read exactly stops the invocation before any machine work,
// and says where and why.
func TestShell_MalformedProjectConfigFailsBeforeMachineWork_REQ_15_1(t *testing.T) {
	pt := newProjectTest(t, "distro = \"fedora\"\npackges = [\"jq\"]\n")

	err := runGuest(context.Background(), pt.App, guestInvocation("true"))
	if err == nil {
		t.Fatal("avr ran with a .avr.toml it cannot read")
	}
	for _, want := range []string{filepath.Join(pt.dir, projconfig.FileName), "line 2", `unknown key "packges"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
	if calls := pt.f.Calls(); len(calls) != 0 {
		t.Errorf("provider operations ran before the file was refused: %v", calls)
	}
}

// An unsupported value in the file fails exactly as the flag does — as a usage
// error — and names the file, because the user never typed the value.
func TestShell_UnsupportedProjectConfigIsAUsageError_REQ_15_1(t *testing.T) {
	pt := newProjectTest(t, "distro = \"fedora:12\"\n")

	err := runGuest(context.Background(), pt.App, guestInvocation("true"))
	if !errors.Is(err, resolve.ErrUnsupportedEnvironment) {
		t.Fatalf("avr = %v, want an unsupported-environment error", err)
	}
	if !strings.Contains(err.Error(), projconfig.FileName) {
		t.Errorf("error does not name the file: %v", err)
	}
	if calls := pt.f.Calls(); len(calls) != 0 {
		t.Errorf("provider operations ran: %v", calls)
	}
}

// PROP-24 through the command layer: in a project with no file, `avr` performs
// the same provider operations a project without project configuration always
// has, and says nothing about configuration.
func TestShell_NoProjectConfigChangesNothing_PROP_24(t *testing.T) {
	pt := newProjectTest(t, "")

	if err := runGuest(context.Background(), pt.App, guestInvocation("true")); err != nil {
		t.Fatalf("avr true: %v", err)
	}
	pt.f.AssertOps(t, fake.OpEnsureMachine, fake.OpAppliedMounts, fake.OpShell)
	if got := pt.f.AssertCalled(t, fake.OpEnsureMachine).Spec.Selector; got != ubuntu() {
		t.Errorf("created %s, want the default %s", got.Label(), ubuntu().Label())
	}
	if strings.Contains(pt.err.String(), projconfig.FileName) || strings.Contains(pt.stdout(), projconfig.FileName) {
		t.Errorf("avr mentioned project configuration in a project without any:\nstdout: %s\nstderr: %s", pt.stdout(), pt.err.String())
	}
}
