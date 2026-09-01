package workspace_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/types"
	"github.com/olamide226/avar/internal/workspace"
)

// Deciding what a synchronization does is a pure function of three manifests, so
// every one of these states a situation directly instead of arranging one inside
// a distribution. That is the point of splitting the decision out of the
// backend: REQ-14.3's promise is checkable here, in milliseconds, over every
// case rather than the two a virtual machine test has time for.

func file(hash string) types.WorkspaceEntry { return types.WorkspaceEntry{Hash: pad(hash)} }

func exe(hash string) types.WorkspaceEntry {
	return types.WorkspaceEntry{Hash: pad(hash), Exec: true}
}

// pad turns a short readable fixture like "a1" into a plausible SHA-256, so a
// test reads as a comparison of contents rather than of hexadecimal.
func pad(s string) string {
	for len(s) < 64 {
		s += "0"
	}
	return s
}

func manifest(pairs map[string]types.WorkspaceEntry) types.WorkspaceManifest {
	out := types.WorkspaceManifest{}
	for k, v := range pairs {
		out[k] = v
	}
	return out
}

// REQ-14.1: entering native mode on a project that has never had one copies the
// whole project in. With no baseline and an empty guest copy, every file the
// host holds is an addition going one way and nothing goes the other.
func TestPlanSync_FirstCopyIsEveryFile_REQ_14_1(t *testing.T) {
	t.Parallel()

	plan := workspace.PlanSync(types.WorkspaceScan{
		Exists:   false,
		Baseline: nil,
		Mount:    manifest(map[string]types.WorkspaceEntry{"main.go": file("a1"), "run.sh": exe("b2")}),
		Guest:    types.WorkspaceManifest{},
	})

	want := []workspace.Change{
		{Path: "main.go", Kind: workspace.Added},
		{Path: "run.sh", Kind: workspace.Added},
	}
	if !reflect.DeepEqual(plan.ToGuest, want) {
		t.Errorf("ToGuest = %v, want every host file as an addition %v", plan.ToGuest, want)
	}
	if len(plan.ToHost) != 0 || len(plan.Conflicts) != 0 {
		t.Errorf("a first copy proposed work in the other direction: ToHost=%v conflicts=%v", plan.ToHost, plan.Conflicts)
	}

	sync := plan.Sync(types.ToGuest)
	if !reflect.DeepEqual(sync.Copy, []string{"main.go", "run.sh"}) {
		t.Errorf("Copy = %v, want both files", sync.Copy)
	}
	// The baseline the copy leaves behind is what both copies then hold, so
	// the next scan reports nothing to do.
	if got := sync.Baseline["run.sh"]; got != exe("b2") {
		t.Errorf("baseline for run.sh = %+v, want the host's entry including its executable bit", got)
	}
}

