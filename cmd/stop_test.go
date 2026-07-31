package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/types"
)

// stopInvocation is `avr stop [--all]` as the grammar hands it over: the
// subcommand's own arguments arrive unparsed (design §3.1).
func stopInvocation(args ...string) cli.Invocation {
	return cli.Invocation{Mode: cli.ModeSubcommand, Subcommand: "stop", SubcommandArgs: args}
}

// resolvedTarget is the machine `avr stop` with no flags targets from this
// directory. It is asked of the same resolver the command uses, so the test
// does not restate the naming rules the resolver owns (PROP-2).
func resolvedTarget(t *testing.T, app *testApp) (name string, label string) {
	t.Helper()
	target, err := app.Resolve(stopInvocation())
	if err != nil {
		t.Fatalf("resolving the target environment: %v", err)
	}
	return target.MachineName, target.Selector.Label()
}

func TestStop_StopsOnlyTheResolvedTarget_REQ_5_2(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	target, label := resolvedTarget(t, app)

	seedMachine(t, f, target, ubuntu(), types.KindShared)
	// A second environment the user also has. `avr stop` is about the one this
	// directory and these flags resolve to, and must leave the rest running.
	seedMachine(t, f, "avr-fedora-42-arm64", types.EnvironmentSelector{
		Distro: types.DistroFedora, Version: "42", Arch: types.HostArch(),
	}, types.KindShared)

	if err := runStop(context.Background(), app.App, stopInvocation()); err != nil {
		t.Fatalf("avr stop: %v", err)
	}

	// One listing of the machines avar owns, then one stop: the target is found
	// in that listing rather than assumed to exist, which is what lets avar tell
	// "already stopped" from "never created" (REQ-5.2).
	f.AssertOps(t, fake.OpStatus, fake.OpStop)
	if call := f.AssertCalled(t, fake.OpStop); call.Machine != target {
		t.Errorf("`avr stop` stopped %s, want the resolved target %s", call.Machine, target)
	}
	f.AssertMachineState(t, target, types.StateStopped)
	f.AssertMachineState(t, "avr-fedora-42-arm64", types.StateRunning)

	if !strings.Contains(app.stdout(), label) {
		t.Errorf("`avr stop` did not say which environment it stopped:\n%s", app.stdout())
	}
	if strings.Contains(app.stdout(), target) {
		t.Errorf("`avr stop` showed the user a machine name (REQ-1.5):\n%s", app.stdout())
	}
}

// Stop converges on a state rather than performing an action, so an environment
// that is already stopped is a success with an explanation, not an error.
func TestStop_AlreadyStoppedIsNotAnError_REQ_5_2(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	target, _ := resolvedTarget(t, app)

	seedMachine(t, f, target, ubuntu(), types.KindShared)
	f.SetMachineState(target, types.StateStopped)

	if err := runStop(context.Background(), app.App, stopInvocation()); err != nil {
		t.Fatalf("stopping an already-stopped environment failed: %v", err)
	}
	if !strings.Contains(app.stdout(), "already stopped") {
		t.Errorf("`avr stop` did not explain that there was nothing to do:\n%s", app.stdout())
	}
	// Nothing to stop is decided from the listing avar already has, so no
	// second round trip is spent proving it (REQ-17.1).
	f.AssertOps(t, fake.OpStatus)
	f.AssertMachineState(t, target, types.StateStopped)
}

// Nothing to stop because nothing was ever created is the same kind of answer:
// avar says so and exits zero.
func TestStop_ReportsAnEnvironmentThatWasNeverCreated_REQ_5_2(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)

	if err := runStop(context.Background(), app.App, stopInvocation()); err != nil {
		t.Fatalf("stopping an environment that does not exist failed: %v", err)
	}
	if !strings.Contains(app.stdout(), "nothing to stop") {
		t.Errorf("`avr stop` did not explain that there is no such environment:\n%s", app.stdout())
	}
	// An environment that was never created must not be reported as a machine
	// avar refuses to touch: nothing is attempted on it at all.
	f.AssertOps(t, fake.OpStatus)
}

