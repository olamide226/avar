//go:build e2e

// Package e2e exercises avar against a real Lima installation.
//
// These tests provision actual virtual machines, so they are behind a build tag
// and run only via `make e2e`. They are deliberately absent from `make test`
// and from CI: a unit suite that takes eight minutes and needs a hypervisor is
// a suite people stop running.
//
// They share one machine on purpose. Provisioning is the slow part, and the
// warm path — machine already running — is what REQ-17.1's latency budget is
// about, so the first test pays for creation and the rest measure what a user
// actually experiences. They are written to survive being run twice in a row.
package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// avrBinary is built once for the whole suite.
var avrBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "avr-e2e-*")
	if err != nil {
		panic("create a temporary directory for the avr binary: " + err.Error())
	}
	defer os.RemoveAll(dir)

	avrBinary = filepath.Join(dir, "avr")
	build := exec.Command("go", "build", "-o", avrBinary, ".")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		panic("build avr for the end-to-end suite: " + err.Error() + "\n" + string(out))
	}

	os.Exit(m.Run())
}

// avr runs the built binary in dir and returns its output and exit code.
//
// Stdin is deliberately not a terminal, so these runs take the non-PTY path —
// the same one a script or a pipeline gets (REQ-2.3).
func avr(t *testing.T, dir string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, avrBinary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)

	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf

	err := cmd.Run()
	code = cmd.ProcessState.ExitCode()
	if err != nil && code < 0 {
		t.Fatalf("avr %v in %s did not run: %v\nstderr:\n%s", args, dir, err, errBuf.String())
	}
	return out.String(), errBuf.String(), code
}

