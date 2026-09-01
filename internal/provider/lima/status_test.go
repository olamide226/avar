//go:build unix

// See shell_test.go: LimaProvider is a macOS backend, so its behaviour tests
// run where it runs.

package lima

import (
	"context"
	"reflect"
	"testing"

	"github.com/olamide226/avar/internal/types"
)

func TestStatus_FiltersForeignInstances_PROP_6(t *testing.T) {
	// The fixture holds two Lima instances the user created themselves —
	// "default" and "colima". Neither may appear, because `avr stop --all` is
	// built directly on this result.
	runner := newFakeRunner().listing(fixture(t, "list-foreign-only.json"))
	p := newTestProvider(t, runner, newFakeRecords())

	got, err := p.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("machines avar does not own were listed: %+v", got)
	}
}

func TestStatus_ListsOnlyAvarMachines_REQ_5_4(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	p := newTestProvider(t, runner, newFakeRecords(
		ownedRecord("avr-ubuntu-24.04-arm64"),
		ownedRecord("avr-fedora-42-amd64"),
		ownedRecord("avr-debian-12-arm64"),
	))

	got, err := p.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	// Sorted by name, and the user's own "default" instance is absent.
	var names []string
	for _, m := range got {
		names = append(names, m.Name)
	}
	want := []string{"avr-debian-12-arm64", "avr-fedora-42-amd64", "avr-ubuntu-24.04-arm64"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("Status listed %v, want %v", names, want)
	}
}

func TestStatus_ReportsStateResourcesAndMounts_REQ_5_1(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-arm64")))

	got, err := p.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var running types.MachineStatus
	for _, m := range got {
		if m.Name == "avr-ubuntu-24.04-arm64" {
			running = m
		}
	}
	if running.State != types.StateRunning {
		t.Errorf("state = %q, want running", running.State)
	}
	if running.CPUs != 4 {
		t.Errorf("cpus = %d, want 4", running.CPUs)
	}
	if running.MemoryGB != 8 {
		t.Errorf("memory = %v GiB, want 8", running.MemoryGB)
	}
	if running.DiskGB != 100 {
		t.Errorf("disk = %v GiB, want 100", running.DiskGB)
	}
	if running.Runtime != "vz" {
		t.Errorf("runtime = %q, want vz", running.Runtime)
	}
	// The backend a listing came from is the one thing every entry can always
	// say for itself, and it is what a record adopted from this listing is
	// stamped with (REQ-18.14).
	if running.Provider != types.ProviderLima {
		t.Errorf("provider = %q, want %q", running.Provider, types.ProviderLima)
	}
	wantMounts := shares("/Users/dev/code/api", "/Users/dev/code/web")
	if !reflect.DeepEqual(running.Mounts, wantMounts) {
		t.Errorf("mounts = %v, want %v", running.Mounts, wantMounts)
	}
	if running.Kind != types.KindShared {
		t.Errorf("kind = %q, want shared", running.Kind)
	}
}

func TestStatus_IsEmptyWhenAvarOwnsNothing_REQ_5_3(t *testing.T) {
	// `limactl list --json` writes nothing at all to stdout when Lima has no
	// instances: the "no instance found" line is a warning on stderr and the
	// exit status is zero. Confirmed against limactl 2.2.0.
	runner := newFakeRunner().listing(fixture(t, "list-empty.json"))
	p := newTestProvider(t, runner, newFakeRecords())

	got, err := p.Status(context.Background())
	if err != nil {
		t.Fatalf("an empty Lima installation is not an error: %v", err)
	}
	if got == nil {
		t.Error("Status returned nil rather than an empty slice; the command layer distinguishes them")
	}
	if len(got) != 0 {
		t.Errorf("Status listed %+v on an empty Lima installation", got)
	}
}

