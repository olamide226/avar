package mounts_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
	existing := types.MountSpec{HostPath: hostPath("/Users/dev/proj-a"), GuestPath: "/Users/dev/proj-a", Writable: true}
	mustSetMounts(t, ctx, fk, "avr-ubuntu-24.04-arm64", []types.MountSpec{existing})
	fk.Reset()

	// The project is already shared — so Ensure should return immediately.
	result, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", existing, existing.GuestPath, 0, nil, types.DiscardProgress)
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

	existing := types.MountSpec{HostPath: hostPath("/Users/dev/proj-a"), GuestPath: "/Users/dev/proj-a", Writable: true}
	mustSetMounts(t, ctx, fk, "avr-ubuntu-24.04-arm64", []types.MountSpec{existing})
	fk.Reset()

	// Add a second project.
	newMount := types.MountSpec{HostPath: hostPath("/Users/dev/proj-b"), GuestPath: "/Users/dev/proj-b", Writable: true}
	result, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", newMount, newMount.GuestPath, 0, nil, types.DiscardProgress)
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
		mount := mountFor(p)
		_, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", mount, mount.GuestPath, 0, nil, types.DiscardProgress)
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
		if applied[i].HostPath != hostPath(want) {
			t.Errorf("applied[%d] = %s, want %s", i, applied[i].HostPath, hostPath(want))
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

	existing := types.MountSpec{HostPath: hostPath("/Users/dev/proj-a"), GuestPath: "/Users/dev/proj-a", Writable: true}
	mustSetMounts(t, ctx, fk, "avr-ubuntu-24.04-arm64", []types.MountSpec{existing})
	fk.Reset()

	// Three other sessions are attached.
	newMount := types.MountSpec{HostPath: hostPath("/Users/dev/proj-b"), GuestPath: "/Users/dev/proj-b", Writable: true}
	result, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", newMount, newMount.GuestPath, 3, nil, types.DiscardProgress)
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
	existing := mountFor("/Users/dev/proj-a")
	mustSetMounts(t, ctx, fk, "avr-ubuntu-24.04-arm64", []types.MountSpec{existing})
	fk.Reset()

	// The mount spec is the same (project root), but guestCwd points deeper.
	// This is what MapProjectPath returns when the user is in a subdirectory.
	// The guest working directory is a Linux path, so it is joined with path
	// rather than filepath: on Windows the latter would build it with backslashes
	// and describe no directory the guest has (REQ-18.5).
	guestCwd := path.Join(existing.GuestPath, "a", "b")
	result, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", existing, guestCwd, 0, nil, types.DiscardProgress)
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

	newMount := types.MountSpec{HostPath: hostPath("/Users/dev/proj-a"), GuestPath: "/Users/dev/proj-a", Writable: true}
	_, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", newMount, newMount.GuestPath, 0, nil, types.DiscardProgress)
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
	_, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", types.MountSpec{HostPath: hostPath("/x"), GuestPath: "/x"}, "/x", 0, nil, types.DiscardProgress)
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

	newMount := types.MountSpec{HostPath: hostPath("/Users/dev/proj-a"), GuestPath: "/Users/dev/proj-a", Writable: true}
	_, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", newMount, newMount.GuestPath, 0, nil, types.DiscardProgress)
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

	existing := types.MountSpec{HostPath: hostPath("/Users/dev/proj-a"), GuestPath: "/Users/dev/proj-a", Writable: true}
	mustSetMounts(t, ctx, fk, "avr-ubuntu-24.04-arm64", []types.MountSpec{existing})
	fk.Reset()

	// Collect progress events through our own sink.
	var events []types.ProgressEvent
	sink := types.ProgressFunc(func(e types.ProgressEvent) {
		events = append(events, e)
	})

	newMount := types.MountSpec{HostPath: hostPath("/Users/dev/proj-b"), GuestPath: "/Users/dev/proj-b", Writable: true}
	_, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", newMount, newMount.GuestPath, 0, nil, sink)
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

	mount := types.MountSpec{HostPath: hostPath("/Users/dev/proj-a"), GuestPath: "/Users/dev/proj-a", Writable: true}
	_, err := mounts.Ensure(ctx, fk, "avr-foreign-00", mount, mount.GuestPath, 0, nil, types.DiscardProgress)
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

	existing := types.MountSpec{HostPath: hostPath("/Users/dev/proj-a"), GuestPath: "/Users/dev/proj-a", Writable: true}
	mustSetMounts(t, ctx, fk, "avr-ubuntu-24.04-arm64", []types.MountSpec{existing})
	restartsBefore := fk.Restarts("avr-ubuntu-24.04-arm64")
	fk.Reset()

	_, err := mounts.Ensure(ctx, fk, "avr-ubuntu-24.04-arm64", existing, existing.GuestPath, 0, nil, types.DiscardProgress)
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

// A machine that shares too many directories cannot boot at all, and the
// failure gives no hint that mounts caused it. The cap is what keeps a user
// who works across many projects from reaching that state.
func TestEnsure_UnsharesTheLeastRecentlyUsedProjectAtTheLimit_REQ_6_1(t *testing.T) {
	ctx := context.Background()
	fk := fake.New()
	const machine = "avr-ubuntu-24.04-arm64"
	fk.AddMachine(machine, selector, types.KindShared, types.StateRunning)

	// Fill the machine to the cap, oldest first.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lastUsed := map[string]time.Time{}
	applied := make([]types.MountSpec, 0, mounts.MaxMounts)
	for i := 0; i < mounts.MaxMounts; i++ {
		mount := mountFor(fmt.Sprintf("/Users/dev/p%02d", i))
		applied = append(applied, mount)
		lastUsed[mount.HostPath] = base.Add(time.Duration(i) * time.Hour)
	}
	if err := fk.SetMounts(ctx, machine, applied, types.DiscardProgress); err != nil {
		t.Fatal(err)
	}
	fk.Reset()

	// Entering one more must not push the machine over the limit.
	newProject := types.MountSpec{HostPath: hostPath("/Users/dev/new"), GuestPath: "/Users/dev/new", Writable: true}
	lastUsed[newProject.HostPath] = base.Add(1000 * time.Hour)

	res, err := mounts.Ensure(ctx, fk, machine, newProject, newProject.GuestPath, 0, lastUsed, types.DiscardProgress)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	call := fk.AssertCalled(t, fake.OpSetMounts)
	if len(call.Mounts) > mounts.MaxMounts {
		t.Errorf("applied %d mounts, over the limit of %d — this machine would not start", len(call.Mounts), mounts.MaxMounts)
	}

	// The oldest went, and it is reported so the user is not left wondering
	// where a directory went.
	if len(res.Unshared) != 1 || res.Unshared[0] != hostPath("/Users/dev/p00") {
		t.Errorf("unshared %v, want the least recently used /Users/dev/p00", res.Unshared)
	}

	// The project being entered must still be there: a shell at a path that
	// is not shared is the failure REQ-6.5 exists to prevent.
	var kept bool
	for _, m := range call.Mounts {
		if m.HostPath == newProject.HostPath {
			kept = true
		}
		if m.HostPath == hostPath("/Users/dev/p00") {
			t.Error("the evicted project was still applied")
		}
	}
	if !kept {
		t.Error("the project being entered was not shared")
	}
}

// Below the limit nothing is given up, which is the ordinary case and must
// stay free of surprises.
func TestEnsure_KeepsEveryProjectBelowTheLimit_REQ_6_1(t *testing.T) {
	ctx := context.Background()
	fk := fake.New()
	const machine = "avr-ubuntu-24.04-arm64"
	fk.AddMachine(machine, selector, types.KindShared, types.StateRunning)

	applied := []types.MountSpec{{HostPath: hostPath("/Users/dev/a"), GuestPath: "/Users/dev/a", Writable: true}}
	if err := fk.SetMounts(ctx, machine, applied, types.DiscardProgress); err != nil {
		t.Fatal(err)
	}
	fk.Reset()

	newProject := types.MountSpec{HostPath: hostPath("/Users/dev/b"), GuestPath: "/Users/dev/b", Writable: true}
	res, err := mounts.Ensure(ctx, fk, machine, newProject, newProject.GuestPath, 0, nil, types.DiscardProgress)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(res.Unshared) != 0 {
		t.Errorf("gave up %v while below the limit", res.Unshared)
	}
	if call := fk.AssertCalled(t, fake.OpSetMounts); len(call.Mounts) != 2 {
		t.Errorf("applied %d mounts, want both", len(call.Mounts))
	}
}

// hostPath renders a POSIX-shaped test path in the host's own vocabulary.
//
// A mount's host path is absolute in the host's syntax by definition, so a
// fixture that hard-codes "/Users/dev/proj-a" is a macOS fixture: on Windows
// that string is relative and is rightly refused. Prefixing the drive keeps
// each case testing the rule it was written for rather than the platform's idea
// of "absolute" (REQ-18.13).
func hostPath(posix string) string {
	if runtime.GOOS != "windows" {
		return posix
	}
	return `C:` + filepath.FromSlash(posix)
}

// mountFor is the mapping a provider would plan for the project directory whose
// POSIX-shaped name is posix: the host half in the host's own vocabulary, the
// guest half always a Linux path. On macOS the two are the identical string,
// which is what Lima applies; on Windows they differ, which is what WSL has to
// do (REQ-6.1, REQ-18.5).
func mountFor(posix string) types.MountSpec {
	return types.MountSpec{HostPath: hostPath(posix), GuestPath: posix, Writable: true}
}
