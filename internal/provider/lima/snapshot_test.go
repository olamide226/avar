//go:build unix

// See shell_test.go: LimaProvider is a macOS backend, so its behaviour tests
// run where it runs.

package lima

// Lima's snapshots are a QEMU feature, so these tests run against an emulated
// machine — the kind `avr --arch amd64` creates. A native vz machine cannot be
// snapshotted at all, which TestSnapshot_RefusedOnAMachineThatCannotDoIt covers.
import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

func TestSnapshot_CapturesANamedSnapshot_REQ_10_1(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-qemu-running.json"))
	runner.snapshotListOut = []byte("NAME\tCREATED\n")
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-amd64")))
	sink := &recordingSink{}

	if err := p.Snapshot(context.Background(), "avr-ubuntu-24.04-amd64", "before-experiment", sink); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	assertArgvs(t, runner.limactlArgvs(), []string{
		"limactl list --json",
		"limactl snapshot list avr-ubuntu-24.04-amd64",
		"limactl stop avr-ubuntu-24.04-amd64",
		"limactl snapshot create avr-ubuntu-24.04-amd64 --tag before-experiment",
		"limactl start --tty=false avr-ubuntu-24.04-amd64",
	})

	// The user was told what was happening.
	kinds := sink.kinds()
	if len(kinds) != 3 {
		t.Fatalf("want 3 progress events (stopping, creating, starting), got %d: %v", len(kinds), kinds)
	}
	if kinds[0] != types.ProgressStopping || kinds[1] != types.ProgressCreating || kinds[2] != types.ProgressStarting {
		t.Errorf("want Stopping, Creating, Starting; got %v", kinds)
	}
}

func TestSnapshot_NoOpWhenMachineIsAlreadyStopped_REQ_10_1(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-qemu-running.json"))
	runner.snapshotListOut = []byte("NAME\tCREATED\n")
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-fedora-42-amd64")))
	sink := &recordingSink{}

	if err := p.Snapshot(context.Background(), "avr-fedora-42-amd64", "stopped-snap", sink); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	assertArgvs(t, runner.limactlArgvs(), []string{
		"limactl list --json",
		"limactl snapshot list avr-fedora-42-amd64",
		"limactl snapshot create avr-fedora-42-amd64 --tag stopped-snap",
	})
	// No stop, no start: the machine was not running.
	kinds := sink.kinds()
	if len(kinds) != 1 || kinds[0] != types.ProgressCreating {
		t.Errorf("want exactly one Creating event for a stopped machine, got %v", kinds)
	}
}

func TestSnapshot_RefusesDuplicateName_REQ_10_1(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-qemu-running.json"))
	runner.snapshotListOut = []byte("NAME\tCREATED\nbefore-experiment\t2024-06-01 12:00:00 +0000 UTC\n")
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-amd64")))

	err := p.Snapshot(context.Background(), "avr-ubuntu-24.04-amd64", "before-experiment", types.DiscardProgress)
	if err == nil {
		t.Fatal("a duplicate snapshot name was accepted")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("the error does not say the name is taken: %v", err)
	}
	// The machine was never stopped because the name check comes first.
	for _, argv := range runner.limactlArgvs() {
		if strings.Contains(argv, "stop") {
			t.Fatalf("the machine was stopped for a snapshot that was never going to be created: %v", runner.limactlArgvs())
		}
	}
}

func TestSnapshot_FailedCreateRestartsTheMachine(t *testing.T) {
	snapFailed := errors.New("exit status 1: snapshot create failed")
	runner := newFakeRunner().listing(fixture(t, "list-qemu-running.json"))
	runner.snapshotListOut = []byte("NAME\tCREATED\n")
	runner.failOn("snapshot create", snapFailed)
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-amd64")))

	err := p.Snapshot(context.Background(), "avr-ubuntu-24.04-amd64", "doomed", types.DiscardProgress)
	if !errors.Is(err, snapFailed) {
		t.Fatalf("want the snapshot failure wrapped, got: %v", err)
	}

	// The machine was running, so it must be restarted even though the
	// snapshot itself failed.
	assertArgvs(t, runner.limactlArgvs(), []string{
		"limactl list --json",
		"limactl snapshot list avr-ubuntu-24.04-amd64",
		"limactl stop avr-ubuntu-24.04-amd64",
		"limactl snapshot create avr-ubuntu-24.04-amd64 --tag doomed",
		"limactl start --tty=false avr-ubuntu-24.04-amd64",
	})
}

