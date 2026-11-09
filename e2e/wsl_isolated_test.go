//go:build e2e && windows

// See harness_test.go for what these tests are and how they are run.
//
// This file gives the WSL half a second place to stand: a test that must not
// touch any environment but its own, because of what it does to the ones it
// finds — stops them, resets them, destroys them.
//
// # Why WSL needs a different answer from Lima
//
// The Lima half isolates with LIMA_HOME, which moves the backend itself
// (isolated_darwin_test.go). WSL has no equivalent: `wsl --list` is one list
// per Windows account, and every distribution avar registers lands in it. So
// a test here cannot have a backend of its own, only a state directory of its
// own and a machine nobody else can be using.
//
// Two hazards decide the shape.
//
// Name collision is today's. The shared machine's name depends only on the
// selector — `avr-ubuntu-24.04-<arch>`, whatever AVR_HOME says — so a test
// that idles, resets or destroys "the shared Ubuntu" from a fresh state
// directory is aiming at the same distribution the rest of the suite, or the
// developer's own avar, is using. A project switched to its own environment
// gets `avr-prj-<hash>-ubuntu-24.04-<arch>` instead, with the hash taken from
// the project's path, and that name belongs to this test's directory alone.
//
// Adoption is tomorrow's. WSL's Status currently lists only machines in avar's
// own records, so a fresh AVR_HOME sees nothing; the Lima backend's
// reconciliation adopts on the `avr-` prefix (docs/lessons.md, "`AVR_HOME` is
// not isolation"), and a change that brought WSL into line would make every
// fresh state directory take in every `avr-` distribution on the host. The
// tests therefore assert, before anything that stops or destroys, that their
// store holds exactly one environment — so adoption fails a test loudly
// instead of letting the idle check stop a stranger.
//
// The distribution a test made is found by asking WSL, not avar: it is the one
// `avr-prj-` name that was not registered when the test began. Reading it from
// avar would make a naming bug invisible in the tests meant to catch one.
//
// # Why the keep-alive
//
// WSL stops a distribution by itself about thirty seconds after the last
// process in it exits (docs/lessons.md, "A test that reports a security
// failure it did not observe"). An idle-check test that waited for "Stopped"
// with nothing holding the distribution open would pass on WSL's timer with
// avar doing nothing. holdOpen keeps a real process running in it that is not
// an avr session, so the only thing that can stop it inside the check is avar.

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olamide226/avar/internal/types"
)

// isolatedNamePrefix is how every project-owned environment's distribution
// name begins. Written out rather than borrowed from the resolver, for the
// reason above.
const isolatedNamePrefix = "avr-prj-"

// wslOwnProject makes a project directory and a state directory for it, and
// returns the environment to run avr with.
//
// The state directory sits in the home directory, as suiteEnv's does, because
// avar keeps its distributions' disks beneath it and the temporary directory
// is somewhere avar deliberately declines to register anything from.
//
// Cleanup destroys only an `avr-prj-` distribution that appeared during the
// test, along the path a user would take (`avr --isolate destroy --yes`: this
// project's own, never --all), and unregisters it directly only if that failed — it is
// this test's by construction, and leaving gigabytes registered helps nobody.
// A plain `avr-` name appearing is reported as an isolation failure and left
// for the suite's own cleanup: it might be the shared machine, which is not
// this test's to delete.
func wslOwnProject(t *testing.T, name string) (dir string, env []string) {
	t.Helper()
	requireWSL(t)

	// project() first, so its cleanup runs after this one's: destroying an
	// environment resolves the project it belongs to.
	dir = project(t, name)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("locate the home directory: %v", err)
	}
	stateDir := filepath.Join(home, "avr-e2e-state-"+name)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("create %s: %v", stateDir, err)
	}
	env = []string{"AVR_HOME=" + stateDir}

	before := registeredDistributions(t)
	t.Cleanup(func() {
		for _, made := range newAvarDistributions(t, before) {
			if !strings.HasPrefix(made, isolatedNamePrefix) {
				t.Errorf("%q was registered during this test, and it is not a project's own environment: "+
					"the test reached past the environment it was meant to confine itself to", made)
				continue
			}
			if stdout, stderr, code := avr(t, dir, env, "--isolate", "destroy", "--yes"); code != 0 {
				t.Errorf("destroying this test's environment: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			if slices.Contains(registeredDistributions(t), made) {
				t.Errorf("%q is still registered after `avr --isolate destroy --yes`; unregistering it directly", made)
				_ = exec.Command("wsl.exe", "--unregister", made).Run()
			}
		}
		if err := removeAllPatiently(stateDir); err != nil {
			t.Errorf("removing %s: %v", stateDir, err)
		}
	})
	return dir, env
}

