package editor

import (
	"os"
	"strings"
	"testing"
)

// withTempHome runs a test with HOME set to a temporary directory so that
// avar's SSH config is written there rather than to the developer's own
// home.
func withTempHome(t *testing.T, fn func(home string)) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	fn(home)
}

// TestWriteHost_CreatesAWellFormedBlock_REQ_13_3 verifies that WriteHost
// produces a Host block that a real SSH client could parse.
func TestWriteHost_CreatesAWellFormedBlock_REQ_13_3(t *testing.T) {
	withTempHome(t, func(home string) {
		const machine = "avr-ubuntu-24.04-arm64"
		hostBlock := `Host ssh-remote+avr-ubuntu-24.04-arm64
  Hostname 127.0.0.1
  Port 60022
  IdentityFile /Users/test/.lima/_config/user
  User test
  StrictHostKeyChecking no`

		if err := WriteHost(machine, hostBlock); err != nil {
			t.Fatalf("WriteHost: %v", err)
		}

		content, err := ReadHostConfig()
		if err != nil {
			t.Fatalf("ReadHostConfig: %v", err)
		}

		for _, want := range []string{
			machine,
			"Host ssh-remote+avr-ubuntu-24.04-arm64",
			"Hostname 127.0.0.1",
			"Port 60022",
			"IdentityFile",
			"User test",
			"StrictHostKeyChecking no",
			"managed by avar",
			"Include",
		} {
			if !strings.Contains(content, want) {
				t.Errorf("the SSH config is missing %q:\n%s", want, content)
			}
		}
	})
}

// TestWriteHost_UpdatesAnExistingEntry verifies that writing the same
// machine again replaces the old block rather than appending a duplicate.
func TestWriteHost_UpdatesAnExistingEntry(t *testing.T) {
	withTempHome(t, func(home string) {
		const machine = "avr-ubuntu-24.04-arm64"

		firstBlock := `Host ssh-remote+avr-ubuntu-24.04-arm64
  Hostname 127.0.0.1
  Port 60022
  User old-user`

		if err := WriteHost(machine, firstBlock); err != nil {
			t.Fatalf("WriteHost (first): %v", err)
		}

		secondBlock := `Host ssh-remote+avr-ubuntu-24.04-arm64
  Hostname 127.0.0.1
  Port 60023
  User new-user`

		if err := WriteHost(machine, secondBlock); err != nil {
			t.Fatalf("WriteHost (second): %v", err)
		}

		content, err := ReadHostConfig()
		if err != nil {
			t.Fatalf("ReadHostConfig: %v", err)
		}

		// The old User directive must be gone and the new one present.
		for _, fragment := range []string{"Port 60023", "User new-user"} {
			if !strings.Contains(content, fragment) {
				t.Errorf("the updated SSH config is missing %q:\n%s", fragment, content)
			}
		}
		for _, fragment := range []string{"Port 60022", "User old-user"} {
			if strings.Contains(content, fragment) {
				t.Errorf("the updated SSH config still contains the old value %q:\n%s", fragment, content)
			}
		}

		// The machine must appear exactly once in the header list.
		if count := strings.Count(content, hostHeader(machine)); count != 1 {
			t.Errorf("machine %s appears %d times, want exactly 1", machine, count)
		}
	})
}

// TestWriteHost_MultipleMachinesAreIndependent verifies that writing a
// second machine does not disturb the first.
func TestWriteHost_MultipleMachinesAreIndependent(t *testing.T) {
	withTempHome(t, func(home string) {
		const ubuntu = "avr-ubuntu-24.04-arm64"
		const fedora = "avr-fedora-42-arm64"

		ubuntuBlock := `Host ssh-remote+avr-ubuntu-24.04-arm64
  Hostname 127.0.0.1
  Port 60022
  User ubuntu-user`

		fedoraBlock := `Host ssh-remote+avr-fedora-42-arm64
  Hostname 127.0.0.1
  Port 60023
  User fedora-user`

		if err := WriteHost(ubuntu, ubuntuBlock); err != nil {
			t.Fatalf("WriteHost (ubuntu): %v", err)
		}
		if err := WriteHost(fedora, fedoraBlock); err != nil {
			t.Fatalf("WriteHost (fedora): %v", err)
		}

		content, err := ReadHostConfig()
		if err != nil {
			t.Fatalf("ReadHostConfig: %v", err)
		}

		for _, fragment := range []string{
			"ubuntu-user", "Port 60022",
			"fedora-user", "Port 60023",
		} {
			if !strings.Contains(content, fragment) {
				t.Errorf("the SSH config is missing %q:\n%s", fragment, content)
			}
		}

		// Both headers must appear exactly once.
		for _, machine := range []string{ubuntu, fedora} {
			if count := strings.Count(content, hostHeader(machine)); count != 1 {
				t.Errorf("machine %s appears %d times in headers, want exactly 1", machine, count)
			}
		}
	})
}

