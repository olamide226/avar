//go:build !windows

package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// REQ-17.5: a state file avar cannot read is reported with what to run, not
// only with the operating system's refusal.
//
// The message a user actually met was "open …\projects.json: Access is
// denied." and nothing else, on a Windows machine where an elevated `avr` had
// left avar's records owned by Administrators. The remedy differs by host; that
// there is one does not.
func TestReadJSON_PermissionDeniedSaysWhatToRun_REQ_17_5(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file whatever its mode, so this cannot be provoked")
	}
	path := filepath.Join(t.TempDir(), "projects.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}

	var value map[string]any
	err := readJSON(path, &value)
	if err == nil {
		t.Fatal("reading an unreadable state file succeeded")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the error does not name the file: %v", err)
	}
	if !strings.Contains(err.Error(), "chown") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
}
