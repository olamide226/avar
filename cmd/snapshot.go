package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/resolve"
)

func init() {
	registerSubcommand("snapshot", runSnapshot)
	registerSubcommand("restore", runRestore)
}

// runSnapshot captures a named snapshot of the current environment, or lists
// existing ones when called with no name (REQ-10.1, REQ-10.4).
//
//	avr snapshot           # list snapshots
//	avr snapshot <name>    # capture a snapshot
func runSnapshot(ctx context.Context, app *App, inv cli.Invocation) error {
	switch len(inv.SubcommandArgs) {
	case 0:
		return listSnapshots(ctx, app, inv)
	case 1:
		return captureSnapshot(ctx, app, inv, inv.SubcommandArgs[0])
	default:
		return Exit(exitUsage, fmt.Errorf("`avr snapshot` takes at most one argument (the snapshot name), but got %d: %q",
			len(inv.SubcommandArgs), strings.Join(inv.SubcommandArgs, " ")))
	}
}

// listSnapshots shows the snapshots held for the current environment, with
// their creation timestamps (REQ-10.4).
func listSnapshots(ctx context.Context, app *App, inv cli.Invocation) error {
	snapter, target, err := resolveSnapshotTarget(ctx, app, inv)
	if err != nil {
		return err
	}

	snaps, err := snapter.ListSnapshots(ctx, target.MachineName)
	if err != nil {
		return err
	}

	if len(snaps) == 0 {
		fmt.Fprintf(app.Out, "No snapshots for %s yet.\n", target.Selector.Label())
		fmt.Fprintln(app.Out)
		fmt.Fprintf(app.Out, "Run `avr snapshot <name>` to capture one. A snapshot preserves the installed packages and files inside your Linux environment so you can restore it later with `avr restore <name>`.\n")
		return nil
	}

	fmt.Fprintf(app.Out, "%s for %s:\n", countLabel(len(snaps), "snapshot", "snapshots"), target.Selector.Label())
	for _, snap := range snaps {
		fmt.Fprintf(app.Out, "  %s  %s\n", snap.Name, snap.CreatedAt.Format(time.RFC3339))
	}
	return nil
}

// captureSnapshot creates a named snapshot of the current environment
// (REQ-10.1).
func captureSnapshot(ctx context.Context, app *App, inv cli.Invocation, name string) error {
	snapter, target, err := resolveSnapshotTarget(ctx, app, inv)
	if err != nil {
		return err
	}

	fmt.Fprintf(app.Err, "Capturing snapshot %q of %s…\n", name, target.Selector.Label())
	if err := snapter.Snapshot(ctx, target.MachineName, name, progressTo(app.Err)); err != nil {
		return err
	}

	fmt.Fprintf(app.Out, "Captured snapshot %q of %s.\n", name, target.Selector.Label())
	fmt.Fprintln(app.Out)
	fmt.Fprintf(app.Out, "Restore it with: avr restore %s\n", name)
	return nil
}

// runRestore returns the current environment to a previously captured snapshot
// (REQ-10.2).
//
//	avr restore <name>    # restore a snapshot
func runRestore(ctx context.Context, app *App, inv cli.Invocation) error {
	if len(inv.SubcommandArgs) == 0 {
		return Exit(exitUsage, fmt.Errorf("`avr restore` needs the name of a snapshot to restore; run `avr snapshot` to see the ones available for this environment"))
	}
	if len(inv.SubcommandArgs) > 1 {
		return Exit(exitUsage, fmt.Errorf("`avr restore` takes exactly one argument (the snapshot name), but got %d: %q",
			len(inv.SubcommandArgs), strings.Join(inv.SubcommandArgs, " ")))
	}

	name := inv.SubcommandArgs[0]
	snapter, target, err := resolveSnapshotTarget(ctx, app, inv)
	if err != nil {
		return err
	}

	fmt.Fprintf(app.Err, "Restoring %s to snapshot %q…\n", target.Selector.Label(), name)
	if err := snapter.RestoreSnapshot(ctx, target.MachineName, name, progressTo(app.Err)); err != nil {
		if errors.Is(err, provider.ErrSnapshotNotFound) {
			return suggestAvailableSnapshots(ctx, err, target, snapter)
		}
		return err
	}

	fmt.Fprintf(app.Out, "Restored %s to snapshot %q.\n", target.Selector.Label(), name)
	return nil
}

// suggestAvailableSnapshots lists the available snapshots after a failed
// restore so the user can pick one without running a second command
// (REQ-10.2).
func suggestAvailableSnapshots(ctx context.Context, err error, target resolve.ResolvedTarget, snapter provider.Snapshotter) error {
	snaps, listErr := snapter.ListSnapshots(ctx, target.MachineName)
	if listErr != nil {
		return fmt.Errorf("%w; listing the available snapshots also failed: %v", err, listErr)
	}
	if len(snaps) == 0 {
		return fmt.Errorf("%w; there are no snapshots at all for %s", err, target.Selector.Label())
	}
	names := make([]string, 0, len(snaps))
	for _, snap := range snaps {
		names = append(names, snap.Name)
	}
	return fmt.Errorf("%w\nAvailable snapshots for %s: %s", err, target.Selector.Label(), strings.Join(names, ", "))
}

// resolveSnapshotTarget obtains the provider and resolves the target machine,
// asserting that the provider supports snapshots. Both the snapshot and
// restore commands start from the same place.
func resolveSnapshotTarget(ctx context.Context, app *App, inv cli.Invocation) (provider.Snapshotter, resolve.ResolvedTarget, error) {
	p, err := app.Provider(ctx)
	if err != nil {
		return nil, resolve.ResolvedTarget{}, err
	}

	snapter, ok := p.(provider.Snapshotter)
	if !ok {
		return nil, resolve.ResolvedTarget{}, fmt.Errorf("the %s backend does not support snapshots; snapshots are a feature of local virtualisation backends", p.ID())
	}

	target, err := app.Resolve(inv)
	if err != nil {
		return nil, resolve.ResolvedTarget{}, err
	}

	return snapter, target, nil
}
