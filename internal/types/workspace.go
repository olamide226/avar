package types

import (
	"fmt"
	"sort"
	"strings"
)

// The vocabulary of Linux-native workspace mode (REQ-14).
//
// A project normally lives on the host and is shared into the guest, so there
// is one copy and nothing to reconcile. Native mode makes a second copy on the
// guest's own filesystem and runs the session there, because a workload that
// stats a dependency tree pays for every crossing of the host/guest boundary.
// Two copies means avar has to be able to say, at any moment, what each of them
// holds and which of them changed — and it has to be able to say that without
// consulting timestamps, which do not survive a filesystem boundary in any form
// worth trusting: a Windows filesystem and a Linux one disagree about
// granularity, about time zones on some configurations, and about what a copy
// does to a modification time.
//
// So the vocabulary is content. A WorkspaceManifest is what a tree holds, keyed
// by path and valued by the hash of the bytes, and every question avar asks — is
// this file new, changed, deleted, or in conflict — is answered by comparing
// three manifests. Nothing here is backend-specific: a manifest of a directory
// is the same idea whichever backend produced it, which is what lets the planner
// in internal/workspace be a pure function and the capability interface in
// internal/provider be one a second backend can satisfy.

// WorkspaceEntry is one regular file, as a synchronization compares them.
//
// It is content and one permission bit, and no more than that. The hash is what
// decides whether a file changed; the executable bit is carried because losing
// it is the one metadata loss a developer notices immediately — a script that
// stops being runnable — and because a Windows filesystem does not record it the
// way Linux does, so a copy in either direction has to restate it rather than
// rely on it surviving.
//
// Ownership and timestamps are deliberately absent. avar does not attempt to
// reproduce them across the boundary, because it cannot: the guest copy belongs
// to the guest account and the host copy belongs to the Windows account, and
// pretending otherwise would make every file look changed on every scan.
type WorkspaceEntry struct {
	// Hash is the lowercase hexadecimal SHA-256 of the file's bytes.
	Hash string
	// Exec reports that the owner-execute bit is set.
	Exec bool
}

// WorkspaceManifest is what one tree holds: relative path to content.
//
// Keys are slash-separated paths relative to the tree's root, with no leading
// "./" and no trailing slash, so that the same file has the same key whichever
// side produced the manifest. A nil manifest means "not known" — for a baseline,
// that no synchronization has ever completed — and an empty one means "known to
// hold nothing".
type WorkspaceManifest map[string]WorkspaceEntry

