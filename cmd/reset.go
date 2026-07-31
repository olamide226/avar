package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/types"
)

func init() { registerSubcommand("reset", runReset) }

// resetYesFlag skips the interactive confirmation so that scripts and tests can
// reset an environment without a terminal (REQ-10.3).
const resetYesFlag = "--yes"

// parseResetArgs reads `reset`'s own flags.
//
// As with every avar subcommand, the grammar hands the command its arguments
// unparsed: where the subcommand begins is the grammar's decision, what its
// arguments mean is the command's (design §3.1). `reset` has exactly one flag.
func parseResetArgs(args []string) (yes bool, err error) {
	for _, arg := range args {
		switch arg {
		case resetYesFlag:
			yes = true
		default:
			return false, fmt.Errorf("`avr reset` does not understand %q: it takes no arguments except %s to skip confirmation", arg, resetYesFlag)
		}
	}
	return yes, nil
}

// runReset returns the current environment to a clean base state — a fresh OS
// with nothing installed (REQ-10.3).
//
// It destroys the machine and everything inside it, then recreates it from the
// same base image. Host project files are never touched: the provider shares
// directories, never copies them, so destroying a machine cannot lose a user's
// work (REQ-10.3, PROP-10).
//
// Reset requires interactive confirmation unless --yes (REQ-10.3). An
// environment that was never created has nothing to reset, and reset says so
// rather than provisioning one without asking.
func runReset(ctx context.Context, app *App, inv cli.Invocation) error {
	yes, err := parseResetArgs(inv.SubcommandArgs)
	if err != nil {
		return Exit(exitUsage, err)
	}

	target, err := app.Resolve(inv)
	if err != nil {
		return err
	}

	p, err := app.Provider(ctx)
	if err != nil {
		return err
	}

	machines, err := p.Status(ctx)
	if err != nil {
		return err
	}
	machines = avarOwned(machines)

	label := target.Selector.Label()
	machine, found := findMachine(machines, target.MachineName)

	if !found {
		fmt.Fprintf(app.Out, "There is no %s environment yet, so there is nothing to reset.\n", label)
		fmt.Fprintln(app.Out)
		fmt.Fprintln(app.Out, "Run `avr` to create one. Reset returns the environment to a clean")
		fmt.Fprintln(app.Out, "base state; it does not create one from nowhere.")
		return nil
	}

	// Print an explicit destruction summary: what will be destroyed and what
	// will survive, so the user cannot confirm without knowing (REQ-10.3).
	writeResetSummary(app, machine, label)

	if !yes {
		confirmed, err := confirmReset(app.Stdin, app.Out, label)
		if err != nil {
			return fmt.Errorf("reading confirmation: %w", err)
		}
		if !confirmed {
			fmt.Fprintln(app.Out, "Reset cancelled. Nothing was changed.")
			return nil
		}
		fmt.Fprintln(app.Out)
	}

	// Destroy then recreate (design §3.5). Delete is fully idempotent: it
	// cannot fail because the machine does not exist, and the listing above
	// already confirmed it does.
	fmt.Fprintf(app.Out, "Destroying %s…\n", label)
	if err := p.Delete(ctx, target.MachineName); err != nil {
		return fmt.Errorf("resetting %s: %w", label, err)
	}
	forgetSSHHost(app, target.MachineName)

	// Recreating goes through the same path as an ordinary first use, so the
	// fresh machine gets the project mount, avar's record is rewritten, and
	// the user sees provisioning progress — a cold create takes minutes, and
	// silence through it reads as a hang.
	fmt.Fprintf(app.Out, "Creating a fresh %s…\n", label)
	if _, err := prepareEnvironment(ctx, app, p, target, progressTo(app.Err)); err != nil {
		return fmt.Errorf("resetting %s after destroying it: %w", label, err)
	}

	fmt.Fprintf(app.Out, "\nReset complete. %s is a fresh environment with nothing installed.\n", label)
	fmt.Fprintf(app.Out, "Run `avr` to enter it.\n")
	return nil
}

// writeResetSummary tells the user what is about to be destroyed: the machine's
// name and environment, any shared project directories, and what the consequences
// are (REQ-10.3).
//
// The summary is printed before any confirmation, so the user must see it before
// they can type anything. Only an explicit --yes skips this step.
func writeResetSummary(app *App, machine types.MachineStatus, label string) {
	fmt.Fprintf(app.Out, "You are about to destroy this Linux environment:\n\n")
	fmt.Fprintf(app.Out, "  environment   %s\n", label)
	if machine.Kind != "" {
		mode := modeLabel(machine.Kind)
		if machine.Kind == types.KindIsolated && len(machine.Mounts) > 0 {
			mode = fmt.Sprintf("this project (%s)", label)
		}
		fmt.Fprintf(app.Out, "  mode          %s\n", mode)
	}
	if len(machine.Mounts) > 0 {
		fmt.Fprintf(app.Out, "  projects      %s\n", machine.Mounts[0].HostPath)
		for _, mount := range machine.Mounts[1:] {
			fmt.Fprintf(app.Out, "                %s\n", mount.HostPath)
		}
	}
	fmt.Fprintln(app.Out)
	fmt.Fprintf(app.Out, "Everything inside the guest — installed packages, configuration, local\n")
	fmt.Fprintf(app.Out, "databases, and files you created inside Linux — will be destroyed\n")
	fmt.Fprintf(app.Out, "permanently.\n")
	fmt.Fprintln(app.Out)
	fmt.Fprintf(app.Out, "Your host project files are not affected. avar shares them, never copies\n")
	fmt.Fprintf(app.Out, "them, so destroying the Linux environment cannot lose your work.\n")
}

// confirmReset asks the user to type the environment label to confirm they
// understand what they are about to destroy.
//
// Typing the label rather than "y" or "yes" is deliberate: reading the label
// and typing it back proves the user saw what was being destroyed, which a
// single-character prompt cannot (REQ-10.3).
func confirmReset(stdin io.Reader, out io.Writer, label string) (bool, error) {
	fmt.Fprintf(out, "\nType %q and press enter to reset, or anything else to cancel: ", label)

	reader := bufio.NewReader(stdin)
	reply, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	reply = strings.TrimSpace(reply)
	return reply == label, nil
}
