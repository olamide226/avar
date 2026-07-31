//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// With no snapshots on a machine, `avr snapshot` prints a helpful message
// instead of an empty table (REQ-10.4).
func TestAvr_EmptySnapshotList_REQ_10_4(t *testing.T) {
	dir := project(t, "snapshots-list-empty")

	stdout, stderr, code := avr(t, dir, nil, "snapshot")
	if code != 0 {
		t.Fatalf("avr snapshot exited %d\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "No snapshots") {
		t.Errorf("expected a 'no snapshots' message, got stdout:\n%s", stdout)
	}
}

// `avr snapshot <name>` captures a named snapshot of the current environment
// (REQ-10.1).
func TestAvr_SnapshotCaptureAndList_REQ_10_1(t *testing.T) {
	dir := project(t, "snapshots-capture")

	const name = "e2e-test-snapshot"
	stdout, stderr, code := avr(t, dir, nil, "snapshot", name)
	if code != 0 {
		t.Fatalf("avr snapshot %s exited %d\nstderr:\n%s", name, code, stderr)
	}
	if !strings.Contains(stdout, name) {
		t.Errorf("expected the snapshot name %q in the output, got:\n%s", name, stdout)
	}

	// Listing must include the snapshot just created, with a timestamp.
	stdout, stderr, code = avr(t, dir, nil, "snapshot")
	if code != 0 {
		t.Fatalf("avr snapshot list exited %d\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, name) {
		t.Errorf("listing does not include %q:\n%s", name, stdout)
	}
}

// `avr restore <name>` restores a previously captured snapshot (REQ-10.2).
func TestAvr_RestoreSnapshot_REQ_10_2(t *testing.T) {
	dir := project(t, "snapshots-restore")

	const name = "e2e-restore-test"
	stdout, stderr, code := avr(t, dir, nil, "snapshot", name)
	if code != 0 {
		t.Fatalf("avr snapshot %s exited %d\nstderr:\n%s", name, code, stderr)
	}

	stdout, stderr, code = avr(t, dir, nil, "restore", name)
	if code != 0 {
		t.Fatalf("avr restore %s exited %d\nstderr:\n%s", name, code, stderr)
	}
	if !strings.Contains(stdout, name) {
		t.Errorf("expected the snapshot name %q in the restore output, got:\n%s", name, stdout)
	}
}

// Restoring an unknown name reports the error and suggests the available
// snapshots so the user can pick one without another command (REQ-10.2).
func TestAvr_RestoreUnknownNameSuggestsAvailable_REQ_10_2(t *testing.T) {
	dir := project(t, "snapshots-restore-unknown")

	stdout, stderr, code := avr(t, dir, nil, "restore", "nonexistent-snapshot")
	if code == 0 {
		t.Fatalf("avr restore of an unknown name succeeded when it should have failed\nstdout:\n%s", stdout)
	}
	// The error output must mention the name and offer available snapshots.
	if !strings.Contains(stderr, "nonexistent-snapshot") {
		t.Errorf("the error does not mention the requested snapshot name:\n%s", stderr)
	}
	// It must suggest running `avr snapshot` or list available ones.
	if !strings.Contains(stderr, "snapshot") {
		t.Errorf("the error does not suggest listing available snapshots:\n%s", stderr)
	}
}
