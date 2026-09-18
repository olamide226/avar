package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/olamide226/avar/internal/state"
)

// buildHelper compiles this command for the host, so its behaviour is tested
// as the scheduler sees it: a process, its arguments, and its exit status.
func buildHelper(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "avrw")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building avrw: %v\n%s", err, out)
	}
	return bin
}

// runHelper runs the helper against its own state directory with the given
// arguments, returning its output and exit status.
func runHelper(t *testing.T, bin, home string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	// A helper that regressed into reading its arguments would run a real
	// `avr shell`: find the host's backend, and register the idle check with
	// the real scheduler, which is how a test once left a developer's launchd
	// agent pointing at a deleted test binary. So the child gets no PATH to
	// find a backend or launchctl on, and a home of its own to write into.
	cmd.Env = []string{
		state.HomeEnv + "=" + home,
		"HOME=" + home,
		"USERPROFILE=" + home,
		"PATH=",
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		cmd.Env = append(cmd.Env, "SystemRoot="+root)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("running avrw: %v", err)
	}
	return out.String(), errOut.String(), code
}

// REQ-5.5: the helper runs the idle check and nothing else, whatever it is
// given. Asked for help it prints none, because it never read the request, and
// with nothing idle it exits 0, which Task Scheduler reports as the last
// result.
func TestHelper_RunsOnlyTheIdleCheck_REQ_5_5(t *testing.T) {
	bin := buildHelper(t)
	home := t.TempDir()

	for _, args := range [][]string{nil, {"--help"}, {"shell"}} {
		stdout, stderr, code := runHelper(t, bin, home, args...)
		if code != 0 || stdout != "" {
			t.Errorf("avrw %q: exit %d, stdout %q, stderr %q; want exit 0 and no output", args, code, stdout, stderr)
		}
	}
}

// REQ-17.7: what the helper runs is the real idle check, so a config.toml it
// cannot read makes it exit non-zero and stop nothing, and Task Scheduler
// records a failed run.
func TestHelper_ReportsAnUnreadableConfigAsAFailedRun_REQ_17_7(t *testing.T) {
	bin := buildHelper(t)
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("idle_timout = \"0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runHelper(t, bin, home, "--help")

	if code == 0 {
		t.Errorf("avrw exited 0 with an unreadable config.toml; stderr:\n%s", stderr)
	}
}
