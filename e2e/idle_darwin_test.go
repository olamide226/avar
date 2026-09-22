//go:build e2e && darwin

// See harness_test.go for what these tests are and how they are run.
//
// Idle auto-stop is the one thing avar does that nobody asked for at the moment
// it happens: a scheduled `avr internal idle-check` stops environments that
// have had no activity for the configured time (REQ-5.5). Everything about it
// is about not stopping the wrong one — a machine with a live session is never
// stopped (PROP-11), and neither is one with an editor window connected to it,
// which avar holds no session record of at all (REQ-5.10, REQ-5.11).
//
// # Why these tests have a backend of their own
//
// They run the check, and the check stops machines. Reconciliation identifies
// avar's machines by the `avr-` prefix rather than by avar's records, so a
// fresh AVR_HOME adopts every `avr-` machine this computer already has —
// see isolated_darwin_test.go, which is why these use LIMA_HOME as well. With
// it, the only machine the check can see is the one the test created.
//
// # Why the editor is a real process and not a fixture
//
// The rule is that a *connected window* keeps a machine, and the process that
// exists once per window is VS Code's extension host, inside the editor's
// server directory. A unit test can assert that avar's parser reads a captured
// process listing correctly; only this can assert that avar asks the guest at
// all, over the real transport, and acts on the answer. The process planted
// below is a real copy of /bin/sh sitting where VS Code puts its server, run
// with the argument the extension host carries — the shape of a window, with
// none of VS Code.

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// idleTimeout is what these tests set in config.toml. It is seconds rather than
// the default two hours because the alternative is a test that sleeps for two
// hours; nothing in the mechanism is sensitive to the number.
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

