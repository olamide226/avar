package workspace

import (
	"fmt"
	"sort"

	"github.com/olamide226/avar/internal/types"
)

// Deciding what a synchronization does, as a pure function of three manifests.
//
// The comparison is three-way and it has to be. A two-way comparison can see
// that the two copies differ; it cannot see which of them moved, so the only
// thing it can do with a difference is pick a winner — and picking a winner is
// exactly what REQ-14.3 forbids. The third manifest is the baseline: what both
// copies held when the last synchronization completed. With it, every file falls
// into one of five cases, and only one of them needs the user:
//
//	unchanged on both        nothing to do
//	changed on the host only  the host's version is the answer
//	changed in the guest only the guest's version is the answer
//	changed on both, alike    the two copies already agree; record it and move on
//	changed on both, unalike  a conflict, which avar surfaces and never resolves
//
// Nothing here consults a timestamp, and that is deliberate rather than
// incidental. The two copies live on different filesystems on opposite sides of
// a translation layer; their clocks, their granularity and what a copy does to a
// modification time all differ, and a synchronization that trusted them would be
// wrong in a way that silently destroys work. Content is the only evidence that
// means the same thing on both sides.
//
// The baseline is advanced per file rather than wholesale, which is what lets a
// one-directional synchronization be correct in the presence of changes going
// the other way. Applying the host's version of one file makes the baseline for
// *that* file the host's version; a file the guest changed keeps its old
// baseline entry and is therefore still reported as a guest-side change the next
// time somebody asks. A whole-tree baseline would have to claim the two copies
// are identical, which after a one-directional sync they are not.

// ChangeKind is what happened to one file on one side.
type ChangeKind string

const (
	// Added means the file exists now and did not at the baseline.
	Added ChangeKind = "added"
	// Modified means the file exists on both sides but its content changed.
	Modified ChangeKind = "modified"
	// Deleted means the file existed at the baseline and is gone.
	Deleted ChangeKind = "deleted"
)

// Change is one file a synchronization would copy or remove.
type Change struct {
	// Path is the file's slash-separated path relative to the project root.
	Path string
	// Kind is what applying the change does at the destination.
	Kind ChangeKind
}

// String renders a change the way a review listing reads.
func (c Change) String() string { return string(c.Kind) + " " + c.Path }

// Conflict is one file both copies changed, differently.
//
// It carries what each side did rather than only the path, because "changed on
// both sides" and "deleted on one side and edited on the other" call for
// different things from the user, and an unresolvable listing that does not say
// which is which sends them to look at a file that is not there.
type Conflict struct {
	Path string
	// Host and Guest are what each copy did since the baseline. They are never
	// equal: two sides that did the same thing are not in conflict.
	Host  ChangeKind
	Guest ChangeKind
}

// String renders a conflict for the listing the user reads.
func (c Conflict) String() string {
	return fmt.Sprintf("%s (%s on the host, %s in Linux)", c.Path, c.Host, c.Guest)
}

// Plan is what a scan implies: the work available in each direction, and the
// files no direction can carry.
//
// It holds the manifests it was built from so that Sync can derive the baseline
// each direction leaves behind. Deriving it here rather than in a backend keeps
// the one piece of reasoning that must not be wrong — what avar will claim both
// copies agreed on — in the package that is tested without a virtual machine.
type Plan struct {
	// ToGuest is what applying the host's changes to the native copy would do.
	ToGuest []Change
	// ToHost is what applying the native copy's changes to the host would do.
	ToHost []Change
	// Conflicts are the files both copies changed differently. avar applies
	// nothing while any exist (REQ-14.3).
	Conflicts []Conflict

	baseline, mount, guest types.WorkspaceManifest
	// converged are the files both copies changed to the same content, or both
	// deleted. They need no copying, but the baseline has to learn about them
	// or they are reported as a conflict forever.
	converged []string
}

// Empty reports whether the two copies are already synchronized.
func (p Plan) Empty() bool {
	return len(p.ToGuest) == 0 && len(p.ToHost) == 0 && len(p.Conflicts) == 0
}

