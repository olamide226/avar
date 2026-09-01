package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/resolve"
	"github.com/olamide226/avar/internal/types"
	"github.com/olamide226/avar/internal/workspace"
)

func init() { registerSubcommand("sync", runSync) }

const (
	// syncToHostFlag applies the Linux side's changes to the host copy, which
	// is the direction REQ-14.2 is about.
	syncToHostFlag = "--to-host"
	// syncToGuestFlag applies the host's changes to the Linux copy.
	syncToGuestFlag = "--to-guest"
	// syncYesFlag skips the confirmation, for scripts and for a user who has
	// already reviewed the same listing with a bare `avr sync`.
	syncYesFlag = "--yes"
)

// listingLimit bounds how many files one review listing prints.
//
// A review the user cannot read is not a review, and the first synchronization
// of a real project is thousands of files. The cap is generous enough that an
// ordinary day's work is listed in full and small enough that a wall of output
// does not scroll the summary off the screen; what is elided is always counted,
// so the number the user confirms is never the number they happened to see.
const listingLimit = 200

// syncArgs is the parsed form of `avr sync`'s own flags.
type syncArgs struct {
	// direction is empty when the user asked only to review.
	direction types.WorkspaceDirection
	yes       bool
}

// parseSyncArgs reads `sync`'s flags. The grammar hands a subcommand its
// arguments unparsed; what they mean is the command's business (design §3.1).
func parseSyncArgs(args []string) (syncArgs, error) {
	var out syncArgs
	for _, arg := range args {
		switch arg {
		case syncToHostFlag:
			if out.direction == types.ToGuest {
				return syncArgs{}, fmt.Errorf("%s and %s are opposite directions; run one, review the result, then run the other if you still want to", syncToGuestFlag, syncToHostFlag)
			}
			out.direction = types.ToHost
		case syncToGuestFlag:
			if out.direction == types.ToHost {
				return syncArgs{}, fmt.Errorf("%s and %s are opposite directions; run one, review the result, then run the other if you still want to", syncToGuestFlag, syncToHostFlag)
			}
			out.direction = types.ToGuest
		case syncYesFlag:
			out.yes = true
		default:
			return syncArgs{}, fmt.Errorf("`avr sync` does not take %q; it accepts %s, %s and %s", arg, syncToHostFlag, syncToGuestFlag, syncYesFlag)
		}
	}
	return out, nil
}

// runSync shows what differs between a project's two copies and, when a
// direction is named, applies one of them after the user has seen it
// (REQ-14.2, REQ-14.3).
//
//	avr sync                # review both directions, change nothing
//	avr sync --to-host      # apply the Linux side's changes to the host
//	avr sync --to-guest     # apply the host's changes to the Linux side
func runSync(ctx context.Context, app *App, inv cli.Invocation) error {
	args, err := parseSyncArgs(inv.SubcommandArgs)
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
	nw, err := nativeWorkspacer(p)
	if err != nil {
		return err
	}

	// The same path an ordinary session takes: the environment is brought up
	// and the project is shared into it, because the share is how the two
	// copies reach each other at all.
	if _, _, err := prepareEnvironment(ctx, app, p, target, progressTo(app.Err)); err != nil {
		return err
	}

	ws, _, err := nw.MapNativeWorkspace(target.Project.ID, target.Project.Path, target.HostCwd)
	if err != nil {
		return err
	}
	scan, err := nw.ScanNativeWorkspace(ctx, target.MachineName, ws)
	if err != nil {
		return err
	}
	if !scan.Exists {
		return fmt.Errorf("%s has no Linux-native copy yet, so there is nothing to synchronize; run `avr --native-fs` in this project to create one", target.Project.Path)
	}

	plan := workspace.PlanSync(scan)
	reportSkipped(app.Err, scan.Skipped)

	if len(plan.Conflicts) > 0 {
		reportConflicts(app.Out, ws, plan.Conflicts)
		return Exit(1, nil)
	}

	if args.direction == "" {
		reportReview(app.Out, ws, plan)
		return nil
	}

	sync := plan.Sync(args.direction)
	if sync.Empty() {
		fmt.Fprintf(app.Out, "%s is already synchronized with its Linux-native copy at %s.\n", ws.HostPath, ws.Path)
		return nil
	}

	reportDirection(app.Out, ws, args.direction, changesFor(plan, args.direction))
	if !args.yes {
		if !stdinIsTerminal() {
			return fmt.Errorf("applying this would change %s; run it in an interactive terminal, or pass %s once you have reviewed the list above",
				destinationOf(ws, args.direction), syncYesFlag)
		}
		if !app.confirmYesNo(fmt.Sprintf("Apply these changes to %s? (y/N) ", destinationOf(ws, args.direction))) {
			return Exit(130, nil)
		}
	}

	if err := nw.ApplyNativeWorkspace(ctx, target.MachineName, ws, sync, progressTo(app.Err)); err != nil {
		return err
	}
	fmt.Fprintf(app.Out, "Applied %s to %s.\n",
		countLabel(len(sync.Copy)+len(sync.Delete), "change", "changes"), destinationOf(ws, args.direction))
	return nil
}

