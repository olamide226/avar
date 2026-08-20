//go:build unix

package state

import (
	"fmt"
	"os"
)

// tightenPerm removes group and other permissions from an existing directory.
//
// A state directory created by an older version, a broken umask, or a stray
// chmod must not stay readable by other local users: it lists every project
// path the user works in (REQ-9).
func tightenPerm(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("inspect avar state directory %s: %w", dir, err)
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(dir, dirPerm); err != nil {
		return fmt.Errorf("restrict permissions on avar state directory %s: %w", dir, err)
	}
	return nil
}
