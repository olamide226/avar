package fake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

const (
	ubuntuMachine = "avr-ubuntu-24.04-arm64"
	amd64Machine  = "avr-ubuntu-24.04-amd64"
)

func ubuntuSelector() types.EnvironmentSelector {
	return types.EnvironmentSelector{
		Distro:  types.DistroUbuntu,
		Version: "24.04",
		Arch:    types.HostArch(),
	}
}

// shares builds the mount set a caller would have obtained from
// Fake.MapProjectPath for these project directories.
func shares(t *testing.T, f *Fake, dirs ...string) []types.MountSpec {
	t.Helper()
	out := make([]types.MountSpec, 0, len(dirs))
	for _, dir := range dirs {
		mount, _, err := f.MapProjectPath("id-"+strings.TrimPrefix(strings.ReplaceAll(dir, "/", "-"), "-"), dir, dir)
		if err != nil {
			t.Fatalf("MapProjectPath(%q): %v", dir, err)
		}
		out = append(out, mount)
	}
	return out
}

func ubuntuSpec() provider.MachineSpec {
	return provider.MachineSpec{
		Name:     ubuntuMachine,
		Selector: ubuntuSelector(),
		Kind:     types.KindShared,
	}
}

// recordingTB captures what an assertion helper reported instead of failing the
// surrounding test, so the helpers themselves can be tested.
type recordingTB struct {
	helpers  int
	failures []string
}

func (r *recordingTB) Helper() { r.helpers++ }

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *recordingTB) failed() bool { return len(r.failures) > 0 }

// TestFake_RecordsOrderedCallSequenceWithArguments_REQ_17_3 is the property the
// whole test double exists for: a flow test can assert what the command layer
// asked the backend to do, in order, with which arguments, without a VM.
func TestFake_RecordsOrderedCallSequenceWithArguments_REQ_17_3(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := New()

	if err := f.EnsureMachine(ctx, ubuntuSpec(), types.DiscardProgress); err != nil {
		t.Fatalf("EnsureMachine: %v", err)
	}
	if _, err := f.AppliedMounts(ctx, ubuntuMachine); err != nil {
		t.Fatalf("AppliedMounts: %v", err)
	}
	if err := f.SetMounts(ctx, ubuntuMachine, shares(t, f, "/Users/dev/code/api"), types.DiscardProgress); err != nil {
		t.Fatalf("SetMounts: %v", err)
	}
	if _, err := f.Shell(ctx, ubuntuMachine, provider.ShellOpts{
		Workdir: "/Users/dev/code/api",
		Argv:    []string{"npm", "test"},
	}); err != nil {
		t.Fatalf("Shell: %v", err)
	}

	f.AssertOps(t, OpEnsureMachine, OpAppliedMounts, OpSetMounts, OpShell)

	ensure := f.AssertCalled(t, OpEnsureMachine)
	if ensure.Spec.Name != ubuntuMachine || ensure.Spec.Selector != ubuntuSelector() {
		t.Errorf("EnsureMachine recorded spec %+v", ensure.Spec)
	}

	mounts := f.AssertCalled(t, OpSetMounts)
	if !equalStrings(types.MountHostPaths(mounts.Mounts), []string{"/Users/dev/code/api"}) {
		t.Errorf("SetMounts recorded mounts %v", mounts.Mounts)
	}

	shell := f.AssertCalled(t, OpShell)
	if shell.Shell.Workdir != "/Users/dev/code/api" {
		t.Errorf("Shell recorded workdir %q", shell.Shell.Workdir)
	}
	if !equalStrings(shell.Shell.Argv, []string{"npm", "test"}) {
		t.Errorf("Shell recorded argv %v", shell.Shell.Argv)
	}
}

// TestFake_EnsureMachineIsIdempotentOnTheWarmPath_REQ_1_2_REQ_1_3 covers the
// three states EnsureMachine has to converge from, including the warm path that
// every single avr invocation takes.
func TestFake_EnsureMachineIsIdempotentOnTheWarmPath_REQ_1_2_REQ_1_3(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("absent machine is created and left running", func(t *testing.T) {
		t.Parallel()
		f := New()
		if err := f.EnsureMachine(ctx, ubuntuSpec(), types.DiscardProgress); err != nil {
			t.Fatalf("EnsureMachine: %v", err)
		}
		f.AssertMachineState(t, ubuntuMachine, types.StateRunning)
		f.AssertProgressKinds(t, types.ProgressCreating)
	})

	t.Run("stopped machine is started", func(t *testing.T) {
		t.Parallel()
		f := New()
		f.AddMachine(ubuntuMachine, ubuntuSelector(), types.KindShared, types.StateStopped)
		if err := f.EnsureMachine(ctx, ubuntuSpec(), types.DiscardProgress); err != nil {
			t.Fatalf("EnsureMachine: %v", err)
		}
		f.AssertMachineState(t, ubuntuMachine, types.StateRunning)
		f.AssertProgressKinds(t, types.ProgressStarting)
	})

	t.Run("running machine is a silent no-op", func(t *testing.T) {
		t.Parallel()
		f := New()
		f.AddMachine(ubuntuMachine, ubuntuSelector(), types.KindShared, types.StateRunning)
		for i := 0; i < 3; i++ {
			if err := f.EnsureMachine(ctx, ubuntuSpec(), types.DiscardProgress); err != nil {
				t.Fatalf("EnsureMachine call %d: %v", i, err)
			}
		}
		f.AssertMachineState(t, ubuntuMachine, types.StateRunning)
		f.AssertRestarts(t, ubuntuMachine, 0)
		if events := f.Events(); len(events) != 0 {
			t.Errorf("warm path emitted progress events: %v", events)
		}
	})
}

