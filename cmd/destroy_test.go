package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/types"
)

// destroyInvocation is `avr destroy [flags]` as the grammar hands it over.
func destroyInvocation(args ...string) cli.Invocation {
	return cli.Invocation{Mode: cli.ModeSubcommand, Subcommand: "destroy", SubcommandArgs: args}
}

func TestDestroy_RemovesTheCurrentEnvironment_REQ_5_6(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	app.Stdin = strings.NewReader("")

	target, label := resolvedTarget(t, app)
	seedMachine(t, f, target, ubuntu(), types.KindShared, "/Users/ola/code/app")

	if err := runDestroy(context.Background(), app.App, destroyInvocation("--yes")); err != nil {
		t.Fatalf("avr destroy --yes: %v", err)
	}

	f.AssertOps(t, fake.OpStatus, fake.OpDelete)
	if del := f.AssertCalled(t, fake.OpDelete); del.Machine != target {
		t.Errorf("destroyed %s, want the resolved target %s", del.Machine, target)
	}

	out := app.stdout()
	if !strings.Contains(out, label) {
		t.Errorf("`avr destroy` did not name the environment:\n%s", out)
	}
	// REQ-1.5: the user chose an environment, not a machine, and should never
	// be shown the generated name.
	if strings.Contains(out, target) {
		t.Errorf("`avr destroy` showed the machine name %s (REQ-1.5):\n%s", target, out)
	}
	// The whole point of the mount model is that destroying a machine cannot
	// lose work, and the user should be told so at the moment it matters.
	if !strings.Contains(out, "not touched") && !strings.Contains(out, "not affected") {
		t.Errorf("`avr destroy` did not say host files are safe:\n%s", out)
	}
}

// Destroying an environment that was never created is not an error, and must
// not create one on the way to removing it.
func TestDestroy_NothingToDestroyIsNotAnError_REQ_5_6(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	app.Stdin = strings.NewReader("")

	if err := runDestroy(context.Background(), app.App, destroyInvocation()); err != nil {
		t.Fatalf("destroying a non-existent environment failed: %v", err)
	}
	if !strings.Contains(app.stdout(), "nothing to destroy") {
		t.Errorf("`avr destroy` did not explain there is no such environment:\n%s", app.stdout())
	}
	f.AssertOps(t, fake.OpStatus)
}

// Without --yes the user types the environment's name, exactly as reset asks.
// Anything else leaves the machine alone.
func TestDestroy_WrongConfirmationChangesNothing_REQ_5_6(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	target, _ := resolvedTarget(t, app)
	seedMachine(t, f, target, ubuntu(), types.KindShared)

	app.Stdin = strings.NewReader("no\n")

	if err := runDestroy(context.Background(), app.App, destroyInvocation()); err != nil {
		t.Fatalf("avr destroy cancelled: %v", err)
	}

	f.AssertOps(t, fake.OpStatus)
	f.AssertMachineState(t, target, types.StateRunning)
	if !strings.Contains(app.stdout(), "Nothing was destroyed") {
		t.Errorf("`avr destroy` did not say it was cancelled:\n%s", app.stdout())
	}
}

func TestDestroy_CorrectConfirmationProceeds_REQ_5_6(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	target, label := resolvedTarget(t, app)
	seedMachine(t, f, target, ubuntu(), types.KindShared)

	app.Stdin = strings.NewReader(label + "\n")

	if err := runDestroy(context.Background(), app.App, destroyInvocation()); err != nil {
		t.Fatalf("avr destroy: %v", err)
	}
	f.AssertOps(t, fake.OpStatus, fake.OpDelete)
}

func TestDestroy_AllRemovesEveryEnvironment_REQ_5_7(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	app.Stdin = strings.NewReader("")

	shared, _ := resolvedTarget(t, app)
	seedMachine(t, f, shared, ubuntu(), types.KindShared, "/Users/ola/code/a")
	const other = "avr-debian-13-arm64"
	seedMachine(t, f, other,
		types.EnvironmentSelector{Distro: types.DistroDebian, Version: "13", Arch: types.HostArch()},
		types.KindShared, "/Users/ola/code/b")

	if err := runDestroy(context.Background(), app.App, destroyInvocation("--all", "--yes")); err != nil {
		t.Fatalf("avr destroy --all --yes: %v", err)
	}

	f.AssertCallCount(t, fake.OpDelete, 2)
	for _, name := range []string{shared, other} {
		var found bool
		for _, c := range f.Calls() {
			if c.Op == fake.OpDelete && c.Machine == name {
				found = true
			}
		}
		if !found {
			t.Errorf("%s survived `avr destroy --all`", name)
		}
	}
}

// --all requires the word "all": a single keystroke should not be able to
// remove every environment a user has.
func TestDestroy_AllNeedsAnExplicitWord_REQ_5_7(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	target, label := resolvedTarget(t, app)
	seedMachine(t, f, target, ubuntu(), types.KindShared)

	// The environment's own name is not enough for --all.
	app.Stdin = strings.NewReader(label + "\n")

	if err := runDestroy(context.Background(), app.App, destroyInvocation("--all")); err != nil {
		t.Fatalf("avr destroy --all: %v", err)
	}
	f.AssertOps(t, fake.OpStatus)
	f.AssertMachineState(t, target, types.StateRunning)
}

