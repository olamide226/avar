//go:build e2e && darwin

// See harness_test.go for what these tests are and how they are run.
//
// A project's .avr.toml may size its own environment, and a size this computer
// cannot give is refused before any machine work rather than discovered as a
// failed boot minutes later (REQ-15.5).

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// REQ-15.5: cpus and memory larger than this computer are refused, naming the
// file, the line, and what the computer actually has — and nothing is created
// on the way to saying so.
//
// The sizes are derived from the host rather than written down. A fixed "64
// CPUs" would be an excess on this Mac and a perfectly ordinary request on a
// larger machine, so the test would pass here and quietly assert nothing there.
func TestProjectSize_OverThisComputerIsRefusedBeforeAnyMachineWork_REQ_15_5(t *testing.T) {
	dir, env := ownProject(t, "size")

	cpus, memoryGiB := hostCapacity(t)
	askCPUs := cpus + 1
	askGiB := memoryGiB + 16

	// Line numbers are part of what the message must get right, so the file is
	// written with distro first and the two sizes on known lines.
	body := fmt.Sprintf("distro = \"ubuntu\"\ncpus = %d\nmemory = \"%dGiB\"\n", askCPUs, askGiB)
	configPath := filepath.Join(dir, ".avr.toml")
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", configPath, err)
	}

	// Sizes apply to the project's own environment, so this is the invocation
	// that would create one at that size.
	stdout, stderr, code := avr(t, dir, env, "--isolate", "true")
	if code == 0 {
		t.Fatalf("`avr --isolate true` accepted a size this computer cannot give\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	report := stdout + stderr
	for _, want := range []string{
		configPath,
		"line 2",
		"line 3",
		fmt.Sprintf("cpus = %d", askCPUs),
		fmt.Sprintf("memory = %q", strconv.Itoa(askGiB)+"GiB"),
		"but this computer has " + strconv.Itoa(cpus) + " CPUs",
		strconv.Itoa(memoryGiB),
		"Nothing was changed",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, report)
		}
	}

	// The refusal has to come before the machine work, not instead of cleaning
	// up after it. This backend was empty when the test began.
	assertNoEnvironments(t, dir, env)
}

// hostCapacity reads what this computer has, the way avar reads it: the logical
// CPU count from the runtime, and physical memory from sysctl. Memory is
// returned floored to whole gibibytes, which is what a .avr.toml can ask for.
func hostCapacity(t *testing.T) (cpus, memoryGiB int) {
	t.Helper()

	out, err := exec.Command("/usr/sbin/sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		t.Skipf("this test needs the computer's memory to write a size larger than it, and `sysctl -n hw.memsize` failed: %v", err)
	}
	bytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || bytes <= 0 {
		t.Skipf("`sysctl -n hw.memsize` printed %q, which is not a size in bytes", strings.TrimSpace(string(out)))
	}
	return runtime.NumCPU(), int(bytes >> 30)
}
