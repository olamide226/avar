//go:build windows

// This backend's tests are Windows tests, for the same reason the Lima
// backend's are Unix tests: they assert on Windows paths, and what counts as an
// absolute path is the host's question, not avar's. `C:\Users\ola\code\app` is
// absolute on Windows and a relative path everywhere else, so path/filepath —
// and with it MapProjectPath, MountSpec.Validate and every mount the two plan —
// answers differently off Windows. Verified by cross-compiling this package's
// tests for linux/amd64 and running them under WSL: fourteen fail, all of them
// on that one difference.
//
// Prefixing a drive letter is not available as a fix here the way it was for
// the host-neutral packages. There the fixture was a stand-in for whatever the
// host calls an absolute path; here the Windows path *is* the subject.
//
// What the macOS build claims for this package is therefore that it compiles —
// which is what keeps `avr` linkable while both backends live in one binary —
// not that a backend which cannot run there passes its behaviour tests.

package wsl2

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// REQ-10.1, REQ-18.12: a snapshot is a copy of the distribution's disk, taken as
// a VHD rather than a tar so that permissions, symbolic links and sparse files
// come back intact — a restored environment that is subtly not the one captured
// is worse than no snapshot at all.
func TestSnapshot_CapturesTheDiskNotTheFiles_REQ_10_1(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	p := newProvider(t, f, recorded(testMachine))

	if err := p.Snapshot(context.Background(), testMachine, "before-upgrade", types.DiscardProgress); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if !f.ran("--export", testMachine) {
		t.Errorf("nothing was exported: %v", f.argvs())
	}
	if !f.ran("--format", "vhd") {
		t.Errorf("the export was not a disk image: %v", f.argvs())
	}
	if _, err := os.Stat(p.snapshotPath(testMachine, "before-upgrade")); err != nil {
		t.Errorf("the snapshot file is not where avar says it is: %v", err)
	}
}

// A running distribution cannot be exported consistently, so it is stopped
// first — and put back the way it was found, with the pause explained, because
// an otherwise instant command that suspends somebody's shell has to say why.
func TestSnapshot_PausesARunningEnvironmentAndRestartsIt_REQ_10_1(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	var kinds []types.ProgressKind
	sink := types.ProgressFunc(func(e types.ProgressEvent) { kinds = append(kinds, e.Kind) })

	if err := p.Snapshot(context.Background(), testMachine, "wip", sink); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if !f.ran("--terminate", testMachine) {
		t.Errorf("a running environment was exported without being paused: %v", f.argvs())
	}
	if !f.running[testMachine] {
		t.Error("the environment was left stopped; a snapshot must leave it as it was found")
	}
	if len(kinds) == 0 || kinds[0] != types.ProgressStopping {
		t.Errorf("progress = %v, want the pause explained before it happens", kinds)
	}
}

// A stopped environment stays stopped: the same rule read from the other side.
func TestSnapshot_LeavesAStoppedEnvironmentStopped_REQ_10_1(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	p := newProvider(t, f, recorded(testMachine))

	if err := p.Snapshot(context.Background(), testMachine, "cold", types.DiscardProgress); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if f.running[testMachine] {
		t.Error("a stopped environment was started by being snapshotted")
	}
}

// Whether to replace an existing snapshot is the caller's decision. A backend
// that overwrote silently would leave `avr snapshot` nothing to ask about.
func TestSnapshot_RefusesAnExistingName_REQ_10_1(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	p := newProvider(t, f, recorded(testMachine))

	if err := p.Snapshot(context.Background(), testMachine, "same", types.DiscardProgress); err != nil {
		t.Fatal(err)
	}
	if err := p.Snapshot(context.Background(), testMachine, "same", types.DiscardProgress); err == nil {
		t.Error("an existing snapshot was silently replaced")
	}
}

// A snapshot name becomes a file name, so it is validated rather than escaped:
// a name silently rewritten is a name the user cannot restore from, and one
// containing a separator must never become a path avar writes to.
func TestSnapshot_RefusesANameThatWouldBecomeAPath(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	p := newProvider(t, f, recorded(testMachine))

	for _, name := range []string{
		`../../machines.json`,
		`..\..\machines`,
		"with/slash",
		`with\backslash`,
		"C:absolute",
		".hidden",
		"",
		strings.Repeat("x", 200),
	} {
		if err := p.Snapshot(context.Background(), testMachine, name, types.DiscardProgress); err == nil {
			t.Errorf("the name %q was accepted", name)
		}
	}
	if f.ranAny("--export") {
		t.Errorf("avar exported something for a name it should have refused: %v", f.argvs())
	}
}

