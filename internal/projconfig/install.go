package projconfig

import (
	"fmt"

	"github.com/olamide226/avar/internal/types"
)

// InstallCommands returns the commands that install names into an environment
// of distribution d, in order, each as an argv for the guest user to run.
//
// Which package manager a distribution uses is avar's own vocabulary, not a
// backend's, so this is decided here and run through any provider's Shell
// (design §3.11). Every command goes through `sudo -n`: the guest user has
// passwordless sudo (REQ-1.4), and -n turns a guest that unexpectedly asks for
// a password into a failure rather than a hang with nobody to answer it.
//
// The names are checked again even though Parse already refused anything
// else. They are about to reach a package manager running as root, and this
// function must be safe whoever calls it.
func InstallCommands(d types.Distro, names []string) ([][]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	for _, name := range names {
		if !ValidPackageName(name) {
			return nil, fmt.Errorf("install packages: %q is not a package name avar will install", name)
		}
	}

	switch d {
	case types.DistroUbuntu, types.DistroDebian:
		// apt-get rather than apt, whose command-line interface is documented
		// as unstable for scripts. The package lists on a fresh image are
		// empty, so an update always comes first; DEBIAN_FRONTEND stops a
		// package's configuration prompt from waiting on a terminal nobody is
		// watching.
		apt := []string{"sudo", "-n", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get"}
		update := append(append([]string(nil), apt...), "update")
		install := append(append(append([]string(nil), apt...), "install", "-y"), names...)
		return [][]string{update, install}, nil
	case types.DistroFedora:
		return [][]string{
			append([]string{"sudo", "-n", "dnf", "install", "-y"}, names...),
		}, nil
	default:
		return nil, fmt.Errorf("install packages: avar does not know how to install packages on %q", d)
	}
}