func TestRestoreSnapshot_RestoresANamedSnapshot_REQ_10_2(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-qemu-running.json"))
	runner.snapshotListOut = []byte("NAME\tCREATED\nbefore-experiment\t2024-06-01 12:00:00 +0000 UTC\n")
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-amd64")))
	sink := &recordingSink{}

	if err := p.RestoreSnapshot(context.Background(), "avr-ubuntu-24.04-amd64", "before-experiment", sink); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	assertArgvs(t, runner.limactlArgvs(), []string{
		"limactl list --json",
		"limactl snapshot list avr-ubuntu-24.04-amd64",
		"limactl stop avr-ubuntu-24.04-amd64",
		"limactl snapshot apply avr-ubuntu-24.04-amd64 --tag before-experiment",
		"limactl start --tty=false avr-ubuntu-24.04-amd64",
	})

	kinds := sink.kinds()
	if len(kinds) != 3 {
		t.Fatalf("want 3 progress events, got %d: %v", len(kinds), kinds)
	}
}

func TestRestoreSnapshot_UnknownNameIsErrSnapshotNotFound_REQ_10_2(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-qemu-running.json"))
	runner.snapshotListOut = []byte("NAME\tCREATED\nonly-snap\t2024-06-01 12:00:00 +0000 UTC\n")
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-amd64")))

	err := p.RestoreSnapshot(context.Background(), "avr-ubuntu-24.04-amd64", "nonexistent", types.DiscardProgress)
	if !errors.Is(err, provider.ErrSnapshotNotFound) {
		t.Fatalf("want ErrSnapshotNotFound, got: %v", err)
	}
	// The machine was never stopped — the name check comes first.
	for _, argv := range runner.limactlArgvs() {
		if strings.Contains(argv, "stop") {
			t.Fatalf("the machine was stopped for a snapshot that does not exist: %v", runner.limactlArgvs())
		}
	}
}

func TestRestoreSnapshot_FailedApplyRestartsTheMachine(t *testing.T) {
	snapFailed := errors.New("exit status 1: snapshot apply failed")
	runner := newFakeRunner().listing(fixture(t, "list-qemu-running.json"))
	runner.snapshotListOut = []byte("NAME\tCREATED\nbefore-experiment\t2024-06-01 12:00:00 +0000 UTC\n")
	runner.failOn("snapshot apply", snapFailed)
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-amd64")))

	err := p.RestoreSnapshot(context.Background(), "avr-ubuntu-24.04-amd64", "before-experiment", types.DiscardProgress)
	if !errors.Is(err, snapFailed) {
		t.Fatalf("want the snapshot failure wrapped, got: %v", err)
	}
	// Machine was restarted despite the failure.
	assertArgvs(t, runner.limactlArgvs(), []string{
		"limactl list --json",
		"limactl snapshot list avr-ubuntu-24.04-amd64",
		"limactl stop avr-ubuntu-24.04-amd64",
		"limactl snapshot apply avr-ubuntu-24.04-amd64 --tag before-experiment",
		"limactl start --tty=false avr-ubuntu-24.04-amd64",
	})
}

func TestRestoreSnapshot_NoOpWhenMachineIsAlreadyStopped_REQ_10_2(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-qemu-running.json"))
	runner.snapshotListOut = []byte("NAME\tCREATED\nbefore-experiment\t2024-06-01 12:00:00 +0000 UTC\n")
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-fedora-42-amd64")))
	sink := &recordingSink{}

	if err := p.RestoreSnapshot(context.Background(), "avr-fedora-42-amd64", "before-experiment", sink); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}

	// No stop, no start.
	assertArgvs(t, runner.limactlArgvs(), []string{
		"limactl list --json",
		"limactl snapshot list avr-fedora-42-amd64",
		"limactl snapshot apply avr-fedora-42-amd64 --tag before-experiment",
	})
}

