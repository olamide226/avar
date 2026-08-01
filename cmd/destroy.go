package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/state"
	"github.com/olamide226/avar/internal/types"
)

func init() { registerSubcommand("destroy", runDestroy) }

const (
	// destroyYesFlag skips the confirmation so scripts and CI can remove an
	// environment without a terminal.
	destroyYesFlag = "--yes"
	// destroyAllFlag removes every environment avar manages (REQ-5.7).
	destroyAllFlag = "--all"
	// destroyOrphanedFlag removes isolated environments whose project
	// directory no longer exists (REQ-5.8).
	destroyOrphanedFlag = "--orphaned"
)

// destroyScope is which environments an invocation is asking to remove.
type destroyScope int

const (
	// scopeCurrent is the environment for the current directory's selector.
	scopeCurrent destroyScope = iota
	scopeAll
	scopeOrphaned
)

// destroyArgs is the parsed form of `avr destroy`'s own flags.
type destroyArgs struct {
	scope destroyScope
	yes   bool
}

// victim is a machine selected for destruction, together with the project it
// served where avar knows one.
//
// The project comes from avar's own records rather than from the backend's
// mount list. Which project an isolated machine serves is avar's bookkeeping
// and nothing a backend can be asked (see the MachineStatus doc comment), and
// REQ-5.8 requires naming it — so it is carried here from the lookup that
// already had to establish it, rather than re-derived later from a mount and
// hoped to be the same thing.
type victim struct {
	machine types.MachineStatus
	project string
	// sessions is how many live avr sessions are attached. It is counted from
	// avar's own records rather than read from MachineStatus.Sessions, which
	// no provider populates — the backend has no idea what an avr session is.
	sessions int
}

// label names a victim the way the user chose it — by environment, and by
// project when that is what distinguishes it — never by the machine name avar
// generated (REQ-1.5).
//
// Several isolated environments share one environment label, so destroying
// three of them would otherwise print the same sentence three times, and
// REQ-5.8 asks for the project each belonged to.
func (v victim) label() string {
	label := environmentLabel(v.machine)
	if v.project == "" {
		return label
	}
	return fmt.Sprintf("%s for %s", label, v.project)
}

// parseDestroyArgs reads `destroy`'s flags. The grammar hands a subcommand its
// arguments unparsed; what they mean is the command's business (design §3.1).
func parseDestroyArgs(args []string) (destroyArgs, error) {
	out := destroyArgs{scope: scopeCurrent}

	for _, arg := range args {
		switch arg {
		case destroyYesFlag:
			out.yes = true
		case destroyAllFlag, destroyOrphanedFlag:
			want := scopeAll
			if arg == destroyOrphanedFlag {
				want = scopeOrphaned
			}
			// Two different scopes in one command is a contradiction rather
			// than a combination, and guessing which was meant would be
			// guessing about destruction. The same one twice is harmless.
			if out.scope != scopeCurrent && out.scope != want {
				return destroyArgs{}, fmt.Errorf("`avr destroy` cannot take both %s and %s: %s removes every environment, %s removes only those whose project directory is gone",
					destroyAllFlag, destroyOrphanedFlag, destroyAllFlag, destroyOrphanedFlag)
			}
			out.scope = want
		default:
			return destroyArgs{}, fmt.Errorf("`avr destroy` does not understand %q: it takes %s, %s, and %s to skip confirmation",
				arg, destroyAllFlag, destroyOrphanedFlag, destroyYesFlag)
		}
	}
	return out, nil
}

