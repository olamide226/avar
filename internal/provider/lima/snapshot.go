package lima

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// The Provider implements provider.Snapshotter so that the command layer can
// offer snapshots and restore without knowing which backend it is talking to
// (REQ-17.3).
var _ provider.Snapshotter = (*Provider)(nil)

// snapshotTag is the limactl flag that names a snapshot.
const snapshotTag = "--tag"

// Snapshot captures the machine's current state durably under name and returns
// once the snapshot is complete (REQ-10.1).
//
// Lima cannot snapshot a running instance, so this stops it, snapshots, and
// starts it again. Each transition goes through progress so the pause is
// explained; the machine is left in the state it was found in. A name that
// already exists is an error: replacing a snapshot is the caller's decision.
func (p *Provider) Snapshot(ctx context.Context, machine, name string, progress types.ProgressSink) error {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("capturing a snapshot of %s: a snapshot name is required", machine)
	}

	v := p.newView()
	inst, err := v.require(ctx, machine)
	if err != nil {
		return err
	}

	// Lima's snapshot create refuses a duplicate tag, but checking here first
	// saves a stop/start cycle and gives a clearer error.
	existing, err := p.listSnapshots(ctx, machine)
	if err != nil {
		return fmt.Errorf("capturing a snapshot of %s: checking whether the name is taken: %w", machine, err)
	}
	if hasSnapshot(existing, name) {
		return fmt.Errorf("capturing snapshot %q of %s: a snapshot with that name already exists; rename the existing one with your virtualization tool, or pick a different name", name, machine)
	}

	wasRunning := inst.running()
	if wasRunning {
		progress.Progress(types.ProgressEvent{
			Kind:    types.ProgressStopping,
			Machine: machine,
			Message: fmt.Sprintf("Pausing %s to capture a snapshot", machine),
		})
		if err := p.stopMachine(ctx, machine, progress); err != nil {
			return fmt.Errorf("capturing a snapshot of %s: stopping the machine first: %w", machine, err)
		}
	}

	progress.Progress(types.ProgressEvent{
		Kind:    types.ProgressCreating,
		Machine: machine,
		Message: fmt.Sprintf("Capturing snapshot %q of %s", name, machine),
	})
	if _, err := p.run(ctx, "snapshot", "create", machine, snapshotTag, name); err != nil {
		// The snapshot failed, but the machine was stopped. Start it again so
		// the user is not left with a silent environment.
		if wasRunning {
			_ = p.startAfterSnapshotOp(ctx, machine)
		}
		return fmt.Errorf("capturing snapshot %q of %s: %w", name, machine, err)
	}

	if wasRunning {
		progress.Progress(types.ProgressEvent{
			Kind:    types.ProgressStarting,
			Machine: machine,
			Message: fmt.Sprintf("Resuming %s", machine),
		})
		if _, err := p.run(ctx, "start", "--tty=false", machine); err != nil {
			return fmt.Errorf("capturing snapshot %q of %s: the snapshot was saved but the machine would not restart: %w; start it yourself with `limactl start %s`",
				name, machine, err, machine)
		}
	}

	return nil
}

// startAfterSnapshotOp restarts a machine after a failed snapshot or restore,
// on a detached context because the caller's context may already be cancelled.
func (p *Provider) startAfterSnapshotOp(ctx context.Context, machine string) error {
	ctx, cancel := detach(ctx)
	defer cancel()
	_, err := p.run(ctx, "start", "--tty=false", machine)
	return err
}

