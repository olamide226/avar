package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/state"
	"github.com/olamide226/avar/internal/types"
)

// These are flow tests for the crash recovery App.Provider performs: they
// drive the real lazy provider path against the in-process Fake and a
// temporary state directory, then assert on what reconciliation did to the
// records, what it asked of the backend, and what the user was told (design
// §7). The decision-table coverage of Reconcile itself lives in
// internal/state; what is proven here is the wiring — that recovery runs,
// runs once, before the command acts, and never eats the command.

// orphanMachine is a healthy machine the backend has but avar has no record
// of: the fingerprint of a run killed between creating it and recording it.
const orphanMachine = "avr-ubuntu-24.04-arm64"

// newReconcileTestApp wires an App to a fake backend and a temporary state
// directory like newTestApp does, but leaves the provider sync.Once to run:
// the Fake is injected as the backend constructor so that the crash recovery
// App.Provider performs is exercised exactly as in production. (newTestApp
// seeds prov directly and marks the Once done, which skips construction and
// recovery alike — the right shape for tests of what happens after startup.)
func newReconcileTestApp(t *testing.T, f *fake.Fake) *testApp {
	t.Helper()

	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening a temporary state directory: %v", err)
	}

	app := &App{Version: "test", Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	app.store = store
	// The store is ready-made; only the backend is built lazily.
	app.once.store.Do(func() {})
	app.buildBackend = func(context.Context) (provider.Provider, error) { return f, nil }

	return &testApp{App: app, out: app.Out.(*bytes.Buffer), err: app.Err.(*bytes.Buffer), store: store}
}

// mustGetProvider drives the path under test: the first Provider call builds
// the backend and reconciles avar's records against it.
func mustGetProvider(t *testing.T, app *testApp) {
	t.Helper()
	if _, err := app.Provider(context.Background()); err != nil {
		t.Fatalf("building the backend: %v", err)
	}
}

// recordOf looks up avar's own record of a machine.
func recordOf(t *testing.T, store *state.Store, name string) (types.MachineRecord, bool) {
	t.Helper()
	rec, found, err := store.Machine(name)
	if err != nil {
		t.Fatalf("reading the record of %s: %v", name, err)
	}
	return rec, found
}

// seedRecord writes a machine record directly, stamped in the past so
// reconciliation cannot mistake it for work another invocation has in
// flight.
func seedRecord(t *testing.T, store *state.Store, rec types.MachineRecord) {
	t.Helper()
	rec.CreatedAt = time.Now().Add(-time.Hour).UTC()
	if err := store.PutMachine(rec); err != nil {
		t.Fatalf("recording %s: %v", rec.Name, err)
	}
}

func TestReconcile_AdoptsAnOrphanOnStartup_REQ_17_5(t *testing.T) {
	f := fake.New()
	seedMachine(t, f, orphanMachine, ubuntu(), types.KindShared)
	app := newReconcileTestApp(t, f)

	mustGetProvider(t, app)

	if _, found := recordOf(t, app.store, orphanMachine); !found {
		t.Error("the orphaned machine was not adopted into avar's records")
	}
	// One listing is the whole cost of recovery here: nothing was deleted
	// and nothing else was probed.
	f.AssertOps(t, fake.OpStatus)
	// Adoption restores the state the user already thought they had, so it
	// prints nothing.
	if app.err.Len() != 0 {
		t.Errorf("adopting a machine printed to stderr; adoption is meant to be silent: %q", app.err.String())
	}
	if app.out.Len() != 0 {
		t.Errorf("reconciliation wrote to stdout, which belongs to the user's command: %q", app.out.String())
	}
}

func TestReconcile_DropsADanglingRecord_REQ_17_5(t *testing.T) {
	f := fake.New()
	app := newReconcileTestApp(t, f)
	// The record of a machine the backend no longer has: it was deleted by
	// hand with limactl, behind avar's back.
	seedRecord(t, app.store, types.MachineRecord{
		Name:     orphanMachine,
		Provider: fake.ProviderID,
		Selector: ubuntu(),
		Kind:     types.KindShared,
	})

	mustGetProvider(t, app)

	if _, found := recordOf(t, app.store, orphanMachine); found {
		t.Error("the record of a machine the backend no longer has survived reconciliation")
	}
	f.AssertOps(t, fake.OpStatus)
	// The machine is already gone, so dropping its record changes nothing
	// the user can see — and prints nothing.
	if app.err.Len() != 0 {
		t.Errorf("dropping a dangling record printed to stderr: %q", app.err.String())
	}
}