// runDestroy removes environments and everything inside them (REQ-5.6–5.8).
//
// Destroy is the counterpart to the implicit create that a first `avr` performs.
// Every other lifecycle verb existed before it did — start, stop, idle-stop,
// reset — which left an environment that could be created but never deliberately
// removed, and a user who wanted the disk back reaching for the backend's own
// tooling (REQ-1.5).
//
// Host project files are never affected: the provider shares directories rather
// than copying them, so removing a machine cannot lose a user's work (PROP-10).
func runDestroy(ctx context.Context, app *App, inv cli.Invocation) error {
	args, err := parseDestroyArgs(inv.SubcommandArgs)
	if err != nil {
		return Exit(exitUsage, err)
	}

	p, err := app.Provider(ctx)
	if err != nil {
		return err
	}

	owned, err := p.Status(ctx)
	if err != nil {
		return err
	}
	owned = avarOwned(owned)

	victims, err := selectForDestruction(app, inv, owned, args.scope)
	if err != nil {
		return err
	}
	if len(victims) == 0 {
		writeNothingToDestroy(app, args.scope)
		return nil
	}
	countLiveSessions(app, victims)

	writeDestroySummary(app, victims, args.scope)

	if !args.yes && !confirmDestruction(app, victims, args.scope) {
		fmt.Fprintln(app.Out, "Nothing was destroyed.")
		return nil
	}
	fmt.Fprintln(app.Out)

	for _, v := range victims {
		fmt.Fprintf(app.Out, "Destroying %s…\n", v.label())
		if err := p.Delete(ctx, v.machine.Name); err != nil {
			return fmt.Errorf("destroying %s: %w", v.label(), err)
		}
		forgetMachine(app, v.machine.Name)
	}

	fmt.Fprintln(app.Out)
	fmt.Fprintf(app.Out, "Destroyed %s. Your project files on this Mac were not touched.\n",
		countLabel(len(victims), "environment", "environments"))
	fmt.Fprintln(app.Out, "Run `avr` in a project to create one again.")
	return nil
}

// selectForDestruction resolves the scope into the machines to remove.
func selectForDestruction(app *App, inv cli.Invocation, owned []types.MachineStatus, scope destroyScope) ([]victim, error) {
	switch scope {
	case scopeAll:
		projects, err := isolatedProjectPaths(app)
		if err != nil {
			return nil, err
		}
		out := make([]victim, 0, len(owned))
		for _, m := range owned {
			out = append(out, victim{machine: m, project: projects[m.Name]})
		}
		return out, nil

	case scopeOrphaned:
		return orphanedMachines(app, owned)

	default:
		// Resolving reaches the environment for this directory the same way
		// every other command does, so `avr destroy` removes exactly what a
		// bare `avr` here would have entered.
		target, err := app.Resolve(inv)
		if err != nil {
			return nil, err
		}
		if m, found := findMachine(owned, target.MachineName); found {
			return []victim{{machine: m}}, nil
		}
		return nil, nil
	}
}