func TestListSnapshots_ListsInChronologicalOrder_REQ_10_4(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-qemu-running.json"))
	runner.snapshotListOut = []byte("NAME\tCREATED\nsecond\t2024-06-02 12:00:00 +0000 UTC\nfirst\t2024-06-01 12:00:00 +0000 UTC\n")
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-amd64")))

	snaps, err := p.ListSnapshots(context.Background(), "avr-ubuntu-24.04-amd64")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}

	assertArgvs(t, runner.limactlArgvs(), []string{
		"limactl list --json",
		"limactl snapshot list avr-ubuntu-24.04-amd64",
	})

	if len(snaps) != 2 {
		t.Fatalf("want 2 snapshots, got %d: %v", len(snaps), snaps)
	}
	// Returned oldest first.
	if snaps[0].Name != "first" || snaps[1].Name != "second" {
		t.Errorf("snapshots not in chronological order: %v", snaps)
	}
}

func TestListSnapshots_EmptyListIsNotAnError(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-qemu-running.json"))
	runner.snapshotListOut = []byte("NAME\tCREATED\n")
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-amd64")))

	snaps, err := p.ListSnapshots(context.Background(), "avr-ubuntu-24.04-amd64")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 0 {
		t.Errorf("want no snapshots, got %v", snaps)
	}
}

func TestSnapshotAndRestore_RefuseUnownedMachine_PROP_6(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-foreign-only.json"))
	p := newTestProvider(t, runner, newFakeRecords())

	t.Run("snapshot", func(t *testing.T) {
		err := p.Snapshot(context.Background(), "avr-ubuntu-24.04-amd64", "test", types.DiscardProgress)
		if !errors.Is(err, provider.ErrNotOwned) {
			t.Fatalf("want ErrNotOwned, got: %v", err)
		}
	})

	t.Run("restore", func(t *testing.T) {
		err := p.RestoreSnapshot(context.Background(), "avr-ubuntu-24.04-amd64", "test", types.DiscardProgress)
		if !errors.Is(err, provider.ErrNotOwned) {
			t.Fatalf("want ErrNotOwned, got: %v", err)
		}
	})

	t.Run("list", func(t *testing.T) {
		_, err := p.ListSnapshots(context.Background(), "avr-ubuntu-24.04-amd64")
		if !errors.Is(err, provider.ErrNotOwned) {
			t.Fatalf("want ErrNotOwned, got: %v", err)
		}
	})
}

func TestParseSnapshotList(t *testing.T) {
	t.Run("standard-output", func(t *testing.T) {
		out := []byte("NAME\tCREATED\ns1\t2024-06-01 12:00:00 +0000 UTC\ns2\t2024-06-02 12:00:00 +0000 UTC\n")
		snaps, err := parseSnapshotList(out)
		if err != nil {
			t.Fatalf("parseSnapshotList: %v", err)
		}
		if len(snaps) != 2 {
			t.Fatalf("want 2 snapshots, got %d", len(snaps))
		}
		if snaps[0].Name != "s1" || snaps[1].Name != "s2" {
			t.Errorf("wrong names: %v", snaps)
		}
	})

	t.Run("empty-output", func(t *testing.T) {
		snaps, err := parseSnapshotList(nil)
		if err != nil || len(snaps) != 0 {
			t.Errorf("empty output: got %v, %v", snaps, err)
		}
	})

	t.Run("header-only", func(t *testing.T) {
		snaps, err := parseSnapshotList([]byte("NAME\tCREATED\n"))
		if err != nil || len(snaps) != 0 {
			t.Errorf("header only: got %v, %v", snaps, err)
		}
	})

	t.Run("with-subsecond-precision", func(t *testing.T) {
		out := []byte("NAME\tCREATED\ns1\t2024-06-01 12:00:00.123456789 +0000 UTC\n")
		snaps, err := parseSnapshotList(out)
		if err != nil {
			t.Fatalf("parseSnapshotList: %v", err)
		}
		if len(snaps) != 1 {
			t.Fatalf("want 1 snapshot, got %d", len(snaps))
		}
	})

	t.Run("sorts-oldest-first", func(t *testing.T) {
		out := []byte("NAME\tCREATED\njun\t2024-06-01 12:00:00 +0000 UTC\njan\t2024-01-01 12:00:00 +0000 UTC\n")
		snaps, err := parseSnapshotList(out)
		if err != nil {
			t.Fatalf("parseSnapshotList: %v", err)
		}
		if snaps[0].Name != "jan" || snaps[1].Name != "jun" {
			t.Errorf("not sorted: %v", snaps)
		}
	})
}

