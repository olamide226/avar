//go:build !windows

package state

import (
	"fmt"
	"os/user"
	"path/filepath"
)

// permissionRemedy is what to do about a state file this account cannot read
// or write, as a sentence to append to the error.
//
// On a Unix host the state directory belongs to one user and is created 0700,
// so the usual cause is a file left by another account — sudo, most often.
func permissionRemedy(path string) string {
	dir := filepath.Dir(path)
	who := "$USER"
	if u, err := user.Current(); err == nil {
		who = u.Username
	}
	return fmt.Sprintf("\n     This account cannot read avar's own state, which happens when a command run with sudo created it."+
		"\n     Give it back, once:"+
		"\n       sudo chown -R %s %s", who, dir)
}
