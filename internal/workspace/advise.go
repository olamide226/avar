// Package workspace decides when to tell a Windows user that their project is
// on the slow side of the filesystem boundary.
//
// WSL reaches a Windows directory through DrvFS, and every file operation
// crosses from Linux into Windows to be served. For editing source that is
// invisible; for a workload that opens tens of thousands of small files — `npm
// install`, `cargo build`, a test run that stats a dependency tree — it is the
// difference between seconds and minutes, and it is the single most common
// complaint about developing in WSL. Microsoft's own guidance is to keep
// Linux-heavy work on the Linux filesystem.
//
// avar cannot fix that here. What it can do is notice, once, and say so, rather
// than let a developer conclude that Linux is slow (REQ-18.11).
//
// The detection is deliberately cheap and deliberately dumb. It stats a fixed
// list of names in the project root and nothing else: no directory walk, no file
// counting, no reading of manifests. A walk would be the more accurate signal
// and it is the wrong trade — it runs on the path to a shell the user is waiting
// for, it grows with the very projects it is trying to detect, and it would be
// slowest exactly where DrvFS is slowest. A lock file or a dependency directory
// in the root is present in nearly every project that has this problem and
// absent from nearly every project that does not, at the cost of a few stats.
package workspace

import (
	"os"
	"path/filepath"
)

// heavyMarkers are the names whose presence in a project root says the project
// has a large dependency tree.
//
// Lock files come first because they are the honest signal: a lock file exists
// precisely when a package manager has resolved a dependency graph, and it is
// there from the first commit rather than only after someone has installed.
// The directories are the second half, for the project that has dependencies
// installed but whose lock file is named something avar has not heard of.
//
// The list is names rather than patterns so that matching is a stat rather than
// a scan, and it is ordered so that the reason avar gives names the most
// recognisable marker it found.
var heavyMarkers = []string{
	// JavaScript, by far the most affected ecosystem: a node_modules tree is
	// tens of thousands of small files and every one of them crosses.
	"package-lock.json",
	"pnpm-lock.yaml",
	"yarn.lock",
	"bun.lockb",
	"node_modules",
	// Rust: a debug build writes an enormous target directory.
	"Cargo.lock",
	"target",
	// Go: a vendored module tree has the same shape as node_modules.
	"vendor",
	// Python, PHP, Ruby: smaller trees, same problem.
	"poetry.lock",
	"Pipfile.lock",
	"uv.lock",
	"composer.lock",
	"Gemfile.lock",
}

// Advice is what avar found and what it would say about it.
type Advice struct {
	// Marker is the name that triggered the advice, so the message can point
	// at something the user recognises in their own project.
	Marker string
}

// Detect reports whether a project is one that will suffer materially from
// living on the Windows filesystem, and what made avar think so.
//
// It never fails. A project directory avar cannot stat is a project avar says
// nothing about: this is advice, and advice that turns into an error on the way
// to a shell is worse than no advice (REQ-18.11).
func Detect(projectRoot string) (Advice, bool) {
	for _, marker := range heavyMarkers {
		if _, err := os.Stat(filepath.Join(projectRoot, marker)); err == nil {
			return Advice{Marker: marker}, true
		}
	}
	return Advice{}, false
}

// Message is what the user reads.
//
// It says what is happening, why, and what to do about it — and what it
// recommends is something the user can act on in the next command they type.
// That was not true when this message was first written: Linux-native workspace
// mode was Requirement 14 and unbuilt, so the advice deliberately stopped at
// describing the problem rather than naming a flag that did not exist. Now that
// `--native-fs` is real, naming it is the second half of REQ-18.11 — "accepting
// that recommendation SHALL use Requirement 14's reviewable synchronization and
// conflict-safety rules" — and the message and the test that guards it moved
// together with the flag.
//
// It is written to be read once and dismissed. A user who has read it and
// decided their project is fine on the Windows side has made a legitimate choice
// — a project that is edited far more than it is built is better where their
// other tools can see it — and avar does not raise it again.
func (a Advice) Message(projectPath string) string {
	return "avr: this project has a large dependency tree (" + a.Marker + ") and lives on the Windows filesystem.\n" +
		"     Linux reaches it through a translation layer, so anything that touches many\n" +
		"     files — installing packages, building, running tests — will be markedly\n" +
		"     slower here than it would be on a Linux filesystem.\n" +
		"     `avr --native-fs` keeps a copy of the project on this environment's own\n" +
		"     filesystem and runs there instead; `avr sync` shows you what changed and\n" +
		"     brings it back, and never overwrites either copy without asking.\n" +
		"     Keeping the project at " + projectPath + " is fine if you mostly edit\n" +
		"     rather than build.\n" +
		"     This is said once per project."
}