// The consistent case is the warm path (REQ-17.1): one listing, no writes —
// not even a rewrite of identical bytes.
func TestReconcile_ConsistentStoreWritesNothing_REQ_17_1(t *testing.T) {
	t.Run("records agree with the backend", func(t *testing.T) {
		f := fake.New()
		seedMachine(t, f, orphanMachine, ubuntu(), types.KindShared)
		app := newReconcileTestApp(t, f)
		seedRecord(t, app.store, types.MachineRecord{
			Name:     orphanMachine,
			Provider: fake.ProviderID,
			Selector: ubuntu(),
			Kind:     types.KindShared,
		})

		machinesJSON := filepath.Join(app.store.Root(), "machines.json")
		info, err := os.Stat(machinesJSON)
		if err != nil {
			t.Fatalf("stat machines.json before reconciliation: %v", err)
		}
		before, err := os.ReadFile(machinesJSON)
		if err != nil {
			t.Fatalf("reading machines.json before reconciliation: %v", err)
		}

		mustGetProvider(t, app)

		afterInfo, err := os.Stat(machinesJSON)
		if err != nil {
			t.Fatalf("stat machines.json after reconciliation: %v", err)
		}
		after, err := os.ReadFile(machinesJSON)
		if err != nil {
			t.Fatalf("reading machines.json after reconciliation: %v", err)
		}
		if !afterInfo.ModTime().Equal(info.ModTime()) {
			t.Errorf("a consistent store was rewritten (mtime %v -> %v)", info.ModTime(), afterInfo.ModTime())
		}
		if !bytes.Equal(before, after) {
			t.Errorf("a consistent store's contents changed:\nbefore: %s\nafter:  %s", before, after)
		}
		if app.err.Len() != 0 || app.out.Len() != 0 {
			t.Errorf("the consistent case printed something (stderr %q, stdout %q)", app.err.String(), app.out.String())
		}
		f.AssertOps(t, fake.OpStatus)
	})

	t.Run("nothing on either side", func(t *testing.T) {
		f := fake.New()
		app := newReconcileTestApp(t, f)

		mustGetProvider(t, app)

		// With nothing to record, recovery must not even create the
		// registry file: no writes means no writes.
		if _, err := os.Stat(filepath.Join(app.store.Root(), "machines.json")); !os.IsNotExist(err) {
			t.Errorf("machines.json exists after reconciling an empty world (stat error: %v)", err)
		}
		f.AssertOps(t, fake.OpStatus)
	})
}

// Machines without avar's prefix are the user's own: recovery must not touch
// them or even mention them (PROP-6).
func TestReconcile_NeverTouchesAnUnownedMachine_PROP_6(t *testing.T) {
	f := fake.New()
	f.AddForeignMachine("default")
	f.AddForeignMachine("docker-desktop")
	app := newReconcileTestApp(t, f)

	mustGetProvider(t, app)

	f.AssertNotCalled(t, fake.OpDelete)
	f.AssertMachineState(t, "default", types.StateRunning)
	f.AssertMachineState(t, "docker-desktop", types.StateRunning)
	if out := app.out.String() + app.err.String(); strings.Contains(out, "default") || strings.Contains(out, "docker") {
		t.Errorf("a machine avar does not own was mentioned in avar's output: %q", out)
	}
}

