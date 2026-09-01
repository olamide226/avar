//go:build e2e && windows

// The prerequisite half of the Windows suite: what avar does on a machine where
// WSL is not usable.
//
// These are the states a developer's machine is actually in before avar has ever
// worked there — WSL missing, disabled, or older than avar supports — and they
// are the states unit tests cover thoroughly and end-to-end tests normally
// cannot, because the machine running the suite has WSL, and uninstalling it to
// find out what avar says is not a test anybody will run twice.
//
// So they are reached the other way round: avar resolves wsl.exe through PATH
// first, so a stub earlier on PATH is the tool it finds. That exercises the real
// binary, the real dependency manager, and the real message a user reads, on a
// machine whose own WSL is left completely alone (REQ-18.3).
//
// What they assert, besides the message, is that nothing was created. A refusal
// that leaves a half-registered environment behind is the failure REQ-18.3
// exists to prevent, and it is invisible from the message alone.

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// stubWSL puts a fake wsl.exe at the front of PATH and returns the environment
// that finds it, along with a private state directory.
//
// The stub is a Go program rather than a batch file because avar runs it as a
// program: a .cmd is not something CreateProcess starts without a shell, and
// putting a shell in the middle would test the shell.
func stubWSL(t *testing.T, source string) []string {
	t.Helper()

	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(source), 0o600); err != nil {
		t.Fatalf("write the stub source: %v", err)
	}

	stub := filepath.Join(dir, "wsl.exe")
	build := exec.Command("go", "build", "-o", stub, src)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the wsl.exe stub: %v\n%s", err, out)
	}

	return []string{
		"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"AVR_HOME=" + filepath.Join(dir, "state"),
	}
}

// unusableWSL is a wsl.exe that fails whatever it is asked, which is how the
// real one behaves when the platform is installed but disabled.
const unusableWSL = `package main

import "os"

func main() { os.Exit(1) }
`

// oldWSL is a wsl.exe that reports a version below avar's minimum, in the
// encoding the real one uses.
const oldWSL = `package main

import (
	"os"
	"unicode/utf16"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		text := "WSL version: 1.2.5.0\r\nWindows version: 10.0.26200.8875\r\n"
		out := []byte{}
		for _, u := range utf16.Encode([]rune(text)) {
			out = append(out, byte(u), byte(u>>8))
		}
		os.Stdout.Write(out)
		return
	}
	os.Exit(1)
}
`

// REQ-18.3: a WSL that cannot report a version is one avar cannot drive. With
// nobody to ask for consent — which is what a test, a script, or a CI job is —
// avar must refuse rather than change the machine, and say what to run by hand.
func TestWSLMissing_RefusesAndSaysWhatToRun_REQ_18_3(t *testing.T) {
	dir := project(t, "no-wsl")
	env := stubWSL(t, unusableWSL)

	stdout, stderr, code := avr(t, dir, env, "true")
	if code == 0 {
		t.Fatalf("avr succeeded with no usable WSL\nstdout:\n%s", stdout)
	}

	message := stdout + stderr
	for _, want := range []string{
		// What is wrong.
		"WSL",
		// What to type. A message that only says "not usable" leaves the user
		// searching the web for something avar already knows.
		"wsl --install --no-distribution",
		"wsl --update",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not mention %q:\n%s", want, message)
		}
	}

	// PROP-13: a Windows invocation's dependency work is WSL's alone. Sending a
	// Windows user to install Homebrew or Docker Desktop would be worse than
	// saying nothing.
	for _, forbidden := range []string{"Lima", "limactl", "Homebrew", "brew", "Docker"} {
		if strings.Contains(message, forbidden) {
			t.Errorf("a Windows dependency message mentions %q:\n%s", forbidden, message)
		}
	}
}

// REQ-18.3: nothing is registered when the prerequisite is not met. This is the
// half of the requirement a message cannot show — avar must leave the machine as
// it found it, so that fixing WSL and running the same command again is the
// whole of the recovery.
func TestWSLMissing_RegistersNothing_REQ_18_3(t *testing.T) {
	dir := project(t, "no-wsl-clean")
	env := stubWSL(t, unusableWSL)

	before := realDistributions(t)

	if _, _, code := avr(t, dir, env, "true"); code == 0 {
		t.Fatal("avr succeeded with no usable WSL")
	}

	after := realDistributions(t)
	for name := range after {
		if !before[name] {
			t.Errorf("avar registered %q on a machine where it had decided WSL was unusable", name)
		}
	}

	// avar's own state directory is allowed to exist — opening it is how avar
	// finds out where anything is — but it must hold no environment record.
	stateDir := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, "AVR_HOME=") {
			stateDir = strings.TrimPrefix(entry, "AVR_HOME=")
		}
	}
	machines, err := os.ReadFile(filepath.Join(stateDir, "machines.json"))
	if err == nil && strings.Contains(string(machines), "avr-") {
		t.Errorf("avar recorded an environment it never created:\n%s", machines)
	}
}

// REQ-18.3: a WSL that is present but older than avar supports is a different
// answer from one that is absent — the remedy is an update, not an install —
// and avar must not drive a version whose flags it has no evidence for.
func TestWSLTooOld_RefusesWithTheVersionAndTheRemedy_REQ_18_3(t *testing.T) {
	dir := project(t, "old-wsl")
	env := stubWSL(t, oldWSL)

	stdout, stderr, code := avr(t, dir, env, "true")
	if code == 0 {
		t.Fatalf("avr ran against a WSL below its minimum\nstdout:\n%s", stdout)
	}

	message := stdout + stderr
	// The version found and the version needed: a refusal that names neither
	// leaves the user unable to tell whether updating would even help.
	for _, want := range []string{"1.2.5", "2.0.0", "wsl --update"} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not mention %q:\n%s", want, message)
		}
	}
}

// realDistributions is what WSL actually has registered, read directly rather
// than through avar — the point of these tests is what avar did to the machine,
// and asking avar would be asking the accused.
func realDistributions(t *testing.T) map[string]bool {
	t.Helper()

	out, err := exec.Command("wsl.exe", "--list", "--quiet").Output()
	if err != nil {
		// A host with no distributions at all reports it by failing.
		return map[string]bool{}
	}

	names := map[string]bool{}
	for _, line := range strings.Split(decodeUTF16(out), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names[name] = true
		}
	}
	return names
}
