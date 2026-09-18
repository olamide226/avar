package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/fake"
)

// agentlessProvider is a backend that cannot lend the guest the host's SSH
// agent: the Fake with its SSHAgentForwarder method hidden, the way the WSL
// backend presents itself to the command layer.
type agentlessProvider struct{ provider.Provider }

// sshAgentInvocation is `avr --ssh-agent [command...]`.
func sshAgentInvocation(guest ...string) cli.Invocation {
	inv := cli.Invocation{SSHAgent: true}
	if len(guest) > 0 {
		inv.Mode, inv.Guest = cli.ModeGuestCommand, guest
	}
	return inv
}

// REQ-12.3: on a backend that cannot forward the host SSH agent, `avr
// --ssh-agent` is refused before any machine work. Handing the user a session
// without the agent they asked for is the failure docs/lessons.md records as
// "`--ssh-agent` was accepted, plumbed, and did nothing": they would find out
// only when an authenticated operation failed inside the guest.
func TestSSHAgent_RefusedBeforeMachineWorkWhereUnsupported_REQ_12_3(t *testing.T) {
	for name, inv := range map[string]cli.Invocation{
		"interactive shell": sshAgentInvocation(),
		"one-shot command":  sshAgentInvocation("git", "fetch"),
	} {
		t.Run(name, func(t *testing.T) {
			f := fake.New()
			app := newTestApp(t, agentlessProvider{f})

			err := runGuest(context.Background(), app.App, inv)
			if err == nil {
				t.Fatal("`avr --ssh-agent` succeeded on a backend that cannot forward an agent")
			}
			var exit *ExitCodeError
			if !errors.As(err, &exit) || exit.Code != exitUsage {
				t.Errorf("error = %v, want exit status %d", err, exitUsage)
			}
			for _, want := range []string{"--ssh-agent", "not supported in this environment yet", "Nothing was started"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not say %q: %v", want, err)
				}
			}
			for _, op := range []fake.Op{fake.OpEnsureMachine, fake.OpAppliedMounts, fake.OpSetMounts, fake.OpShell} {
				if n := f.Count(op); n != 0 {
					t.Errorf("%s was called %d time(s) before the refusal, want none", op, n)
				}
			}
		})
	}
}

// REQ-12.3: on a backend that forwards the agent, the grant reaches the
// backend for the user's own session — and only for it. avar's mount probe
// runs on the same machine in the same invocation and never needs the agent.
func TestSSHAgent_ReachesTheSessionWhereSupported_REQ_12_3(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)

	if err := runGuest(context.Background(), app.App, sshAgentInvocation("git", "fetch")); err != nil {
		t.Fatalf("avr --ssh-agent git fetch: %v", err)
	}

	calls := f.CallsFor(fake.OpShell)
	if len(calls) == 0 {
		t.Fatal("no guest session was started")
	}
	session := calls[len(calls)-1]
	if !session.Shell.ForwardSSHAgent {
		t.Error("the session was started without the agent the user asked for")
	}
	for _, probe := range calls[:len(calls)-1] {
		if probe.Shell.ForwardSSHAgent {
			t.Errorf("avar's own guest probe %v was given the user's SSH agent", probe.Shell.Argv)
		}
	}
}

// REQ-9.2: without the flag no session is given the agent, on any backend.
func TestSSHAgent_NotForwardedByDefault_REQ_9_2(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)

	if err := runGuest(context.Background(), app.App, guestInvocation("true")); err != nil {
		t.Fatalf("avr true: %v", err)
	}
	for _, call := range f.CallsFor(fake.OpShell) {
		if call.Shell.ForwardSSHAgent {
			t.Errorf("guest execution %v was given the SSH agent without --ssh-agent", call.Shell.Argv)
		}
	}
}
