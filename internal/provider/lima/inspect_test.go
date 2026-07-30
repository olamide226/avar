package lima

import (
	"reflect"
	"testing"
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
	wantMounts := []string{"/Users/dev/code/api", "/Users/dev/code/web"}
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

func TestSortedUnique_IsStableAndDeduplicated(t *testing.T) {
	if got := sortedUnique([]string{"/b", "/a", "/b"}); !reflect.DeepEqual(got, []string{"/a", "/b"}) {
		t.Errorf("sortedUnique = %v", got)
	}
	if got := sortedUnique(nil); got != nil {
		t.Errorf("sortedUnique(nil) = %v, want nil so comparisons need not distinguish nil from empty", got)
	}
}
