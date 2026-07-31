package mounts_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/mounts"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/types"
)

// The environments the tests resolve against. Every test uses arm64 so it is
// not emulated; the resolver does not run here, so none of these values matter
// except that they produce valid selectors the Fake's EnsureMachine accepts.
var selector = types.EnvironmentSelector{
	Distro:  types.DistroUbuntu,
	Version: "24.04",
	Arch:    types.ArchARM64,
}

// TestEnsure_AlreadyShared_ReturnsImmediately_REQ_17_1 verifies the warm path:
// when the project is already shared with the machine, nothing changes, no
// restart happens, and no progress event is emitted.
func TestEnsure_AlreadyShared_ReturnsImmediately_REQ_17_1(t *testing.T) {
	ctx := context.Background()
	fk := fake.New()
	fk.AddMachine("avr-ubuntu-24.04-arm64", selector, types.KindShared, types.StateRunning)

	// Seed the machine with an existing mount.
	existing := types.MountSpec{HostPath: "/Users/dev/proj-a", GuestPath: "/Users/dev/proj-a", Writable: true}
	mustSetMounts(t, ctx, fk, "avr-ubuntu-24.04-arm64", []types.MountSpec{existing})
	fk.Reset()

	// The project is already shared — so Ensure should return immediately.
	result, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", existing, existing.GuestPath, 0, types.DiscardProgress)
	if err != nil {
		t.Fatalf("Ensure on an already-shared project: %v", err)
	}
	if result.SessionConflict {
		t.Error("session conflict on a project that is already shared")
	}
	if result.GuestCwd != existing.GuestPath {
		t.Errorf("GuestCwd = %q, want %q", result.GuestCwd, existing.GuestPath)
	}

	// The warm path must not call SetMounts.
	if c := fk.Count(fake.OpSetMounts); c != 0 {
		t.Errorf("warm path called SetMounts %d times, want 0", c)
	}
	// And must not emit a progress event.
	for _, ev := range fk.Events() {
		if ev.Kind == types.ProgressMounting {
			t.Error("warm path emitted ProgressMounting")
		}
	}
}

// TestEnsure_NewProject_NoSessions_AppliesMount verifies that a project not
// yet shared is added through SetMounts alongside the existing shares.
func TestEnsure_NewProject_NoSessions_AppliesMount(t *testing.T) {
	ctx := context.Background()
	fk := fake.New()
	fk.AddMachine("avr-ubuntu-24.04-arm64", selector, types.KindShared, types.StateRunning)

	existing := types.MountSpec{HostPath: "/Users/dev/proj-a", GuestPath: "/Users/dev/proj-a", Writable: true}
	mustSetMounts(t, ctx, fk, "avr-ubuntu-24.04-arm64", []types.MountSpec{existing})
	fk.Reset()

	// Add a second project.
	newMount := types.MountSpec{HostPath: "/Users/dev/proj-b", GuestPath: "/Users/dev/proj-b", Writable: true}
	result, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", newMount, newMount.GuestPath, 0, types.DiscardProgress)
	if err != nil {
		t.Fatalf("Ensure for a new project: %v", err)
	}
	if result.SessionConflict {
		t.Error("session conflict with zero sessions")
	}

	// SetMounts must have been called once, with both mounts.
	if c := fk.Count(fake.OpSetMounts); c != 1 {
		t.Fatalf("SetMounts called %d times, want 1", c)
	}
	call, _ := fk.LastCall(fake.OpSetMounts)
	paths := types.MountHostPaths(call.Mounts)
	if len(paths) != 2 {
		t.Fatalf("SetMounts applied %d mounts, want 2: %v", len(paths), paths)
	}
	if paths[0] != existing.HostPath || paths[1] != newMount.HostPath {
		t.Errorf("SetMounts got %v, want [%s, %s]", paths, existing.HostPath, newMount.HostPath)
	}

	// A ProgressMounting event must be emitted.
	found := false
	for _, ev := range fk.Events() {
		if ev.Kind == types.ProgressMounting {
			found = true
			break
		}
	}
	if !found {
		t.Error("no ProgressMounting event emitted")
	}
}