// PlanSync compares the two copies against their baseline.
//
// It never fails and never asks a question: it is a pure function of the three
// manifests, so the same scan always produces the same plan, and a test can
// state a situation directly instead of arranging one inside a distribution.
func PlanSync(scan types.WorkspaceScan) Plan {
	p := Plan{baseline: scan.Baseline, mount: scan.Mount, guest: scan.Guest}

	for _, path := range unionPaths(scan.Baseline, scan.Mount, scan.Guest) {
		base, hadBase := scan.Baseline[path]
		host, onHost := scan.Mount[path]
		guest, inGuest := scan.Guest[path]

		hostKind, hostChanged := classify(base, hadBase, host, onHost)
		guestKind, guestChanged := classify(base, hadBase, guest, inGuest)

		switch {
		case !hostChanged && !guestChanged:
			// Both copies still hold what the baseline recorded.
		case hostChanged && !guestChanged:
			p.ToGuest = append(p.ToGuest, Change{Path: path, Kind: hostKind})
		case guestChanged && !hostChanged:
			p.ToHost = append(p.ToHost, Change{Path: path, Kind: guestKind})
		case onHost == inGuest && (!onHost || host == guest):
			// Both sides moved, and they moved to the same place: either the
			// file now holds identical bytes in both copies, or both copies
			// deleted it. There is nothing to carry, but the baseline has to
			// record the agreement or this reads as a conflict every time.
			p.converged = append(p.converged, path)
		default:
			p.Conflicts = append(p.Conflicts, Conflict{Path: path, Host: hostKind, Guest: guestKind})
		}
	}
	return p
}

// classify reports what one side did to a file since the baseline, and whether
// it did anything at all.
func classify(base types.WorkspaceEntry, hadBase bool, now types.WorkspaceEntry, present bool) (ChangeKind, bool) {
	switch {
	case present && !hadBase:
		return Added, true
	case !present && hadBase:
		return Deleted, true
	case present && now != base:
		return Modified, true
	default:
		return "", false
	}
}

// Sync turns the plan into the work a backend carries out in one direction, and
// the baseline that work leaves behind.
//
// A plan with conflicts yields nothing at all, in either direction. The
// alternative — carrying the files that are not in conflict and leaving the rest
// — was considered and rejected: it produces a destination that is neither copy,
// so the question the user then has to answer ("which of these is the one I
// want?") has no answer, and REQ-14.3's promise that neither side is overwritten
// is much harder to see. Refusing wholesale keeps one of the two copies
// authoritative at every moment.
func (p Plan) Sync(direction types.WorkspaceDirection) types.WorkspaceSync {
	sync := types.WorkspaceSync{Direction: direction, Baseline: p.baseline.Clone()}
	if len(p.Conflicts) > 0 {
		return sync
	}

	// A file both copies changed identically is recorded whichever way the
	// user is syncing: the agreement is a fact about both copies, not about a
	// direction.
	for _, path := range p.converged {
		if entry, ok := p.mount[path]; ok {
			sync.Baseline[path] = entry
		} else {
			delete(sync.Baseline, path)
		}
	}

	changes, source := p.ToGuest, p.mount
	if direction == types.ToHost {
		changes, source = p.ToHost, p.guest
	}
	for _, change := range changes {
		if change.Kind == Deleted {
			sync.Delete = append(sync.Delete, change.Path)
			delete(sync.Baseline, change.Path)
			continue
		}
		sync.Copy = append(sync.Copy, change.Path)
		sync.Baseline[change.Path] = source[change.Path]
	}

	sort.Strings(sync.Copy)
	sort.Strings(sync.Delete)
	return sync
}

// unionPaths lists every path any of the manifests mentions, in sorted order so
// that a plan's listings are deterministic.
func unionPaths(manifests ...types.WorkspaceManifest) []string {
	seen := make(map[string]struct{})
	for _, m := range manifests {
		for path := range m {
			seen[path] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