// RestoreSnapshot returns the machine to the state captured under name
// (REQ-10.2).
//
// An unknown name is ErrSnapshotNotFound with no side effects. Work done in
// the guest since the snapshot is destroyed; host project files are not
// affected because they are shared and never copied (REQ-10.3, PROP-10).
func (p *Provider) RestoreSnapshot(ctx context.Context, machine, name string, progress types.ProgressSink) error {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("restoring a snapshot of %s: a snapshot name is required", machine)
	}

	v := p.newView()
	inst, err := v.require(ctx, machine)
	if err != nil {
		return err
	}

	// Check the snapshot exists before stopping — failing early is cheaper.
	existing, err := p.listSnapshots(ctx, machine)
	if err != nil {
		return fmt.Errorf("restoring a snapshot of %s: checking the available snapshots: %w", machine, err)
	}
	if !hasSnapshot(existing, name) {
		return fmt.Errorf("%w: %q on %s", provider.ErrSnapshotNotFound, name, machine)
	}

	wasRunning := inst.running()
	if wasRunning {
		progress.Progress(types.ProgressEvent{
			Kind:    types.ProgressStopping,
			Machine: machine,
			Message: fmt.Sprintf("Pausing %s to restore snapshot %q", machine, name),
		})
		if err := p.stopMachine(ctx, machine, progress); err != nil {
			return fmt.Errorf("restoring snapshot %q of %s: stopping the machine first: %w", name, machine, err)
		}
	}

	progress.Progress(types.ProgressEvent{
		Kind:    types.ProgressCreating,
		Machine: machine,
		Message: fmt.Sprintf("Restoring snapshot %q of %s", name, machine),
	})
	if _, err := p.run(ctx, "snapshot", "apply", machine, snapshotTag, name); err != nil {
		if wasRunning {
			_ = p.startAfterSnapshotOp(ctx, machine)
		}
		return fmt.Errorf("restoring snapshot %q of %s: %w", name, machine, err)
	}

	if wasRunning {
		progress.Progress(types.ProgressEvent{
			Kind:    types.ProgressStarting,
			Machine: machine,
			Message: fmt.Sprintf("Resuming %s after restoring snapshot %q", machine, name),
		})
		if _, err := p.run(ctx, "start", "--tty=false", machine); err != nil {
			return fmt.Errorf("restoring snapshot %q of %s: the snapshot was restored but the machine would not restart: %w; start it yourself with `limactl start %s`",
				name, machine, err, machine)
		}
	}

	return nil
}

// ListSnapshots reports the snapshots held for the machine, oldest last, with
// the timestamps `avr snapshot` displays (REQ-10.4). It is a read-only query.
func (p *Provider) ListSnapshots(ctx context.Context, machine string) ([]provider.SnapshotInfo, error) {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return nil, err
	}
	if _, err := p.newView().require(ctx, machine); err != nil {
		return nil, err
	}
	return p.listSnapshots(ctx, machine)
}

// listSnapshots queries lima without the ownership guard, so that Snapshot and
// RestoreSnapshot can use it without a second gate call.
func (p *Provider) listSnapshots(ctx context.Context, machine string) ([]provider.SnapshotInfo, error) {
	out, err := p.run(ctx, "snapshot", "list", machine)
	if err != nil {
		return nil, fmt.Errorf("listing snapshots of %s: %w", machine, err)
	}
	return parseSnapshotList(out)
}

// parseSnapshotList reads the output of `limactl snapshot list <machine>`.
//
// Lima writes one snapshot per line as "NAME\tCREATED" (tab-separated) with a
// header row. This parser skips the header and any blank line. On a parse
// failure it returns what it could parse plus the error, so a single
// unparseable line does not hide the rest.
func parseSnapshotList(out []byte) ([]provider.SnapshotInfo, error) {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	var (
		snaps     []provider.SnapshotInfo
		parseErrs []error
	)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		// Skip the header row by name: a header line's first field is "NAME"
		// and the second is "CREATED".
		if fields[0] == "NAME" && fields[1] == "CREATED" {
			continue
		}

		name := strings.TrimSpace(fields[0])
		timestamp := strings.TrimSpace(fields[1])
		if name == "" {
			continue
		}

		created, err := parseSnapshotTime(timestamp)
		if err != nil {
			parseErrs = append(parseErrs, fmt.Errorf("snapshot %q: %w", name, err))
			continue
		}
		snaps = append(snaps, provider.SnapshotInfo{Name: name, CreatedAt: created})
	}

	sort.Slice(snaps, func(i, j int) bool { return snaps[i].CreatedAt.Before(snaps[j].CreatedAt) })
	return snaps, errors.Join(parseErrs...)
}

// parseSnapshotTime parses the timestamp Lima writes for a snapshot.
//
// Lima uses the format go's time.Time.String() produces, which is
// "2006-01-02 15:04:05.999999999 -0700 MST". The nanosecond field may be
// absent and the offset is always present.
func parseSnapshotTime(s string) (time.Time, error) {
	// Try the full format first, then fall back to one without sub-second
	// precision.
	formats := []string{
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 MST",
		time.RFC3339,
	}
	for _, layout := range formats {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse %q as a snapshot timestamp", s)
}

// hasSnapshot reports whether snaps contains one called name.
func hasSnapshot(snaps []provider.SnapshotInfo, name string) bool {
	return slices.ContainsFunc(snaps, func(s provider.SnapshotInfo) bool { return s.Name == name })
}
