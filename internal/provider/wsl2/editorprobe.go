package wsl2

import (
	"context"
	"fmt"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/editors"
)

// An editor opened on a WSL distribution reaches it through the editor's own
// WSL integration and leaves no host process avar can see, so the guest is the
// only place to look.
var _ provider.EditorProber = (*Provider)(nil)

// ConnectedEditors reports the editor windows connected to the distribution,
// found by the shared guest probe (internal/provider/editors).
//
// A distribution that is not running is an empty answer, and it is never
// entered: wsl.exe starts a stopped distribution in order to run anything in
// it, and a probe that starts the machine it is asking about would undo the
// stop it was asked to decide. Failing to reach a running one is an error,
// because a caller that read it as "no editor" would stop a distribution
// somebody is working in.
func (p *Provider) ConnectedEditors(ctx context.Context, machine string) ([]provider.EditorConnection, error) {
	if err := p.gate(ctx, machine, ownershipPrefix); err != nil {
		return nil, err
	}
	d, ok, err := p.view().lookup(ctx, machine)
	if err != nil {
		return nil, err
	}
	if !ok || !d.Running {
		return nil, nil
	}

	// guestShellArgv runs as root, as the listener probe does. Command lines
	// are world-readable, so root sees nothing an editor's own account could
	// not; it is used because it is how this backend runs its probes.
	out, err := p.run(ctx, guestShellArgv(machine, editors.Script)...)
	if err != nil {
		return nil, fmt.Errorf("looking for editor windows connected to environment %s: %w", machine, err)
	}
	return editors.Parse(out), nil
}