// removeAllPatiently is os.RemoveAll with a few seconds' grace for Windows,
// where a file avr has just written can be briefly held open by something else
// — seen on machines.json straight after `avr destroy`, with no avr process
// left running and the file deletable moments later, which is the shape of a
// scanner reading it — and a delete in that window fails with a sharing
// violation.
func removeAllPatiently(dir string) error {
	var err error
	for range 20 {
		if err = os.RemoveAll(dir); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return err
}

// newAvarDistributions is every `avr-` distribution registered now that was
// not in before.
func newAvarDistributions(t *testing.T, before []string) []string {
	t.Helper()

	var made []string
	for _, name := range registeredDistributions(t) {
		if strings.HasPrefix(name, types.MachineNamePrefix) && !slices.Contains(before, name) {
			made = append(made, name)
		}
	}
	return made
}

// createIsolated creates the project's own environment and makes it the
// project's default, so every later plain `avr` in dir reaches it and nothing
// else. It returns the distribution's name, read from WSL.
//
// `--isolate` comes first because `avr isolate on` needs a recorded project,
// and the only commands that record one create an environment: a plain `avr
// true` would create the shared machine this whole arrangement exists to stay
// away from.
func createIsolated(t *testing.T, dir string, env []string) string {
	t.Helper()

	before := registeredDistributions(t)
	if _, stderr, code := avr(t, dir, env, "--isolate", "true"); code != 0 {
		t.Fatalf("creating this test's own environment: exit %d\nstderr:\n%s", code, stderr)
	}
	if stdout, stderr, code := avr(t, dir, env, "isolate", "on"); code != 0 {
		t.Fatalf("`avr isolate on` exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	made := newAvarDistributions(t, before)
	if len(made) != 1 || !strings.HasPrefix(made[0], isolatedNamePrefix) {
		t.Fatalf("creating a project's own environment registered %v, want exactly one %s distribution", made, isolatedNamePrefix)
	}
	return made[0]
}

// assertOnlyEnvironment fails the test unless this test's store holds exactly
// one environment. It is the guard in front of anything that stops or
// destroys: should avar ever adopt `avr-` distributions it has no record of,
// as the Lima backend does, a fresh state directory would hold more than the
// one the test made, and the check that follows would act on all of them.
func assertOnlyEnvironment(t *testing.T, dir string, env []string) {
	t.Helper()

	stdout, stderr, code := avr(t, dir, env, "status")
	if code != 0 {
		t.Fatalf("avr status exited %d\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "avar is managing 1 Linux environment.") {
		t.Fatalf("this test's own state directory does not hold exactly the one environment it made, "+
			"so what follows could act on somebody else's:\n%s", stdout)
	}
}

// wslState reads a distribution's state from WSL itself.
func wslState(t *testing.T, name string) string {
	t.Helper()

	switch {
	case runningDistributions(t)[name]:
		return "Running"
	case slices.Contains(registeredDistributions(t), name):
		return "Stopped"
	default:
		return "Unregistered"
	}
}

// keepAlive is a process holding a distribution open.
type keepAlive struct {
	cmd    *exec.Cmd
	exited chan struct{}
}

// holdOpen starts a process inside name that is not an avr session and waits
// until WSL reports the distribution running. Killed in cleanup either way.
func holdOpen(t *testing.T, name string) *keepAlive {
	t.Helper()

	k := &keepAlive{
		cmd:    exec.Command("wsl.exe", "--distribution", name, "--exec", "sleep", keepAliveFor),
		exited: make(chan struct{}),
	}
	if err := k.cmd.Start(); err != nil {
		t.Fatalf("holding %q open: %v", name, err)
	}
	go func() {
		_ = k.cmd.Wait()
		close(k.exited)
	}()
	t.Cleanup(func() {
		_ = k.cmd.Process.Kill()
		<-k.exited
	})

	if !waitForRunning(t, name) {
		t.Fatalf("%q did not come up with a process held open inside it", name)
	}
	return k
}

// exitedWithin reports whether the held process ended within d — which, for a
// sleep of ten minutes, means the distribution under it was stopped.
func (k *keepAlive) exitedWithin(d time.Duration) bool {
	select {
	case <-k.exited:
		return true
	case <-time.After(d):
		return false
	}
}

// holdForeignOpen returns a distribution the user owns, running for the
// duration of the test with a process of the test's own inside it.
//
// It is runningForeignDistribution without the terminate in its cleanup. That
// one stops the distribution afterwards when it was not already running, and
// "was not running when the test began" is no promise that nobody opened it
// since. These tests run the idle check, whose failure mode is stopping
// somebody's distribution; they do not get to do it themselves. Killing the
// keep-alive is enough: WSL lets the distribution go on its own timer, which
// is the state it would have reached had the test never touched it.
func holdForeignOpen(t *testing.T) string {
	t.Helper()

	var name string
	for _, candidate := range registeredDistributions(t) {
		if !strings.HasPrefix(candidate, types.MachineNamePrefix) {
			name = candidate
			break
		}
	}
	if name == "" {
		if os.Getenv(requireForeignEnv) != "" {
			t.Fatalf("%s is set, so a distribution of the user's own was expected and none is registered", requireForeignEnv)
		}
		t.Skip("no distribution of the user's own is registered, so there is nothing the idle check could wrongly stop; " +
			"set " + requireForeignEnv + " to make this a failure instead")
	}
	holdOpen(t, name)
	return name
}
