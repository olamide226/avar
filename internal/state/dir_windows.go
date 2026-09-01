//go:build windows

package state

import (
	"fmt"
	"os"
	"path/filepath"
)

// stateDirName is avar's directory inside the per-user application data
// directory. It is the product name rather than a dotted directory: a hidden
// dot-directory is a Unix convention that Windows neither hides nor expects.
const stateDirName = "avar"

// defaultStateDir is %LocalAppData%\avar.
//
// Local, not Roaming, and the distinction is not cosmetic. On a domain-joined
// machine the roaming profile is copied to every machine the user signs in to,
// and avar's state is the opposite of portable: it names Linux distributions
// registered with *this* machine's WSL, disk images stored on *this* machine's
// filesystem, and the process ids of sessions running on it. Roaming it would
// carry records of environments that do not exist onto machines that cannot
// have them (REQ-18.13).
//
// It is also where the distributions themselves live, which is the second reason
// the choice matters: a roaming profile is copied over the network at sign-in,
// and a directory holding several gigabytes of virtual disk does not belong in
// one.
func defaultStateDir() (string, error) {
	if dir := os.Getenv("LocalAppData"); dir != "" {
		return filepath.Join(dir, stateDirName), nil
	}
	// LocalAppData is set for every interactive session, but a service account
	// or a stripped environment can be without it. The documented location
	// beneath the profile is the same directory by another name.
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate your application data directory for avar's state: %w; set %s to choose one explicitly", err, HomeEnv)
	}
	return filepath.Join(home, "AppData", "Local", stateDirName), nil
}