// A broken machine with no record is the other half of a killed run: the
// backend says it cannot work, so recovery removes it — and says so, because
// destroying an environment is never silent (design §6).
func TestReconcile_DeletedBrokenOrphanIsReported_REQ_17_5(t *testing.T) {
	f := fake.New()
	seedMachine(t, f, orphanMachine, ubuntu(), types.KindShared)
	f.SetMachineState(orphanMachine, types.StateBroken)
	app := newReconcileTestApp(t, f)

	mustGetProvider(t, app)

	f.AssertOps(t, fake.OpStatus, fake.OpDelete)
	// Gone from the backend (the Fake reports a missing machine as
	// StateUnknown) and never recorded: the next run provisions cleanly.
	f.AssertMachineState(t, orphanMachine, types.StateUnknown)
	if _, found := recordOf(t, app.store, orphanMachine); found {
		t.Error("a machine deleted as unusable was recorded")
	}
	if out := app.err.String(); !strings.Contains(out, "removed") {
		t.Errorf("destroying an environment was not reported: stderr %q", out)
	}
	if app.out.Len() != 0 {
		t.Errorf("the report went to stdout, which belongs to the user's command: %q", app.out.String())
	}
}

// An isolated machine avar cannot place — which project does it serve? — is
// left alone rather than adopted on a guess or destroyed while healthy, and
// named so it never sits invisible.
func TestReconcile_UnplaceableOrphanIsLeftAloneAndReported_REQ_17_5(t *testing.T) {
	const isolatedOrphan = "avr-ubuntu-24.04-arm64-9f8e7d6c"
	f := fake.New()
	seedMachine(t, f, isolatedOrphan, ubuntu(), types.KindIsolated)
	app := newReconcileTestApp(t, f)

	mustGetProvider(t, app)

	// Not adopted (avar cannot know the project), not deleted (healthy).
	f.AssertOps(t, fake.OpStatus)
	f.AssertMachineState(t, isolatedOrphan, types.StateRunning)
	if _, found := recordOf(t, app.store, isolatedOrphan); found {
		t.Error("an isolated orphan was recorded on a guess about its project")
	}
	if out := app.err.String(); !strings.Contains(out, "left alone") {
		t.Errorf("the machine avar could not place was not mentioned: stderr %q", out)
	}
}

// Crash recovery failing must not take the user's command with it: the
// warning is printed and the command runs. A repair mechanism that bricks
// the tool when it cannot repair is worse than the damage it exists to fix.
func TestReconcile_FailureDoesNotLoseTheCommand_REQ_17_5(t *testing.T) {
	f := fake.New()
	// Only recovery's listing fails; the command's own backend calls must
	// succeed, or the test could not tell "recovery was skipped" apart from
	// "the command was lost".
	f.FailNextOn(fake.OpStatus, errors.New("backend listing exploded"))
	app := newReconcileTestApp(t, f)

	// The user's actual command. Any handler would do — they all reach the
	// backend through App.Provider, which is where recovery runs.
	err := runStatus(context.Background(), app.App, cli.Invocation{
		Mode: cli.ModeSubcommand, Subcommand: "status",
	})
	if err != nil {
		t.Fatalf("reconciliation failing lost the user's command: %v", err)
	}

	// Recovery was attempted first and failed; the command's own listing
	// followed and succeeded.
	calls := f.CallsFor(fake.OpStatus)
	if len(calls) != 2 || calls[0].Err == nil || calls[1].Err != nil {
		t.Fatalf("expected recovery's failed listing, then the command's successful one; calls:\n%s", f.Transcript())
	}
	// The command ran to completion: the empty-state onboarding is its
	// output, on stdout.
	if out := app.out.String(); !strings.Contains(out, "not managing any Linux environments") {
		t.Errorf("the command's own output is missing from stdout: %q", out)
	}
	if out := app.err.String(); !strings.Contains(out, "crash recovery did not finish") {
		t.Errorf("the failed recovery printed no warning: stderr %q", out)
	}
}

// However many callers ask for the backend in one invocation, recovery runs
// once: the warm path pays for one listing, not one per caller (REQ-17.1).
func TestReconcile_RunsOncePerInvocation_REQ_17_1(t *testing.T) {
	f := fake.New()
	app := newReconcileTestApp(t, f)

	mustGetProvider(t, app)
	mustGetProvider(t, app)

	f.AssertCallCount(t, fake.OpStatus, 1)
}
