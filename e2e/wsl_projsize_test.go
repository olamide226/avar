//go:build e2e && windows

// See harness_test.go for what these tests are and how they are run.
//
// Not a port of projsize_darwin_test.go: on WSL the behaviour is the reverse.
// Every distribution runs in the one virtual machine WSL keeps per Windows
// account, so the size of one environment is not something avar can set, and
// the WSL backend is not a MachineSizer. A .avr.toml's cpus and memory
// therefore never refuse a command here (REQ-15.5) — and the failure to watch
// for is the setting being dropped without a word, which is the lenient-reader
// defect in docs/lessons.md over again. What must happen instead is one notice
// that the size cannot apply, and nothing claiming to know what the host has.
//
// It creates its environment with the file already in place, rather than
// sharing the confirm test's, because the creating invocation is the one that
// would refuse were the size checked: on an environment that already exists,
// avar never refuses a size anyway.

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// REQ-15.5, PROP-25: a size the host cannot apply does not block the command,
// and the user is told once that it cannot apply.
func TestWSL_ProjectSizeIsAdvisedNotRefused_REQ_15_5(t *testing.T) {
	dir, env := wslOwnProject(t, "wsize")

	configPath := filepath.Join(dir, ".avr.toml")
	if err := os.WriteFile(configPath, []byte("cpus = 999\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", configPath, err)
	}
	before := registeredDistributions(t)

	stdout, stderr, code := avr(t, dir, env, "--isolate", "true")
	if code != 0 {
		t.Fatalf("`avr --isolate true` refused a size this host cannot set; on WSL it must carry on\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if made := newAvarDistributions(t, before); len(made) != 1 || !strings.HasPrefix(made[0], isolatedNamePrefix) {
		t.Fatalf("`avr --isolate true` registered %v, want exactly one %s distribution", made, isolatedNamePrefix)
	}

	notice := configPath + " sets 999 CPU, but on this host every Linux environment shares one allocation, so the size of one environment cannot be set."
	if n := strings.Count(stderr, notice); n != 1 {
		t.Errorf("stderr carries the notice %d times, want exactly once:\n  want %q\nstderr:\n%s", n, notice, stderr)
	}
	// Nothing that claims to know what this host has, and nothing that says
	// the command stopped: neither is true here.
	for _, absent := range []string{"this computer has", "Nothing was changed"} {
		if strings.Contains(stderr, absent) {
			t.Errorf("stderr says %q, which a host that cannot report its capacity, and a command that carried on, must not:\n%s", absent, stderr)
		}
	}

	// Once: the same file, the same environment, no second notice.
	stdout, stderr, code = avr(t, dir, env, "--isolate", "true")
	if code != 0 {
		t.Fatalf("the second `avr --isolate true` exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.Contains(stderr, "shares one allocation") {
		t.Errorf("the notice was repeated for a declaration already advised:\n%s", stderr)
	}
	assertOnlyEnvironment(t, dir, env)
}