// One side changed and the other did not: the side that changed is the answer,
// and no question is put to the user.
func TestPlanSync_CarriesAOneSidedChange_REQ_14_2(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		baseline      types.WorkspaceManifest
		mount, guest  types.WorkspaceManifest
		wantToGuest   []workspace.Change
		wantToHost    []workspace.Change
		wantConflicts int
	}{
		{
			name:        "host modified",
			baseline:    manifest(map[string]types.WorkspaceEntry{"a.txt": file("a1")}),
			mount:       manifest(map[string]types.WorkspaceEntry{"a.txt": file("a2")}),
			guest:       manifest(map[string]types.WorkspaceEntry{"a.txt": file("a1")}),
			wantToGuest: []workspace.Change{{Path: "a.txt", Kind: workspace.Modified}},
		},
		{
			name:       "guest modified",
			baseline:   manifest(map[string]types.WorkspaceEntry{"a.txt": file("a1")}),
			mount:      manifest(map[string]types.WorkspaceEntry{"a.txt": file("a1")}),
			guest:      manifest(map[string]types.WorkspaceEntry{"a.txt": file("a3")}),
			wantToHost: []workspace.Change{{Path: "a.txt", Kind: workspace.Modified}},
		},
		{
			name:       "guest added",
			baseline:   types.WorkspaceManifest{},
			mount:      types.WorkspaceManifest{},
			guest:      manifest(map[string]types.WorkspaceEntry{"new.txt": file("c1")}),
			wantToHost: []workspace.Change{{Path: "new.txt", Kind: workspace.Added}},
		},
		{
			name:        "host deleted",
			baseline:    manifest(map[string]types.WorkspaceEntry{"gone.txt": file("d1")}),
			mount:       types.WorkspaceManifest{},
			guest:       manifest(map[string]types.WorkspaceEntry{"gone.txt": file("d1")}),
			wantToGuest: []workspace.Change{{Path: "gone.txt", Kind: workspace.Deleted}},
		},
		{
			// The executable bit is content as far as this comparison is
			// concerned: a script that stops being runnable is the metadata
			// loss a developer notices first.
			name:        "host made a file executable",
			baseline:    manifest(map[string]types.WorkspaceEntry{"run.sh": file("e1")}),
			mount:       manifest(map[string]types.WorkspaceEntry{"run.sh": exe("e1")}),
			guest:       manifest(map[string]types.WorkspaceEntry{"run.sh": file("e1")}),
			wantToGuest: []workspace.Change{{Path: "run.sh", Kind: workspace.Modified}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			plan := workspace.PlanSync(types.WorkspaceScan{
				Exists: true, Baseline: tt.baseline, Mount: tt.mount, Guest: tt.guest,
			})
			if !reflect.DeepEqual(plan.ToGuest, tt.wantToGuest) {
				t.Errorf("ToGuest = %v, want %v", plan.ToGuest, tt.wantToGuest)
			}
			if !reflect.DeepEqual(plan.ToHost, tt.wantToHost) {
				t.Errorf("ToHost = %v, want %v", plan.ToHost, tt.wantToHost)
			}
			if len(plan.Conflicts) != tt.wantConflicts {
				t.Errorf("conflicts = %v, want %d", plan.Conflicts, tt.wantConflicts)
			}
		})
	}
}

// REQ-14.3: both copies changed the same file differently. avar says so and
// carries nothing — in either direction, not merely in the one that would have
// overwritten the losing side.
func TestPlanSync_SurfacesAConflictAndCarriesNothing_REQ_14_3(t *testing.T) {
	t.Parallel()

	plan := workspace.PlanSync(types.WorkspaceScan{
		Exists:   true,
		Baseline: manifest(map[string]types.WorkspaceEntry{"a.txt": file("a1"), "b.txt": file("b1")}),
		Mount:    manifest(map[string]types.WorkspaceEntry{"a.txt": file("a2"), "b.txt": file("b2")}),
		Guest:    manifest(map[string]types.WorkspaceEntry{"a.txt": file("a3"), "b.txt": file("b1")}),
	})

	want := []workspace.Conflict{{Path: "a.txt", Host: workspace.Modified, Guest: workspace.Modified}}
	if !reflect.DeepEqual(plan.Conflicts, want) {
		t.Fatalf("conflicts = %v, want %v", plan.Conflicts, want)
	}

	// b.txt changed only on the host, so on its own it would be carried. It is
	// not, because a partly applied synchronization leaves a destination that
	// is neither copy.
	for _, direction := range []types.WorkspaceDirection{types.ToGuest, types.ToHost} {
		sync := plan.Sync(direction)
		if !sync.Empty() {
			t.Errorf("Sync(%s) proposed work while a conflict was outstanding: copy=%v delete=%v",
				direction, sync.Copy, sync.Delete)
		}
	}
}

// A delete on one side and an edit on the other is a conflict too, and the
// report has to say which is which — sending a user to look at a file that is
// not there is worse than saying nothing.
func TestPlanSync_ReportsDeleteAgainstEdit_REQ_14_3(t *testing.T) {
	t.Parallel()

	plan := workspace.PlanSync(types.WorkspaceScan{
		Exists:   true,
		Baseline: manifest(map[string]types.WorkspaceEntry{"a.txt": file("a1")}),
		Mount:    types.WorkspaceManifest{},
		Guest:    manifest(map[string]types.WorkspaceEntry{"a.txt": file("a9")}),
	})

	want := []workspace.Conflict{{Path: "a.txt", Host: workspace.Deleted, Guest: workspace.Modified}}
	if !reflect.DeepEqual(plan.Conflicts, want) {
		t.Errorf("conflicts = %v, want %v", plan.Conflicts, want)
	}
}

