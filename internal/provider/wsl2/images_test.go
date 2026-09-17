// No build constraint, unlike this package's behaviour tests: the registry is a
// table of names with no host path in it, so the answer is the same on every
// host and the check is worth having wherever the suite runs.

package wsl2

import (
	"testing"

	"github.com/olamide226/avar/internal/resolve"
)

// Every environment the resolver accepts can be installed from WSL's registry.
// The documentation site's environment matrix is generated from the resolver
// and states that every distribution and release in it is available on
// Windows; this is what keeps that sentence true. Architecture is not part of
// the question: WSL runs the host's own, and checkSupported refuses the other.
func TestRegistry_CoversEveryEnvironmentTheResolverAccepts_REQ_18_6(t *testing.T) {
	environments := resolve.SupportedEnvironments()
	if len(environments) == 0 {
		t.Fatal("the resolver reports no supported environments, so this test would check nothing")
	}
	for _, sel := range environments {
		if _, err := lookupRegistry(sel); err != nil {
			t.Errorf("%s: %v", sel.Label(), err)
		}
	}
}
