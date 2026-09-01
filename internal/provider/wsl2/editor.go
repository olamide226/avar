package wsl2

import (
	"context"
	"fmt"

	"github.com/olamide226/avar/internal/provider"
)

// EditorTarget describes how VS Code reaches a directory inside an avar
// environment.
//
// It returns an authority and no SSH material at all, which is the whole reason
// Provider.EditorTarget describes a target rather than an SSH stanza. VS Code
// resolves `wsl+<distribution>` through its own WSL integration: there is no
// endpoint to configure, no host entry to write, no key to manage, and no line
// to add to the user's ~/.ssh/config — so avar must not generate any of it
// (REQ-18.10, PROP-17).
//
// The empty SSHConfig is how the caller knows there is nothing to write, without
// having to ask which backend answered. cmd/code.go writes SSH material only
// when a backend hands it some, so the Windows path costs it no branch of its
// own (design §3.9).
func (p *Provider) EditorTarget(ctx context.Context, machine, guestPath string) (provider.EditorTarget, error) {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return provider.EditorTarget{}, err
	}

	d, err := p.newView().require(ctx, machine)
	if err != nil {
		return provider.EditorTarget{}, err
	}
	if d.WSLVersion == 1 {
		return provider.EditorTarget{}, newWSL1Error(machine)
	}
	if !d.Running {
		// The target describes a live endpoint, and VS Code attaching to a
		// stopped distribution would start it outside avar's knowledge.
		return provider.EditorTarget{}, fmt.Errorf("%w: %s is stopped", provider.ErrMachineNotRunning, machine)
	}

	return provider.EditorTarget{
		Authority: EditorAuthority(machine),
		GuestPath: guestPath,
	}, nil
}

// EditorAuthority is the remote authority VS Code uses for a WSL distribution.
//
// It is exported so a test can assert the exact string an editor is given
// without rebuilding the rule, which would be asserting its own arithmetic.
func EditorAuthority(machine string) string { return "wsl+" + machine }