// TestProp_SnapshotPreservesRunningState verifies that the machine ends in the
// same state it started in, regardless of whether the snapshot succeeds.
func TestProp_SnapshotPreservesRunningState(t *testing.T) {
	t.Run("successful-snapshot", func(t *testing.T) {
		runner := newFakeRunner().listing(fixture(t, "list-qemu-running.json"))
		runner.snapshotListOut = []byte("NAME\tCREATED\n")
		p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-amd64")))

		// Before: running. After successful capture: running again.
		if err := p.Snapshot(context.Background(), "avr-ubuntu-24.04-amd64", "s1", types.DiscardProgress); err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		// The last action was a start.
		last := runner.limactlArgvs()[len(runner.limactlArgvs())-1]
		if !strings.Contains(last, "start") {
			t.Errorf("the machine was not restarted after a successful snapshot: %v", runner.limactlArgvs())
		}
	})

	t.Run("successful-snapshot-on-stopped-machine", func(t *testing.T) {
		runner := newFakeRunner().listing(fixture(t, "list-qemu-running.json"))
		runner.snapshotListOut = []byte("NAME\tCREATED\n")
		p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-fedora-42-amd64")))
		// Machine is stopped in the fixture.

		if err := p.Snapshot(context.Background(), "avr-fedora-42-amd64", "s1", types.DiscardProgress); err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		// No stop, no start — the machine stays stopped.
		for _, argv := range runner.limactlArgvs() {
			if strings.Contains(argv, "start") {
				t.Errorf("a stopped machine was started after a snapshot: %v", runner.limactlArgvs())
			}
			if strings.Contains(argv, "stop") {
				t.Errorf("a stopped machine was stopped before a snapshot: %v", runner.limactlArgvs())
			}
		}
	})
}

func TestParseSnapshotTime(t *testing.T) {
	cases := []struct {
		in    string
		valid bool
	}{
		{"2024-06-01 12:00:00 +0000 UTC", true},
		{"2024-06-01 12:00:00.123456789 +0000 UTC", true},
		{"2024-06-01T12:00:00Z", true},
		{"not-a-timestamp", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%q", tc.in), func(t *testing.T) {
			_, err := parseSnapshotTime(tc.in)
			if tc.valid && err != nil {
				t.Errorf("want nil error, got %v", err)
			}
			if !tc.valid && err == nil {
				t.Errorf("want error, got nil")
			}
		})
	}
}

// Lima's snapshots are a QEMU feature. avar runs native-architecture machines
// under vz on purpose — that is what gives VirtioFS speed and Rosetta — so on
// an Apple Silicon Mac the everyday machine is exactly the one that cannot be
// snapshotted, and Lima's own answer is the single word "unimplemented".
//
// Refusing here, before anything is stopped, is what lets the command layer
// say why and what to do instead.
func TestSnapshot_RefusedOnAMachineThatCannotDoIt_REQ_10_1(t *testing.T) {
	const vzMachine = "avr-ubuntu-24.04-arm64" // vz in the fixture

	for _, tc := range []struct {
		name string
		call func(p *Provider) error
	}{
		{"capture", func(p *Provider) error {
			return p.Snapshot(context.Background(), vzMachine, "snap", &recordingSink{})
		}},
		{"restore", func(p *Provider) error {
			return p.RestoreSnapshot(context.Background(), vzMachine, "snap", &recordingSink{})
		}},
		{"list", func(p *Provider) error {
			_, err := p.ListSnapshots(context.Background(), vzMachine)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := newFakeRunner().listing(fixture(t, "list-qemu-running.json"))
			p := newTestProvider(t, runner, newFakeRecords(ownedRecord(vzMachine)))

			err := tc.call(p)
			if !errors.Is(err, provider.ErrUnsupportedCapability) {
				t.Fatalf("want ErrUnsupportedCapability, got %v", err)
			}

			// Nothing may have been done to the machine: a refusal that had
			// already stopped the guest would be worse than the failure it
			// is reporting.
			for _, argv := range runner.limactlArgvs() {
				if strings.Contains(argv, "snapshot") || strings.Contains(argv, "stop") {
					t.Errorf("the machine was acted on before the refusal: %q", argv)
				}
			}
		})
	}
}