// TestFake_WarnsOnceWhenProvisioningAnEmulatedEnvironment_REQ_4_6 proves the
// emulation warning is emitted at provision time and only then.
func TestFake_WarnsOnceWhenProvisioningAnEmulatedEnvironment_REQ_4_6(t *testing.T) {
	t.Parallel()

	// Whichever architecture the host is not, is the emulated one.
	foreign := types.ArchAMD64
	if foreign.Native() {
		foreign = types.ArchARM64
	}

	spec := provider.MachineSpec{
		Name:     amd64Machine,
		Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: foreign},
		Kind:     types.KindShared,
	}

	ctx := context.Background()
	f := New()
	if err := f.EnsureMachine(ctx, spec, types.DiscardProgress); err != nil {
		t.Fatalf("EnsureMachine: %v", err)
	}
	f.AssertProgressKinds(t, types.ProgressCreating, types.ProgressWarning)

	// A second invocation against the warm machine must not repeat the warning.
	before := len(f.Events())
	if err := f.EnsureMachine(ctx, spec, types.DiscardProgress); err != nil {
		t.Fatalf("second EnsureMachine: %v", err)
	}
	if after := len(f.Events()); after != before {
		t.Errorf("warm invocation emitted %d extra events", after-before)
	}
}

// TestFake_ForwardsProgressToTheSink_REQ_1_2 proves progress rendering is
// testable: the sink sees what the recording holds.
func TestFake_ForwardsProgressToTheSink_REQ_1_2(t *testing.T) {
	t.Parallel()

	var seen []types.ProgressEvent
	sink := types.ProgressFunc(func(e types.ProgressEvent) { seen = append(seen, e) })

	f := New()
	if err := f.EnsureMachine(context.Background(), ubuntuSpec(), sink); err != nil {
		t.Fatalf("EnsureMachine: %v", err)
	}

	if len(seen) != 1 {
		t.Fatalf("sink saw %d events, want 1", len(seen))
	}
	if seen[0].Kind != types.ProgressCreating || seen[0].Machine != ubuntuMachine {
		t.Errorf("sink saw %+v", seen[0])
	}
	if seen[0].Message == "" {
		t.Error("progress event carried no message")
	}
	if recorded := f.Events(); len(recorded) != 1 || recorded[0] != seen[0] {
		t.Errorf("recorded events %v do not match the sink", recorded)
	}
}

// TestFake_ShellReturnsProgrammedExitCodeNotError_PROP_3 is the distinction an
// implementor is most likely to get wrong: a guest that exits non-zero ran
// successfully, so the exit code comes back with a nil error.
func TestFake_ShellReturnsProgrammedExitCodeNotError_PROP_3(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for _, want := range []int{0, 1, 42, 130, 255} {
		want := want
		t.Run(fmt.Sprintf("exit_%d", want), func(t *testing.T) {
			t.Parallel()
			f := New()
			f.AddMachine(ubuntuMachine, ubuntuSelector(), types.KindShared, types.StateRunning)
			f.SetExitCode(want)

			got, err := f.Shell(ctx, ubuntuMachine, provider.ShellOpts{Argv: []string{"sh", "-c", "exit " + fmt.Sprint(want)}})
			if err != nil {
				t.Fatalf("Shell returned an error for a guest exit code: %v", err)
			}
			if got != want {
				t.Errorf("Shell returned exit code %d, want %d", got, want)
			}
			if call := f.AssertCalled(t, OpShell); call.ExitCode != want || call.Err != nil {
				t.Errorf("recorded call %s", call)
			}
		})
	}
}

// TestFake_RecordsShellEnvVerbatim_PROP_4 proves the double does not augment the
// guest environment: what policy allowed through is exactly what a flow test
// sees, so a leak in the code under test cannot be hidden by the fake.
func TestFake_RecordsShellEnvVerbatim_PROP_4(t *testing.T) {
	t.Parallel()

	env := map[string]string{"TERM": "xterm-256color", "LANG": "en_US.UTF-8"}

	f := New()
	f.AddMachine(ubuntuMachine, ubuntuSelector(), types.KindShared, types.StateRunning)
	if _, err := f.Shell(context.Background(), ubuntuMachine, provider.ShellOpts{Env: env}); err != nil {
		t.Fatalf("Shell: %v", err)
	}

	got := f.AssertCalled(t, OpShell).Shell.Env
	if len(got) != len(env) {
		t.Fatalf("recorded env has %d entries, want %d: %v", len(got), len(env), got)
	}
	for k, v := range env {
		if got[k] != v {
			t.Errorf("recorded env[%q] = %q, want %q", k, got[k], v)
		}
	}

	// The recording is a snapshot: mutating the caller's map afterwards must
	// not rewrite what the flow was proven to have passed.
	env["AWS_SECRET_ACCESS_KEY"] = "leaked"
	if _, ok := f.AssertCalled(t, OpShell).Shell.Env["AWS_SECRET_ACCESS_KEY"]; ok {
		t.Error("recorded env aliased the caller's map")
	}
}

