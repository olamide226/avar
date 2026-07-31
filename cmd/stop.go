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
func runStop(ctx context.Context, app *App, inv cli.Invocation) error {
	all, err := parseStopArgs(inv.SubcommandArgs)
	if err != nil {
		return Exit(exitUsage, err)
	}

	p, err := app.Provider(ctx)
	if err != nil {
		return err
	}
	if all {
		return stopEverything(ctx, app, p)
	}
	return stopSelected(ctx, app, p, inv)
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
func stopSelected(ctx context.Context, app *App, p provider.Provider, inv cli.Invocation) error {
	target, err := app.Resolve(inv)
	if err != nil {
		return err
	}
	label := target.Selector.Label()

	stopping := false
	progress := types.ProgressFunc(func(event types.ProgressEvent) {
		// The backend's own wording names the machine, and a machine name is
		// something avar's user never has to see (REQ-1.5). The event is what
		// matters — that work has started — so avar says so in its own words.
		if event.Kind == types.ProgressStopping {
			stopping = true
			fmt.Fprintf(app.Out, "Stopping %s…\n", label)
		}
	})

	switch err := p.Stop(ctx, target.MachineName, progress); {
	case errors.Is(err, provider.ErrMachineNotFound):
		// Never created, or already deleted. Nothing to stop is a converged
		// state, not a failure of the command.
		fmt.Fprintf(app.Out, "There is no %s environment yet, so there is nothing to stop.\n", label)
		return nil
	case errors.Is(err, provider.ErrNotOwned):
		return unownedError(err, label)
	case err != nil:
		return err
	}

	if stopping {
		fmt.Fprintf(app.Out, "Stopped %s. Run `avr` to start it again.\n", label)
	} else {
		// Stop converges on a state, so this is a success (REQ-5.2).
		fmt.Fprintf(app.Out, "%s was already stopped.\n", label)
	}
	return nil
}

// stopEverything stops every environment avar manages (REQ-5.2).
//
// The list comes from the provider's own listing, which is filtered to machines
// avar owns, and is filtered again here: --all must never reach a machine the
// user created themselves (REQ-5.4, PROP-6).
func stopEverything(ctx context.Context, app *App, p provider.Provider) error {
	machines, err := p.Status(ctx)
	if err != nil {
		return err
	}
	machines = avarOwned(machines)
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
			alreadyIdle++
			continue
		default:
			// A machine that is coming up, broken, or in a state avar has no
			// word for is left alone: stopping something mid-creation is how
			// one invocation wrecks another's work (design §3.5).
			busy = append(busy, fmt.Sprintf("%s (%s)", label, stateLabel(machine.State)))
			continue
		}

		fmt.Fprintf(app.Out, "Stopping %s…\n", label)
		switch err := p.Stop(ctx, machine.Name, types.DiscardProgress); {
		case errors.Is(err, provider.ErrMachineNotFound):
			// It disappeared between the listing and the stop, which is the
			// state --all was asking for.
			alreadyIdle++
		case errors.Is(err, provider.ErrNotOwned):
			failures = append(failures, unownedError(err, label))
		case err != nil:
			failures = append(failures, err)
		default:
			stopped++
		}
	}

	writeStopSummary(app, stopped, alreadyIdle, busy)
	return joinStopFailures(failures)
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

// joinStopFailures reports the machines --all could not stop, having tried
// every one of them first: one environment refusing to stop must not leave the
// others running.
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
		"run `avr` to let avar adopt or clean it up, or remove it with your virtualization tool directly", label, cause)
}
