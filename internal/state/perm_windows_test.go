//go:build windows

package state

import (
	"os"
	"testing"
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

	protected, err := hasProtectedDACL(st.Root())
	if err != nil {
		t.Fatalf("read the access rules: %v", err)
	}
	if !protected {
		t.Error("the state directory inherits its access rules, so it is as open as wherever it happens to live")
	}

	// Opening again must find the rules already there and leave them alone,
	// which is what keeps the warm path free of a security write it does not
	// need (REQ-17.1).
	if _, err := Open(st.Root()); err != nil {
		t.Fatalf("reopening a state directory avar already secured: %v", err)
	}
}
