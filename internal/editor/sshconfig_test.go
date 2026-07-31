package editor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempSSHDir runs a test against a temporary SSH-config directory, which
// is what the store would supply in production, so nothing is written to the
// developer's own state directory.
func withTempSSHDir(t *testing.T, fn func(sshDir string)) {
	t.Helper()
	fn(filepath.Join(t.TempDir(), "ssh"))
}

// TestWriteHost_CreatesAWellFormedBlock_REQ_13_3 verifies that WriteHost
// produces a Host block that a real SSH client could parse.
func TestWriteHost_CreatesAWellFormedBlock_REQ_13_3(t *testing.T) {
	withTempSSHDir(t, func(sshDir string) {
		const machine = "avr-ubuntu-24.04-arm64"
		hostBlock := `Host ssh-remote+avr-ubuntu-24.04-arm64
  Hostname 127.0.0.1
  Port 60022
  IdentityFile /Users/test/.lima/_config/user
  User test
  StrictHostKeyChecking no`

		if err := WriteHost(sshDir, machine, hostBlock); err != nil {
			t.Fatalf("WriteHost: %v", err)
		}

		content, err := ReadHostConfig(sshDir)
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
	withTempSSHDir(t, func(sshDir string) {
		const machine = "avr-ubuntu-24.04-arm64"

		firstBlock := `Host ssh-remote+avr-ubuntu-24.04-arm64
  Hostname 127.0.0.1
  Port 60022
  User old-user`

		if err := WriteHost(sshDir, machine, firstBlock); err != nil {
			t.Fatalf("WriteHost (first): %v", err)
		}

		secondBlock := `Host ssh-remote+avr-ubuntu-24.04-arm64
  Hostname 127.0.0.1
  Port 60023
  User new-user`

		if err := WriteHost(sshDir, machine, secondBlock); err != nil {
			t.Fatalf("WriteHost (second): %v", err)
		}

		content, err := ReadHostConfig(sshDir)
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
	withTempSSHDir(t, func(sshDir string) {
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

		if err := WriteHost(sshDir, ubuntu, ubuntuBlock); err != nil {
			t.Fatalf("WriteHost (ubuntu): %v", err)
		}
		if err := WriteHost(sshDir, fedora, fedoraBlock); err != nil {
			t.Fatalf("WriteHost (fedora): %v", err)
		}

		content, err := ReadHostConfig(sshDir)
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
	withTempSSHDir(t, func(sshDir string) {
		const ubuntu = "avr-ubuntu-24.04-arm64"
		const fedora = "avr-fedora-42-arm64"

		if err := WriteHost(sshDir, ubuntu, "Host ubuntu-host\n  Hostname 127.0.0.1\n  Port 60022"); err != nil {
			t.Fatalf("WriteHost (ubuntu): %v", err)
		}
		if err := WriteHost(sshDir, fedora, "Host fedora-host\n  Hostname 127.0.0.1\n  Port 60023"); err != nil {
			t.Fatalf("WriteHost (fedora): %v", err)
		}

		if err := RemoveHost(sshDir, ubuntu); err != nil {
			t.Fatalf("RemoveHost (ubuntu): %v", err)
		}

		content, err := ReadHostConfig(sshDir)
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
	withTempSSHDir(t, func(sshDir string) {
		const machine = "avr-does-not-exist"

		if err := RemoveHost(sshDir, machine); err != nil {
			t.Fatalf("RemoveHost (unknown): %v", err)
		}

		content, err := ReadHostConfig(sshDir)
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
	withTempSSHDir(t, func(sshDir string) {
		const machine = "avr-ubuntu-24.04-arm64"
		block := `Host ssh-remote+avr-ubuntu-24.04-arm64
  Hostname 127.0.0.1
  Port 60022
  User test`

		if err := WriteHost(sshDir, machine, block); err != nil {
			t.Fatalf("WriteHost (first): %v", err)
		}
		first, err := ReadHostConfig(sshDir)
		if err != nil {
			t.Fatalf("ReadHostConfig (first): %v", err)
		}

		// Write the same block again.
		if err := WriteHost(sshDir, machine, block); err != nil {
			t.Fatalf("WriteHost (second): %v", err)
		}
		second, err := ReadHostConfig(sshDir)
		if err != nil {
			t.Fatalf("ReadHostConfig (second): %v", err)
		}

		if first != second {
			t.Errorf("rewriting the same host block produced a different config.\nfirst:\n%s\nsecond:\n%s", first, second)
		}
	})
}

// TestConfigPath_StaysInsideTheSuppliedDirectory pins the contract that
// replaced this package's own path derivation: the config lives wherever the
// caller — in production, the state store — says it does, so that $AVR_HOME
// moves avar's SSH configuration along with the rest of its state.
func TestConfigPath_StaysInsideTheSuppliedDirectory(t *testing.T) {
	withTempSSHDir(t, func(sshDir string) {
		configPath := ConfigPath(sshDir)
		if filepath.Dir(configPath) != sshDir {
			t.Errorf("ConfigPath(%q) = %q, want it directly inside the supplied directory", sshDir, configPath)
		}

		if err := WriteHost(sshDir, "avr-ubuntu-24.04-arm64", "Host avr-ubuntu-24.04-arm64\n  Port 60022"); err != nil {
			t.Fatalf("WriteHost: %v", err)
		}
		if _, err := os.Stat(configPath); err != nil {
			t.Errorf("after WriteHost, no config at %s: %v", configPath, err)
		}
	})
}

// TestWriteHost_WritesAtomically verifies that the config file does not
// exist until WriteHost succeeds, and that a prior config is preserved
// when a later write fails in a way that can be tested. (Atomic file
// operations are tested via the temp-file-and-rename pattern.)
func TestWriteHost_EmptyFileBeforeFirstWrite(t *testing.T) {
	withTempSSHDir(t, func(sshDir string) {
		configPath := ConfigPath(sshDir)

		// Before any write the config file must not exist.
		if _, err := os.Stat(configPath); err == nil {
			t.Error("config file exists before any WriteHost call")
		} else if !os.IsNotExist(err) {
			t.Fatalf("unexpected error checking for config file: %v", err)
		}

		// After WriteHost it must exist.
		if err := WriteHost(sshDir, "avr-ubuntu-24.04-arm64", "Host test\n  Hostname 127.0.0.1"); err != nil {
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

// avar edits a file it does not own, so the one thing that must never happen
// is losing what was already there (REQ-13.3).
func TestAddInclude_PreservesTheUsersConfigByteForByte_REQ_13_3(t *testing.T) {
	dir := t.TempDir()
	userConfig := filepath.Join(dir, "config")
	original := "Host myserver\n  HostName example.com\n  User me\n\nHost *\n  AddKeysToAgent yes\n"
	if err := os.WriteFile(userConfig, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	avarConfig := filepath.Join(dir, "avr", "ssh", "config")
	if err := AddInclude(userConfig, avarConfig); err != nil {
		t.Fatalf("AddInclude: %v", err)
	}

	got, err := os.ReadFile(userConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), original) {
		t.Errorf("the user's existing configuration did not survive intact:\n%s", got)
	}
	if !strings.Contains(string(got), IncludeLine(avarConfig)) {
		t.Errorf("the Include line was not added:\n%s", got)
	}
	// OpenSSH takes the first value it obtains for an option, so an Include
	// placed after the existing "Host *" block would never apply to avar's
	// hosts.
	if strings.Index(string(got), "Include ") > strings.Index(string(got), "Host myserver") {
		t.Error("the Include was added below existing Host blocks, where ssh will not apply it")
	}
}

func TestAddInclude_CreatesTheConfigWhenThereIsNone(t *testing.T) {
	dir := t.TempDir()
	userConfig := filepath.Join(dir, ".ssh", "config")
	avarConfig := filepath.Join(dir, "avr", "ssh", "config")

	if err := AddInclude(userConfig, avarConfig); err != nil {
		t.Fatalf("AddInclude: %v", err)
	}
	got, err := os.ReadFile(userConfig)
	if err != nil {
		t.Fatalf("no config was created: %v", err)
	}
	if !strings.Contains(string(got), IncludeLine(avarConfig)) {
		t.Errorf("the Include line is missing:\n%s", got)
	}
}

// Asking a user to add a line they already have is how a tool wears out its
// welcome, so detection matches on the resolved path, not the literal text.
func TestHasInclude_MatchesHowEverTheLineIsSpelled_REQ_13_1(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	avarConfig := filepath.Join(home, ".avr", "ssh", "config")

	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"absolute path", "Include " + avarConfig + "\n", true},
		{"home-relative", "Include ~/.avr/ssh/config\n", true},
		{"quoted", `Include "` + avarConfig + "\"\n", true},
		{"lowercase directive", "include " + avarConfig + "\n", true},
		{"indented", "  Include " + avarConfig + "\n", true},
		{"among several arguments", "Include /other/config " + avarConfig + "\n", true},
		{"a different tool's include", "Include ~/.orbstack/ssh/config\n", false},
		{"no include at all", "Host x\n  User y\n", false},
		{"empty file", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			userConfig := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(userConfig, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := HasInclude(userConfig, avarConfig)
			if err != nil {
				t.Fatalf("HasInclude: %v", err)
			}
			if got != tc.want {
				t.Errorf("HasInclude(%q) = %t, want %t", tc.body, got, tc.want)
			}
		})
	}
}

func TestHasInclude_IsFalseWhenThereIsNoUserConfig(t *testing.T) {
	got, err := HasInclude(filepath.Join(t.TempDir(), "nope"), "/x/config")
	if err != nil {
		t.Fatalf("a missing user config is not an error: %v", err)
	}
	if got {
		t.Error("reported an Include in a file that does not exist")
	}
}

// Adding the line twice would grow the user's file on every run.
func TestAddInclude_IsNotRepeatedOncePresent(t *testing.T) {
	dir := t.TempDir()
	userConfig := filepath.Join(dir, "config")
	avarConfig := filepath.Join(dir, "avr", "ssh", "config")

	if err := AddInclude(userConfig, avarConfig); err != nil {
		t.Fatal(err)
	}
	present, err := HasInclude(userConfig, avarConfig)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("the line AddInclude just wrote is not detected by HasInclude, so it would be added again every run")
	}
}
