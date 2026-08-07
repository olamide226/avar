package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

func init() { registerSubcommand("stop", runStop) }

// stopAllFlag asks for every environment avar manages rather than the one this
// directory and these selector flags resolve to (REQ-5.2).
const stopAllFlag = "--all"

// runStop stops the environment for the current selector, or every environment
// avar manages with --all (REQ-5.2).
//
// Stopping releases memory and destroys nothing: packages, files written inside
// the guest and everything else survive, and the next `avr` starts the machine
// again.
//
// Both paths start from the backend's own listing of the machines avar owns.
// That is what lets avar say "there is no such environment yet" instead of
// reporting a machine it never created as an error, and it is the same listing
// --all is built on, so neither path can reach a machine avar does not own
// (REQ-5.4, PROP-6).
func runStop(ctx context.Context, app *App, inv cli.Invocation) error {
	all, err := parseStopArgs(inv.SubcommandArgs)
	if err != nil {
		return Exit(exitUsage, err)
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

	if all {
		return stopEverything(ctx, app, p, machines)
	}
	return stopSelected(ctx, app, p, inv, machines)
}

// parseStopArgs reads `stop`'s own flags.
//
// internal/cli hands a subcommand its arguments unparsed, deliberately: avar's
// grammar decides where the subcommand begins and the subcommand decides what
// its own arguments mean (design §3.1). `stop` has exactly one flag, so the
// whole of that decision is here.
func parseStopArgs(args []string) (all bool, err error) {
	for _, arg := range args {
		switch arg {
		case stopAllFlag:
			all = true
		default:
			return false, fmt.Errorf("`avr stop` does not understand %q: it takes no arguments, or %s to stop every Linux environment avar manages", arg, stopAllFlag)
		}
	}
	return all, nil
}

// stopSelected stops the machine this directory and these selector flags
// resolve to (REQ-5.2).
func stopSelected(ctx context.Context, app *App, p provider.Provider, inv cli.Invocation, machines []types.MachineStatus) error {
	target, err := app.Resolve(inv)
	if err != nil {
		return err
	}
	label := target.Selector.Label()

	machine, found := findMachine(machines, target.MachineName)
	switch {
	case !found:
		// Never created, or already deleted. Nothing to stop is a converged
		// state, not a failure of the command.
		fmt.Fprintf(app.Out, "There is no %s environment yet, so there is nothing to stop.\n", label)
		return nil
	case machine.State == types.StateStopped:
		// Ask the provider to converge a stale backend process too. Lima can
		// report Stopped while an orphaned host agent is still consuming CPU.
		if err := p.Stop(ctx, machine.Name, types.DiscardProgress); err != nil {
			return err
		}
		fmt.Fprintf(app.Out, "%s is already stopped.\n", label)
		return nil
	case machine.State != types.StateRunning:
		fmt.Fprintf(app.Out, "%s is %s, so avar left it alone. Run `avr status` to see what its backend says about it.\n", label, stateLabel(machine.State))
		return nil
	}

	stopped, err := stopOne(ctx, app, p, machine, label)
	if err != nil {
		return err
	}
	if stopped {
		fmt.Fprintf(app.Out, "Stopped %s. Run `avr` to start it again.\n", label)
	} else {
		fmt.Fprintf(app.Out, "%s is already stopped.\n", label)
	}
	return nil
}

// stopEverything stops every environment avar manages (REQ-5.2).
func stopEverything(ctx context.Context, app *App, p provider.Provider, machines []types.MachineStatus) error {
	if len(machines) == 0 {
		fmt.Fprintln(app.Out, "avar is not managing any Linux environments, so there is nothing to stop.")
		return nil
	}

	var (
		stopped     int
		alreadyIdle int
		busy        []string
		failures    []error
	)
	for _, machine := range machines {
		label := environmentLabel(machine)
		switch machine.State {
		case types.StateRunning:
		case types.StateStopped:
			// A stopped VM can still have an orphaned backend process. Let Stop
			// converge that cleanup without counting it as a newly stopped VM.
			if err := p.Stop(ctx, machine.Name, types.DiscardProgress); err != nil {
				failures = append(failures, err)
			}
			alreadyIdle++
			continue
		default:
			// A machine that is coming up, broken, or in a state avar has no
			// word for is left alone: stopping something mid-creation is how
			// one invocation wrecks another's work (design §3.5).
			busy = append(busy, fmt.Sprintf("%s (%s)", label, stateLabel(machine.State)))
			continue
		}

		switch didStop, err := stopOne(ctx, app, p, machine, label); {
		case err != nil:
			// Every machine is attempted before anything is reported: one
			// environment refusing to stop must not leave the others running.
			failures = append(failures, err)
		case didStop:
			stopped++
		default:
			alreadyIdle++
		}
	}

	writeStopSummary(app, stopped, alreadyIdle, busy)
	return joinStopFailures(failures)
}

// stopOne stops a machine the listing reported as running, and says whether
// there was anything to stop.
//
// A machine that disappeared between the listing and the stop is the state the
// caller was asking for, not a failure.
func stopOne(ctx context.Context, app *App, p provider.Provider, machine types.MachineStatus, label string) (stopped bool, err error) {
	fmt.Fprintf(app.Out, "Stopping %s…\n", label)
	switch err := p.Stop(ctx, machine.Name, types.DiscardProgress); {
	case errors.Is(err, provider.ErrMachineNotFound):
		return false, nil
	case errors.Is(err, provider.ErrNotOwned):
		return false, unownedError(err, label)
	case err != nil:
		return false, err
	default:
		return true, nil
	}
}

// findMachine looks a resolved machine up in the backend's listing.
func findMachine(machines []types.MachineStatus, name string) (types.MachineStatus, bool) {
	for _, machine := range machines {
		if machine.Name == name {
			return machine, true
		}
	}
	return types.MachineStatus{}, false
}

// writeStopSummary reports what --all did, including what it deliberately did
// not do.
func writeStopSummary(app *App, stopped, alreadyIdle int, busy []string) {
	switch {
	case stopped > 0:
		fmt.Fprintf(app.Out, "Stopped %s. Run `avr` to start one again.\n", countLabel(stopped, "Linux environment", "Linux environments"))
	case alreadyIdle > 0:
		fmt.Fprintf(app.Out, "Nothing to stop: %s already stopped.\n", countLabel(alreadyIdle, "Linux environment is", "Linux environments are"))
	}
	for _, machine := range busy {
		fmt.Fprintf(app.Err, "avr: left %s alone; run `avr status` to see it.\n", machine)
	}
}

// joinStopFailures reports the machines that could not be stopped, after every
// one of them has been tried.
func joinStopFailures(failures []error) error {
	switch len(failures) {
	case 0:
		return nil
	case 1:
		return failures[0]
	default:
		messages := make([]string, 0, len(failures))
		for _, failure := range failures {
			messages = append(messages, failure.Error())
		}
		return fmt.Errorf("stopping every Linux environment: %d could not be stopped:\n  %s",
			len(failures), strings.Join(messages, "\n  "))
	}
}

// unownedError explains a machine that carries avar's name but is not in avar's
// records — what an interrupted first-time setup leaves behind (PROP-6, PROP-7).
//
// avar refuses to stop it rather than deciding it is probably fine, and says
// which of the two things the user can do next.
func unownedError(cause error, label string) error {
	return fmt.Errorf("stopping %s: %w. avar will not act on a machine it has no record of creating; "+
		"run `avr` to let avar adopt or clean it up, or stop it with your virtualization tool directly", label, cause)
}