// Paths returns the manifest's keys in sorted order, so that anything rendered
// from a manifest is deterministic.
func (m WorkspaceManifest) Paths() []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Clone returns an independent copy, so that a planner can derive the next
// baseline without mutating the one it was given.
func (m WorkspaceManifest) Clone() WorkspaceManifest {
	out := make(WorkspaceManifest, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// NativeWorkspace is where one project's guest-native copy lives, and how it
// relates to the share it was copied from.
//
// Both guest paths are present because native mode does not replace the share:
// it uses it. The share is how bytes cross the host/guest boundary at all — it
// is a directory the guest can already read and write — so copying is an
// entirely guest-side operation between two guest paths, and avar needs no file
// transfer protocol, no rsync, and no second channel. What changes in native
// mode is only which of the two the session runs in.
type NativeWorkspace struct {
	// ProjectID is the Project_Identity the workspace serves.
	ProjectID string
	// HostPath is the project directory on the host, for messages the user
	// reads. It is never used as a guest path.
	HostPath string
	// MountPath is the guest path of the live share of HostPath — the copy on
	// the host's filesystem, as the guest sees it.
	MountPath string
	// Path is the guest path of the native copy, on the guest's own
	// filesystem. In native mode this is the session's working directory.
	Path string
}

// Validate reports whether the workspace describes two distinct guest
// directories avar can act on.
func (w NativeWorkspace) Validate() error {
	switch {
	case strings.TrimSpace(w.ProjectID) == "":
		return fmt.Errorf("a native workspace needs a project identity")
	case !strings.HasPrefix(w.MountPath, "/"):
		return fmt.Errorf("the shared copy %q must be an absolute Linux path", w.MountPath)
	case !strings.HasPrefix(w.Path, "/"):
		return fmt.Errorf("the native copy %q must be an absolute Linux path", w.Path)
	case w.MountPath == w.Path:
		// One directory cannot be both copies. If it were, every scan would
		// report the two sides as identical and no change would ever be
		// visible in either direction.
		return fmt.Errorf("the native copy and the shared copy are the same directory (%s)", w.Path)
	}
	return nil
}

// WorkspaceDirection names which copy a synchronization reads from.
type WorkspaceDirection string

const (
	// ToGuest copies from the shared host copy into the native one, which is
	// what entering native mode does (REQ-14.1).
	ToGuest WorkspaceDirection = "to-guest"
	// ToHost copies from the native copy back to the host, which is the
	// reviewable sync-back (REQ-14.2).
	ToHost WorkspaceDirection = "to-host"
)

// WorkspaceScan is what a backend found when it looked at both copies.
//
// It is evidence, not a decision: the backend reports three manifests and the
// planner in internal/workspace decides what follows from them. Splitting it
// that way is what makes the whole of REQ-14.3's conflict rule testable without
// a distribution.
type WorkspaceScan struct {
	// Exists reports whether the native copy is there at all. A workspace that
	// has never been created is not an error — it is the first thing
	// `avr --native-fs` fixes.
	Exists bool

	// Baseline is what both copies held when the last synchronization
	// completed. It is nil when none ever has, in which case every file
	// present in both copies with differing content is a genuine conflict:
	// without a common ancestor avar has no evidence about which side changed,
	// and guessing is precisely what REQ-14.3 forbids.
	Baseline WorkspaceManifest

	// Mount is what the shared host copy holds now.
	Mount WorkspaceManifest

	// Guest is what the native copy holds now.
	Guest WorkspaceManifest

	// Skipped lists entries in either copy that avar will not synchronize —
	// symbolic links, sockets, device nodes, and names it cannot represent
	// unambiguously. They are reported rather than silently dropped, because a
	// file the user believes is being synchronized and is not is the worst
	// outcome this feature has.
	Skipped []string
}

// WorkspaceSync is one direction's applicable work, as a backend carries it
// out.
//
// Copy and Delete are relative paths in the manifests' vocabulary. Baseline is
// what the recorded common ancestor becomes once every entry has been applied,
// and a backend writes it only after that — an interrupted synchronization
// leaves the previous baseline in place, which is what makes repeating the
// command converge rather than compound (REQ-17.5).
type WorkspaceSync struct {
	Direction WorkspaceDirection
	Copy      []string
	Delete    []string
	Baseline  WorkspaceManifest
}

// Empty reports whether the synchronization would move nothing.
func (s WorkspaceSync) Empty() bool { return len(s.Copy) == 0 && len(s.Delete) == 0 }

// WorkspaceExcludedDirs are the directory names a native workspace never
// synchronizes.
//
// They are the trees a build produces, and excluding them is the point rather
// than an optimisation: native mode exists so that a dependency tree can live on
// the Linux filesystem and stay there, and copying `node_modules` back onto the
// host filesystem would undo the whole benefit in the one direction the user is
// most likely to ask for. A build output is also the file set a synchronizing
// tool is least able to reason about — it is regenerated wholesale, so every
// entry looks changed on both sides and the review becomes forty thousand lines
// of noise with the three files the user actually edited somewhere inside it.
//
// The list is names, matched anywhere in the tree, and it is deliberately short.
// Every entry is a name that is *only* ever generated output: excluding
// something the user hand-wrote would look exactly like avar losing their work,
// which is a far worse failure than copying a directory they did not need. That
// is why `vendor`, `dist`, `build` and `bin` are absent despite being common
// build outputs — each of them is also, in some ecosystem, checked in on
// purpose.
//
// `target` is the one judgement call, and the repository has already made it:
// internal/workspace.Detect treats a `target` directory as evidence of a
// dependency-heavy project, for exactly this reason. It is Cargo's and Maven's
// build directory and is not a place source is written.
//
// It lives here rather than beside the planner so that the list the walk applies
// and the list the user is told about are the same value. Two copies of an
// exclusion set drift, and the direction they drift in is a file the user
// believes is being synchronized and is not.
var WorkspaceExcludedDirs = []string{
	".direnv",
	".dart_tool",
	".gradle",
	".mypy_cache",
	".next",
	".nuxt",
	".parcel-cache",
	".pytest_cache",
	".ruff_cache",
	".stack-work",
	".svelte-kit",
	".terraform",
	".tox",
	".turbo",
	".venv",
	"__pycache__",
	"node_modules",
	"target",
}

// DescribeWorkspaceExclusions renders the exclusion list for the messages that
// have to tell a user what avar did not look at.
func DescribeWorkspaceExclusions() string { return strings.Join(WorkspaceExcludedDirs, ", ") }
