//go:build e2e && darwin

// See harness_test.go for what these tests are and how they are run.
//
// `avr reset` and `avr destroy` are the two commands that delete everything
// inside an environment, and both ask for its name to be typed first. A
// confirmation can only come from a person at a terminal: a name arriving on a
// pipe was written before the summary it answers was printed, so nobody can
// have read what it agreed to (REQ-5.6, REQ-10.3).
//
// The unit tests hold that rule against a fake. This holds it against a real
// machine, which is the only place the consequence of getting it wrong — an
// environment actually gone — can be observed. It runs in a backend of its own
// for exactly that reason: were the guard to regress, the environment destroyed
// would be this test's and not the suite's.

package e2e

import (
	"strings"
	"testing"
)

// sharedUbuntuLabel is how avar names the shared Ubuntu environment, and what
// reset and destroy ask to have typed before they delete it. It is written out
// here rather than derived, as the destroy test's Ubuntu row is: a label
// assembled from the same code the command uses would agree with it however
// wrong both were.
const sharedUbuntuLabel = "Ubuntu 24.04 · arm64"

// REQ-5.6, REQ-10.3: with input piped rather than typed at a terminal, both
// commands print their summary, delete nothing, and exit non-zero so that a
// script sees that nothing happened.
//
// The piped input is the exact answer each command asks for, which is the case
// worth testing: anything else could be refused for being the wrong word. A
// pipe carrying the environment's own name is still not somebody having read
// the summary, and treating it as one would make every script that pipes
// anything into avr a script that can destroy an environment by accident.
func TestConfirm_ResetAndDestroyNeedATerminal_REQ_5_6_REQ_10_3(t *testing.T) {
	dir, env := ownProject(t, "confirm")

	if _, stderr, code := avr(t, dir, env, "true"); code != 0 {
		t.Fatalf("creating the environment this test is about: exit %d\nstderr:\n%s", code, stderr)
	}

	// The summary each command must print before asking, so that a refusal is
	// distinguishable from a command that never got as far as looking.
	summaries := map[string]string{
		"reset":   "You are about to destroy this Linux environment",
		"destroy": "This will permanently destroy",
	}

	for _, command := range []string{"reset", "destroy"} {
		stdout, stderr, code := avrWithInput(t, dir, env, sharedUbuntuLabel+"\n", command)
		if code == 0 {
			t.Fatalf("`avr %s` went ahead on a piped answer, with no terminal to confirm at\nstdout:\n%s\nstderr:\n%s",
				command, stdout, stderr)
		}

		report := stdout + stderr
		if !strings.Contains(stdout, summaries[command]) {
			t.Errorf("`avr %s` did not print what it was about to destroy:\n%s", command, stdout)
		}
		if !strings.Contains(stdout, sharedUbuntuLabel) {
			t.Errorf("`avr %s` did not name the environment in its summary:\n%s", command, stdout)
		}
		for _, want := range []string{"terminal", "--yes"} {
			if !strings.Contains(report, want) {
				t.Errorf("`avr %s` did not say how to run it unattended (%q missing):\n%s", command, want, report)
			}
		}

		// Nothing was deleted: the environment is still there and still runs
		// commands. `avr sh` rather than `avr status` on purpose — a record
		// surviving a deleted machine is what reconciliation exists to clean
		// up, and would read as success here.
		stdout, stderr, code = avr(t, dir, env, "sh", "-c", "echo alive")
		if code != 0 || strings.TrimSpace(stdout) != "alive" {
			t.Fatalf("after `avr %s` refused, the environment no longer runs commands: exit %d\nstdout:\n%s\nstderr:\n%s",
				command, code, stdout, stderr)
		}
	}
}
