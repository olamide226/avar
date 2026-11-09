//go:build e2e

// See harness_test.go for what these tests are and how they are run.
//
// What the Lima and WSL halves share once a test has a state directory of its
// own: reading it back, writing config.toml into it, and the scripts and waits
// the idle-check tests use. None of it knows which backend it is running
// against; how a test gets a backend of its own does, and stays in each half.

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stateDirOf reads AVR_HOME out of a test's environment.
func stateDirOf(t *testing.T, env []string) string {
	t.Helper()

	const prefix = "AVR_HOME="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	t.Fatalf("no %s in %v", prefix, env)
	return ""
}

// assertNoEnvironments fails unless avar is managing nothing, which is what
// "nothing was started" means when the backend is this test's own.
func assertNoEnvironments(t *testing.T, dir string, env []string) {
	t.Helper()

	stdout, stderr, code := avr(t, dir, env, "status")
	if code != 0 {
		t.Fatalf("avr status exited %d\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "not managing any Linux environments") {
		t.Errorf("an environment exists that no command was supposed to create:\n%s", stdout)
	}
}

// brokenSetting is one key away from idle_timeout. The misspelling is the whole
// point: the lenient reader this replaced ignored it, so a user who had turned
// idle auto-stop down to half an hour kept the two-hour default and had nothing
// to tell them (docs/lessons.md, "A lenient reader turns every mistake into a
// setting that silently does not apply").
const brokenSetting = "idle_timeot = \"30m\"\n"

// writeBrokenConfig puts a config.toml with one misspelt key in the state
// directory the environment names, and returns its path.
func writeBrokenConfig(t *testing.T, env []string) string {
	t.Helper()

	path := filepath.Join(stateDirOf(t, env), "config.toml")
	if err := os.WriteFile(path, []byte(brokenSetting), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// idleTimeout is what the idle tests set in config.toml. It is seconds rather
// than the default two hours because the alternative is a test that sleeps for
// two hours; nothing in the mechanism is sensitive to the number.
const idleTimeout = "1s"

// idleSettle is how long to wait before a check that should find the
// environment idle. It is comfortably longer than idleTimeout: the clock starts
// when the last session detaches, which is when the previous avr command
// returned.
const idleSettle = 3 * time.Second

// noProcps is what the plant script exits with when the guest image has no
// pkill, so the test says why it cannot run rather than failing as though avar
// were at fault.
const noProcps = 3

// plantScript starts a process with the shape of a connected VS Code window and
// waits until the guest's own /proc shows it, so the test never races the probe.
//
// It is written to a file in the project rather than passed to `sh -c`, and
// that is not a style choice: a script passed inline becomes the guest shell's
// own command line, which would then contain --type=extensionHost and make
// every "is it still there?" check answer yes forever.
const plantScript = `#!/bin/sh
command -v pkill >/dev/null 2>&1 || exit 3

dir="$HOME/.vscode-server/bin/e2e"
mkdir -p "$dir"
cp /bin/sh "$dir/node"

# sh accepts a name after -c and otherwise ignores it, so the extension host's
# argument rides along without the program having to understand it. The loop
# keeps this shell alive: a simple command would be exec'd over, and the
# process avar looks for would be the sleep.
setsid "$dir/node" -c 'while :; do sleep 60; done' --type=extensionHost </dev/null >/dev/null 2>&1 &

i=0
while [ "$i" -lt 40 ]; do
	if pgrep -f 'type=extensionHost' >/dev/null 2>&1; then
		echo planted
		exit 0
	fi
	sleep 0.25
	i=$((i + 1))
done
echo 'the planted process never appeared' >&2
exit 1
`

// closeScript ends the window, and waits until it is really gone: a process
// that is still dying would keep the machine for another timeout.
const closeScript = `#!/bin/sh
pkill -f 'type=extensionHost' 2>/dev/null

i=0
while [ "$i" -lt 40 ]; do
	if ! pgrep -f 'type=extensionHost' >/dev/null 2>&1; then
		echo closed
		exit 0
	fi
	sleep 0.25
	i=$((i + 1))
done
echo 'the planted process is still running' >&2
exit 1
`

// writeIdleTimeout sets idle_timeout in the config.toml of the state directory
// the environment names.
func writeIdleTimeout(t *testing.T, env []string, timeout string) {
	t.Helper()

	path := filepath.Join(stateDirOf(t, env), "config.toml")
	if err := os.WriteFile(path, []byte("idle_timeout = \""+timeout+"\"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeScript puts an executable script in the project, where the guest sees it.
func writeScript(t *testing.T, dir, name, body string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// waitForFile waits for a file the guest is expected to create.
func waitForFile(t *testing.T, path string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s never appeared, so the session that was to hold the environment never started", path)
}

// nonEmptyLines splits output into its meaningful lines.
func nonEmptyLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
