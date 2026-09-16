package editor

import (
	"errors"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const testMachine = "avr-ubuntu-24.04-arm64"

// A project directory whose name would be mangled by naive URL assembly: a
// space, a '#' that would otherwise start a fragment, a '?' that would start a
// query, and a non-ASCII letter.
const awkwardGuestPath = "/Users/dev/code/my app #2?/café"

// Zed's CLI parses an ssh:// argument as a URL and percent-decodes the path it
// reads back out (crates/zed/src/zed/open_listener.rs, parse_ssh_file_path), so
// what matters is that a standard URL parser recovers exactly the host alias
// and the guest path — with no user or port, which would override the ones in
// avar's stanza.
func TestZed_SSHTargetIsAURLNamingTheHostAlias_REQ_13_6(t *testing.T) {
	args, err := Zed.Args("ssh-remote+"+testMachine, awkwardGuestPath)
	if err != nil {
		t.Fatalf("Zed.Args: %v", err)
	}
	if len(args) != 1 {
		t.Fatalf("Zed.Args = %q, want a single ssh:// URL", args)
	}

	u, err := url.Parse(args[0])
	if err != nil {
		t.Fatalf("%q is not a URL: %v", args[0], err)
	}
	if u.Scheme != "ssh" || u.Host != testMachine {
		t.Errorf("%q names %s://%s, want ssh://%s", args[0], u.Scheme, u.Host, testMachine)
	}
	if u.User != nil || u.Port() != "" {
		t.Errorf("%q carries a user or port, which would override avar's SSH stanza", args[0])
	}
	if u.Path != awkwardGuestPath || u.RawQuery != "" || u.Fragment != "" {
		t.Errorf("%q reads back as path %q (query %q, fragment %q), want path %q",
			args[0], u.Path, u.RawQuery, u.Fragment, awkwardGuestPath)
	}
}

func TestZed_WSLTargetUsesZedsWSLFlag_REQ_13_6(t *testing.T) {
	const guest = "/mnt/avr/projects/app-0123456789/src"
	args, err := Zed.Args("wsl+"+testMachine, guest)
	if err != nil {
		t.Fatalf("Zed.Args: %v", err)
	}
	if want := []string{"--wsl", testMachine, guest}; !slices.Equal(args, want) {
		t.Errorf("Zed.Args = %q, want %q", args, want)
	}
}

func TestZed_RefusesTargetsItCannotReach_REQ_13_8(t *testing.T) {
	for _, authority := range []string{"", "ssh-remote+", "wsl+", "fake+" + testMachine, "dev-container+abc", testMachine} {
		_, err := Zed.Args(authority, "/work")
		if !errors.Is(err, ErrUnsupportedTarget) {
			t.Errorf("Zed.Args(%q): got %v, want ErrUnsupportedTarget", authority, err)
			continue
		}
		if strings.Contains(err.Error(), testMachine) {
			t.Errorf("Zed.Args(%q) shows the machine name to the user: %v", authority, err)
		}
		if !strings.Contains(err.Error(), "avr code") {
			t.Errorf("Zed.Args(%q) does not suggest an editor that can open it: %v", authority, err)
		}
	}
}

func TestZed_RefusesARelativeSSHPath(t *testing.T) {
	if _, err := Zed.Args("ssh-remote+"+testMachine, "code/app"); err == nil {
		t.Error("Zed.Args accepted a relative guest path, which would open relative to the remote home")
	}
}

// Cursor keeps VS Code's launcher interface, so the two are given identical
// arguments for every kind of target, including ones avar does not produce.
func TestCursor_TakesVSCodesRemoteArguments_REQ_13_5(t *testing.T) {
	for _, authority := range []string{"ssh-remote+" + testMachine, "wsl+" + testMachine, "fake+" + testMachine} {
		code, err := VSCode.Args(authority, awkwardGuestPath)
		if err != nil {
			t.Fatalf("VSCode.Args(%q): %v", authority, err)
		}
		cursor, err := Cursor.Args(authority, awkwardGuestPath)
		if err != nil {
			t.Fatalf("Cursor.Args(%q): %v", authority, err)
		}
		if want := []string{"--remote", authority, awkwardGuestPath}; !slices.Equal(cursor, want) || !slices.Equal(code, want) {
			t.Errorf("for %q: code %q, cursor %q, want both %q", authority, code, cursor, want)
		}
	}
}

func TestLocate_MissingLauncherSaysHowToInstallIt_REQ_13_2_REQ_13_7(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, ed := range []Editor{VSCode, Cursor, Zed} {
		_, err := ed.Locate()
		if !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("%s: Locate with an empty PATH: got %v, want exec.ErrNotFound", ed.Name, err)
			continue
		}
		if !strings.Contains(err.Error(), "`"+ed.Command+"`") {
			t.Errorf("%s: the error does not name `%s`: %v", ed.Name, ed.Command, err)
		}
		if hint := ed.installHint(runtime.GOOS); !strings.Contains(err.Error(), hint) {
			t.Errorf("%s: the error does not carry this host's install instructions %q: %v", ed.Name, hint, err)
		}
	}
}