// project makes a directory for a test to stand in. t.TempDir is not used:
// avar mounts the project into the guest, and macOS's per-test temporary paths
// are long, symlinked through /var, and awkward to reason about when the same
// path has to exist inside Linux.
func project(t *testing.T, name string) string {
	t.Helper()

	dir := filepath.Join(os.Getenv("HOME"), "avr-e2e", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create the project directory %s: %v", dir, err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return resolved
}

// The cold path: no machine yet, so this provisions one and then runs the
// command, all within the single invocation (REQ-1.2).
func TestAvr_ColdStartProvisionsAndRuns_REQ_1_2(t *testing.T) {
	dir := project(t, "cold")

	stdout, stderr, code := avr(t, dir, nil, "true")
	if code != 0 {
		t.Fatalf("avr true exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}

// A guest command's status is avar's status, whatever it is (PROP-3).
func TestAvr_PropagatesGuestExitCode_PROP_3(t *testing.T) {
	dir := project(t, "exit-code")

	for _, want := range []int{0, 1, 42, 255} {
		_, stderr, code := avr(t, dir, nil, "sh", "-c", "exit "+strconv.Itoa(want))
		if code != want {
			t.Errorf("avr sh -c 'exit %d' exited %d, want %d\nstderr:\n%s", want, code, want, stderr)
		}
	}
}

// The guest starts in the same directory the user was standing in, including
// from a subdirectory of the project (PROP-1, REQ-6.6).
func TestAvr_GuestWorkingDirectoryMatchesTheHost_PROP_1(t *testing.T) {
	dir := project(t, "cwd")

	stdout, stderr, code := avr(t, dir, nil, "pwd")
	if code != 0 {
		t.Fatalf("avr pwd exited %d\nstderr:\n%s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != dir {
		t.Errorf("guest pwd = %q, want the host directory %q", got, dir)
	}

	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create %s: %v", nested, err)
	}
	stdout, stderr, code = avr(t, nested, nil, "pwd")
	if code != 0 {
		t.Fatalf("avr pwd in a subdirectory exited %d\nstderr:\n%s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != nested {
		t.Errorf("guest pwd = %q, want the host subdirectory %q", got, nested)
	}
}

// Files written on either side are visible on the other with no sync step
// (REQ-6.2).
func TestAvr_ProjectFilesAreLiveInBothDirections_REQ_6_2(t *testing.T) {
	dir := project(t, "files")

	if err := os.WriteFile(filepath.Join(dir, "from-host"), []byte("host\n"), 0o644); err != nil {
		t.Fatalf("write the host file: %v", err)
	}

	stdout, stderr, code := avr(t, dir, nil, "cat", "from-host")
	if code != 0 {
		t.Fatalf("avr cat exited %d\nstderr:\n%s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "host" {
		t.Errorf("guest read %q from the host's file, want %q", got, "host")
	}

	if _, stderr, code = avr(t, dir, nil, "sh", "-c", "echo guest > from-guest"); code != 0 {
		t.Fatalf("avr writing a file exited %d\nstderr:\n%s", code, stderr)
	}
	written, err := os.ReadFile(filepath.Join(dir, "from-guest"))
	if err != nil {
		t.Fatalf("the file the guest wrote is not visible on the host: %v", err)
	}
	if got := strings.TrimSpace(string(written)); got != "guest" {
		t.Errorf("host read %q from the guest's file, want %q", got, "guest")
	}
}

// Nothing crosses into the guest that the user did not grant (PROP-4, REQ-9.1).
func TestAvr_HostEnvironmentDoesNotLeakIntoTheGuest_PROP_4(t *testing.T) {
	dir := project(t, "env")

	const marker = "AVR_E2E_SECRET"
	stdout, stderr, code := avr(t, dir, []string{marker + "=leaked"}, "env")
	if code != 0 {
		t.Fatalf("avr env exited %d\nstderr:\n%s", code, stderr)
	}
	if strings.Contains(stdout, marker) {
		t.Errorf("%s crossed into the guest; the guest environment was:\n%s", marker, stdout)
	}
	// TERM is in the allowlist and must be present, or full-screen programs
	// render badly enough to look like a bug (REQ-3.2).
	if !strings.Contains(stdout, "TERM=") {
		t.Errorf("TERM is missing from the guest environment:\n%s", stdout)
	}
}

// The guest runs as a non-root user matching the host, with passwordless sudo
// (REQ-1.4).
func TestAvr_GuestUserMatchesTheHostAndHasSudo_REQ_1_4(t *testing.T) {
	dir := project(t, "user")

	stdout, stderr, code := avr(t, dir, nil, "whoami")
	if code != 0 {
		t.Fatalf("avr whoami exited %d\nstderr:\n%s", code, stderr)
	}
	if got, want := strings.TrimSpace(stdout), os.Getenv("USER"); got != want {
		t.Errorf("guest user = %q, want the host user %q", got, want)
	}

	stdout, stderr, code = avr(t, dir, nil, "sudo", "-n", "id", "-u")
	if code != 0 {
		t.Fatalf("passwordless sudo failed with %d\nstderr:\n%s", code, stderr)
	}
	if got := strings.TrimSpace(stdout); got != "0" {
		t.Errorf("sudo id -u = %q, want %q", got, "0")
	}
}

// avar's own output must never reach stdout, or a pipeline consuming the
// guest's output would be corrupted by a provisioning notice (REQ-2.3).
func TestAvr_StdoutCarriesOnlyTheGuestOutput_REQ_2_3(t *testing.T) {
	dir := project(t, "streams")

	stdout, _, code := avr(t, dir, nil, "echo", "only-this")
	if code != 0 {
		t.Fatalf("avr echo exited %d", code)
	}
	if got := strings.TrimSpace(stdout); got != "only-this" {
		t.Errorf("stdout = %q, want exactly the guest's output %q", got, "only-this")
	}
}

// The warm path is what a user experiences all day: the machine is already
// running, and avar should add very little to it (REQ-17.1).
//
// The measurement is reported whether or not it meets the budget. A number that
// fails is information; a test that hides it is not.
func TestAvr_WarmPathLatency_REQ_17_1(t *testing.T) {
	dir := project(t, "warm")

	// Pay for any provisioning outside the measurement.
	if _, stderr, code := avr(t, dir, nil, "true"); code != 0 {
		t.Fatalf("warming up failed with %d\nstderr:\n%s", code, stderr)
	}

	const runs = 5
	var total time.Duration
	best := time.Hour
	for i := 0; i < runs; i++ {
		start := time.Now()
		if _, _, code := avr(t, dir, nil, "true"); code != 0 {
			t.Fatalf("warm run %d exited %d", i, code)
		}
		elapsed := time.Since(start)
		total += elapsed
		if elapsed < best {
			best = elapsed
		}
	}

	mean := total / runs
	t.Logf("warm path over %d runs: mean %v, best %v (REQ-17.1 budget ~500ms)", runs, mean, best)

	// The budget is avar's own overhead. The whole invocation includes
	// process start and the SSH round trip, so this is a generous ceiling
	// that still catches a real regression.
	const ceiling = 3 * time.Second
	if mean > ceiling {
		t.Errorf("warm path mean %v exceeds %v; REQ-17.1 budgets ~500ms of avar overhead", mean, ceiling)
	}
}