// Both sides made the same edit. That is agreement, not conflict, and the
// baseline has to learn about it or the two copies are reported as conflicting
// forever.
func TestPlanSync_RecordsAgreementWithoutCopying_REQ_14_3(t *testing.T) {
	t.Parallel()

	plan := workspace.PlanSync(types.WorkspaceScan{
		Exists:   true,
		Baseline: manifest(map[string]types.WorkspaceEntry{"a.txt": file("a1"), "b.txt": file("b1")}),
		Mount:    manifest(map[string]types.WorkspaceEntry{"a.txt": file("a2")}),
		Guest:    manifest(map[string]types.WorkspaceEntry{"a.txt": file("a2")}),
	})

	if !plan.Empty() {
		t.Fatalf("two copies that agree were reported as work: %+v", plan)
	}

	sync := plan.Sync(types.ToHost)
	if !sync.Empty() {
		t.Errorf("Sync copied something for two copies that already agree: %+v", sync)
	}
	if got := sync.Baseline["a.txt"]; got != file("a2") {
		t.Errorf("baseline for a.txt = %+v, want the content both copies now hold", got)
	}
	if _, still := sync.Baseline["b.txt"]; still {
		t.Errorf("baseline still records b.txt, which both copies deleted: %v", sync.Baseline)
	}
}

// The baseline advances per file, not per tree. Syncing one direction must leave
// the other direction's changes still pending, or the next scan would conclude
// the guest had reverted work nobody touched.
func TestSync_AdvancesTheBaselinePerFile_REQ_17_5(t *testing.T) {
	t.Parallel()

	scan := types.WorkspaceScan{
		Exists:   true,
		Baseline: manifest(map[string]types.WorkspaceEntry{"host.txt": file("h1"), "guest.txt": file("g1")}),
		Mount:    manifest(map[string]types.WorkspaceEntry{"host.txt": file("h2"), "guest.txt": file("g1")}),
		Guest:    manifest(map[string]types.WorkspaceEntry{"host.txt": file("h1"), "guest.txt": file("g2")}),
	}
	sync := workspace.PlanSync(scan).Sync(types.ToGuest)

	if !reflect.DeepEqual(sync.Copy, []string{"host.txt"}) {
		t.Fatalf("Copy = %v, want only the host's own change", sync.Copy)
	}
	if got := sync.Baseline["host.txt"]; got != file("h2") {
		t.Errorf("baseline for the carried file = %+v, want the host's new content", got)
	}
	// The untouched half is the point: it still records what the guest started
	// from, so `avr sync --to-host` still has something to offer afterwards.
	if got := sync.Baseline["guest.txt"]; got != file("g1") {
		t.Errorf("baseline for the guest's own change = %+v, want it unchanged at %+v", got, file("g1"))
	}

	// Applying it and rescanning leaves exactly the guest's change outstanding.
	after := types.WorkspaceScan{
		Exists:   true,
		Baseline: sync.Baseline,
		Mount:    scan.Mount,
		Guest:    manifest(map[string]types.WorkspaceEntry{"host.txt": file("h2"), "guest.txt": file("g2")}),
	}
	plan := workspace.PlanSync(after)
	if len(plan.ToGuest) != 0 || len(plan.Conflicts) != 0 {
		t.Errorf("after applying to the guest, the plan still proposes %v / %v", plan.ToGuest, plan.Conflicts)
	}
	if want := []workspace.Change{{Path: "guest.txt", Kind: workspace.Modified}}; !reflect.DeepEqual(plan.ToHost, want) {
		t.Errorf("ToHost = %v, want the guest's own change still pending %v", plan.ToHost, want)
	}
}