// enterNativeWorkspace puts this invocation's session in the project's copy on
// the guest's own filesystem, creating and updating that copy first (REQ-14.1).
//
// The host's changes are carried into the copy without asking, and only they:
// applying a change to a file the guest has not touched since the last
// synchronization cannot lose anything, which is what makes it safe to do on
// the way to a shell. Anything the guest changed stays where it is and is
// reported by `avr sync`, and a file both sides changed is a conflict that
// stops the copying entirely — avar says so and hands over the shell anyway,
// because resolving it is something the user needs a shell to do (REQ-14.3).
func enterNativeWorkspace(ctx context.Context, app *App, p provider.Provider, target resolve.ResolvedTarget, progress types.ProgressSink) (string, error) {
	nw, err := nativeWorkspacer(p)
	if err != nil {
		return "", err
	}

	ws, guestCwd, err := nw.MapNativeWorkspace(target.Project.ID, target.Project.Path, target.HostCwd)
	if err != nil {
		return "", err
	}
	scan, err := nw.ScanNativeWorkspace(ctx, target.MachineName, ws)
	if err != nil {
		return "", err
	}

	plan := workspace.PlanSync(scan)

	if len(plan.Conflicts) > 0 {
		reportConflicts(app.Err, ws, plan.Conflicts)
		return guestCwd, nil
	}

	sync := plan.Sync(types.ToGuest)
	if sync.Empty() && scan.Exists {
		return guestCwd, nil
	}
	if !scan.Exists {
		fmt.Fprintf(app.Err, "avr: copying %s into this environment's own filesystem at %s.\n", ws.HostPath, ws.Path)
		fmt.Fprintf(app.Err, "     Build output stays in Linux; run `avr sync` to bring your changes back.\n")
		// Named when the copy is made rather than on every session in it: this
		// is the moment the user decides whether the copy is what they wanted,
		// and repeating it before every command would be noise they learn to
		// scroll past. `avr sync` says it again every time it is asked.
		reportSkipped(app.Err, scan.Skipped)
	}
	if err := nw.ApplyNativeWorkspace(ctx, target.MachineName, ws, sync, progress); err != nil {
		return "", err
	}
	return guestCwd, nil
}

// nativeWorkspacer asserts the capability and explains its absence in the one
// sentence a user can act on.
//
// The command layer degrades rather than assuming: a backend that shares a
// project at native speed has nothing to gain from a second copy, and telling
// the user that is a better answer than a type assertion panicking or a flag
// silently doing nothing.
func nativeWorkspacer(p provider.Provider) (provider.NativeWorkspacer, error) {
	nw, ok := p.(provider.NativeWorkspacer)
	if !ok {
		return nil, fmt.Errorf("this environment reaches your project directly rather than across a filesystem boundary, so it has no Linux-native workspace to keep; run avr without --native-fs")
	}
	return nw, nil
}

