//go:build e2e && windows

// See harness_test.go for what these tests are and how they are run. This file
// is part of the WSL half.

package e2e

import (
	"strings"
	"testing"
)

// REQ-12.3: --ssh-agent is refused on a backend that cannot lend the guest the
// host's SSH agent, before anything is started.
//
// This belongs to the WSL half because it is WSL that cannot: the Lima backend
// forwards the agent, so on macOS the flag works and there is nothing to
// refuse. That asymmetry is the whole point of the test. A flag that weakens or
// strengthens a security boundary must never be accepted silently — the user
// believes the grant happened, and finds out when a `git push` inside Linux
// asks for a password (docs/lessons.md, "`--ssh-agent` was accepted, plumbed,
// and did nothing").
func TestWSL_RefusesToForwardTheSSHAgent_REQ_12_3(t *testing.T) {
	requireWSL(t)
	dir := project(t, "ssh-agent")

	// A command with output of its own, so "nothing was started" can be
	// checked rather than taken on the message's word.
	stdout, stderr, code := avr(t, dir, suiteEnv(t), "--ssh-agent", "sh", "-c", "echo the-session-ran")
	if code == 0 {
		t.Fatalf("`avr --ssh-agent` succeeded on a backend that cannot forward an agent\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if code != 2 {
		t.Errorf("`avr --ssh-agent` exited %d, want 2: a flag this backend cannot honour is a usage error", code)
	}
	if strings.Contains(stdout, "the-session-ran") {
		t.Errorf("the session ran despite the refusal, so the user got one without the agent they asked for:\n%s", stdout)
	}

	report := stdout + stderr
	for _, want := range []string{"--ssh-agent", "not supported", "Nothing was started"} {
		if !strings.Contains(report, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, report)
		}
	}

	// And avar is otherwise fine: the refusal is about the flag, not about the
	// environment, which still runs commands.
	stdout, stderr, code = avr(t, dir, suiteEnv(t), "sh", "-c", "echo without-the-flag")
	if code != 0 || strings.TrimSpace(stdout) != "without-the-flag" {
		t.Fatalf("without --ssh-agent the same command should run: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
}