// The case the flag exists for: an isolated environment whose project directory
// has been deleted cannot be reached by `avr isolate off`, which must be run
// from inside that directory (REQ-5.8).
func TestDestroy_OrphanedRemovesOnlyEnvironmentsWhoseProjectIsGone_REQ_5_8(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	app.Stdin = strings.NewReader("")

	living := filepath.Join(t.TempDir(), "still-here")
	deleted := filepath.Join(t.TempDir(), "deleted")
	for _, dir := range []string{living, deleted} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	orphan := seedIsolated(t, app, f, deleted)
	kept := seedIsolated(t, app, f, living)

	// The project directory goes away after the environment exists, which is
	// the sequence that strands it: avar registered it while it was there.
	if err := os.RemoveAll(deleted); err != nil {
		t.Fatal(err)
	}

	// A shared machine can never be orphaned: no single directory
	// disappearing makes it unwanted.
	shared, _ := resolvedTarget(t, app)
	seedMachine(t, f, shared, ubuntu(), types.KindShared, living)

	if err := runDestroy(context.Background(), app.App, destroyInvocation("--orphaned", "--yes")); err != nil {
		t.Fatalf("avr destroy --orphaned --yes: %v", err)
	}

	f.AssertCallCount(t, fake.OpDelete, 1)
	if del := f.AssertCalled(t, fake.OpDelete); del.Machine != orphan {
		t.Errorf("destroyed %s, want only the orphan %s", del.Machine, orphan)
	}
	f.AssertMachineState(t, kept, types.StateRunning)
	f.AssertMachineState(t, shared, types.StateRunning)
}

func TestDestroy_OrphanedWithNoneSaysSo_REQ_5_8(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	app.Stdin = strings.NewReader("")

	if err := runDestroy(context.Background(), app.App, destroyInvocation("--orphaned")); err != nil {
		t.Fatalf("avr destroy --orphaned: %v", err)
	}
	if !strings.Contains(app.stdout(), "No environments are orphaned") {
		t.Errorf("did not report an empty orphan set:\n%s", app.stdout())
	}
	f.AssertOps(t, fake.OpStatus)
}

func TestDestroy_RejectsContradictoryScopes(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)

	err := runDestroy(context.Background(), app.App, destroyInvocation("--all", "--orphaned"))
	assertExitCode(t, err, exitUsage)
	f.AssertOps(t)
}

func TestDestroy_RejectsUnknownArguments(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)

	err := runDestroy(context.Background(), app.App, destroyInvocation("--force"))
	assertExitCode(t, err, exitUsage)
	if !strings.Contains(err.Error(), destroyYesFlag) {
		t.Errorf("the usage error does not name the flags destroy does take: %v", err)
	}
	f.AssertOps(t)
}

func TestParseDestroyArgs(t *testing.T) {
	for name, tc := range map[string]struct {
		args      []string
		wantScope destroyScope
		wantYes   bool
		wantErr   bool
	}{
		"no arguments":      {args: nil, wantScope: scopeCurrent},
		"--yes":             {args: []string{"--yes"}, wantScope: scopeCurrent, wantYes: true},
		"--all":             {args: []string{"--all"}, wantScope: scopeAll},
		"--orphaned":        {args: []string{"--orphaned"}, wantScope: scopeOrphaned},
		"--all --yes":       {args: []string{"--all", "--yes"}, wantScope: scopeAll, wantYes: true},
		"repeated --all":    {args: []string{"--all", "--all"}, wantScope: scopeAll},
		"both scopes":       {args: []string{"--all", "--orphaned"}, wantErr: true},
		"unknown flag":      {args: []string{"--force"}, wantErr: true},
		"positional name  ": {args: []string{"ubuntu"}, wantErr: true},
		"short y rejected":  {args: []string{"-y"}, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseDestroyArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseDestroyArgs(%v) succeeded, want a usage error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseDestroyArgs(%v): %v", tc.args, err)
			}
			if got.scope != tc.wantScope || got.yes != tc.wantYes {
				t.Errorf("parseDestroyArgs(%v) = %+v, want scope %v yes %t", tc.args, got, tc.wantScope, tc.wantYes)
			}
		})
	}
}

// seedIsolated registers a project and gives it an isolated machine, in both
// the fake backend and avar's records.
//
// Orphan detection reads the store, not the backend: which project an isolated
// machine serves is avar's own bookkeeping and nothing a backend can be asked
// (see the MachineStatus doc comment). A test that seeded only the fake would
// therefore find no orphans and pass for the wrong reason.
func seedIsolated(t *testing.T, app *testApp, f *fake.Fake, projectDir string) string {
	t.Helper()

	project, err := app.store.EnsureProject(projectDir)
	if err != nil {
		t.Fatalf("registering the project %s: %v", projectDir, err)
	}

	name := "avr-prj-" + project.ID[:10] + "-ubuntu-24.04-" + string(types.HostArch())
	seedMachine(t, f, name, ubuntu(), types.KindIsolated, projectDir)

	if err := app.store.PutMachine(types.MachineRecord{
		Name:      name,
		Provider:  types.ProviderLima,
		Selector:  ubuntu(),
		Kind:      types.KindIsolated,
		ProjectID: project.ID,
		Mounts:    []types.MountSpec{{ProjectID: project.ID, HostPath: projectDir, GuestPath: projectDir, Writable: true}},
	}); err != nil {
		t.Fatalf("recording the isolated machine %s: %v", name, err)
	}
	return name
}