// A synchronization killed halfway leaves the destination holding some of the
// new files and the previous baseline still recorded. Running it again has to
// finish the job — and in particular must not report a conflict on a file the
// interrupted run had already copied, which is what would make the user unable
// to tell which copy is authoritative (REQ-17.5).
func TestPlanSync_ConvergesAfterAnInterruptedRun_REQ_17_5(t *testing.T) {
	t.Parallel()

	baseline := manifest(map[string]types.WorkspaceEntry{"one.txt": file("11"), "two.txt": file("22")})
	mount := manifest(map[string]types.WorkspaceEntry{"one.txt": file("1a"), "two.txt": file("2a")})

	// The first run copied one.txt and died before two.txt and before the
	// baseline was written.
	interrupted := types.WorkspaceScan{
		Exists:   true,
		Baseline: baseline,
		Mount:    mount,
		Guest:    manifest(map[string]types.WorkspaceEntry{"one.txt": file("1a"), "two.txt": file("22")}),
	}

	plan := workspace.PlanSync(interrupted)
	if len(plan.Conflicts) != 0 {
		t.Fatalf("an interrupted copy produced conflicts on files nobody edited: %v", plan.Conflicts)
	}
	sync := plan.Sync(types.ToGuest)
	if !reflect.DeepEqual(sync.Copy, []string{"two.txt"}) {
		t.Errorf("Copy = %v, want only the file the interrupted run had not reached", sync.Copy)
	}
}

// PROP-22, exhaustively. Over every combination of what each copy can hold for
// one file, avar never proposes to carry a file in both directions, and never
// proposes to carry one that both copies changed differently. Those two together
// are the whole of "never silently overwrite either side".
func TestProp_NeitherCopyIsEverOverwritten_PROP_22(t *testing.T) {
	t.Parallel()

	// The state space: absent, or one of three contents, on each of the three
	// sides. 4^3 = 64 combinations, which is every case the comparison has.
	states := []struct {
		name  string
		entry types.WorkspaceEntry
		gone  bool
	}{
		{name: "absent", gone: true},
		{name: "v1", entry: file("11")},
		{name: "v2", entry: file("22")},
		{name: "v3", entry: file("33")},
	}

	put := func(m types.WorkspaceManifest, i int) {
		if !states[i].gone {
			m["f"] = states[i].entry
		}
	}

	for b := range states {
		for h := range states {
			for g := range states {
				baseline, mount, guest := types.WorkspaceManifest{}, types.WorkspaceManifest{}, types.WorkspaceManifest{}
				put(baseline, b)
				put(mount, h)
				put(guest, g)

				plan := workspace.PlanSync(types.WorkspaceScan{
					Exists: true, Baseline: baseline, Mount: mount, Guest: guest,
				})

				name := states[b].name + "/" + states[h].name + "/" + states[g].name
				if len(plan.ToGuest) > 0 && len(plan.ToHost) > 0 {
					t.Errorf("%s: one file proposed in both directions at once", name)
				}

				hostMoved := mount["f"] != baseline["f"]
				guestMoved := guest["f"] != baseline["f"]
				diverged := hostMoved && guestMoved && mount["f"] != guest["f"]

				if diverged && len(plan.Conflicts) == 0 {
					t.Errorf("%s: both copies changed differently and no conflict was reported", name)
				}
				if !diverged && len(plan.Conflicts) > 0 {
					t.Errorf("%s: a conflict was reported where one side did not move", name)
				}
				for _, direction := range []types.WorkspaceDirection{types.ToGuest, types.ToHost} {
					if sync := plan.Sync(direction); diverged && !sync.Empty() {
						t.Errorf("%s: Sync(%s) would have overwritten a divergent file", name, direction)
					}
				}
			}
		}
	}
}

// The exclusion list is what avar does not look at, and the user has to be able
// to be told what it is. One value, so the walk and the explanation cannot
// disagree.
func TestWorkspaceExclusions_AreOneList_REQ_14_2(t *testing.T) {
	t.Parallel()

	described := types.DescribeWorkspaceExclusions()
	for _, name := range types.WorkspaceExcludedDirs {
		if !strings.Contains(described, name) {
			t.Errorf("the description of the exclusion list omits %q: %s", name, described)
		}
	}
	// The names that are ambiguous — checked in by somebody, generated by
	// somebody else — must not be excluded, because excluding source looks
	// exactly like avar losing a user's work.
	for _, name := range []string{"vendor", "dist", "build", "bin", ".git"} {
		for _, excluded := range types.WorkspaceExcludedDirs {
			if excluded == name {
				t.Errorf("%q is excluded, but it is a directory people check in", name)
			}
		}
	}
}