// TestEnsure_AppliedSetEqualsRegisteredRoots_PROP_5 verifies mount confinement:
// after adding multiple projects, the applied set is exactly the registered
// roots — never the home directory or an unregistered sibling.
func TestEnsure_AppliedSetEqualsRegisteredRoots_PROP_5(t *testing.T) {
	ctx := context.Background()
	fk := fake.New()
	fk.AddMachine("avr-ubuntu-24.04-arm64", selector, types.KindShared, types.StateRunning)

	// Register three projects one at a time.
	projects := []string{"/Users/dev/proj-a", "/Users/dev/proj-b", "/Users/dev/proj-c"}
	for _, p := range projects {
		mount := types.MountSpec{HostPath: p, GuestPath: p, Writable: true}
		_, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", mount, mount.GuestPath, 0, types.DiscardProgress)
		if err != nil {
			t.Fatalf("Ensure for %s: %v", p, err)
		}
	}

	// The applied mounts must be exactly the registered roots.
	applied, err := fk.AppliedMounts(ctx, "avr-ubuntu-24.04-arm64")
	if err != nil {
		t.Fatalf("AppliedMounts: %v", err)
	}
	if len(applied) != 3 {
		t.Fatalf("applied %d mounts, want 3: %v", len(applied), types.MountHostPaths(applied))
	}
	for i, want := range projects {
		if applied[i].HostPath != want {
			t.Errorf("applied[%d] = %s, want %s", i, applied[i].HostPath, want)
		}
	}

	// Confinement: the home directory must never appear.
	for _, m := range applied {
		home, _ := os.UserHomeDir()
		if strings.HasPrefix(m.GuestPath, home+"/") || m.GuestPath == home {
			t.Errorf("guest path %s includes the home directory %s; PROP-5 forbids sharing the home", m.GuestPath, home)
		}
	}
}

// TestEnsure_NewProject_WithSessions_ReturnsConflict_REQ_6_4 verifies that a
// mount change on a machine with live sessions is refused, leaving the caller
// to prompt the user rather than restarting under someone else's shell.
func TestEnsure_NewProject_WithSessions_ReturnsConflict_REQ_6_4(t *testing.T) {
	ctx := context.Background()
	fk := fake.New()
	fk.AddMachine("avr-ubuntu-24.04-arm64", selector, types.KindShared, types.StateRunning)

	existing := types.MountSpec{HostPath: "/Users/dev/proj-a", GuestPath: "/Users/dev/proj-a", Writable: true}
	mustSetMounts(t, ctx, fk, "avr-ubuntu-24.04-arm64", []types.MountSpec{existing})
	fk.Reset()

	// Three other sessions are attached.
	newMount := types.MountSpec{HostPath: "/Users/dev/proj-b", GuestPath: "/Users/dev/proj-b", Writable: true}
	result, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", newMount, newMount.GuestPath, 3, types.DiscardProgress)
	if err != nil {
		t.Fatalf("Ensure with sessions: %v", err)
	}
	if !result.SessionConflict {
		t.Fatal("session conflict was not reported")
	}
	if result.SessionCount != 3 {
		t.Errorf("SessionCount = %d, want 3", result.SessionCount)
	}

	// The mounts must not have been modified.
	if c := fk.Count(fake.OpSetMounts); c != 0 {
		t.Errorf("SetMounts was called %d times despite live sessions", c)
	}
}

// TestEnsure_SubdirectoryReuse_REQ_6_6 verifies that invoking avr from a
// subdirectory of an already-registered project reuses the project root's
// mount — the mount's host path is the project root, and that is what was
// already applied.
func TestEnsure_SubdirectoryReuse_REQ_6_6(t *testing.T) {
	ctx := context.Background()
	fk := fake.New()
	fk.AddMachine("avr-ubuntu-24.04-arm64", selector, types.KindShared, types.StateRunning)

	// The project root is shared, and the user is in a/b beneath it. The
	// resolver gives MapProjectPath the project root as the mount source
	// and the subdirectory as GuestCwd.
	root := "/Users/dev/proj-a"
	existing := types.MountSpec{HostPath: root, GuestPath: root, Writable: true}
	mustSetMounts(t, ctx, fk, "avr-ubuntu-24.04-arm64", []types.MountSpec{existing})
	fk.Reset()

	// The mount spec is the same (project root), but guestCwd points deeper.
	// This is what MapProjectPath returns when the user is in a subdirectory.
	guestCwd := filepath.Join(root, "a", "b")
	result, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", existing, guestCwd, 0, types.DiscardProgress)
	if err != nil {
		t.Fatalf("Ensure from a subdirectory: %v", err)
	}
	if result.SessionConflict {
		t.Error("session conflict on already-shared project root")
	}
	if result.GuestCwd != guestCwd {
		t.Errorf("GuestCwd = %q, want %q", result.GuestCwd, guestCwd)
	}

	// The existing mount must be reused — no SetMounts call.
	if c := fk.Count(fake.OpSetMounts); c != 0 {
		t.Errorf("SetMounts called %d times; subdirectory should reuse existing mount", c)
	}
}

// TestEnsure_VerifyMountFails_ReturnsError_REQ_6_5 verifies that a mount that
// does not actually land in the guest produces a hard error rather than
// silently dropping the user into a shell at an empty path.
func TestEnsure_VerifyMountFails_ReturnsError_REQ_6_5(t *testing.T) {
	ctx := context.Background()
	fk := fake.New()
	fk.AddMachine("avr-ubuntu-24.04-arm64", selector, types.KindShared, types.StateRunning)

	// The verification probe runs Shell with `test -d <guestPath>`. Making
	// the guest process exit non-zero simulates the directory not being
	// present.
	fk.SetExitCode(1)

	newMount := types.MountSpec{HostPath: "/Users/dev/proj-a", GuestPath: "/Users/dev/proj-a", Writable: true}
	_, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", newMount, newMount.GuestPath, 0, types.DiscardProgress)
	if err == nil {
		t.Fatal("Ensure succeeded when mount verification should have failed")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("error = %v, want it to say the path is not a directory", err)
	}
}