// TestFake_ShellRequiresARunningMachine_REQ_1_3 keeps impossible sequences from
// passing: attaching without ensuring first is a flow bug, not a no-op.
func TestFake_ShellRequiresARunningMachine_REQ_1_3(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		f := New()
		if _, err := f.Shell(ctx, ubuntuMachine, provider.ShellOpts{}); !errors.Is(err, provider.ErrMachineNotFound) {
			t.Fatalf("Shell on an absent machine: got %v, want ErrMachineNotFound", err)
		}
	})

	t.Run("stopped", func(t *testing.T) {
		t.Parallel()
		f := New()
		f.AddMachine(ubuntuMachine, ubuntuSelector(), types.KindShared, types.StateStopped)
		if _, err := f.Shell(ctx, ubuntuMachine, provider.ShellOpts{}); !errors.Is(err, provider.ErrMachineNotRunning) {
			t.Fatalf("Shell on a stopped machine: got %v, want ErrMachineNotRunning", err)
		}
	})
}

// TestFake_ShellRejectsTTYWithRedirectedStreams_PROP_8 encodes the ShellOpts
// invariant: a pseudo-terminal is bound to the real terminal, so a redirected
// stream and TTY cannot both be honoured.
func TestFake_ShellRejectsTTYWithRedirectedStreams_PROP_8(t *testing.T) {
	t.Parallel()

	f := New()
	f.AddMachine(ubuntuMachine, ubuntuSelector(), types.KindShared, types.StateRunning)

	var out bytes.Buffer
	if _, err := f.Shell(context.Background(), ubuntuMachine, provider.ShellOpts{TTY: true, Stdout: &out}); err == nil {
		t.Fatal("Shell accepted TTY together with a redirected stdout")
	}
}