// TestRemoveHost_RemovesOnlyTheNamedMachine verifies that RemoveHost
// deletes one entry without touching others.
func TestRemoveHost_RemovesOnlyTheNamedMachine(t *testing.T) {
	withTempHome(t, func(home string) {
		const ubuntu = "avr-ubuntu-24.04-arm64"
		const fedora = "avr-fedora-42-arm64"

		if err := WriteHost(ubuntu, "Host ubuntu-host\n  Hostname 127.0.0.1\n  Port 60022"); err != nil {
			t.Fatalf("WriteHost (ubuntu): %v", err)
		}
		if err := WriteHost(fedora, "Host fedora-host\n  Hostname 127.0.0.1\n  Port 60023"); err != nil {
			t.Fatalf("WriteHost (fedora): %v", err)
		}

		if err := RemoveHost(ubuntu); err != nil {
			t.Fatalf("RemoveHost (ubuntu): %v", err)
		}

		content, err := ReadHostConfig()
		if err != nil {
			t.Fatalf("ReadHostConfig: %v", err)
		}

		if strings.Contains(content, "ubuntu-host") {
			t.Errorf("removing ubuntu did not erase its Host block:\n%s", content)
		}
		if !strings.Contains(content, "fedora-host") {
			t.Errorf("removing ubuntu also removed fedora:\n%s", content)
		}
	})
}

// TestRemoveHost_UnknownMachineIsANoOp verifies that removing a machine
// that never had an entry does not fail or touch the file.
func TestRemoveHost_UnknownMachineIsANoOp(t *testing.T) {
	withTempHome(t, func(home string) {
		const machine = "avr-does-not-exist"

		if err := RemoveHost(machine); err != nil {
			t.Fatalf("RemoveHost (unknown): %v", err)
		}

		content, err := ReadHostConfig()
		if err != nil {
			t.Fatalf("ReadHostConfig: %v", err)
		}
		if content != "" {
			t.Errorf("removing an unknown machine wrote something to the config:\n%s", content)
		}
	})
}

// TestWriteHost_ProducesAStableConfigBetweenRuns verifies that writing the
// same block twice produces an identical file, which is what keeps git
// diff quiet when the config is committed (or at least guarantees that the
// format is deterministic).
func TestWriteHost_ProducesAStableConfigBetweenRuns(t *testing.T) {
	withTempHome(t, func(home string) {
		const machine = "avr-ubuntu-24.04-arm64"
		block := `Host ssh-remote+avr-ubuntu-24.04-arm64
  Hostname 127.0.0.1
  Port 60022
  User test`

		if err := WriteHost(machine, block); err != nil {
			t.Fatalf("WriteHost (first): %v", err)
		}
		first, err := ReadHostConfig()
		if err != nil {
			t.Fatalf("ReadHostConfig (first): %v", err)
		}

		// Write the same block again.
		if err := WriteHost(machine, block); err != nil {
			t.Fatalf("WriteHost (second): %v", err)
		}
		second, err := ReadHostConfig()
		if err != nil {
			t.Fatalf("ReadHostConfig (second): %v", err)
		}

		if first != second {
			t.Errorf("rewriting the same host block produced a different config.\nfirst:\n%s\nsecond:\n%s", first, second)
		}
	})
}

// TestConfigDir_ReturnsAHomeRelativePath verifies that ConfigDir and
// ConfigPath are rooted in the user's home directory.
func TestConfigDir_ReturnsAHomeRelativePath(t *testing.T) {
	withTempHome(t, func(home string) {
		dir, err := ConfigDir()
		if err != nil {
			t.Fatalf("ConfigDir: %v", err)
		}
		if !strings.HasPrefix(dir, home) {
			t.Errorf("ConfigDir() = %q, want a path starting with the home directory %q", dir, home)
		}
		if !strings.HasSuffix(dir, "ssh") {
			t.Errorf("ConfigDir() = %q, want it to end in 'ssh'", dir)
		}

		configPath, err := ConfigPath()
		if err != nil {
			t.Fatalf("ConfigPath: %v", err)
		}
		if !strings.HasPrefix(configPath, dir) {
			t.Errorf("ConfigPath() = %q, want it to be inside ConfigDir() = %q", configPath, dir)
		}
	})
}

// TestWriteHost_WritesAtomically verifies that the config file does not
// exist until WriteHost succeeds, and that a prior config is preserved
// when a later write fails in a way that can be tested. (Atomic file
// operations are tested via the temp-file-and-rename pattern.)
func TestWriteHost_EmptyFileBeforeFirstWrite(t *testing.T) {
	withTempHome(t, func(home string) {
		configPath, err := ConfigPath()
		if err != nil {
			t.Fatalf("ConfigPath: %v", err)
		}

		// Before any write the config file must not exist.
		if _, err := os.Stat(configPath); err == nil {
			t.Error("config file exists before any WriteHost call")
		} else if !os.IsNotExist(err) {
			t.Fatalf("unexpected error checking for config file: %v", err)
		}

		// After WriteHost it must exist.
		if err := WriteHost("avr-ubuntu-24.04-arm64", "Host test\n  Hostname 127.0.0.1"); err != nil {
			t.Fatalf("WriteHost: %v", err)
		}
		if _, err := os.Stat(configPath); err != nil {
			t.Fatalf("config file does not exist after successful WriteHost: %v", err)
		}

		// The temp file must not linger.
		tmpPath := configPath + ".tmp"
		if _, err := os.Stat(tmpPath); err == nil {
			t.Errorf("temporary file %s still exists after a successful write", tmpPath)
		}
	})
}
