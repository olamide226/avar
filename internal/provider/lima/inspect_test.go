package lima

import (
	"reflect"
	"testing"

	"github.com/olamide226/avar/internal/types"
)

func TestParseInstances_ReadsJSONLines(t *testing.T) {
	// `limactl list --json` renders each instance through a Go template and
	// prints one JSON object per line, rather than a JSON array. Confirmed
	// against limactl 2.2.0's list command, which still documents "Each line
	// contains the JSON object for one Lima instance".
	got, err := parseInstances(fixture(t, "list-mixed.json"))
	if err != nil {
		t.Fatalf("parseInstances: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("parsed %d instances, want 4", len(got))
	}
	if got[0].Name != "avr-ubuntu-24.04-arm64" || got[0].Status != "Running" {
		t.Errorf("first instance = %+v", got[0])
	}
	if got[0].Memory != 8589934592 {
		t.Errorf("memory = %d bytes, want 8 GiB", got[0].Memory)
	}
	wantMounts := []types.MountSpec{
		{HostPath: "/Users/dev/code/api", GuestPath: "/Users/dev/code/api", Writable: true},
		{HostPath: "/Users/dev/code/web", GuestPath: "/Users/dev/code/web", Writable: true},
	}
	if !reflect.DeepEqual(got[0].mounts(), wantMounts) {
		t.Errorf("mounts = %v, want %v", got[0].mounts(), wantMounts)
	}
}

func TestParseInstances_EmptyOutputIsAnEmptyList_REQ_5_3(t *testing.T) {
	// With no instances, limactl writes nothing to stdout, warns on stderr and
	// exits zero. Empty output is the answer, not a failure. Confirmed by
	// running `limactl list --json` against limactl 2.2.0 with no instances.
	for _, out := range [][]byte{nil, {}, []byte("   \n\n")} {
		got, err := parseInstances(out)
		if err != nil {
			t.Fatalf("parseInstances(%q): %v", out, err)
		}
		if len(got) != 0 {
			t.Errorf("parseInstances(%q) = %v", out, got)
		}
	}
}

func TestParseInstances_AlsoReadsAJSONArray(t *testing.T) {
	// Defensive: if a future Lima switches to array output, the failure mode
	// of guessing wrong is that avar sees no machines and tries to provision
	// one that already exists.
	got, err := parseInstances(fixture(t, "list-array.json"))
	if err != nil {
		t.Fatalf("parseInstances: %v", err)
	}
	if len(got) != 1 || got[0].Name != "avr-ubuntu-24.04-arm64" {
		t.Fatalf("parsed %+v", got)
	}
}

func TestParseInstances_RejectsOutputItCannotRead(t *testing.T) {
	if _, err := parseInstances([]byte("limactl: command not found")); err == nil {
		t.Fatal("output that is not JSON was accepted as an instance list")
	}
}

func TestSortedUniqueMounts_IsStableAndDeduplicated(t *testing.T) {
	b := types.MountSpec{HostPath: "/b", GuestPath: "/b", Writable: true}
	a := types.MountSpec{HostPath: "/a", GuestPath: "/a", Writable: true}
	if got := sortedUniqueMounts([]types.MountSpec{b, a, b}); !reflect.DeepEqual(got, []types.MountSpec{a, b}) {
		t.Errorf("sortedUniqueMounts = %v", got)
	}
	if got := sortedUniqueMounts(nil); got != nil {
		t.Errorf("sortedUniqueMounts(nil) = %v, want nil so comparisons need not distinguish nil from empty", got)
	}
}

// Reading is more tolerant than writing: a hand-edited instance whose mount
// point is not its location is reported as it stands, so that avar can see the
// difference and replace the set rather than being unable to look.
func TestInstanceMounts_ReportsTheGuestPathLimaActuallyHas(t *testing.T) {
	inst := instance{Config: &instanceLimaConfig{Mounts: []instanceMount{
		{Location: "/Users/dev/code/api", MountPoint: "/somewhere/else", Writable: true},
		{Location: "/Users/dev/code/web"},
		{MountPoint: "/no/location"},
	}}}
	want := []types.MountSpec{
		{HostPath: "/Users/dev/code/api", GuestPath: "/somewhere/else", Writable: true},
		// An omitted mountPoint is Lima's own default: the location itself.
		{HostPath: "/Users/dev/code/web", GuestPath: "/Users/dev/code/web"},
	}
	if got := inst.mounts(); !reflect.DeepEqual(got, want) {
		t.Errorf("mounts = %v, want %v", got, want)
	}
}