// Every editor has install instructions for both hosts avar supports, and they
// name that editor's own command rather than a copy of another's.
func TestInstallHints_NameTheirOwnCommandOnEverySupportedHost(t *testing.T) {
	for _, ed := range []Editor{VSCode, Cursor, Zed} {
		for _, goos := range []string{"darwin", "windows"} {
			if hint := ed.installHint(goos); !strings.Contains(hint, ed.Command) {
				t.Errorf("%s on %s: %q does not mention `%s`", ed.Name, goos, hint, ed.Command)
			}
		}
	}
}

// Zed opens an SSH project by running the system `ssh` against the URL's host
// and inheriting ~/.ssh/config (zed.dev/docs/remote-development). This puts the
// host from the URL avar builds in front of real OpenSSH, through the same
// Include chain `avr zed` sets up, and checks it lands on the stanza's endpoint
// rather than being treated as a DNS name.
//
// It proves the half of the contract that belongs to OpenSSH. That Zed passes
// the host through unchanged is from Zed's source and documentation, not from
// running Zed.
func TestZed_SSHHostResolvesThroughAvarsIncludeWithRealOpenSSH_REQ_13_6(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the SSH target is produced for macOS hosts; Windows paths in Include need their own quoting and are not what this checks")
	}
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("no ssh client on PATH")
	}

	dir := t.TempDir()
	sshDir := filepath.Join(dir, "avr", "ssh")
	// Lima 2.2.0's generated ssh.config with its Host line rewritten to the
	// machine name, as the Lima backend's EditorTarget returns it.
	stanza := "Host " + testMachine + `
  IdentityFile "/Users/dev/.lima/_config/user"
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  NoHostAuthenticationForLocalhost yes
  PreferredAuthentications publickey
  BatchMode yes
  IdentitiesOnly yes
  User dev
  ControlMaster auto
  ControlPath "/Users/dev/.lima/` + testMachine + `/ssh.sock"
  ControlPersist yes
  Hostname 127.0.0.1
  Port 59003`
	if err := WriteHost(sshDir, testMachine, stanza); err != nil {
		t.Fatalf("WriteHost: %v", err)
	}
	userConfig := filepath.Join(dir, "home", ".ssh", "config")
	if err := AddInclude(userConfig, ConfigPath(sshDir)); err != nil {
		t.Fatalf("AddInclude: %v", err)
	}

	args, err := Zed.Args("ssh-remote+"+testMachine, "/Users/dev/code/app")
	if err != nil {
		t.Fatalf("Zed.Args: %v", err)
	}
	u, err := url.Parse(args[0])
	if err != nil {
		t.Fatalf("parsing %q: %v", args[0], err)
	}

	out, err := exec.Command(sshBin, "-G", "-F", userConfig, u.Hostname()).CombinedOutput()
	if err != nil {
		t.Fatalf("ssh -G %s: %v\n%s", u.Hostname(), err, out)
	}
	settings := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			settings[key] = value
		}
	}
	for key, want := range map[string]string{"hostname": "127.0.0.1", "port": "59003", "user": "dev"} {
		if settings[key] != want {
			t.Errorf("ssh resolves %s for %s to %q, want %q from avar's stanza", key, u.Hostname(), settings[key], want)
		}
	}
}