// TestEnsure_AppliedMountsError_ReturnsWrappedError verifies that a failure
// reading the applied mounts is surfaced to the caller.
func TestEnsure_AppliedMountsError_ReturnsWrappedError(t *testing.T) {
	ctx := context.Background()
	fk := fake.New()

	// The machine does not exist — AppliedMounts should fail.
	_, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", types.MountSpec{HostPath: "/x", GuestPath: "/x"}, "/x", 0, types.DiscardProgress)
	if err == nil {
		t.Fatal("Ensure succeeded on a nonexistent machine")
	}
}

// TestEnsure_SetMountsError_ReturnsWrappedError verifies that a SetMounts
// failure is surfaced.
func TestEnsure_SetMountsError_ReturnsWrappedError(t *testing.T) {
	ctx := context.Background()
	fk := fake.New()
	fk.AddMachine("avr-ubuntu-24.04-arm64", selector, types.KindShared, types.StateRunning)

	// Program SetMounts to fail.
	fk.FailOn(fake.OpSetMounts, errors.New("cannot edit configuration"))

	newMount := types.MountSpec{HostPath: "/Users/dev/proj-a", GuestPath: "/Users/dev/proj-a", Writable: true}
	_, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", newMount, newMount.GuestPath, 0, types.DiscardProgress)
	if err == nil {
		t.Fatal("Ensure succeeded when SetMounts should have failed")
	}
}

// TestEnsure_ProgressEvents_Delivered verifies that progress events are
// forwarded to the sink.
func TestEnsure_ProgressEvents_Delivered(t *testing.T) {
	ctx := context.Background()
	fk := fake.New()
	fk.AddMachine("avr-ubuntu-24.04-arm64", selector, types.KindShared, types.StateRunning)

	existing := types.MountSpec{HostPath: "/Users/dev/proj-a", GuestPath: "/Users/dev/proj-a", Writable: true}
	mustSetMounts(t, ctx, fk, "avr-ubuntu-24.04-arm64", []types.MountSpec{existing})
	fk.Reset()

	// Collect progress events through our own sink.
	var events []types.ProgressEvent
	sink := types.ProgressFunc(func(e types.ProgressEvent) {
		events = append(events, e)
	})

	newMount := types.MountSpec{HostPath: "/Users/dev/proj-b", GuestPath: "/Users/dev/proj-b", Writable: true}
	_, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", newMount, newMount.GuestPath, 0, sink)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(events) == 0 {
		t.Error("no events delivered to the progress sink")
	}
}

// TestEnsure_MachineNotOwned_ReturnsError_PROP_6 verifies that the ownership
// guard is applied: an operation on a machine avar does not own is refused.
func TestEnsure_MachineNotOwned_ReturnsError_PROP_6(t *testing.T) {
	ctx := context.Background()
	fk := fake.New()
	fk.AddForeignMachine("avr-foreign-00")

	mount := types.MountSpec{HostPath: "/Users/dev/proj-a", GuestPath: "/Users/dev/proj-a", Writable: true}
	_, err := mounts.Ensure(ctx, fk, "avr-foreign-00", mount, mount.GuestPath, 0, types.DiscardProgress)
	if !errors.Is(err, provider.ErrNotOwned) {
		t.Fatalf("error = %v, want ErrNotOwned", err)
	}
}

// TestEnsure_WarmPathNoRestart_PROP_5_Warm confirms that visiting a known
// project causes no restart. The Fake records a restart count per machine,
// and a warm visit must leave it unchanged.
func TestEnsure_WarmPathNoRestart_PROP_5_Warm(t *testing.T) {
	ctx := context.Background()
	fk := fake.New()
	fk.AddMachine("avr-ubuntu-24.04-arm64", selector, types.KindShared, types.StateRunning)

	existing := types.MountSpec{HostPath: "/Users/dev/proj-a", GuestPath: "/Users/dev/proj-a", Writable: true}
	mustSetMounts(t, ctx, fk, "avr-ubuntu-24.04-arm64", []types.MountSpec{existing})
	restartsBefore := fk.Restarts("avr-ubuntu-24.04-arm64")
	fk.Reset()

	_, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", existing, existing.GuestPath, 0, types.DiscardProgress)
	if err != nil {
		t.Fatalf("Ensure on already-shared project: %v", err)
	}
	if fk.Restarts("avr-ubuntu-24.04-arm64") != restartsBefore {
		t.Error("warm path restarted the machine")
	}
}

// mustSetMounts applies mounts to a Fake and fails the test on error.
func mustSetMounts(t *testing.T, ctx context.Context, fk *fake.Fake, machine string, mounts []types.MountSpec) {
	t.Helper()
	if err := fk.SetMounts(ctx, machine, mounts, types.DiscardProgress); err != nil {
		t.Fatalf("seed mounts for %s: %v", machine, err)
	}
}
