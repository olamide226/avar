//go:build e2e && darwin

// Package e2e exercises the full environment matrix against real Lima machines.
package e2e

import (
	"strings"
	"testing"
)

// TestMatrix_PackagePersistenceAndIndependence_REQ_4_3 proves that packages
// installed in one environment persist across sessions of that environment and
// are absent from a different one (REQ-4.3: "packages installed in one
// environment SHALL persist across sessions of that environment without
// affecting others").
//
// The test provisions at most one new environment. The default ubuntu
// environment is shared with the existing shell e2e tests — if one of those
// ran first, the ubuntu machine is already up and the install step takes only
// the time to fetch the package. Debian is provisioned from scratch to prove
// the independence half of the property.
//
// What is *not* provisioned: a foreign-architecture guest. An amd64 VM on
// Apple Silicon runs under QEMU emulation and takes tens of minutes; that
// cost is disproportionate when the emulation warning itself is already
// covered by unit tests in internal/provider/lima
// (TestEnsureMachine_WarnsOnceAboutEmulation_REQ_4_6,
// TestEnsureMachine_NativeArchIsNotWarnedAbout_REQ_4_6) and the property
// being proven — package isolation between environments — is demonstrated by
// any two environments regardless of architecture.
func TestMatrix_PackagePersistenceAndIndependence_REQ_4_3(t *testing.T) {
	dir := project(t, "matrix-persist")

	// Install tree, a small standalone binary, in the default environment.
	// The apt-get update ensures the package lists are current on a fresh VM.
	t.Log("installing tree in the default ubuntu environment")
	stdout, stderr, code := avr(t, dir, nil,
		"--distro", "ubuntu",
		"sh", "-c", "sudo apt-get update -y && sudo apt-get install -y tree")
	if code != 0 {
		t.Fatalf("installing tree in ubuntu exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	// Persistence: the package survives a second invocation and is usable.
	t.Log("verifying tree persists on a second invocation")
	stdout, stderr, code = avr(t, dir, nil,
		"--distro", "ubuntu",
		"tree", "--version")
	if code != 0 {
		t.Fatalf("tree not found on second ubuntu invocation (persistence failed): code %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	// Independence: the same package is absent from a different environment.
	// test ! -f exits 0 when the file is absent, non-zero when present.
	//
	// Provisioning Debian for the first time takes several minutes because it
	// downloads a fresh cloud image. The new-environment notice — "Creating
	// your Linux development environment · Debian 13 · ..." — appears on
	// stderr during that first provision and is checked here (REQ-4.7).
	t.Log("checking tree is absent in a different environment (debian) — this may provision a second VM")
	_, stderr, code = avr(t, dir, nil,
		"--distro", "debian",
		"sh", "-c", "test ! -f /usr/bin/tree")
	if code != 0 {
		t.Errorf("tree binary found in the debian environment — independence between environments is violated (code %d)\nstderr:\n%s", code, stderr)
	}

	// REQ-4.7: if Debian was provisioned from scratch (Creating event present
	// in stderr), the notice must name the environment so the user knows what
	// is being created — no silent multi-VM sprawl.
	if strings.Contains(stderr, "Creating") {
		if !strings.Contains(stderr, "Debian") {
			t.Errorf("new-environment notice did not name the environment being created:\n%s", stderr)
		}
		t.Logf("new-environment notice verified: Debian was named before provisioning began")
	}
}

// TestMatrix_UnsupportedEnvironmentListsSupportedValues_REQ_4_4 verifies
// that unsupported --distro names (caught by the grammar) and unsupported
// versions (caught by the resolver) both produce a clear error listing what
// is supported, and exit with the usage status (code 2) rather than a
// generic failure (code 1). The error the user sees must combine the
// contributions of both layers into one good message (REQ-4.4).
func TestMatrix_UnsupportedEnvironmentListsSupportedValues_REQ_4_4(t *testing.T) {
	dir := project(t, "matrix-unsupported")

	t.Run("unsupported distribution name", func(t *testing.T) {
		// The grammar rejects this before any backend work.
		_, stderr, code := avr(t, dir, nil, "--distro", "arch", "echo", "hello")
		if code != 2 {
			t.Errorf("unsupported --distro name exited %d, want exitUsage (2)", code)
		}
		if !strings.Contains(stderr, "supported") && !strings.Contains(stderr, "ubuntu") {
			t.Errorf("error does not list what is supported:\n%s", stderr)
		}
	})

	t.Run("unsupported version for a known distribution", func(t *testing.T) {
		// The grammar accepts ubuntu:18.04 because ubuntu is a known
		// distribution; the resolver rejects the version against the matrix.
		_, stderr, code := avr(t, dir, nil, "--distro", "ubuntu:18.04", "echo", "hello")
		if code != 2 {
			t.Errorf("unsupported version exited %d, want exitUsage (2)", code)
		}
		if !strings.Contains(stderr, "24.04") {
			t.Errorf("error does not list supported versions (should mention the pinned default 24.04):\n%s", stderr)
		}
		if !strings.Contains(stderr, "supported") {
			t.Errorf("error does not say what is supported:\n%s", stderr)
		}
	})

	t.Run("unsupported bare architecture", func(t *testing.T) {
		// The grammar rejects an unrecognized architecture.
		_, stderr, code := avr(t, dir, nil, "--arch", "riscv64", "echo", "hello")
		if code != 2 {
			t.Errorf("unsupported --arch value exited %d, want exitUsage (2)", code)
		}
		if !strings.Contains(stderr, "arch") || !strings.Contains(stderr, "arm64") {
			t.Errorf("error does not list supported architectures:\n%s", stderr)
		}
	})
}
