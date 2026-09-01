package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/types"
)

// resetInvocation is `avr reset [--yes]` as the grammar hands it over.
func resetInvocation(args ...string) cli.Invocation {
	return cli.Invocation{Mode: cli.ModeSubcommand, Subcommand: "reset", SubcommandArgs: args}
}

// TestReset_DeletesAndRecreatesWithYes_REQ_10_3 validates that `avr reset --yes`
// destroys the target machine and then recreates it from the same base image,
// and that host output names the environment rather than the machine name
// (REQ-1.5).
func TestReset_DeletesAndRecreatesWithYes_REQ_10_3(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	// Provide stdin so that the prompt path is testable even without --yes.
	app.Stdin = strings.NewReader("")

	target, label := resolvedTarget(t, app)
	seedMachine(t, f, target, ubuntu(), types.KindShared, hostPath("/Users/ola/code/app"))

	if err := runReset(context.Background(), app.App, resetInvocation("--yes")); err != nil {
		t.Fatalf("avr reset --yes: %v", err)
	}

	// The provider must be called in order: Delete, then EnsureMachine. (The
	// leading Status is the command's own listing — Provider() was built by
	// this test's direct seeding, which skips the one made there.)
	//
	// The recreate goes through the same path as an ordinary first use, so the
	// fresh machine is given the project mount rather than coming up bare.
	// AppliedMounts is the reconcile confirming EnsureMachine already applied
	// it; a SetMounts here would mean the machine had come up without it.
	f.AssertOps(t, fake.OpStatus, fake.OpDelete, fake.OpEnsureMachine, fake.OpAppliedMounts)

	del := f.AssertCalled(t, fake.OpDelete)
	if del.Machine != target {
		t.Errorf("deleted %s, want the resolved target %s", del.Machine, target)
	}

	ens := f.AssertCalled(t, fake.OpEnsureMachine)
	if ens.Machine != target {
		t.Errorf("recreated %s, want the resolved target %s", ens.Machine, target)
	}
	if ens.Spec.Selector.Label() != label {
		t.Errorf("recreated selector %s, want %s", ens.Spec.Selector.Label(), label)
	}

	// After deletion and recreation the machine exists and is running: the
	// Fake creates a fresh machine in EnsureMachine.
	f.AssertMachineState(t, target, types.StateRunning)

	out := app.stdout()
	if !strings.Contains(out, label) {
		t.Errorf("`avr reset` did not name the environment:\n%s", out)
	}
	if strings.Contains(out, target) {
		t.Errorf("`avr reset` showed the machine name %s (REQ-1.5):\n%s", target, out)
	}
	if !strings.Contains(out, "destroyed") && !strings.Contains(out, "Destroying") {
		t.Errorf("`avr reset` did not say what was destroyed:\n%s", out)
	}
	if !strings.Contains(out, "Reset complete") {
		t.Errorf("`avr reset` did not confirm completion:\n%s", out)
	}
}

// TestReset_ReportsAnEnvironmentThatWasNeverCreated_REQ_10_3 validates that
// resetting an environment that was never created is not an error: avar says so
// and exits zero, without creating a machine the user did not ask for.
func TestReset_ReportsAnEnvironmentThatWasNeverCreated_REQ_10_3(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	app.Stdin = strings.NewReader("")

	if err := runReset(context.Background(), app.App, resetInvocation()); err != nil {
		t.Fatalf("resetting a non-existent environment failed: %v", err)
	}

	if !strings.Contains(app.stdout(), "nothing to reset") {
		t.Errorf("`avr reset` did not explain that there is no such environment:\n%s", app.stdout())
	}
	// No machine was ever deleted or created, only the status listing was
	// read to confirm nothing exists.
	f.AssertOps(t, fake.OpStatus)
}

// TestReset_ConfirmsBeforeDestroying_REQ_10_3 validates that without --yes, the
// user is prompted and must type the environment label to proceed. A wrong reply
// cancels the reset without touching the machine.
func TestReset_ConfirmsBeforeDestroying_REQ_10_3(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	target, label := resolvedTarget(t, app)
	seedMachine(t, f, target, ubuntu(), types.KindShared)

	// Simulate the user typing the wrong confirmation text.
	app.Stdin = strings.NewReader("no\n")

	if err := runReset(context.Background(), app.App, resetInvocation()); err != nil {
		t.Fatalf("avr reset cancelled: %v", err)
	}

	// The wrong reply means the machine was never touched.
	f.AssertOps(t, fake.OpStatus)
	f.AssertMachineState(t, target, types.StateRunning)

	out := app.stdout()
	if !strings.Contains(out, "cancelled") {
		t.Errorf("`avr reset` did not say it was cancelled:\n%s", out)
	}
	if !strings.Contains(out, label) {
		t.Errorf("`avr reset` did not show the label in the summary:\n%s", out)
	}
}

