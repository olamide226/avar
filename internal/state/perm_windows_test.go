//go:build windows

package state

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// REQ-9: the state directory lists every project path the user works in, and on
// Windows it holds the environments themselves. It must not be readable by the
// other people who use the machine.
//
// The check is that avar's access rules do not inherit from the parent
// directory, which is what makes them avar's own rather than whatever the
// enclosing directory happened to allow. Windows expresses access control as an
// access-control list, so this is a different test from the Unix one rather than
// the same test with different constants — mode bits report 0777 for every
// directory here whatever its real permissions are.
func TestStore_StateDirectoryIsPrivate_REQ_9(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	if _, err := os.Stat(st.Root()); err != nil {
		t.Fatalf("stat the state directory: %v", err)
	}

	sid, err := currentUserSID()
	if err != nil {
		t.Fatalf("read this account's SID: %v", err)
	}
	named, err := daclNamesUser(st.Root(), sid)
	if err != nil {
		t.Fatalf("read the access rules: %v", err)
	}
	if !named {
		t.Error("the state directory's access rules are inherited, or do not name this account")
	}

	// Opening again must find the rules already there and leave them alone,
	// which is what keeps the warm path free of a security write it does not
	// need (REQ-17.1).
	if _, err := Open(st.Root()); err != nil {
		t.Fatalf("reopening a state directory avar already secured: %v", err)
	}
}

// REQ-9, REQ-17.5: avar repairs a state directory whose rules do not name this
// account, which is the directory avar itself used to create.
//
// Until 2026-09-22 the list granted OWNER RIGHTS rather than the user, so a
// single `avr` run elevated left files owned by BUILTIN\Administrators that an
// ordinary shell could not read: avar locked the user out of its own records.
// Re-stamping the list is what repairs that without anybody reaching for
// icacls, so this plants the old list and asserts avar replaces it.
func TestStore_RepairsRulesThatDoNotNameThisUser_REQ_9(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	descriptor, err := windows.SecurityDescriptorFromString("D:PAI(A;OICI;FA;;;OW)(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)")
	if err != nil {
		t.Fatalf("build the old access rules: %v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("read the old access rules: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatalf("plant the old access rules: %v", err)
	}

	sid, err := currentUserSID()
	if err != nil {
		t.Fatalf("read this account's SID: %v", err)
	}
	if named, err := daclNamesUser(dir, sid); err != nil || named {
		t.Fatalf("the planted rules already name this account (named=%t, err=%v); the test proves nothing", named, err)
	}

	if err := tightenPerm(dir); err != nil {
		t.Fatalf("repair the access rules: %v", err)
	}

	named, err := daclNamesUser(dir, sid)
	if err != nil {
		t.Fatalf("read the repaired access rules: %v", err)
	}
	if !named {
		t.Error("avar left rules that do not name this account, so an elevated run can still lock the user out")
	}
}
