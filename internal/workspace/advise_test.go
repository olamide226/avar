package workspace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/workspace"
)

// REQ-18.11: the projects worth warning about are the ones with a large
// dependency tree, because every file in it crosses the filesystem boundary on
// every build. A lock file is the honest signal — it exists precisely when a
// package manager has resolved a graph — and a dependency directory is the
// second half, for a project whose lock file avar has not heard of.
func TestDetect_RecognisesADependencyHeavyProject_REQ_18_11(t *testing.T) {
	t.Parallel()

	heavy := []string{
		"package-lock.json",
		"pnpm-lock.yaml",
		"yarn.lock",
		"bun.lockb",
		"Cargo.lock",
		"poetry.lock",
		"Pipfile.lock",
		"uv.lock",
		"composer.lock",
		"Gemfile.lock",
	}

	for _, marker := range heavy {
		t.Run(marker, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			write(t, filepath.Join(root, marker), "")

			advice, ok := workspace.Detect(root)
			if !ok {
				t.Fatalf("a project with %s was not recognised as dependency-heavy", marker)
			}
			if advice.Marker != marker {
				t.Errorf("Marker = %q, want %q — the message points at something the user recognises", advice.Marker, marker)
			}
		})
	}
}

// A dependency directory counts too: a node_modules tree is tens of thousands of
// small files whether or not the lock file beside it is one avar knows.
func TestDetect_RecognisesADependencyDirectory_REQ_18_11(t *testing.T) {
	t.Parallel()

	for _, dir := range []string{"node_modules", "vendor", "target"} {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
				t.Fatal(err)
			}
			if _, ok := workspace.Detect(root); !ok {
				t.Errorf("a project with a %s directory was not recognised", dir)
			}
		})
	}
}

// A project without a dependency tree gets nothing. Advice on a project that
// does not have the problem is noise, and noise is how the advice that matters
// stops being read.
func TestDetect_SaysNothingAboutAnOrdinaryProject_REQ_18_11(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write(t, filepath.Join(root, "main.go"), "package main\n")
	write(t, filepath.Join(root, "go.mod"), "module example\n")
	write(t, filepath.Join(root, "README.md"), "# example\n")

	if advice, ok := workspace.Detect(root); ok {
		t.Errorf("an ordinary project was advised, on the strength of %q", advice.Marker)
	}
}

// The detection is a fixed list of stats in the project root and nothing else:
// no walk, no recursion, no reading of files. A marker buried in a subdirectory
// is not a signal about the project, and finding it would cost a directory walk
// on the path to a shell the user is waiting for.
func TestDetect_LooksOnlyAtTheProjectRoot_REQ_17_1(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	nested := filepath.Join(root, "docs", "examples")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(nested, "package-lock.json"), "")

	if _, ok := workspace.Detect(root); ok {
		t.Error("a lock file in a subdirectory triggered advice about the whole project")
	}
}

// This is advice on the way to a shell. A project directory avar cannot read is
// a project it says nothing about, never an error that fails the command.
func TestDetect_NeverFails_REQ_18_11(t *testing.T) {
	t.Parallel()

	if _, ok := workspace.Detect(filepath.Join(t.TempDir(), "gone")); ok {
		t.Error("a directory that does not exist was advised")
	}
	if _, ok := workspace.Detect(""); ok {
		t.Error("an empty path was advised")
	}
}

// The message has to say what is happening, why, and what the user can do about
// it — today. Until Requirement 14 was built this test asserted the opposite of
// what it asserts now: that the message did *not* mention --native-fs, because
// recommending a flag that did not exist is not advice. The flag exists, so
// REQ-18.11's second clause is now reachable and the recommendation has to name
// the thing that carries it out.
func TestMessage_IsActionableToday_REQ_18_11(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	write(t, filepath.Join(root, "package-lock.json"), "")

	advice, ok := workspace.Detect(root)
	if !ok {
		t.Fatal("the project was not recognised")
	}
	message := advice.Message(root)

	for _, want := range []string{
		// What avar found, so the user can check it themselves.
		"package-lock.json",
		// Where the project is.
		root,
		// That it is said once, so a user who ignores it knows it will not
		// keep interrupting them.
		"once per project",
		// The recommendation REQ-18.11 asks for, named so the user can act on
		// it without going to look for it.
		"--native-fs",
		// And the half that makes accepting it safe: work done in Linux comes
		// back through a reviewable synchronization (REQ-14.2, REQ-14.3).
		"avr sync",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not mention %q:\n%s", want, message)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