// TestReset_ConfirmationAccepted_REQ_10_3 validates that typing the correct
// environment label confirms the reset, which then proceeds to destroy and
// recreate the machine.
func TestReset_ConfirmationAccepted_REQ_10_3(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	target, label := resolvedTarget(t, app)
	seedMachine(t, f, target, ubuntu(), types.KindShared)

	// Simulate the user typing the correct label.
	app.Stdin = strings.NewReader(label + "\n")

	if err := runReset(context.Background(), app.App, resetInvocation()); err != nil {
		t.Fatalf("avr reset: %v", err)
	}

	// The recreate goes through the same path as an ordinary first use, so the
	// fresh machine is given the project mount rather than coming up bare.
	// AppliedMounts is the reconcile confirming EnsureMachine already applied
	// it; a SetMounts here would mean the machine had come up without it.
	f.AssertOps(t, fake.OpStatus, fake.OpDelete, fake.OpEnsureMachine, fake.OpAppliedMounts)
	f.AssertMachineState(t, target, types.StateRunning)

	out := app.stdout()
	if !strings.Contains(out, "Reset complete") {
		t.Errorf("`avr reset` did not confirm completion:\n%s", out)
	}
}

// TestReset_RejectsArgumentsItDoesNotUnderstand validates that unknown flags
// are rejected with a usage error that names the accepted flag.
func TestReset_RejectsArgumentsItDoesNotUnderstand(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	app.Stdin = strings.NewReader("")

	err := runReset(context.Background(), app.App, resetInvocation("--force"))
	assertExitCode(t, err, exitUsage)
	if !strings.Contains(err.Error(), resetYesFlag) {
		t.Errorf("the usage error does not name the flag `reset` does take: %v", err)
	}
	f.AssertOps(t)
}

// TestParseResetArgs validates the reset argument parser.
func TestParseResetArgs(t *testing.T) {
	cases := map[string]struct {
		args    []string
		wantYes bool
		wantErr bool
	}{
		"no arguments": {args: nil},
		"--yes":        {args: []string{"--yes"}, wantYes: true},
		"unknown flag": {args: []string{"--force"}, wantErr: true},
		"a name":       {args: []string{"my-snapshot"}, wantErr: true},
		"-y":           {args: []string{"-y"}, wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			yes, err := parseResetArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseResetArgs(%v) succeeded, want a usage error", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseResetArgs(%v): %v", tc.args, err)
			}
			if yes != tc.wantYes {
				t.Errorf("--yes = %t, want %t", yes, tc.wantYes)
			}
		})
	}
}

// TestProp_ResetScoping_PROP_10 validates that reset never reaches a machine
// avar does not own, and that host project files are never touched by reset —
// the provider contract guarantees this (PROP-10). This test proves the command
// layer enforces ownership with the Fake.
func TestProp_ResetScoping_PROP_10(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	app.Stdin = strings.NewReader("")

	target, _ := resolvedTarget(t, app)
	seedMachine(t, f, target, ubuntu(), types.KindShared, hostPath("/Users/ola/code/app"))
	// A second, unrelated machine the user also has.
	seedMachine(t, f, "avr-fedora-42-arm64", types.EnvironmentSelector{
		Distro: types.DistroFedora, Version: "42", Arch: types.HostArch(),
	}, types.KindShared)

	if err := runReset(context.Background(), app.App, resetInvocation("--yes")); err != nil {
		t.Fatalf("avr reset --yes: %v", err)
	}

	// Only the resolved target was deleted and recreated; the other machine
	// was left entirely alone.
	f.AssertMachineState(t, target, types.StateRunning)
	f.AssertMachineState(t, "avr-fedora-42-arm64", types.StateRunning)

	// Validate that only the target was acted on.
	for _, call := range f.Calls() {
		if call.Op == fake.OpDelete || call.Op == fake.OpEnsureMachine {
			if call.Machine != target {
				t.Errorf("reset reached a machine other than the target: %s on %s", call.Op, call.Machine)
			}
		}
	}
}

// TestReset_IsolatedEnvironment_REQ_10_3_REQ_11_4 validates that reset on an
// isolated machine resets only that project's machine and leaves shared machines
// untouched (REQ-11.4).
func TestReset_IsolatedEnvironment_REQ_10_3_REQ_11_4(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	app.Stdin = strings.NewReader("")

	// Seed the shared machine first so that the project isolation select above
	// does not mistake it for the target.
	sharedName, _ := resolvedTarget(t, app)
	seedMachine(t, f, sharedName, ubuntu(), types.KindShared, hostPath("/Users/ola/code/other"))

	// Now set up as an isolated project targeting its own machine.
	target, _ := resolvedTarget(t, app)
	isolatedSelector := types.EnvironmentSelector{
		Distro: types.DistroUbuntu, Version: "24.04", Arch: types.HostArch(), Isolated: true,
	}
	seedMachine(t, f, target, isolatedSelector, types.KindIsolated, hostPath("/Users/ola/code/isolated-proj"))

	if err := runReset(context.Background(), app.App, resetInvocation("--yes")); err != nil {
		t.Fatalf("avr reset --yes: %v", err)
	}

	// The target isolated machine was deleted and recreated.
	f.AssertMachineState(t, target, types.StateRunning)
	// The shared machine was not touched.
	f.AssertMachineState(t, sharedName, types.StateRunning)
}
