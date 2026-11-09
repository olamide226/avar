//go:build e2e && windows

// See harness_test.go for what these tests are and how they are run.
//
// The WSL half of idle_darwin_test.go. A scheduled `avr internal idle-check`
// stops environments that have had no activity for the configured time
// (REQ-5.5); one with a live session is never stopped (PROP-11), and neither
// is one with an editor window connected to it (REQ-5.10, REQ-5.11). WSL is an
// EditorProber, so the editor cases port.
//
// Every case runs against ONE environment of the project's own, as subtests,
// because a WSL create is a full root-filesystem download. Which environment,
// and why it cannot be anybody else's, is wsl_isolated_test.go; the guard in
// front of every check is assertOnlyEnvironment.
//
// Every check expected to stop the environment runs with holdOpen's process
// inside it and asserts that process ended. Without it, "Stopped" would be
// what WSL does on its own thirty seconds after the last process exits, and
// the test would pass with avar doing nothing.
//
// Every check also runs with a distribution of the user's own held open, and
// asserts afterwards that it is still running: the idle check is the one avar
// command that stops machines nobody asked it to.

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stopGrace is how long a held process gets to notice its distribution was
// terminated. The check returns once WSL says the distribution is stopped, and
// wsl.exe exits moments after.
const stopGrace = 30 * time.Second

func TestWSL_IdleCheckStopsOnlyWhatNobodyIsUsing_REQ_5_5(t *testing.T) {
	dir, env := wslOwnProject(t, "widle")
	foreign := holdForeignOpen(t)
	writeIdleTimeout(t, env, idleTimeout)
	writeScript(t, dir, "plant.sh", plantScript)
	writeScript(t, dir, "close.sh", closeScript)
	name := createIsolated(t, dir, env)

	// idleCheck runs the check after the environment has been idle for longer
	// than the timeout, having first made sure there is nothing else in this
	// test's store for it to act on.
	idleCheck := func(t *testing.T) (stdout, stderr string) {
		t.Helper()
		assertOnlyEnvironment(t, dir, env)
		time.Sleep(idleSettle)
		stdout, stderr, code := avr(t, dir, env, "internal", "idle-check")
		if code != 0 {
			t.Fatalf("`avr internal idle-check` exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		if !runningDistributions(t)[foreign] {
			t.Fatalf("the idle check stopped %q, a distribution avar did not create (PROP-6)", foreign)
		}
		return stdout, stderr
	}

	// assertStoppedByAvar is the only way a subtest concludes that the check
	// stopped the environment: WSL says so, and the process holding it open
	// ended, which nothing but a stop does to a ten-minute sleep.
	assertStoppedByAvar := func(t *testing.T, held *keepAlive, why string) {
		t.Helper()
		if state := wslState(t, name); state != "Stopped" {
			t.Errorf("%s is %s: %s", name, state, why)
		}
		if !held.exitedWithin(stopGrace) {
			t.Errorf("the process holding %s open is still running, so the distribution was not stopped: %s", name, why)
		}
	}

	// REQ-5.5: an environment nobody is using is stopped once it has been
	// idle for the configured time.
	t.Run("IdleEnvironmentIsStopped_REQ_5_5", func(t *testing.T) {
		if _, stderr, code := avr(t, dir, env, "true"); code != 0 {
			t.Fatalf("using the environment: exit %d\nstderr:\n%s", code, stderr)
		}
		held := holdOpen(t, name)
		if state := wslState(t, name); state != "Running" {
			t.Fatalf("%s is %s before the idle check, so this would prove nothing", name, state)
		}

		idleCheck(t)
		assertStoppedByAvar(t, held, "an environment idle for longer than idle_timeout must be stopped (REQ-5.5)")
	})

	// PROP-11: an environment with a live session is never stopped. The
	// session is a real `avr`, announced by a file it can only write once avar
	// has recorded it and handed the command over. Releasing it and checking
	// again is the control.
	t.Run("LiveSessionIsNeverStopped_PROP_11", func(t *testing.T) {
		attached := filepath.Join(dir, "attached")
		_ = os.Remove(attached)

		// The session's output goes to a file, not a pipe: wsl.exe beneath it
		// inherits a pipe and can hold it open after avr is gone, and what
		// the session said is the only evidence when it fails to start.
		output, err := os.Create(filepath.Join(t.TempDir(), "session.log"))
		if err != nil {
			t.Fatal(err)
		}
		session := exec.Command(avrBinary, "sh", "-c", "touch attached; sleep 30")
		session.Dir = dir
		session.Env = append(os.Environ(), env...)
		session.Stdout, session.Stderr = output, output
		if err := session.Start(); err != nil {
			t.Fatalf("starting the session that holds the environment: %v", err)
		}
		var sessionErr error
		ended := make(chan struct{})
		go func() {
			sessionErr = session.Wait()
			close(ended)
		}()
		t.Cleanup(func() {
			_ = session.Process.Kill()
			select {
			case <-ended:
			case <-time.After(stopGrace):
				t.Errorf("the session did not exit after being killed")
			}
			_ = output.Close()
			if t.Failed() {
				said, _ := os.ReadFile(output.Name())
				t.Logf("what the session printed:\n%s", said)
			}
		})
		waitForFile(t, attached)

		idleCheck(t)
		if state := wslState(t, name); state != "Running" {
			t.Fatalf("%s is %s while a session is attached to it (PROP-11)", name, state)
		}
		select {
		case <-ended:
			t.Fatalf("the session ended during the check (%v): the check stopped an environment with a session attached (PROP-11)", sessionErr)
		default:
		}

		// The control: the same environment, the same timeout, no session.
		<-ended
		if sessionErr != nil {
			t.Fatalf("the session that held the environment did not finish cleanly: %v", sessionErr)
		}
		held := holdOpen(t, name)
		idleCheck(t)
		assertStoppedByAvar(t, held, "once its session has gone the environment must stop, or the check above proved nothing about the session")
	})

	// REQ-5.10, REQ-5.11: an editor window connected to the environment keeps
	// it running, and it stops once the window is gone. The second half is
	// what makes the first mean anything.
	t.Run("EditorWindowKeepsTheEnvironment_REQ_5_10", func(t *testing.T) {
		stdout, stderr, code := avr(t, dir, env, "sh", "plant.sh")
		if code == noProcps {
			t.Skip("this guest image has no pkill, so the test cannot start and stop a process that stands in for an editor window")
		}
		if code != 0 || !strings.Contains(stdout, "planted") {
			t.Fatalf("planting a process shaped like a connected editor window: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		held := holdOpen(t, name)

		stdout, stderr = idleCheck(t)
		if report := stdout + stderr; !strings.Contains(report, "kept") || !strings.Contains(report, "VS Code") {
			t.Errorf("the idle check did not say it kept the environment for an editor:\n%s", report)
		}
		if state := wslState(t, name); state != "Running" {
			t.Fatalf("%s is %s: the idle check stopped an environment with an editor window connected to it (REQ-5.10)", name, state)
		}

		stdout, stderr, code = avr(t, dir, env, "sh", "close.sh")
		if code != 0 || !strings.Contains(stdout, "closed") {
			t.Fatalf("closing the window: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}

		idleCheck(t)
		assertStoppedByAvar(t, held, "an editor that is gone must stop keeping the environment (REQ-5.11)")
	})
}
