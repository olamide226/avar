//go:build e2e && windows

// See harness_test.go for what these tests are and how they are run.
//
// The WSL half of confirm_darwin_test.go. `avr reset` and `avr destroy` delete
// everything inside an environment and ask for its name to be typed first; a
// name arriving on a pipe was written before the summary it answers was
// printed, so nobody can have read what it agreed to (REQ-5.6, REQ-10.3).
//
// The environment is the project's own (wsl_isolated_test.go). Were the guard
// to regress, what it destroyed would be this test's, not the shared Ubuntu
// the rest of the suite — or the developer's own avar — is using under the
// same name.

package e2e

import (
	"runtime"
	"slices"
	"strings"
	"testing"
)

// REQ-5.6, REQ-10.3: with input piped rather than typed at a terminal, both
// commands print their summary, delete nothing, and exit non-zero.
//
// The piped input is the exact answer each command asks for, which is the case
// worth testing: anything else could be refused for being the wrong word.
func TestWSL_ConfirmResetAndDestroyNeedATerminal_REQ_5_6_REQ_10_3(t *testing.T) {
	dir, env := wslOwnProject(t, "wconfirm")
	name := createIsolated(t, dir, env)

	// Written out rather than derived from avar's own label code, so the two
	// cannot agree on something wrong. WSL runs only the host's architecture.
	label := "Ubuntu 24.04 · " + runtime.GOARCH

	summaries := map[string]string{
		"reset":   "You are about to destroy this Linux environment",
		"destroy": "This will permanently destroy",
	}

	for _, command := range []string{"reset", "destroy"} {
		assertOnlyEnvironment(t, dir, env)

		stdout, stderr, code := avrWithInput(t, dir, env, label+"\n", command)
		if code == 0 {
			t.Fatalf("`avr %s` went ahead on a piped answer, with no terminal to confirm at\nstdout:\n%s\nstderr:\n%s",
				command, stdout, stderr)
		}

		report := stdout + stderr
		if !strings.Contains(stdout, summaries[command]) {
			t.Errorf("`avr %s` did not print what it was about to destroy:\n%s", command, stdout)
		}
		if !strings.Contains(stdout, label) {
			t.Errorf("`avr %s` did not name the environment %q in its summary:\n%s", command, label, stdout)
		}
		for _, want := range []string{"terminal", "--yes"} {
			if !strings.Contains(report, want) {
				t.Errorf("`avr %s` did not say how to run it unattended (%q missing):\n%s", command, want, report)
			}
		}

		// Nothing was deleted, asked of WSL and then of the environment
		// itself. A record surviving an unregistered distribution would read
		// as success to `avr status`.
		if !slices.Contains(registeredDistributions(t), name) {
			t.Fatalf("after `avr %s` refused, %q is no longer registered with WSL", command, name)
		}
		stdout, stderr, code = avr(t, dir, env, "sh", "-c", "echo alive")
		if code != 0 || strings.TrimSpace(stdout) != "alive" {
			t.Fatalf("after `avr %s` refused, the environment no longer runs commands: exit %d\nstdout:\n%s\nstderr:\n%s",
				command, code, stdout, stderr)
		}
	}
}