// changesFor is the plan's work in one direction.
func changesFor(plan workspace.Plan, direction types.WorkspaceDirection) []workspace.Change {
	if direction == types.ToHost {
		return plan.ToHost
	}
	return plan.ToGuest
}

// destinationOf names the copy a direction writes to, in the words the user
// thinks in rather than as a path they did not choose.
func destinationOf(ws types.NativeWorkspace, direction types.WorkspaceDirection) string {
	if direction == types.ToHost {
		return ws.HostPath
	}
	return ws.Path
}

// reportReview prints what each direction would do and applies nothing, which
// is the whole of what a bare `avr sync` is for (REQ-14.2).
func reportReview(w io.Writer, ws types.NativeWorkspace, plan workspace.Plan) {
	fmt.Fprintf(w, "%s\n", ws.HostPath)
	fmt.Fprintf(w, "  Linux-native copy: %s\n", ws.Path)
	fmt.Fprintln(w)

	if plan.Empty() {
		fmt.Fprintln(w, "The two copies are synchronized. Nothing to do.")
		return
	}

	if len(plan.ToHost) > 0 {
		reportDirection(w, ws, types.ToHost, plan.ToHost)
	}
	if len(plan.ToGuest) > 0 {
		reportDirection(w, ws, types.ToGuest, plan.ToGuest)
	}
	fmt.Fprintln(w, "Nothing has been changed. Run `avr sync --to-host` or `avr sync --to-guest` to apply one of these.")
}

// reportDirection lists one direction's changes.
func reportDirection(w io.Writer, ws types.NativeWorkspace, direction types.WorkspaceDirection, changes []workspace.Change) {
	fmt.Fprintf(w, "%s would be changed in %s:\n",
		countLabel(len(changes), "file", "files"), destinationOf(ws, direction))
	for i, change := range changes {
		if i == listingLimit {
			fmt.Fprintf(w, "  … and %d more\n", len(changes)-listingLimit)
			break
		}
		fmt.Fprintf(w, "  %-9s %s\n", change.Kind, change.Path)
	}
	fmt.Fprintln(w)
}

// reportConflicts says which files both copies changed, and stops.
//
// It never suggests a way to make the conflict go away by picking a side. avar
// does not know which version the user wants, and a command that offered to
// choose would be the silent overwrite REQ-14.3 exists to forbid, one
// keystroke away.
func reportConflicts(w io.Writer, ws types.NativeWorkspace, conflicts []workspace.Conflict) {
	fmt.Fprintf(w, "avr: %s changed in both copies of %s since they were last synchronized:\n",
		countLabel(len(conflicts), "file has", "files have"), ws.HostPath)
	for i, conflict := range conflicts {
		if i == listingLimit {
			fmt.Fprintf(w, "       … and %d more\n", len(conflicts)-listingLimit)
			break
		}
		fmt.Fprintf(w, "       %s: %s on the host, %s in Linux\n", conflict.Path, conflict.Host, conflict.Guest)
	}
	fmt.Fprintf(w, "     avar will not overwrite either copy, so nothing has been changed.\n")
	fmt.Fprintf(w, "     The host copy is at %s and the Linux copy is at %s inside the environment.\n", ws.HostPath, ws.Path)
	fmt.Fprintf(w, "     Make the two agree — or delete one side's version — then run `avr sync` again.\n")
}

// reportSkipped names what avar will not carry, because a file the user
// believes is being synchronized and is not is this feature's worst failure.
func reportSkipped(w io.Writer, skipped []string) {
	if len(skipped) == 0 {
		return
	}
	fmt.Fprintf(w, "avr: %s not synchronized, because %s not a regular file: %s\n",
		countLabel(len(skipped), "entry is", "entries are"),
		pluralize(len(skipped), "it is", "they are"),
		strings.Join(truncate(skipped, 10), ", "))
}

// truncate shortens a list for a one-line message, saying how much it left out.
func truncate(items []string, limit int) []string {
	if len(items) <= limit {
		return items
	}
	return append(append([]string(nil), items[:limit]...), fmt.Sprintf("and %d more", len(items)-limit))
}
