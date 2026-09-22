//go:build windows

package state

import (
	"fmt"
	"path/filepath"
)

// permissionRemedy is what to do about a state file this account cannot read
// or write, as a sentence to append to the error.
//
// On Windows the cause is nearly always ownership: a single `avr` run from an
// elevated PowerShell creates its files owned by BUILTIN\Administrators, and
// avar's own access rules named only OWNER RIGHTS until 2026-09-22, so an
// ordinary shell afterwards matched nothing. A current avar repairs the
// directory's rules by itself, but it cannot change rules on a file owned by
// somebody else — that needs an administrator, once.
func permissionRemedy(path string) string {
	dir := filepath.Dir(path)
	return fmt.Sprintf("\n     This account cannot read avar's own state, which happens when an elevated `avr` created it."+
		"\n     In an Administrator PowerShell, once:"+
		"\n       icacls \"%s\" /grant \"$($env:USERDOMAIN)\\$($env:USERNAME):(OI)(CI)F\" /T /C", dir)
}
