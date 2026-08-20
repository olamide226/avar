//go:build windows

package state

import (
	"fmt"

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

// stateDirSDDL is the access-control list avar puts on its state directory.
//
// D:PAI is a discretionary list that does not inherit from the parent
// directory — which is the whole point, since inheriting is what avar is
// replacing — and the three entries grant full control, inheritable by
// everything beneath, to the directory's owner, the Administrators group and
// SYSTEM.
//
// The last two are not a weakening. An administrator can take ownership of any
// file on the machine and SYSTEM can read any of them, so excluding them would
// buy no privacy and would break backup and antivirus software that expects to
// be able to walk the profile. What the list keeps out is other interactive
// users of the same machine, which is the threat REQ-9 actually names.
const stateDirSDDL = "D:PAI(A;OICI;FA;;;OW)(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)"

// tightenPerm gives the directory avar's own access-control list, unless it
// already has one.
//
// The check is what keeps this off the warm path in any meaningful sense: a
// protected list is one that was set deliberately rather than inherited, so
// finding one means avar has already been here and there is nothing to do
// (REQ-17.1). Every invocation pays one security query, which is immaterial
// beside the subprocesses it is about to run.
func tightenPerm(dir string) error {
	if protected, err := hasProtectedDACL(dir); err == nil && protected {
		return nil
	}

	descriptor, err := windows.SecurityDescriptorFromString(stateDirSDDL)
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

// hasProtectedDACL reports whether the directory already carries a list of its
// own rather than one inherited from its parent.
func hasProtectedDACL(dir string) (bool, error) {
	descriptor, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return false, err
	}
	return control&windows.SE_DACL_PROTECTED != 0, nil
}
