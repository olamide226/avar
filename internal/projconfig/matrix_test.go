package projconfig_test

import (
	"testing"

	"github.com/olamide226/avar/internal/projconfig"
	"github.com/olamide226/avar/internal/resolve"
)

// Every distribution avar can run has a way to install packages, so a package
// list can never be accepted for an environment avar supports and then mean
// nothing there. The matrix is the resolver's, so this lives in an external
// test package: the resolver imports projconfig.
func TestInstallCommands_CoverEverySupportedDistribution(t *testing.T) {
	distros := resolve.SupportedDistros()
	if len(distros) == 0 {
		t.Fatal("the resolver reports no supported distributions, so this test would check nothing")
	}
	for _, d := range distros {
		if _, err := projconfig.InstallCommands(d, []string{"jq"}); err != nil {
			t.Errorf("%s: %v", d, err)
		}
	}
}
