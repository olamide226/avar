package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/projconfig"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/state"
	"github.com/olamide226/avar/internal/types"
)

// Flow tests for avar's global config.toml: what a file the strict reader
// refuses does to each command, and the three ways the lenient readers it
// replaced misread a file without a word.

// writeUserConfig writes config.toml into the test's state directory and
// returns its path.
func writeUserConfig(t *testing.T, app *testApp, body string) string {
	t.Helper()
	path := app.store.ConfigPath()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}
	return path
}

// idleFor backdates machine's idle clock, as though its last session had
// detached that long ago.
func idleFor(t *testing.T, app *testApp, machine string, d time.Duration) {
	t.Helper()
	body, err := json.Marshal(map[string]time.Time{machine: time.Now().UTC().Add(-d)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app.store.Root(), "idle_since.json"), body, 0o600); err != nil {
		t.Fatalf("backdating the idle clock: %v", err)
	}
}

func assertMentions(t *testing.T, err error, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q:\n%v", want, err)
		}
	}
}

// Defect: a misspelt key was ignored without a word, so a user who wrote
// idle_timout = "0" believed auto-stop was off. It now fails the command, before
// anything reaches the backend, and names the key it was probably meant to be.
func TestConfig_MisspeltKeyFailsBeforeAnyMachineWork_REQ_17_7(t *testing.T) {
	pt := newProjectTest(t, "")
	path := writeUserConfig(t, pt.testApp, "# stop nothing\nidle_timout = \"0\"\n")

	err := dispatch(context.Background(), pt.App, guestInvocation("true"))

	if calls := pt.f.Calls(); len(calls) != 0 {
		t.Errorf("the backend was used before the configuration was refused:\n%s", pt.f.Transcript())
	}
	if err == nil {
		t.Fatal("avr true succeeded with a misspelt key in config.toml")
	}
	assertMentions(t, err, path+" line 2", `unknown key "idle_timout"`, "did you mean idle_timeout?")
}

// Defect: the idle_timeout reader matched only the literal prefix
// `idle_timeout = `, so idle_timeout="0" was not read and environments were
// stopped after the default two hours although the user had disabled it.
func TestIdleCheck_IdleTimeoutWithoutSpacesIsRead_REQ_5_5(t *testing.T) {
	const machine = "avr-ubuntu-24.04-arm64"
	f := fake.New()
	app := newTestApp(t, f)
	writeUserConfig(t, app, "idle_timeout=\"0\"\n")
	idleMachine(t, app, f, machine, ubuntu(), types.StateRunning)
	idleFor(t, app, machine, 3*time.Hour)

	if err := runIdleCheck(context.Background(), app.App); err != nil {
		t.Fatalf("avr internal idle-check: %v", err)
	}
	if n := f.Count(fake.OpStop); n != 0 {
		t.Errorf("idle_timeout=\"0\" disables auto-stop, but the idle check stopped the environment: %v", f.Calls())
	}
}

// The companion to the test above: the same environment under a timeout it has
// exceeded is stopped, so that test passes because the file was read and not
// because backdating the clock did nothing.
func TestIdleCheck_StopsAnEnvironmentIdleLongerThanTheTimeout_REQ_5_5(t *testing.T) {
	const machine = "avr-ubuntu-24.04-arm64"
	f := fake.New()
	app := newTestApp(t, f)
	writeUserConfig(t, app, "idle_timeout = \"2h\"\n")
	idleMachine(t, app, f, machine, ubuntu(), types.StateRunning)
	idleFor(t, app, machine, 3*time.Hour)

	if err := runIdleCheck(context.Background(), app.App); err != nil {
		t.Fatalf("avr internal idle-check: %v", err)
	}
	if n := f.Count(fake.OpStop); n != 1 {
		t.Errorf("an environment idle for 3h under a 2h timeout was stopped %d times, want once: %v", n, f.Calls())
	}
}

// Defect: the forward_env reader split on every comma, quoted or not, so
// ["A,B"] granted two variables that nobody listed. It is now refused as the
// single name it is, and neither variable crosses.
func TestShell_ForwardEnvCommaInsideQuotesIsNotTwoNames_REQ_12_4(t *testing.T) {
	t.Setenv("AVR_TEST_A", "a-value")
	t.Setenv("AVR_TEST_B", "b-value")
	pt := newProjectTest(t, "")
	path := writeUserConfig(t, pt.testApp, "forward_env = [\"AVR_TEST_A,AVR_TEST_B\"]\n")

	err := dispatch(context.Background(), pt.App, guestInvocation("env"))

	for _, c := range pt.f.CallsFor(fake.OpShell) {
		for _, name := range []string{"AVR_TEST_A", "AVR_TEST_B"} {
			if _, crossed := c.Shell.Env[name]; crossed {
				t.Errorf("%s crossed into the guest from a list that never named it", name)
			}
		}
	}
	if err == nil {
		t.Fatal("avr env succeeded with a forward_env entry that is not a variable name")
	}
	assertMentions(t, err, path+" line 1", "forward_env", `"AVR_TEST_A,AVR_TEST_B" is not a variable name`)
}

// brokenConfig is a config.toml the strict reader refuses: the misspelling the
// lenient readers ignored.
const brokenConfig = "idle_timout = \"0\"\n"

func subcommand(name string, args ...string) cli.Invocation {
	return cli.Invocation{Mode: cli.ModeSubcommand, Subcommand: name, SubcommandArgs: args}
}

