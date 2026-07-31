//go:build e2e

package e2e

import (
	"os"
	"strings"
	"testing"
)

// TestSSHAgent_ForwardsOnlyWhenAsked_REQ_12_3_REQ_9_2 proves the two halves of
// the agent boundary against a real machine: --ssh-agent puts a working agent
// socket in the guest, and its absence leaves the guest with none.
//
// This is an end-to-end test rather than a unit test on purpose. The unit test
// can only assert that avar asks ssh to forward the agent; whether the agent
// actually arrives depends on ssh, on Lima's connection multiplexing, and on
// the guest's sshd — none of which avar controls, and the first version of
// this feature was silently defeated by exactly that multiplexing.
func TestSSHAgent_ForwardsOnlyWhenAsked_REQ_12_3_REQ_9_2(t *testing.T) {
	if os.Getenv("SSH_AUTH_SOCK") == "" {
		t.Skip("no SSH agent on the host, so there is nothing to forward")
	}
	dir := project(t, "agent-forwarding")

	// Without the flag the guest must have no agent (REQ-9.2): the agent is a
	// credential and does not cross by default.
	stdout, stderr, code := avr(t, dir, nil, "sh", "-c", `printf '[%s]' "$SSH_AUTH_SOCK"`)
	if code != 0 {
		t.Fatalf("avr without --ssh-agent: exit %d\nstderr: %s", code, stderr)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("the guest had an agent socket without --ssh-agent being given: %q", stdout)
	}

	// With the flag the guest gets a socket, and it must be a live one: a
	// path that exists but answers nothing would pass a naive check while
	// leaving every authenticated operation broken.
	stdout, stderr, code = avr(t, dir, nil, "--ssh-agent", "sh", "-c",
		`printf '[%s]' "$SSH_AUTH_SOCK"; ssh-add -l >/dev/null 2>&1; printf 'rc=%d' "$?"`)
	if code != 0 {
		t.Fatalf("avr --ssh-agent: exit %d\nstderr: %s", code, stderr)
	}
	if strings.Contains(stdout, "[]") {
		t.Fatalf("--ssh-agent did not put an agent socket in the guest: %q", stdout)
	}
	// ssh-add exits 0 with keys and 1 when the agent is reachable but empty.
	// Either proves the channel works; 2 means it could not contact an agent.
	if strings.Contains(stdout, "rc=2") {
		t.Errorf("the forwarded socket exists but no agent answers on it: %q", stdout)
	}
}
