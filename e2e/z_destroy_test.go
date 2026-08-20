//go:build e2e && darwin

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDestroy_RemovesTheEnvironmentAndLeavesTheProject_REQ_5_6_PROP_10 proves
// the two halves of the promise against a real machine: the environment is
// genuinely gone, and the project directory it shared is untouched.
//
// The file name starts with z_ so it runs last: it destroys the shared
// environment every other test in this package depends on.
func TestDestroy_RemovesTheEnvironmentAndLeavesTheProject_REQ_5_6_PROP_10(t *testing.T) {
	dir := project(t, "destroy-shared")

	// A file on the host, and one written from inside the guest, so that both
	// directions of the share are represented in what must survive.
	hostFile := filepath.Join(dir, "from-host.txt")
	if err := os.WriteFile(hostFile, []byte("host wrote this\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := avr(t, dir, nil, "sh", "-c", "echo 'guest wrote this' > from-guest.txt"); code != 0 {
		t.Fatalf("writing from the guest: exit %d\nstderr: %s", code, stderr)
	}

	// The environment exists now. Match the shared Ubuntu row specifically:
	// a bare "Ubuntu" also matches the base image and any isolated machine
	// another test left behind, which would make this assertion pass whatever
	// destroy did.
	stdout, _, code := avr(t, dir, nil, "status")
	if code != 0 || !hasSharedUbuntu(stdout) {
		t.Fatalf("expected a shared Ubuntu environment before destroying it:\n%s", stdout)
	}

	stdout, stderr, code := avr(t, dir, nil, "destroy", "--yes")
	if code != 0 {
		t.Fatalf("avr destroy --yes: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Destroyed") {
		t.Errorf("destroy did not confirm what it did:\n%s", stdout)
	}

	// PROP-10: project files are shared, never copied, so destroying the
	// machine cannot take them with it.
	for _, name := range []string{"from-host.txt", "from-guest.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("destroy lost a host project file (%s): %v", name, err)
		}
	}

	// And the environment really is gone, not merely stopped.
	stdout, _, _ = avr(t, dir, nil, "status")
	if hasSharedUbuntu(stdout) {
		t.Errorf("the shared environment is still listed after being destroyed:\n%s", stdout)
	}
}

// hasSharedUbuntu reports whether `avr status` lists a shared Ubuntu
// environment, as opposed to the base image or an isolated one.
func hasSharedUbuntu(status string) bool {
	for _, line := range strings.Split(status, "\n") {
		if strings.Contains(line, "Ubuntu 24.04") && strings.Contains(line, "shared") {
			return true
		}
	}
	return false
}

// Destroying an environment that does not exist is not an error and must not
// create one on the way to removing it.
func TestDestroy_NothingToDestroy_REQ_5_6(t *testing.T) {
	dir := project(t, "destroy-absent")

	stdout, stderr, code := avr(t, dir, nil, "destroy", "--yes")
	if code != 0 {
		t.Fatalf("avr destroy on a fresh project: exit %d\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "nothing to destroy") {
		t.Errorf("expected avar to say there is nothing to destroy:\n%s", stdout)
	}
}