// isolatedProjectPaths maps each isolated machine to the host path of the
// project it serves, from avar's own records.
//
// Both reads happen in one transaction. Two would be cheap, but they would also
// be two answers about one question, and the join below is only meaningful if
// the machine records and the project records describe the same moment.
func isolatedProjectPaths(app *App) (map[string]string, error) {
	store, err := app.Store()
	if err != nil {
		return nil, err
	}

	var (
		machines []types.MachineRecord
		projects []types.ProjectRecord
	)
	if err := store.Update(func(tx *state.Tx) error {
		machines = tx.Machines()
		projects = tx.Projects()
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read avar's machine and project records: %w", err)
	}

	pathOf := make(map[string]string, len(projects))
	for _, pr := range projects {
		pathOf[pr.ID] = pr.Path
	}
	out := make(map[string]string, len(machines))
	for _, r := range machines {
		if r.Kind == types.KindIsolated {
			out[r.Name] = pathOf[r.ProjectID]
		}
	}
	return out, nil
}

// orphanedMachines returns the isolated environments whose project directory no
// longer exists on the host (REQ-5.8).
//
// These are unreachable by any other route: `avr isolate off` removes a
// project's machine but must be run from inside that project, and a directory
// that has been deleted cannot be stood in. Without this they accumulate
// forever, holding disk that nothing will reclaim.
func orphanedMachines(app *App, owned []types.MachineStatus) ([]victim, error) {
	projects, err := isolatedProjectPaths(app)
	if err != nil {
		return nil, err
	}

	var out []victim
	for _, m := range owned {
		// Only an isolated machine can be orphaned. A shared machine serves
		// every project at once, so no single directory disappearing makes it
		// unwanted, and a base machine serves future clones.
		//
		// An empty path means avar has no record of which project this serves,
		// so it cannot be shown to be orphaned and is left alone — the same
		// caution reconciliation applies to an isolated machine it cannot
		// place.
		path := projects[m.Name]
		if m.Kind != types.KindIsolated || path == "" {
			continue
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			out = append(out, victim{machine: m, project: path})
		}
	}
	// Provider.Status returns machines ordered by name and avarOwned preserves
	// that, so the selection is already stable.
	return out, nil
}

// countLiveSessions fills in how many avr sessions are attached to each
// selected environment, so the summary can say when destroying one would take
// somebody else's terminal with it.
//
// A failure leaves the counts at zero rather than stopping the command: not
// knowing about a session is a worse summary, but refusing to destroy anything
// because the session file could not be read would be a worse command.
func countLiveSessions(app *App, victims []victim) {
	store, err := app.Store()
	if err != nil {
		return
	}
	sessions, err := store.Sessions()
	if err != nil {
		return
	}
	for i := range victims {
		victims[i].sessions = countSessions(sessions, victims[i].machine.Name)
	}
}

// writeNothingToDestroy explains an empty selection in the terms the user asked
// in, rather than printing an empty list.
func writeNothingToDestroy(app *App, scope destroyScope) {
	switch scope {
	case scopeAll:
		fmt.Fprintln(app.Out, "There are no avar environments to destroy.")
	case scopeOrphaned:
		fmt.Fprintln(app.Out, "No environments are orphaned: every isolated environment still has its project directory.")
	default:
		fmt.Fprintln(app.Out, "There is no environment for this directory, so there is nothing to destroy.")
		fmt.Fprintln(app.Out)
		fmt.Fprintln(app.Out, "Run `avr status` to see the environments that do exist.")
	}
}

// writeDestroySummary states what is about to be destroyed and what survives,
// so that a user cannot confirm without having been told (REQ-5.6).
func writeDestroySummary(app *App, victims []victim, scope destroyScope) {
	if scope == scopeOrphaned {
		fmt.Fprintf(app.Out, "%s no longer have a project directory:\n\n",
			countLabel(len(victims), "environment", "environments"))
	} else {
		fmt.Fprintf(app.Out, "This will permanently destroy %s:\n\n",
			countLabel(len(victims), "environment", "environments"))
	}

	for _, v := range victims {
		fmt.Fprintf(app.Out, "  %s  (%s)\n", v.label(), modeLabel(v.machine.Kind))
		for _, mount := range v.machine.Mounts {
			fmt.Fprintf(app.Out, "      shares %s\n", mount.HostPath)
		}
		// A machine somebody is sitting in is the one case where destroying
		// costs someone else their terminal, so it is named before the
		// confirmation rather than discovered after it (REQ-5.6).
		if v.sessions > 0 {
			fmt.Fprintf(app.Out, "      %s attached right now\n",
				countLabel(v.sessions, "session is", "sessions are"))
		}
	}

	fmt.Fprintln(app.Out)
	fmt.Fprintln(app.Out, "Everything installed inside them is lost: packages, files outside your")
	fmt.Fprintln(app.Out, "project directories, and any running state.")
	fmt.Fprintln(app.Out, "Your project files on this Mac are shared, never copied, and are not affected.")
}

// confirmDestruction asks for confirmation proportionate to what is at stake.
//
// A single environment is confirmed by typing its name, exactly as `avr reset`
// does, so the two read the same. `--all` asks for the word "all", because a
// keystroke that removes everything should not be one keystroke. `--orphaned`
// takes a yes, because every environment in that list belongs to a project
// directory the user has already deleted, and the list has just been shown.
func confirmDestruction(app *App, victims []victim, scope destroyScope) bool {
	switch scope {
	case scopeAll:
		return app.confirmByTyping("\nType \"all\" and press enter to destroy all of them, or anything else to cancel: ", "all")

	case scopeOrphaned:
		return app.confirmByTyping("\nType \"yes\" and press enter to destroy them, or anything else to cancel: ", "yes")

	default:
		label := victims[0].label()
		return app.confirmByTyping(
			fmt.Sprintf("\nType %q and press enter to destroy it, or anything else to cancel: ", label), label)
	}
}

// forgetMachine drops everything that described a machine avar has just
// destroyed: its record, and its entry in avar's SSH configuration.
//
// The two belong together. A record naming a machine that does not exist is
// what reconciliation exists to clean up, and an SSH stanza pointing at a dead
// guest is cleaned up by nothing at all — so a caller that removes one and
// forgets the other leaves the worse half behind.
//
// Failures are not reported: the machine is genuinely gone, which is what the
// user asked for, and turning bookkeeping into an error would report a failure
// against an operation that succeeded.
func forgetMachine(app *App, machine string) {
	store, err := app.Store()
	if err != nil {
		return
	}
	_ = store.DeleteMachine(machine)
	forgetSSHHost(app, machine)
}