// TestFake_ShellWritesProgrammedOutputToRedirectedStreams_REQ_6_5 supports the
// probe pattern: avar checks something inside the guest and keeps the output off
// the user's terminal.
func TestFake_ShellWritesProgrammedOutputToRedirectedStreams_REQ_6_5(t *testing.T) {
	t.Parallel()

	f := New()
	f.AddMachine(ubuntuMachine, ubuntuSelector(), types.KindShared, types.StateRunning)
	f.SetShellOutput("/Users/dev/code/api\n", "warning\n")

	var stdout, stderr bytes.Buffer
	code, err := f.Shell(context.Background(), ubuntuMachine, provider.ShellOpts{
		Argv:   []string{"pwd"},
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code %d, want 0", code)
	}
	if stdout.String() != "/Users/dev/code/api\n" {
		t.Errorf("stdout = %q", stdout.String())
	}
	if stderr.String() != "warning\n" {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// TestFake_ProgrammedErrorsAreReturned_REQ_1_6 covers both failure programming
// mechanisms: sticky and one-shot.
func TestFake_ProgrammedErrorsAreReturned_REQ_1_6(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	boom := errors.New("image download failed")

	t.Run("sticky failure applies to every call", func(t *testing.T) {
		t.Parallel()
		f := New()
		f.FailOn(OpEnsureMachine, boom)
		for i := 0; i < 2; i++ {
			if err := f.EnsureMachine(ctx, ubuntuSpec(), types.DiscardProgress); !errors.Is(err, boom) {
				t.Fatalf("call %d: got %v, want %v", i, err, boom)
			}
		}
		// A failed create leaves nothing behind (REQ-1.6, PROP-7).
		f.AssertMachineState(t, ubuntuMachine, types.StateUnknown)
		if call := f.AssertCalled(t, OpEnsureMachine); !errors.Is(call.Err, boom) {
			t.Errorf("failed call was recorded as %s", call)
		}
	})

	t.Run("one-shot failure applies to the next call only", func(t *testing.T) {
		t.Parallel()
		f := New()
		f.FailNextOn(OpEnsureMachine, boom)
		if err := f.EnsureMachine(ctx, ubuntuSpec(), types.DiscardProgress); !errors.Is(err, boom) {
			t.Fatalf("first call: got %v, want %v", err, boom)
		}
		if err := f.EnsureMachine(ctx, ubuntuSpec(), types.DiscardProgress); err != nil {
			t.Fatalf("retry: %v", err)
		}
		f.AssertMachineState(t, ubuntuMachine, types.StateRunning)
	})

	t.Run("clearing a sticky failure restores success", func(t *testing.T) {
		t.Parallel()
		f := New()
		f.FailOn(OpStop, boom)
		f.FailOn(OpStop, nil)
		f.AddMachine(ubuntuMachine, ubuntuSelector(), types.KindShared, types.StateRunning)
		if err := f.Stop(ctx, ubuntuMachine, types.DiscardProgress); err != nil {
			t.Fatalf("Stop: %v", err)
		}
	})
}

// TestFake_StateIsCoherentAcrossASequence_REQ_5_2 walks a machine through its
// whole life and checks the Fake behaves like a backend at every step rather
// than merely recording.
func TestFake_StateIsCoherentAcrossASequence_REQ_5_2(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := New()

	if err := f.EnsureMachine(ctx, ubuntuSpec(), types.DiscardProgress); err != nil {
		t.Fatalf("EnsureMachine: %v", err)
	}
	f.AssertMachineState(t, ubuntuMachine, types.StateRunning)

	if err := f.SetMounts(ctx, ubuntuMachine, shares(t, f, "/Users/dev/code/api"), types.DiscardProgress); err != nil {
		t.Fatalf("SetMounts: %v", err)
	}
	applied, err := f.AppliedMounts(ctx, ubuntuMachine)
	if err != nil {
		t.Fatalf("AppliedMounts: %v", err)
	}
	if !types.EqualMounts(applied, shares(t, f, "/Users/dev/code/api")) {
		t.Fatalf("AppliedMounts = %v", applied)
	}

	statuses, err := f.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) != 1 || statuses[0].Name != ubuntuMachine || statuses[0].State != types.StateRunning {
		t.Fatalf("Status = %+v", statuses)
	}
	if !types.EqualMounts(statuses[0].Mounts, shares(t, f, "/Users/dev/code/api")) {
		t.Errorf("Status mounts = %v", statuses[0].Mounts)
	}
	if statuses[0].Provider != f.ID() {
		t.Errorf("Status provider = %q, want %q", statuses[0].Provider, f.ID())
	}
	if statuses[0].CPUs == 0 || statuses[0].MemoryGB == 0 {
		t.Errorf("Status reported no resources: %+v", statuses[0])
	}

	if err := f.Stop(ctx, ubuntuMachine, types.DiscardProgress); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	f.AssertMachineState(t, ubuntuMachine, types.StateStopped)

	// Stopping again converges rather than failing, and says nothing.
	events := len(f.Events())
	if err := f.Stop(ctx, ubuntuMachine, types.DiscardProgress); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if len(f.Events()) != events {
		t.Error("stopping an already stopped machine emitted progress")
	}

	if err := f.Delete(ctx, ubuntuMachine); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	f.AssertMachineState(t, ubuntuMachine, types.StateUnknown)
	if statuses, err := f.Status(ctx); err != nil || len(statuses) != 0 {
		t.Fatalf("Status after delete = %+v, %v", statuses, err)
	}
}

// TestFake_DeleteIsIdempotentAndStopIsNot_REQ_17_5 encodes the deliberate
// asymmetry in the contract: cleanup must be repeatable, while `avr stop` needs
// to tell "already stopped" from "no environment here".
func TestFake_DeleteIsIdempotentAndStopIsNot_REQ_17_5(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := New()

	if err := f.Delete(ctx, ubuntuMachine); err != nil {
		t.Errorf("Delete of an absent machine: %v, want nil", err)
	}
	if err := f.Stop(ctx, ubuntuMachine, types.DiscardProgress); !errors.Is(err, provider.ErrMachineNotFound) {
		t.Errorf("Stop of an absent machine: got %v, want ErrMachineNotFound", err)
	}
}

// TestFake_SetMountsRestartsOnlyWhenTheSetChanges_REQ_6_4 is the warm-path
// guarantee: registering a new project restarts once, revisiting a known one
// does nothing at all.
func TestFake_SetMountsRestartsOnlyWhenTheSetChanges_REQ_6_4(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := New()
	if err := f.EnsureMachine(ctx, ubuntuSpec(), types.DiscardProgress); err != nil {
		t.Fatalf("EnsureMachine: %v", err)
	}

	if err := f.SetMounts(ctx, ubuntuMachine, shares(t, f, "/Users/dev/code/api"), types.DiscardProgress); err != nil {
		t.Fatalf("first SetMounts: %v", err)
	}
	f.AssertRestarts(t, ubuntuMachine, 1)
	f.AssertProgressKinds(t, types.ProgressCreating, types.ProgressMounting)

	// Same set, in a different order: no change, no restart, no message.
	if err := f.SetMounts(ctx, ubuntuMachine, shares(t, f, "/Users/dev/code/api"), types.DiscardProgress); err != nil {
		t.Fatalf("repeat SetMounts: %v", err)
	}
	f.AssertRestarts(t, ubuntuMachine, 1)
	f.AssertProgressKinds(t, types.ProgressCreating, types.ProgressMounting)

	// Adding a project restarts once more and replaces the set.
	if err := f.SetMounts(ctx, ubuntuMachine, shares(t, f, "/Users/dev/code/web", "/Users/dev/code/api"), types.DiscardProgress); err != nil {
		t.Fatalf("second SetMounts: %v", err)
	}
	f.AssertRestarts(t, ubuntuMachine, 2)
	f.AssertMounts(t, ubuntuMachine, "/Users/dev/code/api", "/Users/dev/code/web")
}

// TestFake_SetMountsReplacesRatherThanAccumulates_PROP_5 proves mount
// confinement is expressible: a directory dropped from the desired set stops
// being reachable.
func TestFake_SetMountsReplacesRatherThanAccumulates_PROP_5(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := New()
	if err := f.EnsureMachine(ctx, ubuntuSpec(), types.DiscardProgress); err != nil {
		t.Fatalf("EnsureMachine: %v", err)
	}
	if err := f.SetMounts(ctx, ubuntuMachine, shares(t, f, "/Users/dev/code/api", "/Users/dev/code/web"), types.DiscardProgress); err != nil {
		t.Fatalf("SetMounts: %v", err)
	}
	if err := f.SetMounts(ctx, ubuntuMachine, shares(t, f, "/Users/dev/code/api"), types.DiscardProgress); err != nil {
		t.Fatalf("SetMounts: %v", err)
	}
	f.AssertMounts(t, ubuntuMachine, "/Users/dev/code/api")
}

// TestFake_EnsureMachineAppliesSpecMountsAtCreate_REQ_6_1 checks the create-time
// mount list is honoured, so a first-run flow needs no separate SetMounts.
func TestFake_EnsureMachineAppliesSpecMountsAtCreate_REQ_6_1(t *testing.T) {
	t.Parallel()

	f := New()
	spec := ubuntuSpec()
	spec.Mounts = shares(t, f, "/Users/dev/code/api")

	if err := f.EnsureMachine(context.Background(), spec, types.DiscardProgress); err != nil {
		t.Fatalf("EnsureMachine: %v", err)
	}
	f.AssertMounts(t, ubuntuMachine, "/Users/dev/code/api")
	f.AssertRestarts(t, ubuntuMachine, 0)
}

// TestFake_RefusesMachinesAvarDoesNotOwn_REQ_5_4_PROP_6 is the ownership guard:
// every operation that names a machine validates the name first, so no test can
// prove behaviour the real backend would refuse to perform.
func TestFake_RefusesMachinesAvarDoesNotOwn_REQ_5_4_PROP_6(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const foreign = "docker-desktop"

	ops := map[Op]func(f *Fake) error{
		OpEnsureMachine: func(f *Fake) error {
			spec := ubuntuSpec()
			spec.Name = foreign
			return f.EnsureMachine(ctx, spec, types.DiscardProgress)
		},
		OpShell: func(f *Fake) error {
			_, err := f.Shell(ctx, foreign, provider.ShellOpts{})
			return err
		},
		OpAppliedMounts: func(f *Fake) error {
			_, err := f.AppliedMounts(ctx, foreign)
			return err
		},
		OpSetMounts: func(f *Fake) error {
			return f.SetMounts(ctx, foreign, shares(t, f, "/tmp"), types.DiscardProgress)
		},
		OpStop: func(f *Fake) error {
			return f.Stop(ctx, foreign, types.DiscardProgress)
		},
		OpDelete: func(f *Fake) error {
			return f.Delete(ctx, foreign)
		},
		OpSnapshot: func(f *Fake) error {
			return f.Snapshot(ctx, foreign, "before", types.DiscardProgress)
		},
		OpRestoreSnapshot: func(f *Fake) error {
			return f.RestoreSnapshot(ctx, foreign, "before", types.DiscardProgress)
		},
		OpListSnapshots: func(f *Fake) error {
			_, err := f.ListSnapshots(ctx, foreign)
			return err
		},
		OpEditorTarget: func(f *Fake) error {
			_, err := f.EditorTarget(ctx, foreign, "/tmp")
			return err
		},
		OpPortDiagnostics: func(f *Fake) error {
			_, err := f.PortDiagnostics(ctx, foreign)
			return err
		},
	}

	for op, call := range ops {
		op, call := op, call
		t.Run(string(op), func(t *testing.T) {
			t.Parallel()
			f := New()
			f.AddForeignMachine(foreign)

			if err := call(f); !errors.Is(err, provider.ErrNotOwned) {
				t.Fatalf("%s on a foreign machine: got %v, want ErrNotOwned", op, err)
			}
			// The foreign machine is untouched and the refusal is on record.
			if f.MachineState(foreign) != types.StateRunning {
				t.Errorf("%s modified a machine avar does not own", op)
			}
			if recorded := f.AssertCalled(t, op); recorded.Err == nil {
				t.Errorf("%s recorded no error for a refused call", op)
			}
		})
	}
}

// TestFake_RefusesAnOwnedNameThatIsNotInAvarsRecords_REQ_5_4_PROP_6 covers the
// second half of ownership: the prefix alone is not enough, the machine must
// also be one avar created.
func TestFake_RefusesAnOwnedNameThatIsNotInAvarsRecords_REQ_5_4_PROP_6(t *testing.T) {
	t.Parallel()

	// A machine that carries the prefix but that avar did not create.
	const impostor = "avr-someone-elses"

	f := New()
	f.AddForeignMachine(impostor)

	if err := f.Stop(context.Background(), impostor, types.DiscardProgress); !errors.Is(err, provider.ErrNotOwned) {
		t.Fatalf("Stop on an unrecorded machine: got %v, want ErrNotOwned", err)
	}
	if f.MachineState(impostor) != types.StateRunning {
		t.Error("Stop touched a machine that is not in avar's records")
	}
}

// TestFake_StatusListsOnlyAvarOwnedMachines_REQ_5_4_PROP_6 proves Status filters
// rather than annotates — the guarantee `avr status` and `avr stop --all` rest
// on.
func TestFake_StatusListsOnlyAvarOwnedMachines_REQ_5_4_PROP_6(t *testing.T) {
	t.Parallel()

	f := New()
	f.AddMachine(amd64Machine, ubuntuSelector(), types.KindShared, types.StateStopped)
	f.AddMachine(ubuntuMachine, ubuntuSelector(), types.KindShared, types.StateRunning)
	f.AddForeignMachine("docker-desktop")
	f.AddForeignMachine("avr-not-in-records")

	statuses, err := f.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var names []string
	for _, s := range statuses {
		names = append(names, s.Name)
	}
	// Sorted by name, and only avar's own machines.
	if !equalStrings(names, []string{amd64Machine, ubuntuMachine}) {
		t.Fatalf("Status listed %v", names)
	}
}

// TestFake_StatusOnAnEmptyBackend_REQ_5_3 is the onboarding case: nothing owned
// is an empty result, not an error.
func TestFake_StatusOnAnEmptyBackend_REQ_5_3(t *testing.T) {
	t.Parallel()

	statuses, err := New().Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) != 0 {
		t.Fatalf("Status listed %d machines on an empty backend", len(statuses))
	}
}

// TestFake_SnapshotRoundTrip_REQ_10_1_REQ_10_2_REQ_10_4 covers capture, listing
// with timestamps, restore, and the unknown-name case the CLI turns into a list
// of what does exist.
func TestFake_SnapshotRoundTrip_REQ_10_1_REQ_10_2_REQ_10_4(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := New()
	f.AddMachine(ubuntuMachine, ubuntuSelector(), types.KindShared, types.StateRunning)

	if err := f.Snapshot(ctx, ubuntuMachine, "before-upgrade", types.DiscardProgress); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// Capturing a running machine cycles it and says so, then leaves it running.
	f.AssertProgressKinds(t, types.ProgressStopping, types.ProgressStarting)
	f.AssertMachineState(t, ubuntuMachine, types.StateRunning)

	if err := f.Snapshot(ctx, ubuntuMachine, "before-upgrade", types.DiscardProgress); err == nil {
		t.Error("Snapshot accepted a duplicate name")
	}

	snaps, err := f.ListSnapshots(ctx, ubuntuMachine)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 1 || snaps[0].Name != "before-upgrade" {
		t.Fatalf("ListSnapshots = %+v", snaps)
	}
	if snaps[0].CreatedAt.IsZero() {
		t.Error("snapshot carried no timestamp")
	}

	if err := f.RestoreSnapshot(ctx, ubuntuMachine, "before-upgrade", types.DiscardProgress); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	if err := f.RestoreSnapshot(ctx, ubuntuMachine, "nope", types.DiscardProgress); !errors.Is(err, provider.ErrSnapshotNotFound) {
		t.Fatalf("RestoreSnapshot of an unknown name: got %v, want ErrSnapshotNotFound", err)
	}
}

// TestFake_EditorTargetRequiresARunningMachine_REQ_13_1 mirrors the contract:
// the target describes a live endpoint, so `avr code` must ensure the machine
// first.
func TestFake_EditorTargetRequiresARunningMachine_REQ_13_1(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := New()
	f.AddMachine(ubuntuMachine, ubuntuSelector(), types.KindShared, types.StateStopped)

	if _, err := f.EditorTarget(ctx, ubuntuMachine, "/work"); !errors.Is(err, provider.ErrMachineNotRunning) {
		t.Fatalf("EditorTarget on a stopped machine: got %v, want ErrMachineNotRunning", err)
	}

	f.SetMachineState(ubuntuMachine, types.StateRunning)
	target, err := f.EditorTarget(ctx, ubuntuMachine, "/work")
	if err != nil {
		t.Fatalf("EditorTarget: %v", err)
	}
	if target.Authority == "" {
		t.Error("an editor target with no authority cannot be opened")
	}
	if target.GuestPath != "/work" {
		t.Errorf("target guest path = %q, want the path asked for", target.GuestPath)
	}
	// A backend an editor reaches without SSH hands back no SSH material, and
	// that is how the caller knows there is nothing to write (REQ-18.10,
	// PROP-17).
	if target.SSHConfig != "" {
		t.Errorf("the default target carries SSH material a WSL-style backend would not have: %q", target.SSHConfig)
	}

	// A backend that is reached over SSH says so with a stanza, in the same
	// shape, so the launcher needs no branch of its own (REQ-13.3).
	f.SetEditorTarget(ubuntuMachine, provider.EditorTarget{
		Authority: "ssh-remote+" + ubuntuMachine,
		SSHConfig: "Host " + ubuntuMachine + "\n  HostName 127.0.0.1\n",
	})
	target, err = f.EditorTarget(ctx, ubuntuMachine, "/work")
	if err != nil {
		t.Fatalf("EditorTarget: %v", err)
	}
	if !strings.HasPrefix(target.Authority, "ssh-remote+") || !strings.HasPrefix(target.SSHConfig, "Host ") {
		t.Errorf("programmed SSH target came back as %+v", target)
	}
	if target.GuestPath != "/work" {
		t.Errorf("a programmed target must still open the path asked for, got %q", target.GuestPath)
	}
}

// TestFake_PortDiagnosticsReportConflicts_REQ_7_2 proves an unforwardable port
// is data in the result rather than an error.
func TestFake_PortDiagnosticsReportConflicts_REQ_7_2(t *testing.T) {
	t.Parallel()

	f := New()
	f.AddMachine(ubuntuMachine, ubuntuSelector(), types.KindShared, types.StateRunning)
	f.SetPortDiagnostics(ubuntuMachine, []provider.PortDiagnostic{
		{GuestPort: 3000, HostPort: 3000, Forwarded: true},
		{GuestPort: 5432, Forwarded: false, Reason: "host port 5432 is already in use"},
	})

	diags, err := f.PortDiagnostics(context.Background(), ubuntuMachine)
	if err != nil {
		t.Fatalf("PortDiagnostics: %v", err)
	}
	if len(diags) != 2 || diags[1].Forwarded || diags[1].Reason == "" {
		t.Fatalf("PortDiagnostics = %+v", diags)
	}
}

// TestFake_HonoursContextCancellation_REQ_17_5 proves every operation checks the
// context before doing anything, so a cancelled invocation leaves no changes.
func TestFake_HonoursContextCancellation_REQ_17_5(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := New()
	f.AddMachine(ubuntuMachine, ubuntuSelector(), types.KindShared, types.StateRunning)

	if err := f.EnsureMachine(ctx, ubuntuSpec(), types.DiscardProgress); !errors.Is(err, context.Canceled) {
		t.Errorf("EnsureMachine: got %v, want context.Canceled", err)
	}
	if _, err := f.Shell(ctx, ubuntuMachine, provider.ShellOpts{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Shell: got %v, want context.Canceled", err)
	}
	if err := f.Stop(ctx, ubuntuMachine, types.DiscardProgress); !errors.Is(err, context.Canceled) {
		t.Errorf("Stop: got %v, want context.Canceled", err)
	}
	if _, err := f.Status(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Status: got %v, want context.Canceled", err)
	}
	f.AssertMachineState(t, ubuntuMachine, types.StateRunning)
}

// TestFake_IsSafeForConcurrentUse_REQ_17_5 keeps the double usable from a test
// that exercises concurrent avr invocations. Run with -race.
func TestFake_IsSafeForConcurrentUse_REQ_17_5(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := New()

	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("avr-worker-%d", i%3)
			spec := ubuntuSpec()
			spec.Name = name
			_ = f.EnsureMachine(ctx, spec, types.DiscardProgress)
			_ = f.SetMounts(ctx, name, shares(t, f, fmt.Sprintf("/Users/dev/p%d", i)), types.DiscardProgress)
			_, _ = f.Shell(ctx, name, provider.ShellOpts{Argv: []string{"true"}})
			_, _ = f.Status(ctx)
			_, _ = f.AppliedMounts(ctx, name)
			_ = f.Stop(ctx, name, types.DiscardProgress)
			f.SetExitCode(i)
			_ = f.Calls()
			_ = f.Events()
			_ = f.Transcript()
		}(i)
	}
	wg.Wait()

	// Every operation of every worker is on record exactly once.
	if got, want := len(f.Calls()), workers*6; got != want {
		t.Errorf("recorded %d calls, want %d", got, want)
	}
}

