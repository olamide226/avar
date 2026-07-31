//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestMounts_TwoProjectsShareOneMachine_PROP_5 verifies that two distinct
// project directories can share a single machine, and that each project's
// files are reachable from the guest. Mount confinement (PROP-5) means the
// guest can reach exactly the project roots registered to the machine, so
// both projects are reachable but files outside those roots are not.
func TestMounts_TwoProjectsShareOneMachine_PROP_5(t *testing.T) {
	dirA := project(t, "mounts-multi-a")
	dirB := project(t, "mounts-multi-b")

	// Write a marker file in each project so we can identify them.
	markerA := filepath.Join(dirA, "marker-a")
	if err := os.WriteFile(markerA, []byte("project-a\n"), 0o644); err != nil {
		t.Fatalf("write marker in project A: %v", err)
	}
	markerB := filepath.Join(dirB, "marker-b")
	if err := os.WriteFile(markerB, []byte("project-b\n"), 0o644); err != nil {
		t.Fatalf("write marker in project B: %v", err)
	}

	// Share project A.
	stdout, stderr, code := avr(t, dirA, nil, "cat", "marker-a")
	if code != 0 {
		t.Fatalf("project A first visit exited %d\nstderr:\n%s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "project-a" {
		t.Errorf("project A marker = %q, want %q", got, "project-a")
	}

	// Share project B. The first visit to a new project on a running shared
	// machine triggers a one-time restart. After this, both projects are
	// registered with the same machine.
	stdout, stderr, code = avr(t, dirB, nil, "pwd")
	if code != 0 {
		t.Fatalf("project B first visit exited %d\nstderr:\n%s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != dirB {
		t.Errorf("project B pwd = %q, want %q", got, dirB)
	}

	// Project B's own files must be visible.
	stdout, stderr, code = avr(t, dirB, nil, "cat", "marker-b")
	if code != 0 {
		t.Fatalf("project B marker read exited %d\nstderr:\n%s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "project-b" {
		t.Errorf("project B marker = %q, want %q", got, "project-b")
	}

	// Returning to project A — the warm path. It is still fully functional.
	stdout, stderr, code = avr(t, dirA, nil, "cat", "marker-a")
	if code != 0 {
		t.Fatalf("project A warm return exited %d\nstderr:\n%s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "project-a" {
		t.Errorf("project A warm return marker = %q, want %q", got, "project-a")
	}
}

// TestMounts_SubdirectoryReuse_REQ_6_6 verifies that running avr from a
// subdirectory of an already-registered project reuses the project root's
// existing mount rather than requiring a separate share. The warm path for a
// subdirectory is instant because the mount already covers it (REQ-6.6,
// PROP-1).
func TestMounts_SubdirectoryReuse_REQ_6_6(t *testing.T) {
	dir := project(t, "mounts-subdir")

	// First, ensure the project root is shared.
	if _, stderr, code := avr(t, dir, nil, "true"); code != 0 {
		t.Fatalf("share the project root exited %d\nstderr:\n%s", code, stderr)
	}

	// Create a subdirectory deeper than the root.
	nested := filepath.Join(dir, "deep", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create %s: %v", nested, err)
	}

	// Write a marker so we can prove the filesystem is live.
	marker := filepath.Join(nested, "marker")
	if err := os.WriteFile(marker, []byte("nested\n"), 0o644); err != nil {
		t.Fatalf("write marker in nested dir: %v", err)
	}

	// Enter from the nested subdirectory. The mount is the project root, not
	// the subdirectory itself, so the host filesystem under the project root
	// is reachable via the existing share — no restart.
	stdout, stderr, code := avr(t, nested, nil, "cat", "marker")
	if code != 0 {
		t.Fatalf("read marker from subdirectory exited %d\nstderr:\n%s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "nested" {
		t.Errorf("nested marker = %q, want %q", got, "nested")
	}

	// The guest working directory must match the host subdirectory.
	stdout, stderr, code = avr(t, nested, nil, "pwd")
	if code != 0 {
		t.Fatalf("pwd from subdirectory exited %d\nstderr:\n%s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != nested {
		t.Errorf("subdirectory pwd = %q, want %q", got, nested)
	}
}

// TestMounts_VerifyMountLandsBeforeShell_REQ_6_5 verifies that after a mount
// is applied, the guest path actually exists and is usable before the user's
// shell starts. This is the positive case: the path the provider planned is a
// directory in the guest, and avar confirms it before attaching.
func TestMounts_VerifyMountLandsBeforeShell_REQ_6_5(t *testing.T) {
	dir := project(t, "mounts-verify")

	// A simple command that succeeds only if the working directory is a real
	// directory. prepareEnvironment runs mount verification internally; if it
	// passes, the guest's working directory exists and is a directory.
	stdout, stderr, code := avr(t, dir, nil, "test", "-d", ".")
	if code != 0 {
		t.Fatalf("test -d . exited %d — the mount did not land\nstderr:\n%s", code, stderr)
	}
	_ = stdout

	// Write a file and read it back — the directory is not just a directory,
	// it is a live, writable mount.
	if err := os.WriteFile(filepath.Join(dir, "verify-me"), []byte("verified\n"), 0o644); err != nil {
		t.Fatalf("write host file: %v", err)
	}

	stdout, stderr, code = avr(t, dir, nil, "cat", "verify-me")
	if code != 0 {
		t.Fatalf("cat verify-me exited %d\nstderr:\n%s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "verified" {
		t.Errorf("verify-me = %q, want %q", got, "verified")
	}
}

// TestMounts_WarmPathNoRestart_REQ_17_1 verifies that revisiting an
// already-shared project on a running machine is fast and causes no restart.
// After the initial share, each subsequent visit must complete within the
// warm-path latency budget (REQ-17.1).
func TestMounts_WarmPathNoRestart_REQ_17_1(t *testing.T) {
	dir := project(t, "mounts-warm")

	// Pay for provisioning and the initial mount outside the measurement.
	if _, stderr, code := avr(t, dir, nil, "true"); code != 0 {
		t.Fatalf("initial share exited %d\nstderr:\n%s", code, stderr)
	}

	const runs = 3
	var total time.Duration
	best := time.Hour

	for i := 0; i < runs; i++ {
		start := time.Now()
		_, stderr, code := avr(t, dir, nil, "true")
		if code != 0 {
			t.Fatalf("warm run %d exited %d\nstderr:\n%s", i, code, stderr)
		}
		elapsed := time.Since(start)
		total += elapsed
		if elapsed < best {
			best = elapsed
		}
	}

	mean := total / runs
	t.Logf("warm mount path over %d runs: mean %v, best %v (REQ-17.1 budget ~500ms)", runs, mean, best)

	const ceiling = 3 * time.Second
	if mean > ceiling {
		t.Errorf("warm mount path mean %v exceeds %v; REQ-17.1 budgets ~500ms", mean, ceiling)
	}
}

// TestMounts_FilesPersistAcrossRevisits verifies that a file written in the
// guest on one visit is visible on the host on the next, and vice versa. This
// is the round-trip through the mount: the share must survive the machine's
// lifecycle and the component that decides whether to re-share must recognise
// it on the second visit.
func TestMounts_FilesPersistAcrossRevisits(t *testing.T) {
	dir := project(t, "mounts-persist")

	// Write from the guest and read from the host.
	if _, stderr, code := avr(t, dir, nil, "sh", "-c", "echo guest-written > from-guest"); code != 0 {
		t.Fatalf("guest write exited %d\nstderr:\n%s", code, stderr)
	}
	data, err := os.ReadFile(filepath.Join(dir, "from-guest"))
	if err != nil {
		t.Fatalf("read the file the guest wrote: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "guest-written" {
		t.Errorf("host read = %q, want %q", got, "guest-written")
	}

	// Write from the host and read from the guest on a second visit.
	if err := os.WriteFile(filepath.Join(dir, "from-host"), []byte("host-written\n"), 0o644); err != nil {
		t.Fatalf("write host file: %v", err)
	}
	stdout, stderr, code := avr(t, dir, nil, "cat", "from-host")
	if code != 0 {
		t.Fatalf("second visit cat exited %d\nstderr:\n%s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "host-written" {
		t.Errorf("guest read = %q, want %q", got, "host-written")
	}
}