// REQ-10.2: restoring replaces the environment with the captured one. WSL has no
// in-place restore, so it is unregister-then-import — and the import copies the
// disk rather than registering the file where it lies, so the same snapshot can
// be restored from again tomorrow.
func TestRestoreSnapshot_ReplacesTheEnvironmentAndKeepsTheSnapshot_REQ_10_2(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	if err := p.Snapshot(context.Background(), testMachine, "good", types.DiscardProgress); err != nil {
		t.Fatal(err)
	}
	if err := p.RestoreSnapshot(context.Background(), testMachine, "good", types.DiscardProgress); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	if !f.ran("--unregister", testMachine) {
		t.Errorf("the current environment was not removed: %v", f.argvs())
	}
	if !f.ran("--import", testMachine) || !f.ranAny("--vhd") {
		t.Errorf("the snapshot was not imported as a disk: %v", f.argvs())
	}
	if _, err := os.Stat(p.snapshotPath(testMachine, "good")); err != nil {
		t.Errorf("restoring consumed the snapshot, so it cannot be restored from again: %v", err)
	}
	if !f.running[testMachine] {
		t.Error("an environment that was running was left stopped after a restore")
	}
}

// REQ-10.2: an unknown name costs nothing and leaves the environment exactly as
// it was, so the caller can list what does exist instead of the user discovering
// their environment is gone.
func TestRestoreSnapshot_UnknownNameDestroysNothing_REQ_10_2(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	err := p.RestoreSnapshot(context.Background(), testMachine, "never-taken", types.DiscardProgress)
	if !errors.Is(err, provider.ErrSnapshotNotFound) {
		t.Fatalf("error = %v, want ErrSnapshotNotFound", err)
	}
	if f.ranAny("--unregister") {
		t.Errorf("avar removed the environment before finding out the snapshot does not exist: %v", f.argvs())
	}
	if _, still := f.registered[testMachine]; !still {
		t.Error("the environment is gone")
	}
}

// REQ-10.4: the list is what `avr snapshot` prints, newest last, with the times
// avar recorded rather than the times the filesystem happens to carry.
func TestListSnapshots_ReportsWhatWasCapturedNewestLast_REQ_10_4(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	p := newProvider(t, f, recorded(testMachine))

	for _, name := range []string{"first", "second", "third"} {
		if err := p.Snapshot(context.Background(), testMachine, name, types.DiscardProgress); err != nil {
			t.Fatalf("Snapshot(%s): %v", name, err)
		}
	}

	got, err := p.ListSnapshots(context.Background(), testMachine)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListSnapshots = %v, want all three", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].CreatedAt.Before(got[i-1].CreatedAt) {
			t.Errorf("ListSnapshots = %v, want them oldest first", got)
		}
	}
	for _, info := range got {
		if info.CreatedAt.IsZero() {
			t.Errorf("snapshot %q has no capture time, so `avr snapshot` cannot say when it was taken", info.Name)
		}
		// The sidecar is avar's bookkeeping and must not appear as a snapshot
		// the user could restore from.
		if strings.HasSuffix(info.Name, snapshotMetaExt) {
			t.Errorf("the metadata file %q was listed as a snapshot", info.Name)
		}
	}
}

// An environment that has never been snapshotted has none, which is an empty
// list rather than a failure: `avr snapshot` prints it as "no snapshots yet".
func TestListSnapshots_NoneIsNotAnError_REQ_10_4(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	p := newProvider(t, f, recorded(testMachine))

	got, err := p.ListSnapshots(context.Background(), testMachine)
	if err != nil {
		t.Fatalf("ListSnapshots on an environment with none: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListSnapshots = %v, want nothing", got)
	}
}

// PROP-6: snapshots are not a way around the ownership rule. Exporting a
// distribution avar does not own would copy somebody's whole filesystem into
// avar's state directory.
func TestSnapshotOperations_RefuseADistributionAvarDoesNotOwn_PROP_6(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register("Ubuntu", 2, true)
	p := newProvider(t, f, recorded())

	ctx := context.Background()
	_, listErr := p.ListSnapshots(ctx, "Ubuntu")
	operations := map[string]error{
		"Snapshot": p.Snapshot(ctx, "Ubuntu", "mine", types.DiscardProgress),
		"Restore":  p.RestoreSnapshot(ctx, "Ubuntu", "mine", types.DiscardProgress),
		"List":     listErr,
	}
	for name, err := range operations {
		if !errors.Is(err, provider.ErrNotOwned) {
			t.Errorf("%s = %v, want ErrNotOwned", name, err)
		}
	}
	if f.ranAny("--export") || f.ranAny("--unregister") {
		t.Errorf("avar acted on a distribution it does not own: %v", f.argvs())
	}
}

