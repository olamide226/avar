package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/olamide226/avar/internal/cli"
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

// parseDestroyArgs reads `destroy`'s flags. The grammar hands a subcommand its
// arguments unparsed; what they mean is the command's business (design §3.1).
func parseDestroyArgs(args []string) (destroyArgs, error) {
	out := destroyArgs{scope: scopeCurrent}
	scoped := ""

	for _, arg := range args {
		switch arg {
		case destroyYesFlag:
			out.yes = true
		case destroyAllFlag, destroyOrphanedFlag:
			// Two scopes in one command is a contradiction rather than a
			// combination, and guessing which the user meant would be
			// guessing about destruction.
			if scoped != "" && scoped != arg {
				return destroyArgs{}, fmt.Errorf("`avr destroy` cannot take both %s and %s: %s removes every environment, %s removes only those whose project directory is gone",
					scoped, arg, destroyAllFlag, destroyOrphanedFlag)
			}
			scoped = arg
			if arg == destroyAllFlag {
				out.scope = scopeAll
			} else {
				out.scope = scopeOrphaned
			}
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

	writeDestroySummary(app, victims, args.scope)

	if !args.yes && !confirmDestruction(app, victims, args.scope) {
		fmt.Fprintln(app.Out, "Nothing was destroyed.")
		return nil
	}
	fmt.Fprintln(app.Out)

	destroyed := 0
	for _, m := range victims {
		fmt.Fprintf(app.Out, "Destroying %s…\n", environmentOf(m))
		if err := p.Delete(ctx, m.Name); err != nil {
			return fmt.Errorf("destroying %s: %w", environmentOf(m), err)
		}
		// The machine is gone, so the things that describe it must go too:
		// a record naming a machine that does not exist is exactly what
		// reconciliation exists to clean up, and an SSH stanza pointing at a
		// dead guest accumulates silently.
		forgetMachineRecord(app, m.Name)
		forgetSSHHost(app, m.Name)
		destroyed++
	}

	fmt.Fprintln(app.Out)
	fmt.Fprintf(app.Out, "Destroyed %s. Your project files on this Mac were not touched.\n",
		countLabel(destroyed, "environment", "environments"))
	fmt.Fprintln(app.Out, "Run `avr` in a project to create one again.")
	return nil
}

// selectForDestruction resolves the scope into the machines to remove.
func selectForDestruction(app *App, inv cli.Invocation, owned []types.MachineStatus, scope destroyScope) ([]types.MachineStatus, error) {
	switch scope {
	case scopeAll:
		return owned, nil

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
			return []types.MachineStatus{m}, nil
		}
		return nil, nil
	}
}

// orphanedMachines returns the isolated environments whose project directory no
// longer exists on the host (REQ-5.8).
//
// These are unreachable by any other route: `avr isolate off` removes a
// project's machine but must be run from inside that project, and a directory
// that has been deleted cannot be stood in. Without this they accumulate
// forever, holding disk that nothing will reclaim.
func orphanedMachines(app *App, owned []types.MachineStatus) ([]types.MachineStatus, error) {
	store, err := app.Store()
	if err != nil {
		return nil, err
	}
	records, err := store.Machines()
	if err != nil {
		return nil, fmt.Errorf("read avar's machine records: %w", err)
	}
	projects, err := store.Projects()
	if err != nil {
		return nil, fmt.Errorf("read avar's project records: %w", err)
	}

	pathOf := make(map[string]string, len(projects))
	for _, pr := range projects {
		pathOf[pr.ID] = pr.Path
	}
	projectOf := make(map[string]string, len(records))
	for _, r := range records {
		if r.Kind == types.KindIsolated && r.ProjectID != "" {
			projectOf[r.Name] = pathOf[r.ProjectID]
		}
	}

	var out []types.MachineStatus
	for _, m := range owned {
		// Only isolated machines can be orphaned. A shared machine serves
		// every project at once, so no single directory disappearing makes
		// it unwanted, and a base machine serves future clones.
		if m.Kind != types.KindIsolated {
			continue
		}
		path, known := projectOf[m.Name]
		if !known || path == "" {
			// avar has no record of which project this serves. It cannot be
			// shown to be orphaned, so it is left alone — the same caution
			// reconciliation applies to an isolated machine it cannot place.
			continue
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
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
func writeDestroySummary(app *App, victims []types.MachineStatus, scope destroyScope) {
	if scope == scopeOrphaned {
		fmt.Fprintf(app.Out, "%s no longer has its project directory:\n\n",
			countLabel(len(victims), "environment", "environments"))
	} else {
		fmt.Fprintf(app.Out, "This will permanently destroy %s:\n\n",
			countLabel(len(victims), "environment", "environments"))
	}

	for _, m := range victims {
		fmt.Fprintf(app.Out, "  %s  (%s)\n", environmentOf(m), modeLabel(m.Kind))
		for _, mount := range m.Mounts {
			fmt.Fprintf(app.Out, "      shares %s\n", mount.HostPath)
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
func confirmDestruction(app *App, victims []types.MachineStatus, scope destroyScope) bool {
	switch scope {
	case scopeAll:
		fmt.Fprintf(app.Out, "\nType %q and press enter to destroy all of them, or anything else to cancel: ", "all")
		return readLine(app) == "all"

	case scopeOrphaned:
		return app.confirmYesNo("\nDestroy them? (y/N) ")

	default:
		label := environmentOf(victims[0])
		fmt.Fprintf(app.Out, "\nType %q and press enter to destroy it, or anything else to cancel: ", label)
		return readLine(app) == label
	}
}

// readLine reads one trimmed line from the App's input.
//
// A whole line, not a token: environment labels contain spaces ("Ubuntu 24.04 ·
// arm64"), so a word-wise read would compare the user's answer against only its
// first word and no correct answer would ever be accepted.
//
// An empty or closed input returns the empty string, which fails every caller's
// comparison — the safe direction for a destructive command.
func readLine(app *App) string {
	line, err := bufio.NewReader(app.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimSpace(line)
}

// environmentOf names a machine the way the user chose it — by environment, and
// by the project when that is what distinguishes it — rather than by the machine
// name avar generated (REQ-1.5).
//
// Isolated environments carry their project, because several of them share one
// environment label: destroying three of them otherwise prints the same
// sentence three times, and REQ-5.8 requires naming the project each belonged
// to. A shared environment serves every project at once and has no single
// project to name.
func environmentOf(m types.MachineStatus) string {
	label := m.Selector.Label()
	if m.Kind != types.KindIsolated {
		return label
	}
	for _, mount := range m.Mounts {
		if mount.HostPath != "" {
			return fmt.Sprintf("%s for %s", label, mount.HostPath)
		}
	}
	return label
}

// forgetMachineRecord drops avar's record of a machine that no longer exists.
//
// Failure is not reported: the machine is genuinely gone, which is what the
// user asked for, and a stale record is repaired by reconciliation on the next
// invocation. Turning that into an error would report a failure against an
// operation that succeeded.
func forgetMachineRecord(app *App, machine string) {
	store, err := app.Store()
	if err != nil {
		return
	}
	_ = store.DeleteMachine(machine)
}
