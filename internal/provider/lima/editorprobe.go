package lima

import (
	"context"
	"fmt"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/editors"
)

// An editor opened on a Lima machine reaches it over SSH and leaves no host
// process avar can see, so the guest is the only place to look.
var _ provider.EditorProber = (*Provider)(nil)

// ConnectedEditors reports the editor windows connected to the machine, found by
// the shared guest probe (internal/provider/editors) run through `limactl shell`
// as the guest user.
//
// A machine that is not running is an empty answer, not an error. Failing to
// reach a running one is an error, because a caller that read it as "no editor"
// would stop a machine somebody is working in.
func (p *Provider) ConnectedEditors(ctx context.Context, machine string) ([]provider.EditorConnection, error) {
	// The prefix alone, as in Status and PortDiagnostics: this is a query
	// (design §3.5, PROP-6).
	if err := p.gate(ctx, machine, ownershipPrefix); err != nil {
		return nil, err
	}
	inst, err := p.newView().require(ctx, machine)
	if err != nil {
		return nil, err
	}
	if !inst.running() {
		return nil, nil
	}

	out, err := p.run(ctx, guestScriptArgv(machine, editors.Script)...)
	if err != nil {
		return nil, fmt.Errorf("looking for editor windows connected to machine %s: %w", machine, err)
	}
	return editors.Parse(string(out)), nil
}
