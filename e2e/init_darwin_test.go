//go:build e2e && darwin

// See harness_test.go for what these tests are and how they are run.
//
// `avr init` writes a file into somebody's project. It starts nothing and
// installs nothing, and it writes only after the user confirms — there is no
// --yes, because REQ-15.2 grants no bypass of that confirmation, unlike the
// ones reset and destroy have.

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// REQ-15.2: with no terminal to confirm at, `avr init` shows what it would
// write, writes nothing, and says why.
//
// Showing the proposal and then refusing is the deliberate shape: somebody
// running it from a script or a pipeline still learns what avar detected and
// what the file would contain, and can write it themselves.
func TestInit_WithoutATerminalWritesNothing_REQ_15_2(t *testing.T) {
	dir, env := ownProject(t, "init")

	// A manifest avar recognises, so there is something to propose. Without
	// one, init says it has nothing to propose and exits zero — a different
	// path, and not the one this test is about.
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{\"name\":\"widget\"}\n"), 0o644); err != nil {
		t.Fatalf("write the manifest: %v", err)
	}
	path := filepath.Join(dir, ".avr.toml")

	stdout, stderr, code := avr(t, dir, env, "init")
	if code == 0 {
		t.Fatalf("`avr init` reported success with nobody to confirm to\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	for _, want := range []string{"package.json", "Node.js", "Proposed", ".avr.toml"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("`avr init` did not show what it found and would write (%q missing):\n%s", want, stdout)
		}
	}
	report := stdout + stderr
	for _, want := range []string{"nothing was written", "terminal"} {
		if !strings.Contains(report, want) {
			t.Errorf("`avr init` did not say why it wrote nothing (%q missing):\n%s", want, report)
		}
	}

	if _, err := os.Stat(path); err == nil {
		body, _ := os.ReadFile(path)
		t.Errorf("`avr init` wrote %s anyway:\n%s", path, body)
	} else if !os.IsNotExist(err) {
		t.Errorf("checking %s: %v", path, err)
	}
}

// REQ-15.2: a project that already has a .avr.toml keeps it, byte for byte.
// The file is hand-edited and may carry a team's decisions; init only ever
// writes a new one.
func TestInit_NeverOverwritesAnExistingFile_REQ_15_2(t *testing.T) {
	dir, env := ownProject(t, "init-keep")

	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{\"name\":\"widget\"}\n"), 0o644); err != nil {
		t.Fatalf("write the manifest: %v", err)
	}
	// Deliberately not what init would propose for a Node.js project, so that
	// an overwrite is visible rather than coincidentally identical.
	const existing = "distro = \"debian\"\npackages = [\"jq\"]\n"
	path := filepath.Join(dir, ".avr.toml")
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("write the existing %s: %v", path, err)
	}

	stdout, stderr, code := avr(t, dir, env, "init")
	if code == 0 {
		t.Fatalf("`avr init` reported success over an existing file\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	report := stdout + stderr
	for _, want := range []string{path, "already exists", "nothing was changed"} {
		if !strings.Contains(report, want) {
			t.Errorf("`avr init` did not say it left the file alone (%q missing):\n%s", want, report)
		}
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s back: %v", path, err)
	}
	if string(body) != existing {
		t.Errorf("`avr init` changed an existing file:\n  got  %q\n  want %q", body, existing)
	}
}