// TestFake_AddMachineRejectsAnUnownedName_PROP_6 stops a test from seeding a
// machine the real backend would never manage.
func TestFake_AddMachineRejectsAnUnownedName_PROP_6(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Error("AddMachine accepted a machine name avar does not own")
		}
	}()
	New().AddMachine("docker-desktop", ubuntuSelector(), types.KindShared, types.StateRunning)
}

// TestFake_AssertionHelpers checks the helpers report what they claim to, using
// a recording TB so both outcomes are observable.
func TestFake_AssertionHelpers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	newFake := func(t *testing.T) *Fake {
		t.Helper()
		f := New()
		if err := f.EnsureMachine(ctx, ubuntuSpec(), types.DiscardProgress); err != nil {
			t.Fatalf("EnsureMachine: %v", err)
		}
		if _, err := f.Shell(ctx, ubuntuMachine, provider.ShellOpts{}); err != nil {
			t.Fatalf("Shell: %v", err)
		}
		return f
	}

	t.Run("AssertOps passes on the exact sequence", func(t *testing.T) {
		t.Parallel()
		tb := &recordingTB{}
		newFake(t).AssertOps(tb, OpEnsureMachine, OpShell)
		if tb.failed() {
			t.Errorf("AssertOps failed on a matching sequence: %v", tb.failures)
		}
		if tb.helpers == 0 {
			t.Error("AssertOps did not mark itself as a helper")
		}
	})

	t.Run("AssertOps fails on a wrong order", func(t *testing.T) {
		t.Parallel()
		tb := &recordingTB{}
		newFake(t).AssertOps(tb, OpShell, OpEnsureMachine)
		if !tb.failed() {
			t.Error("AssertOps passed on a reordered sequence")
		}
	})

	t.Run("AssertOps fails on a missing call", func(t *testing.T) {
		t.Parallel()
		tb := &recordingTB{}
		newFake(t).AssertOps(tb, OpEnsureMachine)
		if !tb.failed() {
			t.Error("AssertOps passed while a call was unaccounted for")
		}
	})

	t.Run("AssertOpsInOrder ignores calls in between", func(t *testing.T) {
		t.Parallel()
		f := newFake(t)
		if err := f.SetMounts(ctx, ubuntuMachine, shares(t, f, "/Users/dev/code/api"), types.DiscardProgress); err != nil {
			t.Fatalf("SetMounts: %v", err)
		}
		tb := &recordingTB{}
		f.AssertOpsInOrder(tb, OpEnsureMachine, OpSetMounts)
		if tb.failed() {
			t.Errorf("AssertOpsInOrder failed on a valid subsequence: %v", tb.failures)
		}

		reversed := &recordingTB{}
		f.AssertOpsInOrder(reversed, OpSetMounts, OpEnsureMachine)
		if !reversed.failed() {
			t.Error("AssertOpsInOrder passed on a reversed subsequence")
		}
	})

	t.Run("AssertNotCalled and AssertCallCount", func(t *testing.T) {
		t.Parallel()
		f := newFake(t)

		tb := &recordingTB{}
		f.AssertNotCalled(tb, OpSetMounts)
		if tb.failed() {
			t.Errorf("AssertNotCalled failed for an operation that never happened: %v", tb.failures)
		}

		called := &recordingTB{}
		f.AssertNotCalled(called, OpShell)
		if !called.failed() {
			t.Error("AssertNotCalled passed for an operation that happened")
		}

		count := &recordingTB{}
		f.AssertCallCount(count, OpShell, 1)
		if count.failed() {
			t.Errorf("AssertCallCount failed on a correct count: %v", count.failures)
		}
	})

	t.Run("AssertCalled reports the last call", func(t *testing.T) {
		t.Parallel()
		f := newFake(t)
		if _, err := f.Shell(ctx, ubuntuMachine, provider.ShellOpts{Argv: []string{"ls"}}); err != nil {
			t.Fatalf("Shell: %v", err)
		}
		tb := &recordingTB{}
		call := f.AssertCalled(tb, OpShell)
		if tb.failed() {
			t.Fatalf("AssertCalled failed: %v", tb.failures)
		}
		if !equalStrings(call.Shell.Argv, []string{"ls"}) {
			t.Errorf("AssertCalled returned %s", call)
		}

		missing := &recordingTB{}
		if got := f.AssertCalled(missing, OpDelete); !missing.failed() || got.Op != "" {
			t.Error("AssertCalled passed for an operation that never happened")
		}
	})

	t.Run("Transcript describes the calls", func(t *testing.T) {
		t.Parallel()
		transcript := newFake(t).Transcript()
		for _, want := range []string{"EnsureMachine", "Shell", ubuntuMachine} {
			if !strings.Contains(transcript, want) {
				t.Errorf("transcript %q does not mention %q", transcript, want)
			}
		}
		if empty := New().Transcript(); !strings.Contains(empty, "no provider calls") {
			t.Errorf("empty transcript = %q", empty)
		}
	})
}