// A failed export must not leave a file that looks like a snapshot and is not:
// the user would restore from it and lose their environment.
func TestSnapshot_AFailedExportLeavesNoSnapshot_PROP_7(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	f.failOn["--export"] = errors.New("the disk is full")
	p := newProvider(t, f, recorded(testMachine))

	if err := p.Snapshot(context.Background(), testMachine, "doomed", types.DiscardProgress); err == nil {
		t.Fatal("Snapshot reported success although the export failed")
	}

	got, err := p.ListSnapshots(context.Background(), testMachine)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a failed export left %v behind", got)
	}
	if _, err := os.Stat(filepath.Join(p.snapshotDir(testMachine), "doomed"+snapshotExt)); !errors.Is(err, os.ErrNotExist) {
		t.Error("the partial export was left on disk")
	}
}

// PROP-15: a WSL 1 registration is not exported, imported, or otherwise acted
// on.
func TestSnapshot_RefusesWSL1_PROP_15(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 1, false)
	p := newProvider(t, f, recorded(testMachine))

	if err := p.Snapshot(context.Background(), testMachine, "one", types.DiscardProgress); err == nil {
		t.Fatal("a WSL 1 distribution was snapshotted")
	}
	if f.ranAny("--export") {
		t.Errorf("avar exported a WSL 1 distribution: %v", f.argvs())
	}
}

// REQ-18.12: an interrupted restore must leave a state avar can recover from.
// A restore is unregister-then-import, so the window between the two is one
// where the environment does not exist — and running the same command again has
// to finish the job rather than report that there is nothing to restore.
func TestRestoreSnapshot_IsRetryableAfterAnInterruption_REQ_18_12(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	p := newProvider(t, f, recorded(testMachine))

	if err := p.Snapshot(context.Background(), testMachine, "good", types.DiscardProgress); err != nil {
		t.Fatal(err)
	}

	// The state an interrupted restore leaves: the snapshot is on disk and the
	// distribution is not registered.
	delete(f.registered, testMachine)
	delete(f.running, testMachine)

	if err := p.RestoreSnapshot(context.Background(), testMachine, "good", types.DiscardProgress); err != nil {
		t.Fatalf("retrying a restore after an interruption: %v", err)
	}
	if _, back := f.registered[testMachine]; !back {
		t.Error("the retry did not bring the environment back")
	}
}

// REQ-9.3, PROP-5: a restored disk is checked for confinement rather than
// assumed to have it.
//
// /etc/wsl.conf travels inside the VHD, so the policy is expected to come back
// with the snapshot — but that is an assumption about a file inside an artifact
// exported some time ago, and an environment reaching a user with the Windows
// drives mounted is exactly what the provisioning check exists to prevent.
// Restore was the one path that could hand one over unasserted.
func TestRestoreSnapshot_RefusesADiskThatComesBackUnconfined_REQ_9_3(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	p := newProvider(t, f, recorded(testMachine))

	if err := p.Snapshot(context.Background(), testMachine, "good", types.DiscardProgress); err != nil {
		t.Fatal(err)
	}

	// The disk comes back with automount on, which is what a wsl.conf that did
	// not survive the round trip looks like from the guest.
	f.facts[testMachine] = "os-id=ubuntu\nos-version=24.04\n" +
		`marker={"provider":"wsl2","machine":"` + testMachine + `","user":"` + testUser + `"}` + "\n" +
		"user=" + testUser + "\nsudo=yes\nmounts=/mnt/c,/mnt/d,\n"

	err := p.RestoreSnapshot(context.Background(), testMachine, "good", types.DiscardProgress)
	if err == nil {
		t.Fatal("RestoreSnapshot accepted a guest with the Windows drives mounted")
	}
	if !strings.Contains(err.Error(), "/mnt/c") {
		t.Errorf("error = %v, want it to name what is mounted", err)
	}
	// The snapshot is still there, so the user can act on the refusal.
	if !strings.Contains(err.Error(), "good") {
		t.Errorf("error = %v, want it to say the snapshot is still available", err)
	}
}
