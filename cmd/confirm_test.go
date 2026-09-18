package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/types"
)

// `avr reset` and `avr destroy` delete everything inside an environment, and
// both ask for the environment's name to be typed first. These tests hold the
// two to one rule: a confirmation can only come from a person at a terminal.
// Without one, nothing is deleted, the command says to use --yes, and it exits
// non-zero so a script sees that nothing happened. At a terminal, running out
// of input is a cancellation, the same for both.

// confirmingCommand is one of the two commands, run against a seeded machine.
type confirmingCommand struct {
	name string
	run  func(ctx context.Context, app *App, inv cli.Invocation) error
	inv  func(args ...string) cli.Invocation
}

var confirmingCommands = []confirmingCommand{
	{name: "reset", run: runReset, inv: resetInvocation},
	{name: "destroy", run: runDestroy, inv: destroyInvocation},
}

// seededConfirmation returns a test app with one running environment for the
// current directory, its label, and the Fake behind it.
func seededConfirmation(t *testing.T, terminal bool, stdin string) (*testApp, *fake.Fake, string, string) {
	t.Helper()
	f := fake.New()
	app := newTestApp(t, f)
	target, label := resolvedTarget(t, app)
	seedMachine(t, f, target, ubuntu(), types.KindShared, hostPath("/Users/ola/code/app"))
	app.terminal = func() bool { return terminal }
	app.Stdin = strings.NewReader(stdin)
	return app, f, target, label
}

// REQ-10.3, REQ-5.6: without a terminal there is nobody to confirm, so nothing
// is deleted and the command fails, saying how to run it unattended. That holds
// even when the right answer arrives on stdin: a piped name is not evidence
// that anyone read the summary.
func TestConfirm_RefusedWithoutATerminal_REQ_10_3_REQ_5_6(t *testing.T) {
	for _, cmd := range confirmingCommands {
		for name, stdin := range map[string]string{
			"end of input":     "",
			"a piped answer":   "LABEL\n",
			"no final newline": "LABEL",
		} {
			t.Run(cmd.name+"/"+name, func(t *testing.T) {
				app, f, target, label := seededConfirmation(t, false, "")
				app.Stdin = strings.NewReader(strings.ReplaceAll(stdin, "LABEL", label))

				err := cmd.run(context.Background(), app.App, cmd.inv())
				if err == nil {
					t.Fatalf("`avr %s` without a terminal exited 0", cmd.name)
				}
				var exit *ExitCodeError
				if errors.As(err, &exit) && exit.Code == 0 {
					t.Fatalf("`avr %s` without a terminal exited 0: %v", cmd.name, err)
				}
				if !strings.Contains(err.Error(), "--yes") {
					t.Errorf("the refusal does not say to use --yes: %v", err)
				}
				if n := f.Count(fake.OpDelete); n != 0 {
					t.Errorf("Delete was called %d time(s) without a terminal", n)
				}
				f.AssertMachineState(t, target, types.StateRunning)
			})
		}
	}
}

// --yes is the unattended path, and it needs no terminal.
func TestConfirm_YesNeedsNoTerminal_REQ_10_3_REQ_5_6(t *testing.T) {
	for _, cmd := range confirmingCommands {
		t.Run(cmd.name, func(t *testing.T) {
			app, f, _, _ := seededConfirmation(t, false, "")

			if err := cmd.run(context.Background(), app.App, cmd.inv("--yes")); err != nil {
				t.Fatalf("`avr %s --yes` without a terminal: %v", cmd.name, err)
			}
			if f.Count(fake.OpDelete) != 1 {
				t.Errorf("`avr %s --yes` did not delete the environment", cmd.name)
			}
		})
	}
}

// At a terminal, running out of input (Ctrl-D) cancels, and both commands
// treat it the way they treat any other answer that is not the name: nothing
// is deleted and they exit 0, because the user chose not to go ahead. Reset
// used to fail with "reading confirmation: EOF" while destroy exited 0.
func TestConfirm_EndOfInputAtATerminalCancels_REQ_10_3_REQ_5_6(t *testing.T) {
	for _, cmd := range confirmingCommands {
		t.Run(cmd.name, func(t *testing.T) {
			app, f, target, _ := seededConfirmation(t, true, "")

			if err := cmd.run(context.Background(), app.App, cmd.inv()); err != nil {
				t.Fatalf("`avr %s` cancelled with Ctrl-D returned %v, want a clean cancellation", cmd.name, err)
			}
			if n := f.Count(fake.OpDelete); n != 0 {
				t.Errorf("Delete was called %d time(s) after a cancellation", n)
			}
			f.AssertMachineState(t, target, types.StateRunning)
		})
	}
}