// --all stops every environment avar manages, and nothing else: the machines
// come from the backend's owned-machines listing, which is filtered again in the
// command layer (REQ-5.4, PROP-6).
func TestStop_AllStopsEveryOwnedMachineAndNothingElse_REQ_5_2(t *testing.T) {
	f := fake.New()
	seedMachine(t, f, "avr-ubuntu-24.04-arm64", ubuntu(), types.KindShared)
	seedMachine(t, f, "avr-fedora-42-arm64", types.EnvironmentSelector{
		Distro: types.DistroFedora, Version: "42", Arch: types.HostArch(),
	}, types.KindShared)
	seedMachine(t, f, "avr-debian-12-arm64", types.EnvironmentSelector{
		Distro: types.DistroDebian, Version: "12", Arch: types.HostArch(),
	}, types.KindShared)
	f.SetMachineState("avr-debian-12-arm64", types.StateStopped)
	// The user's own virtual machines, which avar must never reach.
	f.AddForeignMachine("colima")
	f.AddForeignMachine("default")

	app := newTestApp(t, f)
	if err := runStop(context.Background(), app.App, stopInvocation("--all")); err != nil {
		t.Fatalf("avr stop --all: %v", err)
	}

	// Exactly one listing, then one Stop per running owned machine — the
	// already-stopped one is not worth a subprocess.
	f.AssertOps(t, fake.OpStatus, fake.OpStop, fake.OpStop)
	stopped := map[string]bool{}
	for _, call := range f.CallsFor(fake.OpStop) {
		stopped[call.Machine] = true
	}
	for _, want := range []string{"avr-fedora-42-arm64", "avr-ubuntu-24.04-arm64"} {
		if !stopped[want] {
			t.Errorf("`avr stop --all` left %s running", want)
		}
	}
	for _, call := range f.Calls() {
		if call.Machine == "colima" || call.Machine == "default" {
			t.Fatalf("`avr stop --all` reached a machine avar does not own: %s", call)
		}
	}
	if f.MachineState("colima") != types.StateRunning || f.MachineState("default") != types.StateRunning {
		t.Error("a machine the user created themselves was stopped")
	}
	if !strings.Contains(app.stdout(), "Stopped 2 Linux environments") {
		t.Errorf("`avr stop --all` did not report what it stopped:\n%s", app.stdout())
	}
}

func TestStop_AllOnAnEmptyInstallationSaysSo_REQ_5_2(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)

	if err := runStop(context.Background(), app.App, stopInvocation("--all")); err != nil {
		t.Fatalf("avr stop --all: %v", err)
	}
	f.AssertOps(t, fake.OpStatus)
	if !strings.Contains(app.stdout(), "nothing to stop") {
		t.Errorf("`avr stop --all` did not explain that avar manages nothing:\n%s", app.stdout())
	}
}

// One environment refusing to stop must not leave the others running, and the
// refusal must still be reported rather than swallowed.
func TestStop_AllKeepsGoingWhenOneMachineRefuses_REQ_5_2(t *testing.T) {
	f := fake.New()
	seedMachine(t, f, "avr-fedora-42-arm64", types.EnvironmentSelector{
		Distro: types.DistroFedora, Version: "42", Arch: types.HostArch(),
	}, types.KindShared)
	seedMachine(t, f, "avr-ubuntu-24.04-arm64", ubuntu(), types.KindShared)
	// Sorted by name, Fedora is stopped first: it is the one that refuses.
	f.FailNextOn(fake.OpStop, fmt.Errorf("%w: avr-fedora-42-arm64", provider.ErrNotOwned))

	app := newTestApp(t, f)
	err := runStop(context.Background(), app.App, stopInvocation("--all"))
	if err == nil {
		t.Fatal("`avr stop --all` reported success although one environment could not be stopped")
	}
	if !errors.Is(err, provider.ErrNotOwned) {
		t.Errorf("the refusal was reworded rather than wrapped: %v", err)
	}
	if !strings.Contains(err.Error(), "avr") {
		t.Errorf("the error does not say what to do next: %v", err)
	}
	f.AssertMachineState(t, "avr-ubuntu-24.04-arm64", types.StateStopped)
}

// A machine that is coming up has no record yet and is somebody else's work in
// progress. --all leaves it alone rather than interrupting a create.
func TestStop_AllLeavesAMachineThatIsNotUpAlone_PROP_7(t *testing.T) {
	const installing = types.MachineState("installing")

	f := fake.New()
	seedMachine(t, f, "avr-ubuntu-24.04-arm64", ubuntu(), types.KindShared)
	f.SetMachineState("avr-ubuntu-24.04-arm64", installing)

	app := newTestApp(t, f)
	if err := runStop(context.Background(), app.App, stopInvocation("--all")); err != nil {
		t.Fatalf("avr stop --all: %v", err)
	}

	f.AssertOps(t, fake.OpStatus)
	f.AssertMachineState(t, "avr-ubuntu-24.04-arm64", installing)
	if !strings.Contains(app.err.String(), "left") {
		t.Errorf("`avr stop --all` said nothing about the environment it skipped:\n%s", app.err.String())
	}
}

func TestStop_RejectsArgumentsItDoesNotUnderstand(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)

	err := runStop(context.Background(), app.App, stopInvocation("--everything"))
	assertExitCode(t, err, exitUsage)
	if !strings.Contains(err.Error(), "--all") {
		t.Errorf("the usage error does not name the flag `stop` does take: %v", err)
	}
	f.AssertOps(t)
}

func TestParseStopArgs(t *testing.T) {
	cases := map[string]struct {
		args    []string
		wantAll bool
		wantErr bool
	}{
		"no arguments":   {args: nil},
		"--all":          {args: []string{"--all"}, wantAll: true},
		"repeated --all": {args: []string{"--all", "--all"}, wantAll: true},
		"unknown flag":   {args: []string{"--force"}, wantErr: true},
		"a machine name": {args: []string{"avr-ubuntu-24.04-arm64"}, wantErr: true},
		"-a":             {args: []string{"-a"}, wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			all, err := parseStopArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseStopArgs(%v) succeeded, want a usage error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseStopArgs(%v): %v", tc.args, err)
			}
			if all != tc.wantAll {
				t.Errorf("--all = %t, want %t", all, tc.wantAll)
			}
		})
	}
}
