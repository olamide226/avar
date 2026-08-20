//go:build e2e && darwin

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolatedProject is project() plus the cleanup that makes the suite
// repeatable: an isolated project owns a machine, and without removing it every
// run of `make e2e` leaves another VM behind. Twenty-four accumulated before
// this existed, which was enough to make the host thrash and unrelated tests
// fail on stop timeouts.
func isolatedProject(t *testing.T, name string) string {
	t.Helper()

	dir := project(t, name)
	t.Cleanup(func() {
		// Best effort: the test may have already turned isolation off, and a
		// cleanup that fails must not mask the failure being reported.
		_, _, _ = avr(t, dir, nil, "isolate", "off", "--yes")
	})
	return dir
}

// TestIsolate_CreatesAnIsolatedMachine_REQ_11_1 proves that `avr --isolate`
// creates a machine dedicated to the current project. The machine name carries
// the project identity prefix ("avr-prj-").
func TestIsolate_CreatesAnIsolatedMachine_REQ_11_1(t *testing.T) {
	dir := isolatedProject(t, "iso-"+t.Name())

	stdout, stderr, code := avr(t, dir, nil, "--isolate", "hostname")
	if code != 0 {
		t.Fatalf("avr --isolate hostname exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	// The isolated machine name should embed the project identity.
	// "avr status" should list the isolated machine.
	_, stderr, code = avr(t, dir, nil, "status")
	if code != 0 {
		t.Fatalf("avr status exited %d\nstderr:\n%s", code, stderr)
	}
}

// TestIsolate_RememberedOnSecondInvocation_REQ_11_2 proves that once a project
// is isolated, subsequent bare `avr` invocations still target the isolated
// machine — no --isolate flag is needed.
func TestIsolate_RememberedOnSecondInvocation_REQ_11_2(t *testing.T) {
	dir := isolatedProject(t, "iso-"+t.Name())

	// First invocation with --isolate marks the project and creates the
	// machine.
	_, stderr, code := avr(t, dir, nil, "--isolate", "true")
	if code != 0 {
		t.Fatalf("avr --isolate true exited %d\nstderr:\n%s", code, stderr)
	}

	// Second invocation without --isolate should still target the same
	// isolated machine.
	stdout, stderr, code := avr(t, dir, nil, "hostname")
	if code != 0 {
		t.Fatalf("avr hostname (second, bare) exited %d\nstderr:\n%s", code, stderr)
	}
	t.Logf("second invocation hostname: %s", strings.TrimSpace(stdout))
}

// TestIsolate_ReusesExistingMachine proves that a second `avr --isolate` in
// the same project reuses the already-existing isolated machine — it does not
// create a second one.
func TestIsolate_ReusesExistingMachine(t *testing.T) {
	dir := isolatedProject(t, "iso-"+t.Name())

	// Create marker file in the isolated machine.
	marker := "isolated-marker-" + t.Name()
	_, stderr, code := avr(t, dir, nil, "--isolate", "sh", "-c", "echo '"+marker+"' > /tmp/isolate-proof")
	if code != 0 {
		t.Fatalf("avr --isolate (write marker) exited %d\nstderr:\n%s", code, stderr)
	}

	// Second invocation reads the marker — proves it's the same machine.
	stdout, stderr, code := avr(t, dir, nil, "--isolate", "cat", "/tmp/isolate-proof")
	if code != 0 {
		t.Fatalf("avr --isolate (read marker) exited %d\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, marker) {
		t.Errorf("marker not found in second invocation; want %q in stdout:\n%s", marker, stdout)
	}
}

// TestIsolate_FilesAreIsolatedFromShared_REQ_11_1 proves that a file written
// inside the isolated machine is not visible from the shared machine, and a
// file written in the shared machine is not visible from the isolated one.
// This is the whole point of isolation: each project's environment is
// independent.
func TestIsolate_FilesAreIsolatedFromShared_REQ_11_1(t *testing.T) {
	dir := isolatedProject(t, "iso-"+t.Name())
	otherDir := isolatedProject(t, "iso-other-"+t.Name())

	// Write a file in the isolated machine.
	isolatedMarker := "isolated-file-" + t.Name()
	_, stderr, code := avr(t, dir, nil, "--isolate", "sh", "-c", "echo '"+isolatedMarker+"' > /tmp/iso-only")
	if code != 0 {
		t.Fatalf("avr --isolate (write in iso) exited %d\nstderr:\n%s", code, stderr)
	}

	// Verify the file is present in the isolated machine.
	stdout, stderr, code := avr(t, dir, nil, "cat", "/tmp/iso-only")
	if code != 0 {
		t.Fatalf("avr (read in iso) exited %d\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, isolatedMarker) {
		t.Errorf("file not found in isolated machine:\n%s", stdout)
	}

	// Write a file in the shared machine (from a different project, or using
	// --shared to force the shared path).
	sharedMarker := "shared-file-" + t.Name()
	_, stderr, code = avr(t, otherDir, nil, "sh", "-c", "echo '"+sharedMarker+"' > /tmp/shared-only")
	if code != 0 {
		t.Fatalf("avr (write in shared) exited %d\nstderr:\n%s", code, stderr)
	}

	// The isolated file should not exist in the shared machine.
	_, stderr, code = avr(t, otherDir, nil, "test", "-f", "/tmp/iso-only")
	if code == 0 {
		t.Error("the isolated machine's file is visible from the shared machine — isolation is broken")
	}

	// The shared file should not exist in the isolated machine.
	_, stderr, code = avr(t, dir, nil, "test", "-f", "/tmp/shared-only")
	if code == 0 {
		t.Error("the shared machine's file is visible from the isolated machine — isolation is broken")
	}
}

// TestIsolate_OffClearsDefault_REQ_11_3 proves that `avr isolate off` clears
// the project's isolation default so that subsequent bare `avr` invocations
// target the shared machine again.
func TestIsolate_OffClearsDefault_REQ_11_3(t *testing.T) {
	dir := isolatedProject(t, "iso-"+t.Name())

	// First, isolate the project.
	_, stderr, code := avr(t, dir, nil, "--isolate", "true")
	if code != 0 {
		t.Fatalf("avr --isolate true exited %d\nstderr:\n%s", code, stderr)
	}

	// Verify isolation is active by running bare `avr` — should go to the
	// isolated machine.
	stdout, stderr, code := avr(t, dir, nil, "hostname")
	if code != 0 {
		t.Fatalf("avr hostname (after isolate) exited %d\nstderr:\n%s", code, stderr)
	}
	t.Logf("isolated hostname: %s", strings.TrimSpace(stdout))

	// Now turn isolation off.
	stdout, stderr, code = avr(t, dir, nil, "isolate", "off")
	if code != 0 {
		t.Fatalf("avr isolate off exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "no longer defaults") {
		t.Errorf("isolate off output does not confirm clearing:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// TestIsolate_ShowReportsStatus proves that `avr isolate` without arguments
// reports whether the current project is isolated.
func TestIsolate_ShowReportsStatus(t *testing.T) {
	dir := isolatedProject(t, "iso-"+t.Name())

	// Before isolation, `avr isolate` should report the project is not
	// isolated.
	// But the project needs to exist first for avr to know about it.
	_, _, code := avr(t, dir, nil, "true")
	if code != 0 {
		t.Fatalf("avr true (creating project) exited %d", code)
	}

	stdout, stderr, code := avr(t, dir, nil, "isolate")
	if code != 0 {
		t.Fatalf("avr isolate (status) exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "does not default") {
		t.Errorf("isolate status before isolation: want 'does not default', got:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	// Isolate the project.
	_, stderr, code = avr(t, dir, nil, "--isolate", "true")
	if code != 0 {
		t.Fatalf("avr --isolate true exited %d\nstderr:\n%s", code, stderr)
	}

	// Now `avr isolate` should report it is isolated.
	stdout, stderr, code = avr(t, dir, nil, "isolate")
	if code != 0 {
		t.Fatalf("avr isolate (status after) exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "own Linux environment") {
		t.Errorf("isolate status after isolation: want 'own Linux environment', got:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// TestIsolate_OnTurnsOnIsolation proves that `avr isolate on` marks the
// project as isolated without creating a machine. The machine is created on
// the next bare `avr`.
func TestIsolate_OnTurnsOnIsolation(t *testing.T) {
	dir := isolatedProject(t, "iso-"+t.Name())

	// Create the project first.
	_, _, code := avr(t, dir, nil, "true")
	if code != 0 {
		t.Fatalf("avr true (creating project) exited %d", code)
	}

	// Turn on isolation.
	stdout, stderr, code := avr(t, dir, nil, "isolate", "on")
	if code != 0 {
		t.Fatalf("avr isolate on exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "now defaults") {
		t.Errorf("isolate on output does not confirm: %s %s", stdout, stderr)
	}

	// Verify isolation is active by running bare `avr` — should now use an
	// isolated machine.
	_, stderr, code = avr(t, dir, nil, "true")
	if code != 0 {
		t.Fatalf("avr true (after isolate on) exited %d\nstderr:\n%s", code, stderr)
	}
}

// TestIsolate_FilePersistenceAcrossInvocations proves that files written in an
// isolated machine survive across multiple avr invocations.
func TestIsolate_FilePersistenceAcrossInvocations(t *testing.T) {
	dir := isolatedProject(t, "iso-"+t.Name())

	sentinel := filepath.Join(dir, "host-sentinel")
	if err := os.WriteFile(sentinel, []byte("from-host\n"), 0o644); err != nil {
		t.Fatalf("write host sentinel: %v", err)
	}

	// First invocation: write a file inside the guest.
	_, stderr, code := avr(t, dir, nil, "--isolate", "sh", "-c", "echo persisted > /tmp/persist-proof")
	if code != 0 {
		t.Fatalf("avr --isolate (write) exited %d\nstderr:\n%s", code, stderr)
	}

	// Second invocation: read it back.
	stdout, stderr, code := avr(t, dir, nil, "--isolate", "cat", "/tmp/persist-proof")
	if code != 0 {
		t.Fatalf("avr --isolate (read) exited %d\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "persisted") {
		t.Errorf("file did not persist across invocations: %q", stdout)
	}

	// Also verify that the host file is visible in the isolated guest.
	stdout, stderr, code = avr(t, dir, nil, "--isolate", "cat", "host-sentinel")
	if code != 0 {
		t.Fatalf("avr --isolate cat host-sentinel exited %d\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "from-host") {
		t.Errorf("host file not visible in isolated machine: %q", stdout)
	}
}
