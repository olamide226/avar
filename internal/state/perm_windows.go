//go:build windows

package state

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The state directory has to stay private on both hosts, and REQ-9 is the same
// requirement either way: it lists every project path the user works in, and on
// Windows it also holds the distributions themselves. What differs is how the
// platform expresses that, and mode bits are not it — os.Stat reports 0777 for
// every Windows directory whatever its real permissions are, and os.Chmod can
// only toggle a file's read-only attribute. The Unix side's check would
// therefore pass on a world-readable directory and its fix would do nothing.
//
// Windows expresses it as an access-control list, and the default is already
// almost right: a directory created beneath %LocalAppData% inherits the user
// profile's list, which grants the user, SYSTEM and the Administrators group and
// nobody else. avar stamps its own list anyway, for the case the default does
// not cover — a state directory somewhere else because AVR_HOME points there,
// or a profile whose inherited permissions somebody has widened.

// stateDirSDDL is the access-control list avar puts on its state directory,
// for the account running it.
//
// D:PAI is a discretionary list that does not inherit from the parent
// directory — which is the whole point, since inheriting is what avar is
// replacing — and the three entries grant full control, inheritable by
// everything beneath, to this user, the Administrators group and SYSTEM.
//
// The user is named by SID rather than left to the OWNER RIGHTS entry this
// list used until 2026-09-22. OWNER RIGHTS grants whoever owns the file, and
// the owner is not always the person running avar: a single `avr` in an
// elevated PowerShell creates its files owned by BUILTIN\Administrators, and
// from then on an ordinary shell matched none of the three entries and could
// not read avar's own records. That is not hypothetical — it locked the
// maintainer out of projects.json, machines.json, sessions.json, idle-task and
// idle_since.json on a real machine, with "Access is denied" from every
// command.
//
// Administrators and SYSTEM are not a weakening. An administrator can take
// ownership of any file on the machine and SYSTEM can read any of them, so
// excluding them would buy no privacy and would break backup and antivirus
// software that expects to walk the profile. What the list keeps out is other
// interactive users of the same machine, which is the threat REQ-9 names.
func stateDirSDDL(userSID string) string {
	return "D:PAI(A;OICI;FA;;;" + userSID + ")(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)"
}

// currentUserSID is the account avar is running as.
func currentUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}

// tightenPerm gives the directory avar's own access-control list, unless it
// already has one that names this account.
//
// The check is what keeps this off the warm path in any meaningful sense: a
// protected list naming this user is one avar set, so finding one means there
// is nothing to do (REQ-17.1). Every invocation pays one security query, which
// is immaterial beside the subprocesses it is about to run.
//
// Re-stamping a list that does not name this user is what repairs a directory
// avar itself locked the user out of, without anybody having to know about
// icacls. Windows propagates the new inheritable entries to the files beneath
// that inherit them.
func tightenPerm(dir string) error {
	sid, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("identify the account avar is running as, to set the access rules on its state directory %s: %w", dir, err)
	}

	if granted, err := daclNamesUser(dir, sid); err == nil && granted {
		return nil
	}

	descriptor, err := windows.SecurityDescriptorFromString(stateDirSDDL(sid.String()))
	if err != nil {
		return fmt.Errorf("build the access rules for avar's state directory %s: %w", dir, err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read the access rules for avar's state directory %s: %w", dir, err)
	}

	if err := windows.SetNamedSecurityInfo(
		dir,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	); err != nil {
		return fmt.Errorf("restrict access to avar's state directory %s: %w", dir, err)
	}
	return nil
}

// daclNamesUser reports whether dir already carries a list of its own that
// names this account, which is both halves of "avar has already been here":
// a list it did not set may name nobody useful, and its own older list named
// only OWNER RIGHTS.
//
// The entries are walked rather than rendered to SDDL and searched, because
// SECURITY_DESCRIPTOR.String asks the system for every kind of security
// information, including the audit list this descriptor was not fetched with,
// and returns an empty string when that fails — a check against which passes
// nothing and quietly reported every list as wrong.
func daclNamesUser(dir string, sid *windows.SID) (bool, error) {
	descriptor, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return false, err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return false, nil
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return false, err
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		if (*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(sid) {
			return true, nil
		}
	}
	return false, nil
}
