package state

import (
	"reflect"
	"testing"

	"github.com/olamide226/avar/internal/types"
)

// What avar installed into a machine is recorded on the machine, grows like its
// mounts, survives a stale record written back, and goes with the machine.
func TestStore_InstalledPackagesOnlyGrowAndGoWithTheMachine_REQ_15_1(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)

	rec := sharedMachine("avr-ubuntu-24.04-arm64")
	if err := st.PutMachine(rec); err != nil {
		t.Fatalf("PutMachine: %v", err)
	}
	if err := st.AddPackages(rec.Name, []string{"jq", "ripgrep"}); err != nil {
		t.Fatalf("AddPackages: %v", err)
	}
	if err := st.AddPackages(rec.Name, []string{"ripgrep", "curl"}); err != nil {
		t.Fatalf("AddPackages again: %v", err)
	}
	// The shell path writes the machine's record on every invocation, built
	// without packages. That must not forget them.
	if err := st.PutMachine(rec); err != nil {
		t.Fatalf("PutMachine with a record that carries no packages: %v", err)
	}

	got, ok, err := st.Machine(rec.Name)
	if err != nil || !ok {
		t.Fatalf("Machine: (%t, %v)", ok, err)
	}
	if want := []string{"jq", "ripgrep", "curl"}; !reflect.DeepEqual(got.Packages, want) {
		t.Errorf("packages = %v, want %v", got.Packages, want)
	}

	got.Packages[0] = "changed"
	again, _, _ := st.Machine(rec.Name)
	if again.Packages[0] != "jq" {
		t.Error("mutating a returned record changed the stored one")
	}

	if err := st.AddPackages("avr-unknown-machine", []string{"jq"}); err == nil {
		t.Error("AddPackages succeeded for a machine avar has no record of")
	}

	// Deleting the machine forgets what was installed in it, so a recreated
	// machine gets its packages again.
	if err := st.DeleteMachine(rec.Name); err != nil {
		t.Fatalf("DeleteMachine: %v", err)
	}
	if err := st.PutMachine(rec); err != nil {
		t.Fatalf("PutMachine: %v", err)
	}
	fresh, _, _ := st.Machine(rec.Name)
	if fresh.Packages != nil {
		t.Errorf("a recreated machine inherited %v from its predecessor", fresh.Packages)
	}
}

// Approvals round-trip through the state directory and are copied, not shared,
// when read back.
func TestStore_ProjectApprovalsRoundTrip_REQ_15_3(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	dir := resolved(t, mkdir(t, "app"))

	rec, err := st.EnsureProject(dir)
	if err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	if _, err := st.UpdateProject(rec.ID, func(p *types.ProjectRecord) {
		p.ApprovedForwardEnv = []string{"GITHUB_TOKEN"}
		p.ApprovedPackages = map[string][]string{"avr-ubuntu-24.04-arm64": {"jq"}}
		p.AdvisedResources = "cpus=4"
	}); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}

	reopened, err := Open(st.Root())
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := reopened.Project(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Project: (%t, %v)", ok, err)
	}
	if !reflect.DeepEqual(got.ApprovedForwardEnv, []string{"GITHUB_TOKEN"}) ||
		!reflect.DeepEqual(got.ApprovedPackages, map[string][]string{"avr-ubuntu-24.04-arm64": {"jq"}}) ||
		got.AdvisedResources != "cpus=4" {
		t.Errorf("approvals did not round-trip: %+v", got)
	}

	got.ApprovedPackages["avr-ubuntu-24.04-arm64"][0] = "changed"
	got.ApprovedForwardEnv[0] = "changed"
	again, _, _ := reopened.Project(rec.ID)
	if again.ApprovedPackages["avr-ubuntu-24.04-arm64"][0] != "jq" || again.ApprovedForwardEnv[0] != "GITHUB_TOKEN" {
		t.Error("mutating a returned project record changed the stored one")
	}
}
