package lima

import (
	"context"
	"fmt"

	"github.com/olamide226/avar/internal/provider"
)

// Shell is not implemented yet: task 8 (shell attachment) owns this file and
// replaces it wholesale.
//
// It exists only because Provider is one interface and Go requires the whole
// method set. Attaching a guest shell is a substantial piece of work in its own
// right — pseudo-terminal allocation, signal and resize forwarding, exit-code
// propagation, and the environment policy that decides what crosses the
// boundary — and none of it belongs to machine lifecycle.
func (p *Provider) Shell(_ context.Context, machine string, _ provider.ShellOpts) (int, error) {
	return 0, fmt.Errorf("attaching a shell to machine %s: not implemented yet: task 8 (shell attachment)", machine)
}