// TestFake_ResetKeepsMachinesAndClearsTheRecording supports flow tests that
// exercise a second avr invocation against the same warm environment.
func TestFake_ResetKeepsMachinesAndClearsTheRecording(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	f := New()
	if err := f.EnsureMachine(ctx, ubuntuSpec(), types.DiscardProgress); err != nil {
		t.Fatalf("EnsureMachine: %v", err)
	}
	f.SetExitCode(7)

	f.Reset()

	if calls := f.Calls(); len(calls) != 0 {
		t.Errorf("Reset left %d calls behind", len(calls))
	}
	if events := f.Events(); len(events) != 0 {
		t.Errorf("Reset left %d events behind", len(events))
	}
	f.AssertMachineState(t, ubuntuMachine, types.StateRunning)

	code, err := f.Shell(ctx, ubuntuMachine, provider.ShellOpts{})
	if err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if code != 0 {
		t.Errorf("Reset left the programmed exit code %d in place", code)
	}
}

// The Fake's mapping is deliberately not the identity: a flow proven against it
// is a flow that does not assume the host path is also the guest path, which is
// the assumption Requirement 18 breaks (REQ-18.5, PROP-1).
func TestFake_MapProjectPath_IsNotTheIdentityMapping_REQ_18_5(t *testing.T) {
	t.Parallel()

	f := New()
	mount, guestCwd, err := f.MapProjectPath("3fa9c2b1d0", "/Users/dev/code/app", "/Users/dev/code/app/api")
	if err != nil {
		t.Fatalf("MapProjectPath: %v", err)
	}
	if mount.HostPath == mount.GuestPath {
		t.Error("the test double maps a project to its own host path, so no flow test can catch a caller that assumes it")
	}
	if mount.GuestPath != GuestProjectRoot+"/3fa9c2b1d0" {
		t.Errorf("guest root = %q", mount.GuestPath)
	}
	// The relative suffix is preserved, which is what makes a subdirectory land
	// in the matching subdirectory (REQ-6.6, PROP-1).
	if guestCwd != GuestProjectRoot+"/3fa9c2b1d0/api" {
		t.Errorf("guest cwd = %q", guestCwd)
	}
	if !mount.Writable {
		t.Error("a project is shared writable")
	}
	if mount.ProjectID != "3fa9c2b1d0" {
		t.Errorf("project id = %q", mount.ProjectID)
	}
}

