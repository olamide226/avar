//go:build e2e && darwin

// See harness_test.go for what these tests are and how they are run.
//
// config.toml is read by exactly one strict reader, and what it cannot read
// exactly is refused with the line it is on (REQ-17.7). The unit tests cover
// the reader; this covers the consequence a user meets — which commands stop
// working, which keep working, and that nothing is started on the way to the
// refusal.

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brokenSetting is one key away from idle_timeout. The misspelling is the whole
// point: the lenient reader this replaced ignored it, so a user who had turned
// idle auto-stop down to half an hour kept the two-hour default and had nothing
// to tell them (docs/lessons.md, "A lenient reader turns every mistake into a
// setting that silently does not apply").
const brokenSetting = "idle_timeot = \"30m\"\n"

// REQ-17.7: an ordinary command refuses before any machine work when
// config.toml cannot be read, and the refusal names the file, the line and the
// key that was meant.
func TestUserConfig_AnOrdinaryCommandRefusesAndStartsNothing_REQ_17_7(t *testing.T) {
	dir, env := ownProject(t, "cfg")
	configPath := writeBrokenConfig(t, env)

	stdout, stderr, code := avr(t, dir, env, "true")
	if code == 0 {
		t.Fatalf("`avr true` ran with an unreadable config.toml\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	report := stdout + stderr
	for _, want := range []string{
		configPath,            // which file
		"line 1",              // which line
		"idle_timeot",         // what is wrong
		"did you mean",        // what was meant
		"idle_timeout",        //
		"Nothing was started", // and that it stopped before doing anything
		"avr status",          // what still works
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, report)
		}
	}

	// "Before any machine work" is the part a message cannot prove. avar's own
	// listing can: the isolated backend was empty when this test started, so
	// an environment here would have to have been created by the command that
	// was supposed to refuse.
	assertNoEnvironments(t, dir, env)
}

// REQ-17.7: the commands that let a user see and release what avar is running
// keep working, because a broken file must not lock anybody out of fixing
// things. Each says the file is broken before carrying on.
func TestUserConfig_StatusStopAndDestroyStillRun_REQ_17_7(t *testing.T) {
	dir, env := ownProject(t, "cfg-run")
	configPath := writeBrokenConfig(t, env)

	for _, command := range []string{"status", "stop", "destroy"} {
		stdout, stderr, code := avr(t, dir, env, command)
		if code != 0 {
			t.Errorf("`avr %s` exited %d with an unreadable config.toml; it is one of the commands that must keep working\nstdout:\n%s\nstderr:\n%s",
				command, code, stdout, stderr)
			continue
		}
		report := stdout + stderr
		if !strings.Contains(report, configPath) || !strings.Contains(report, "carries on without it") {
			t.Errorf("`avr %s` did not say the configuration is broken before carrying on:\n%s", command, report)
		}
	}
}

// REQ-17.7: help and version never reach the command dispatch that reads the
// file, so they answer whatever state config.toml is in. A user who cannot read
// the help is a user who cannot look up the fix.
func TestUserConfig_HelpAndVersionStillWork_REQ_17_7(t *testing.T) {
	dir, env := ownProject(t, "cfg-help")
	writeBrokenConfig(t, env)

	stdout, stderr, code := avr(t, dir, env, "help")
	if code != 0 {
		t.Errorf("`avr help` exited %d with an unreadable config.toml\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "avr") || !strings.Contains(stdout, "Linux") {
		t.Errorf("`avr help` printed no help:\n%s", stdout)
	}

	stdout, stderr, code = avr(t, dir, env, "version")
	if code != 0 {
		t.Errorf("`avr version` exited %d with an unreadable config.toml\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "avr") {
		t.Errorf("`avr version` printed no version:\n%s", stdout)
	}
}

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
