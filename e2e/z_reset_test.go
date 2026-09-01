//go:build e2e && darwin

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReset_DestroysGuestPackagesAndPreservesHostFiles_REQ_10_3_PROP_10 validates
// the whole of `avr reset`: a marker file written on the host survives, a package
// installed in the guest is gone after reset, and the machine is usable again.
//
// This test destroys and recreates the shared machine, so it runs last by
// convention (file name z_reset_test.go sorts after the other e2e files).
// Every other e2e test therefore runs against a warm machine first.
func TestReset_DestroysGuestPackagesAndPreservesHostFiles_REQ_10_3_PROP_10(t *testing.T) {
	dir := project(t, "reset-e2e")

	// 1. Write a marker file on the host. This file must survive the reset
	//    byte-for-byte: the provider shares the project directory, never copies
	//    it, so destroying the machine cannot lose host files (PROP-10).
	const markerContent = "this-file-survives-reset\n"
	marker := filepath.Join(dir, "MARKER")
	if err := os.WriteFile(marker, []byte(markerContent), 0o644); err != nil {
		t.Fatalf("write the marker file: %v", err)
	}

	// 2. Install a small package in the guest so we can prove it disappears.
	const packageName = "sl"
	_, stderr, code := avr(t, dir, nil, "sudo", "apt-get", "update", "-qq")
	if code != 0 {
		t.Logf("apt-get update non-zero, continuing: %s", stderr)
	}
	_, stderr, code = avr(t, dir, nil, "sudo", "apt-get", "install", "-y", "-qq", packageName)
	if code != 0 {
		t.Fatalf("installing %s: exit %d\nstderr:\n%s", packageName, code, stderr)
	}

	// Verify the package is installed before we reset.
	stdout, stderr, code := avr(t, dir, nil, "dpkg", "-s", packageName)
	if code != 0 {
		t.Fatalf("verifying %s is installed before reset: exit %d\nstdout:\n%s\nstderr:\n%s",
			packageName, code, stdout, stderr)
	}
	if !strings.Contains(stdout, "install ok installed") {
		t.Fatalf("%s does not report as installed before reset:\n%s", packageName, stdout)
	}

	// 3. Reset with --yes to skip the confirmation prompt.
	stdout, stderr, code = avr(t, dir, nil, "reset", "--yes")
	if code != 0 {
		t.Fatalf("avr reset --yes exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Reset complete") {
		t.Errorf("`avr reset` did not confirm completion:\n%s", stdout)
	}

	// 4. The marker file on the host must still be present and unmodified.
	read, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the host marker file is gone after reset: %v", err)
	}
	if string(read) != markerContent {
		t.Errorf("the host marker file was modified by reset:\n  got  %q\n  want %q", string(read), markerContent)
	}

	// 5. After reset, the package must be gone — the machine was rebuilt from
	//    a clean base.
	stdout, stderr, code = avr(t, dir, nil, "dpkg", "-s", packageName)
	if code == 0 && strings.Contains(stdout, "install ok installed") {
		t.Errorf("%s survived the reset; want it gone:\n%s", packageName, stdout)
	}
	t.Logf("package %s correctly disappeared after reset (dpkg exited %d)", packageName, code)

	// 6. The machine is usable after reset: a guest command runs in the project.
	stdout, stderr, code = avr(t, dir, nil, "pwd")
	if code != 0 {
		t.Fatalf("avr pwd after reset exited %d\nstderr:\n%s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != dir {
		t.Errorf("guest pwd after reset = %q, want the host directory %q", got, dir)
	}
}