func TestFake_MapProjectPath_RefusesWhatItCannotMap(t *testing.T) {
	t.Parallel()

	f := New()
	cases := map[string][3]string{
		"cwd outside the project": {"abc", "/Users/dev/code/app", "/Users/dev/code/other"},
		"cwd is a sibling prefix": {"abc", "/Users/dev/code/app", "/Users/dev/code/application"},
		"relative project root":   {"abc", "code/app", "/Users/dev/code/app"},
		"no project identity":     {"", "/Users/dev/code/app", "/Users/dev/code/app"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := f.MapProjectPath(args[0], args[1], args[2]); err == nil {
				t.Fatalf("MapProjectPath(%q, %q, %q) was accepted", args[0], args[1], args[2])
			}
		})
	}
}

// A spec addressed to another backend is refused rather than built, so a
// mis-routed invocation fails loudly instead of creating something the caller
// did not ask for (REQ-18.1).
func TestFake_EnsureMachineRefusesASpecForAnotherBackend_REQ_18_1(t *testing.T) {
	t.Parallel()

	f := New()
	spec := ubuntuSpec()
	spec.Provider = types.ProviderLima
	if err := f.EnsureMachine(context.Background(), spec, types.DiscardProgress); err == nil {
		t.Fatal("a spec addressed to another backend was built anyway")
	}

	spec.Provider = f.ID()
	if err := f.EnsureMachine(context.Background(), spec, types.DiscardProgress); err != nil {
		t.Fatalf("a spec addressed to this backend: %v", err)
	}
}
