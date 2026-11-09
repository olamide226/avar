//go:build e2e && windows

// See harness_test.go for what these tests are and how they are run.
//
// The WSL half of userconfig_darwin_test.go: config.toml is read by one strict
// reader, and what it cannot read is refused with the line it is on
// (REQ-17.7). The behaviour lives in cmd/ and knows nothing of a backend; this
// is the check that it holds on the host where the commands it lets through
// reach WSL.
//
// Nothing here should create a machine. The commands that might, were the
// guard to regress, are run with `--isolate`, so a regression creates this
// project's own environment — which wslOwnProject destroys — and never the
// shared one the rest of the suite is using (wsl_isolated_test.go).

package e2e

import (
	"strings"
	"testing"
)

// REQ-17.7: an ordinary command refuses before any machine work when
// config.toml cannot be read, and the refusal names the file, the line and the
// key that was meant.
func TestWSL_UserConfigOrdinaryCommandRefusesAndStartsNothing_REQ_17_7(t *testing.T) {
	dir, env := wslOwnProject(t, "wcfg")
	configPath := writeBrokenConfig(t, env)
	before := registeredDistributions(t)

	stdout, stderr, code := avr(t, dir, env, "--isolate", "true")
	if code == 0 {
		t.Fatalf("`avr --isolate true` ran with an unreadable config.toml\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
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

	// "Before any machine work", asked of both avar and WSL: avar's records
	// are empty, and WSL registered nothing.
	assertNoEnvironments(t, dir, env)
	if made := newAvarDistributions(t, before); len(made) != 0 {
		t.Errorf("the refusing command registered %v with WSL", made)
	}
}

// REQ-17.7: the commands that let a user see and release what avar is running
// keep working, and each says the file is broken before carrying on.
//
// stop and destroy are given `--isolate` so the environment they resolve is
// this project's own, which does not exist: in a fresh state directory the
// shared name is the suite's machine, and a stop aimed at it is not this
// test's to make.
func TestWSL_UserConfigStatusStopAndDestroyStillRun_REQ_17_7(t *testing.T) {
	dir, env := wslOwnProject(t, "wcfg-run")
	configPath := writeBrokenConfig(t, env)

	for _, args := range [][]string{{"status"}, {"--isolate", "stop"}, {"--isolate", "destroy"}} {
		stdout, stderr, code := avr(t, dir, env, args...)
		command := strings.Join(args, " ")
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
// file, so they answer whatever state config.toml is in.
func TestWSL_UserConfigHelpAndVersionStillWork_REQ_17_7(t *testing.T) {
	dir, env := wslOwnProject(t, "wcfg-help")
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
