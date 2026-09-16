package cmd

import (
	"os"
	"testing"
)

// PROP-8: a guest gets a pseudo-terminal only when a person is at one.
// /dev/null is a character device and not a terminal, and `avr <cmd>
// </dev/null` is exactly how a script or a cron job runs avar. Giving that a
// PTY rewrites the guest's line endings in captured output and invites
// programs to prompt a user who is not there.
func TestStdinIsTerminal_DevNullIsNotATerminal_PROP_8(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { devNull.Close() })

	original := os.Stdin
	os.Stdin = devNull
	t.Cleanup(func() { os.Stdin = original })

	if stdinIsTerminal() {
		t.Errorf("stdin from %s was treated as a terminal, so the guest would get a PTY", os.DevNull)
	}
}