// REQ-5.10, REQ-5.11: an editor window connected to an environment keeps it
// running, and the environment stops once the window is gone.
//
// The second half is what makes the first half mean anything. A check that
// never stopped this environment at all would pass the "kept it" assertion
// perfectly.
func TestIdle_AnEditorWindowKeepsTheEnvironmentRunning_REQ_5_10(t *testing.T) {
	dir, env := ownProject(t, "editor")
	writeIdleTimeout(t, env, idleTimeout)
	writeScript(t, dir, "plant.sh", plantScript)
	writeScript(t, dir, "close.sh", closeScript)

	if _, stderr, code := avr(t, dir, env, "true"); code != 0 {
		t.Fatalf("creating the environment this test is about: exit %d\nstderr:\n%s", code, stderr)
	}

	stdout, stderr, code := avr(t, dir, env, "sh", "plant.sh")
	if code == noProcps {
		t.Skip("this guest image has no pkill, so the test cannot start and stop a process that stands in for an editor window")
	}
	if code != 0 || !strings.Contains(stdout, "planted") {
		t.Fatalf("planting a process shaped like a connected editor window: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	time.Sleep(idleSettle)
	stdout, stderr, code = avr(t, dir, env, "internal", "idle-check")
	if code != 0 {
		t.Fatalf("`avr internal idle-check` exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if report := stdout + stderr; !strings.Contains(report, "kept") || !strings.Contains(report, "VS Code") {
		t.Errorf("the idle check did not say it kept the environment for an editor:\n%s", report)
	}
	if state := limaState(t, env); state != "Running" {
		t.Fatalf("the environment is %s: the idle check stopped a machine with an editor window connected to it (REQ-5.10)", state)
	}

	// Close the window. The machine now has nothing holding it, and the next
	// check after a full timeout stops it.
	stdout, stderr, code = avr(t, dir, env, "sh", "close.sh")
	if code != 0 || !strings.Contains(stdout, "closed") {
		t.Fatalf("closing the window: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	time.Sleep(idleSettle)
	if _, stderr, code := avr(t, dir, env, "internal", "idle-check"); code != 0 {
		t.Fatalf("`avr internal idle-check` exited %d\nstderr:\n%s", code, stderr)
	}
	if state := limaState(t, env); state != "Stopped" {
		t.Errorf("the environment is %s after the window closed; an editor that is gone must stop keeping it (REQ-5.11)", state)
	}
}

// REQ-5.5: an environment nobody is using is stopped once it has been idle for
// the configured time.
func TestIdle_StopsAnEnvironmentNobodyIsUsing_REQ_5_5(t *testing.T) {
	dir, env := ownProject(t, "idle")
	writeIdleTimeout(t, env, idleTimeout)

	if _, stderr, code := avr(t, dir, env, "true"); code != 0 {
		t.Fatalf("creating the environment this test is about: exit %d\nstderr:\n%s", code, stderr)
	}
	if state := limaState(t, env); state != "Running" {
		t.Fatalf("the environment is %s before the idle check, so this test would prove nothing", state)
	}

	time.Sleep(idleSettle)
	if _, stderr, code := avr(t, dir, env, "internal", "idle-check"); code != 0 {
		t.Fatalf("`avr internal idle-check` exited %d\nstderr:\n%s", code, stderr)
	}
	if state := limaState(t, env); state != "Stopped" {
		t.Errorf("the environment is %s: an environment idle for longer than idle_timeout must be stopped (REQ-5.5)", state)
	}
}

// PROP-11: an environment with a live session is never stopped, however long
// the timeout says it has been idle.
//
// The session is a real `avr` holding the machine, not a record written by the
// test: the property is about what avar does with its own bookkeeping, and
// bookkeeping a test wrote is bookkeeping the test can get wrong. The session
// announces itself by touching a file inside the guest, which it can only do
// once avar has recorded it and handed the command over.
//
// Releasing the session and checking again is the control. Without it, a check
// that had simply found nothing to do would pass.
func TestIdle_NeverStopsAnEnvironmentWithALiveSession_PROP_11(t *testing.T) {
	dir, env := ownProject(t, "session")
	writeIdleTimeout(t, env, idleTimeout)

	if _, stderr, code := avr(t, dir, env, "true"); code != 0 {
		t.Fatalf("creating the environment this test is about: exit %d\nstderr:\n%s", code, stderr)
	}

	// A session that outlives the check below, and ends on its own so that
	// nothing has to be killed.
	held := exec.Command(avrBinary, "sh", "-c", "touch attached; sleep 20")
	held.Dir = dir
	held.Env = append(os.Environ(), env...)
	if err := held.Start(); err != nil {
		t.Fatalf("starting the session that holds the environment: %v", err)
	}
	t.Cleanup(func() {
		_ = held.Process.Kill()
		_ = held.Wait()
	})
	waitForFile(t, filepath.Join(dir, "attached"))

	time.Sleep(idleSettle)
	if _, stderr, code := avr(t, dir, env, "internal", "idle-check"); code != 0 {
		t.Fatalf("`avr internal idle-check` exited %d\nstderr:\n%s", code, stderr)
	}
	if state := limaState(t, env); state != "Running" {
		t.Fatalf("the environment is %s while a session is attached to it (PROP-11)", state)
	}

	// The control: the same environment, the same timeout, no session.
	if err := held.Wait(); err != nil {
		t.Fatalf("the session that held the environment did not finish cleanly: %v", err)
	}
	time.Sleep(idleSettle)
	if _, stderr, code := avr(t, dir, env, "internal", "idle-check"); code != 0 {
		t.Fatalf("`avr internal idle-check` exited %d\nstderr:\n%s", code, stderr)
	}
	if state := limaState(t, env); state != "Stopped" {
		t.Errorf("the environment is %s once its session has gone, so the check above proved nothing about the session", state)
	}
}

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

// limaState reads the state of the one machine in this test's own Lima home.
//
// It asks Lima rather than avar: a test that read avar's own report of whether
// it stopped a machine could not tell a machine left running from a machine
// avar believes it stopped (docs/lessons.md, "A test double that shares the
// code's assumption confirms the assumption, not the behaviour").
func limaState(t *testing.T, env []string) string {
	t.Helper()

	cmd := exec.Command("limactl", "list", "--json")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("asking Lima what it has in this test's own home: %v", err)
	}

	// One instance, so one line. More than one would make the answer below
	// ambiguous, and would mean the isolation this test depends on has failed.
	lines := nonEmptyLines(string(out))
	if len(lines) != 1 {
		t.Fatalf("this test's Lima home holds %d machines, want exactly the one it created:\n%s", len(lines), out)
	}

	for _, state := range []string{"Running", "Stopped", "Broken"} {
		if strings.Contains(lines[0], "\"status\":\""+state+"\"") {
			return state
		}
	}
	t.Fatalf("Lima reports a state this test has no word for:\n%s", lines[0])
	return ""
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
