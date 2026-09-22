//go:build e2e && darwin

// See harness_test.go for what these tests are and how they are run.
//
// This file gives the Lima half a second place to stand: a test that must not
// see the machines this computer already has, because of what it does to the
// ones it finds.

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// limaSocketBudget is what Lima adds beneath LIMA_HOME for the SSH control
// socket of the shared environment these tests create:
//
//	/avr-ubuntu-24.04-arm64/ssh.sock.1234567890123456
//
// macOS caps a Unix socket path at UNIX_PATH_MAX = 104 bytes, and Lima refuses
// to create an instance whose socket would exceed it — measured, by doing
// exactly that from a temporary directory: "instance name
// `avr-ubuntu-24.04-arm64` too long: … must be less than UNIX_PATH_MAX=104
// characters, but is 168". That budget is why these directories sit directly in
// the home directory rather than under t.TempDir(), and why their names are
// short.
const limaSocketBudget = len("/avr-ubuntu-24.04-arm64/ssh.sock.1234567890123456")

// unixPathMax is macOS's limit on the length of a Unix socket path.
const unixPathMax = 104

// isolatedBackend gives a test avar's state and Lima's own installation
// somewhere of its own, and returns the environment to run avr with.
//
// AVR_HOME alone is not isolation, and the difference matters enough to state.
// Reconciliation identifies avar's machines by the `avr-` prefix rather than by
// the records avar holds — deliberately, because the missing record is the
// damage it repairs (internal/state/reconcile.go, "Ownership, and the one
// asymmetry in it"). So the first avr run against an empty state directory
// adopts every `avr-` machine this computer's Lima already has, the developer's
// included, and writes them into the test's own machines.json. A test that then
// runs the idle check would find them idle and stop them — during a full run,
// that includes the shared machine every other test in this suite is using.
//
// LIMA_HOME is the part that actually isolates: it puts the backend's instances
// somewhere else entirely, so there is nothing of anybody's to adopt.
// `limactl list` for this computer is unchanged from the first line of such a
// test to the last, whatever the test does.
//
// The cost is a cold provision per test that asks for one, because the new
// LIMA_HOME has no machine to reuse. Measured on an M-series Mac with the image
// already in Lima's download cache, that is about 13 seconds — the image cache
// lives outside LIMA_HOME, which is what keeps it cheap.
//
// Everything created is removed in t.Cleanup through avar's own `destroy`,
// rather than through limactl, so a suite that allocates something expensive
// frees it (docs/lessons.md, "A test suite that leaks resources eventually
// fails tests it has nothing to do with") and does so along the path a user
// would.
func isolatedBackend(t *testing.T, name string) []string {
	t.Helper()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("locate the home directory: %v", err)
	}
	stateDir := filepath.Join(home, "avr-e2e-state-"+name)
	limaDir := filepath.Join(home, "avr-e2e-lima-"+name)

	if len(limaDir)+limaSocketBudget >= unixPathMax {
		t.Skipf("this test needs a Lima home of its own at %s, and Lima could not create an instance there: "+
			"its SSH socket would be longer than the %d bytes macOS allows for a socket path. "+
			"A shorter home directory is the only fix", limaDir, unixPathMax)
	}

	for _, dir := range []string{stateDir, limaDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	env := []string{"AVR_HOME=" + stateDir, "LIMA_HOME=" + limaDir}

	t.Cleanup(func() {
		// Run from the state directory: `destroy --all` does not resolve the
		// current directory, and the test's project directory may already
		// have been removed by its own cleanup.
		stdout, stderr, code := avr(t, stateDir, env, "destroy", "--all", "--yes")
		if code != 0 {
			t.Errorf("cleaning up the isolated backend %s: exit %d\nstdout:\n%s\nstderr:\n%s", name, code, stdout, stderr)
		}
		for _, dir := range []string{stateDir, limaDir} {
			if err := os.RemoveAll(dir); err != nil {
				t.Errorf("removing %s: %v", dir, err)
			}
		}
	})
	return env
}

// ownProject makes a project directory and an isolated backend for it,
// which is what every test in this group wants.
func ownProject(t *testing.T, name string) (dir string, env []string) {
	t.Helper()
	// project() first, so its cleanup runs after the backend's: t.Cleanup is
	// last-in-first-out, and destroying an environment names the project it
	// shared.
	dir = project(t, name)
	return dir, isolatedBackend(t, name)
}

// stateDirOf reads AVR_HOME out of the environment isolatedBackend returned.
func stateDirOf(t *testing.T, env []string) string {
	t.Helper()

	const prefix = "AVR_HOME="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	t.Fatalf("no %s in %v", prefix, env)
	return ""
}

// assertNoEnvironments fails unless avar is managing nothing, which is what
// "nothing was started" means when the backend is this test's own.
func assertNoEnvironments(t *testing.T, dir string, env []string) {
	t.Helper()

	stdout, stderr, code := avr(t, dir, env, "status")
	if code != 0 {
		t.Fatalf("avr status exited %d\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "not managing any Linux environments") {
		t.Errorf("an environment exists that no command was supposed to create:\n%s", stdout)
	}
}