// Every command that is not a way out refuses a config.toml it cannot read,
// before it resolves, provisions, starts or touches anything, and says what
// still works meanwhile.
//
// The editor commands are refused by the same check and are left out of this
// table on purpose: were the check ever to regress, they would run the real
// editor launcher on PATH.
func TestConfig_BrokenFileRefusesCommandsBeforeAnyMachineWork_REQ_17_7(t *testing.T) {
	for _, inv := range []cli.Invocation{
		{Mode: cli.ModeShell},
		guestInvocation("true"),
		subcommand("ports"),
		subcommand("open", "3000"),
		subcommand("reset", "--yes"),
		subcommand("snapshot", "before"),
		subcommand("restore", "before"),
		subcommand("isolate", "on"),
		subcommand("sync", "--to-host", "--yes"),
		subcommand("init"),
	} {
		name := strings.TrimSpace(strings.Join(append([]string{inv.Subcommand}, append(inv.SubcommandArgs, inv.Guest...)...), " "))
		if name == "" {
			name = "shell"
		}
		t.Run(name, func(t *testing.T) {
			pt := newProjectTest(t, "")
			seedMachine(t, pt.f, ubuntuMachine, ubuntu(), types.KindShared, pt.dir)
			path := writeUserConfig(t, pt.testApp, brokenConfig)

			err := dispatch(context.Background(), pt.App, inv)

			if calls := pt.f.Calls(); len(calls) != 0 {
				t.Errorf("the backend was used before the configuration was refused:\n%s", pt.f.Transcript())
			}
			if err == nil {
				t.Fatal("the command ran with a config.toml avar cannot read")
			}
			assertMentions(t, err, path+" line 1", "Nothing was started or changed", "`avr status`, `avr stop` and `avr destroy` still work")
			if _, statErr := os.Stat(filepath.Join(pt.dir, projconfig.FileName)); statErr == nil {
				t.Error("a refused `avr init` wrote a .avr.toml")
			}
			if projects, perr := pt.store.Projects(); perr != nil || len(projects) != 0 {
				t.Errorf("a refused command registered the project: %v %v", projects, perr)
			}
		})
	}
}

// The ways out survive a broken config.toml: the user can still see what is
// running, and stop or remove it. Each says the file is broken, then does its
// job.
func TestConfig_StatusStopAndDestroySurviveABrokenFile_REQ_17_7(t *testing.T) {
	for _, tc := range []struct {
		inv  cli.Invocation
		want fake.Op
	}{
		{subcommand("status"), fake.OpStatus},
		{subcommand("stop", "--all"), fake.OpStop},
		{subcommand("destroy", "--all", "--yes"), fake.OpDelete},
	} {
		t.Run(tc.inv.Subcommand, func(t *testing.T) {
			pt := newProjectTest(t, "")
			seedMachine(t, pt.f, ubuntuMachine, ubuntu(), types.KindShared, pt.dir)
			if err := pt.store.PutMachine(types.MachineRecord{Name: ubuntuMachine, Provider: fake.ProviderID, Selector: ubuntu(), Kind: types.KindShared}); err != nil {
				t.Fatal(err)
			}
			path := writeUserConfig(t, pt.testApp, brokenConfig)

			if err := dispatch(context.Background(), pt.App, tc.inv); err != nil {
				t.Fatalf("avr %s with a broken config.toml: %v", tc.inv.Subcommand, err)
			}
			pt.f.AssertCalled(t, tc.want)
			for _, want := range []string{path + " line 1", "did you mean idle_timeout?", "`avr " + tc.inv.Subcommand + "` carries on without it", "idle auto-stop is paused"} {
				if !strings.Contains(pt.err.String(), want) {
					t.Errorf("stderr does not mention %q:\n%s", want, pt.err.String())
				}
			}
		})
	}
}

// Nobody watches the scheduled idle check. Given a config.toml it cannot read
// it stops nothing, because no guess is safe — the broken line might have been
// idle_timeout = "0" — and it exits non-zero, so the scheduler's record of its
// last run shows a failure until the file is fixed.
func TestIdleCheck_BrokenFileStopsNothing_REQ_17_7(t *testing.T) {
	const machine = "avr-ubuntu-24.04-arm64"
	f := fake.New()
	app := newTestApp(t, f)
	path := writeUserConfig(t, app, "idle_timeout = \"0\n")
	idleMachine(t, app, f, machine, ubuntu(), types.StateRunning)
	idleFor(t, app, machine, 30*24*time.Hour)

	err := dispatch(context.Background(), app.App, subcommand("internal", "idle-check"))

	if n := f.Count(fake.OpStop); n != 0 {
		t.Errorf("the idle check stopped an environment with a config.toml it could not read: %v", f.Calls())
	}
	if err == nil {
		t.Fatal("the idle check reported success with a config.toml it could not read")
	}
	assertMentions(t, err, "idle check stopped nothing", path+" line 1", "not closed")
}

// `avr help` and `avr version` never read config.toml, so a broken one cannot
// stand between the user and the documentation for fixing it.
func TestExecute_HelpAndVersionIgnoreABrokenFile_REQ_17_7(t *testing.T) {
	home := t.TempDir()
	t.Setenv(state.HomeEnv, home)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(brokenConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, argv := range [][]string{{"help"}, {"--help"}, {"help", "stop"}, {"version"}, {"--version"}} {
		if got := Execute(context.Background(), "test", argv); got != 0 {
			t.Errorf("avr %s exited %d with a broken config.toml, want 0", strings.Join(argv, " "), got)
		}
	}
	// The file is still refused through the same entry point. `avr isolate`
	// is the command used because it never reaches a backend, so a regression
	// here cannot start a real one, and because without the refusal it exits
	// 0, so this cannot pass by accident.
	if got := Execute(context.Background(), "test", []string{"isolate"}); got != 1 {
		t.Errorf("avr isolate exited %d with a broken config.toml, want 1", got)
	}
}
