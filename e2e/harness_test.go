//go:build e2e

// Package e2e exercises avar against the real backend for the host it runs on:
// Lima on macOS, WSL 2 on Windows.
//
// These tests create actual Linux environments — a virtual machine on one host,
// a registered distribution on the other — so they are behind a build tag and
// run only via `make e2e`. They are deliberately absent from `make test` and
// from CI: a unit suite that takes eight minutes and needs a hypervisor is a
// suite people stop running.
//
// The two halves are separate files rather than one suite with branches in it,
// because what they assert is genuinely different. Lima shares a project at its
// own path, so the macOS tests check that `avr pwd` prints the host path; WSL
// cannot, so the Windows tests check that it prints the guest path the provider
// planned and that the same bytes are visible from both sides. Writing that as
// one test with a host branch would make each half harder to read than either
// is alone. `shell_test.go` and its siblings are the Lima half; the `wsl_`
// files are the WSL half. This file is what they share: building the binary,
// running it, and giving a test somewhere to stand.
//
// Each half shares one environment on purpose. Provisioning is the slow part,
// and the warm path is what REQ-17.1's latency budget is about, so the first
// test pays for creation and the rest measure what a user actually experiences.
// They are written to survive being run twice in a row.
package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

	// The extension is not cosmetic on Windows: a file without it is not a
	// program, and exec would refuse to start it.
	name := "avr"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	avrBinary = filepath.Join(dir, name)

	build := exec.Command("go", "build", "-o", avrBinary, ".")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		panic("build avr for the end-to-end suite: " + err.Error() + "\n" + string(out))
	}

	code := m.Run()
	cleanupBackend()
	os.Exit(code)
}

// avr runs the built binary in dir and returns its output and exit code.
//
// Stdin is deliberately not a terminal, so these runs take the non-PTY path —
// the same one a script or a pipeline gets (REQ-2.3).
func avr(t *testing.T, dir string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
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

// project makes a directory for a test to stand in.
//
// t.TempDir is not used: avar shares the project into the guest, and a
// per-test temporary path is long, symlinked through /var on macOS, and awkward
// to reason about when the same directory has to be reachable from inside
// Linux.
func project(t *testing.T, name string) string {
	t.Helper()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("locate the home directory: %v", err)
	}
	dir := filepath.Join(home, "avr-e2e", name)
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
