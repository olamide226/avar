//go:build unix

package state

import (
	"fmt"
	"os"
	"path/filepath"
)

// stateDirName is avar's directory inside the home directory.
//
// A dotted directory is where a Unix developer expects to find a tool's state,
// and where they will look for it without being told.
const stateDirName = ".avr"

// defaultStateDir is ~/.avr.
func defaultStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate your home directory for avar's state directory: %w; set %s to choose one explicitly", err, HomeEnv)
	}
	return filepath.Join(home, stateDirName), nil
}