func TestStatus_ShowsMachinesAvarHasNoRecordOf_PROP_7(t *testing.T) {
	// A machine created but not yet recorded is exactly what a crash mid-create
	// leaves behind. Reconciliation exists to adopt or clean up those orphans,
	// which it can only do if a listing shows them — so Status filters on the
	// avr- prefix alone, while the mutating operations keep the stricter
	// prefix-and-record guard.
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	p := newTestProvider(t, runner, newFakeRecords()) // avar has recorded nothing

	got, err := p.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var names []string
	for _, m := range got {
		names = append(names, m.Name)
	}
	want := []string{"avr-debian-12-arm64", "avr-fedora-42-amd64", "avr-ubuntu-24.04-arm64"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("unrecorded avar machines were hidden from Status: got %v, want %v", names, want)
	}
}

func TestStatus_DescribesUnrecordedMachinesFromTheirNames(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	p := newTestProvider(t, runner, newFakeRecords())

	got, err := p.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	byName := map[string]types.MachineStatus{}
	for _, m := range got {
		byName[m.Name] = m
	}

	ubuntu := byName["avr-ubuntu-24.04-arm64"]
	want := types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64}
	if ubuntu.Selector != want {
		t.Errorf("selector derived from the machine name = %+v, want %+v", ubuntu.Selector, want)
	}
	if ubuntu.Kind != types.KindShared {
		t.Errorf("kind = %q, want shared", ubuntu.Kind)
	}
}

func TestSelectorFromName_DerivesWhatItCanAndGuessesNothing(t *testing.T) {
	cases := []struct {
		name     string
		limaArch string
		want     types.EnvironmentSelector
		wantKind types.MachineKind
	}{
		{
			name:     "avr-ubuntu-24.04-arm64",
			limaArch: "aarch64",
			want:     types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64},
			wantKind: types.KindShared,
		},
		{
			name:     "avr-fedora-42-amd64",
			limaArch: "x86_64",
			want:     types.EnvironmentSelector{Distro: types.DistroFedora, Version: "42", Arch: types.ArchAMD64},
			wantKind: types.KindShared,
		},
		{
			name:     "avr-base-debian-12-arm64",
			limaArch: "aarch64",
			want:     types.EnvironmentSelector{Distro: types.DistroDebian, Version: "12", Arch: types.ArchARM64},
			wantKind: types.KindBase,
		},
		{
			// A per-project machine's name is a hash: its environment is
			// genuinely unknowable from a listing, so only what Lima itself
			// reports is filled in.
			name:     "avr-prj-3fa9c2b1de",
			limaArch: "aarch64",
			want:     types.EnvironmentSelector{Isolated: true, Arch: types.ArchARM64},
			wantKind: types.KindIsolated,
		},
		{
			// A name avar's own resolver would never produce: report the
			// architecture Lima gave and nothing else.
			name:     "avr-something-nobody-planned",
			limaArch: "aarch64",
			want:     types.EnvironmentSelector{Arch: types.ArchARM64},
			wantKind: types.KindShared,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selector, kind := describeUnrecorded(instance{Name: tc.name, Arch: tc.limaArch})
			if selector != tc.want {
				t.Errorf("selector = %+v, want %+v", selector, tc.want)
			}
			if kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", kind, tc.wantKind)
			}
		})
	}
}

func TestStatus_MachineBeingCreatedIsNotReportedBrokenOrUnknown(t *testing.T) {
	// A machine that is coming up has no record yet, so to a concurrent avr
	// invocation it is indistinguishable from a crash orphan. Reconciliation
	// deletes unrecorded machines it considers broken or unknown, so reporting
	// either here would let one invocation delete the machine another is still
	// provisioning.
	for _, limaStatus := range []string{limaStatusInstalling, limaStatusUninitialized} {
		t.Run(limaStatus, func(t *testing.T) {
			state := instance{Name: testMachine, Status: limaStatus}.state()
			if state == types.StateBroken || state == types.StateUnknown {
				t.Fatalf("Lima status %q maps to %q, which reconciliation treats as safe to delete", limaStatus, state)
			}
			if state != StateInstalling {
				t.Errorf("state = %q, want %q", state, StateInstalling)
			}
		})
	}
}

func TestStatus_RefusesToGuessWhenTheRecordsCannotBeRead(t *testing.T) {
	records := newFakeRecords()
	records.err = errStubbornRecords
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	p := newTestProvider(t, runner, records)

	if _, err := p.Status(context.Background()); err == nil {
		t.Fatal("Status succeeded despite being unable to read avar's records")
	}
}
