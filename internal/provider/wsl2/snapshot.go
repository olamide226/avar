package wsl2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// A snapshot on this backend is a copy of the distribution's virtual disk.
//
// WSL exports a distribution as a tar or as a VHD, and avar takes the VHD. A tar
// is a copy of the files; a VHD is a copy of the filesystem, which means it comes
// back with its permissions, its symbolic links, its sparse files and its
// extended attributes intact. Restoring from a tar would quietly lose all of
// that, and a restored environment that is subtly not the one that was captured
// is worse than no snapshot at all (REQ-10.1, REQ-10.2, REQ-18.12).
//
// Restoring is unregister-then-import, because WSL has no in-place restore.
// That is destructive by construction, so the order matters: the replacement is
// checked for before anything is removed, and the export is copied rather than
// moved by --import, so the snapshot survives being restored from and can be
// restored from again. Host project files are never touched either way — a
// project is shared into the guest, never copied into it (PROP-10, PROP-16).
//
// Note on the flags. design §3.6 records the export as `--export <name> <file>
// --vhd`; the real tool takes `--format vhd` for an export and reserves `--vhd`
// for an import, and this uses what the tool takes. Verified against WSL 2.7.12.

// snapshotExt is the extension of the exported disk. WSL chooses the format from
// the --format flag rather than from the name, but a file called .vhdx is one
// Windows itself can mount and inspect, which is worth more than a private
// extension.
const snapshotExt = ".vhdx"

// snapshotMetaExt is the sidecar recording what a snapshot is of and when it was
// taken.
//
// The timestamp is stored rather than read from the file's modification time,
// which anything from a backup agent to a virus scanner may rewrite. A snapshot
// list whose order changes because something touched a file would make `avr
// restore` a guess.
const snapshotMetaExt = ".json"

// snapshotNamePattern constrains a snapshot name.
//
// The name becomes a file name, so it is validated rather than escaped: a name
// containing a separator, a colon or a leading dot is refused outright, because
// a "snapshot" called `..\..\machines.json` must never become a path avar writes
// to. Refusing is also the better user experience — a name silently rewritten is
// a name the user cannot use to restore.
var snapshotNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// snapshotMeta is what avar records beside an exported disk.
type snapshotMeta struct {
	Name      string                    `json:"name"`
	Machine   string                    `json:"machine"`
	Provider  types.ProviderID          `json:"provider"`
	Selector  types.EnvironmentSelector `json:"selector"`
	CreatedAt time.Time                 `json:"created_at"`
}

// Snapshot captures the environment's current state under name.
//
// WSL cannot export a running distribution consistently, so a running one is
// terminated first and left in the state it was found in — reported through
// progress, because an otherwise instant command that pauses somebody's shell
// has to say why (REQ-10.1).
func (p *Provider) Snapshot(ctx context.Context, machine, name string, progress types.ProgressSink) error {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return err
	}
	if err := validateSnapshotName(name); err != nil {
		return err
	}
	if progress == nil {
		progress = types.DiscardProgress
	}

	d, err := p.view().require(ctx, machine)
	if err != nil {
		return err
	}
	if d.WSLVersion == 1 {
		return newWSL1Error(machine)
	}

	path := p.snapshotPath(machine, name)
	if _, err := os.Stat(path); err == nil {
		// Whether to replace a snapshot is the caller's business, not the
		// backend's: `avr snapshot` can ask, and a backend that overwrote
		// silently would leave it nothing to ask about.
		return fmt.Errorf("environment %s already has a snapshot called %q", machine, name)
	}
	if err := os.MkdirAll(p.snapshotDir(machine), dirPerm); err != nil {
		return fmt.Errorf("creating the snapshot directory for environment %s: %w", machine, err)
	}

	if d.Running {
		progress.Progress(types.ProgressEvent{
			Kind:    types.ProgressStopping,
			Machine: machine,
			Message: "pausing to capture a consistent snapshot",
		})
		if err := p.terminate(ctx, machine); err != nil {
			return err
		}
	}

	if _, err := p.run(ctx, "--export", machine, path, "--format", "vhd"); err != nil {
		// A partial export is a file that looks like a snapshot and is not.
		_ = os.Remove(path)
		return fmt.Errorf("capturing a snapshot of environment %s: %w", machine, err)
	}

	if err := p.writeSnapshotMeta(machine, name); err != nil {
		_ = os.Remove(path)
		return err
	}

	if d.Running {
		if err := p.start(ctx, machine); err != nil {
			return err
		}
	}
	return nil
}

// RestoreSnapshot returns the environment to the state captured under name.
//
// WSL has no in-place restore, so this unregisters the distribution and imports
// the snapshot in its place. Everything done in the guest since the snapshot is
// destroyed, which is what a restore is; host project files are untouched,
// because they were shared into the guest and never copied into it (REQ-10.2,
// PROP-10, PROP-16).
//
// The snapshot is read before anything is destroyed. An unknown name costs
// nothing and leaves the environment exactly as it was, so the caller can list
// what does exist instead of the user discovering their environment is gone.
func (p *Provider) RestoreSnapshot(ctx context.Context, machine, name string, progress types.ProgressSink) error {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return err
	}
	if err := validateSnapshotName(name); err != nil {
		return err
	}
	if progress == nil {
		progress = types.DiscardProgress
	}

	path := p.snapshotPath(machine, name)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("%w: %s has no snapshot called %q", provider.ErrSnapshotNotFound, machine, name)
	}

	// The distribution is looked up but not required, which is what makes a
	// restore retryable and is the whole of avar's answer to REQ-18.12 here.
	// The obvious alternative — exporting a rollback disk first and reimporting
	// it on failure — is a full copy of the environment, gigabytes and minutes
	// on every restore, insuring against a failure whose recovery can fail the
	// same way. Not requiring the distribution to exist costs nothing and means
	// a restore interrupted after the unregister leaves a state that running
	// the same command again recovers from.
	d, found, err := p.view().lookup(ctx, machine)
	if err != nil {
		return err
	}
	if found && d.WSLVersion == 1 {
		return newWSL1Error(machine)
	}

	progress.Progress(types.ProgressEvent{
		Kind:    types.ProgressStopping,
		Machine: machine,
		Message: fmt.Sprintf("restoring %s", name),
	})

	if found {
		if _, err := p.run(ctx, "--unregister", machine); err != nil {
			return fmt.Errorf("restoring environment %s: removing the current one: %w", machine, err)
		}
		p.forget()
	}
	// `wsl --unregister` deletes the root filesystem but leaves the directory,
	// and an import into a directory that still holds a disk fails. Clearing it
	// is also what makes the retry path work after an interrupted restore.
	if err := os.RemoveAll(p.installDir(machine)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("restoring environment %s: clearing %s: %w", machine, p.installDir(machine), err)
	}

	// --import with --vhd copies the disk into the install location rather than
	// registering the file where it lies, which is what lets the same snapshot
	// be restored from again tomorrow.
	_, err = p.run(ctx, "--import", machine, p.installDir(machine), path, "--vhd")
	p.forget()
	if err != nil {
		return fmt.Errorf("restoring environment %s from snapshot %q: %w; the snapshot is still at %s, so running the same command again retries the restore",
			machine, name, err, path)
	}

	// A restored disk is verified rather than assumed to be what it was when it
	// was captured. Provisioning refuses to record an environment that does not
	// carry avar's marker, lacks the account, or still has the Windows drives
	// mounted; a restore that skipped those checks would be the one way an
	// environment reaches a user without them ever having been asserted. The
	// release is deliberately not checked — a snapshot holds the release it
	// held, and refusing that would refuse the restore for doing its job.
	facts, err := p.readGuestFacts(ctx, machine)
	if err != nil {
		return fmt.Errorf("restoring environment %s: checking the restored environment: %w", machine, err)
	}
	if err := facts.checkOwnedAndConfined(machine, p.guestUser); err != nil {
		return fmt.Errorf("restoring environment %s: %w; the snapshot is still at %s", machine, err, path)
	}

	// Asking the guest anything started it, so the running state is set
	// explicitly rather than left wherever the check happened to leave it.
	//
	// On the retry path — a restore interrupted after the unregister, so found
	// is false — d is the zero distribution and the environment is left
	// stopped, whatever the pre-restore one had been doing. That is deliberate:
	// nothing survived the interruption to say which it was, and EnsureMachine
	// starts it on the next command anyway.
	if d.Running {
		return p.start(ctx, machine)
	}
	return p.terminate(ctx, machine)
}

// ListSnapshots reports the snapshots held for the environment, newest last.
//
// A disk with no readable metadata beside it is listed with a zero time rather
// than hidden: it is a file the user can restore from and would be alarmed not
// to see, and the sidecar is avar's bookkeeping rather than the snapshot itself.
func (p *Provider) ListSnapshots(ctx context.Context, machine string) ([]provider.SnapshotInfo, error) {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(p.snapshotDir(machine))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing the snapshots of environment %s: %w", machine, err)
	}

	out := make([]provider.SnapshotInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), snapshotExt) {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), snapshotExt)
		out = append(out, provider.SnapshotInfo{Name: name, CreatedAt: p.snapshotTime(machine, name)})
	}

	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// writeSnapshotMeta records what the snapshot is of.
func (p *Provider) writeSnapshotMeta(machine, name string) error {
	selector := types.EnvironmentSelector{}
	if p.records != nil {
		if rec, ok, err := p.records.Machine(machine); err == nil && ok {
			selector = rec.Selector
		}
	}

	body, err := json.MarshalIndent(snapshotMeta{
		Name:      name,
		Machine:   machine,
		Provider:  p.ID(),
		Selector:  selector,
		CreatedAt: time.Now().UTC(),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("recording what snapshot %q of environment %s is: %w", name, machine, err)
	}
	if err := os.WriteFile(p.snapshotMetaPath(machine, name), append(body, '\n'), filePerm); err != nil {
		return fmt.Errorf("recording what snapshot %q of environment %s is: %w", name, machine, err)
	}
	return nil
}

// snapshotTime reads a snapshot's recorded capture time, or the zero time.
func (p *Provider) snapshotTime(machine, name string) time.Time {
	body, err := os.ReadFile(p.snapshotMetaPath(machine, name))
	if err != nil {
		return time.Time{}
	}
	var meta snapshotMeta
	if err := json.Unmarshal(body, &meta); err != nil {
		return time.Time{}
	}
	return meta.CreatedAt
}

func (p *Provider) snapshotDir(machine string) string {
	return filepath.Join(p.snapshotsDir, machine)
}

func (p *Provider) snapshotPath(machine, name string) string {
	return filepath.Join(p.snapshotDir(machine), name+snapshotExt)
}

func (p *Provider) snapshotMetaPath(machine, name string) string {
	return filepath.Join(p.snapshotDir(machine), name+snapshotMetaExt)
}

// validateSnapshotName refuses a name avar must not turn into a file path.
func validateSnapshotName(name string) error {
	if !snapshotNamePattern.MatchString(name) {
		return fmt.Errorf("%q is not a usable snapshot name: use letters, digits, dots, dashes and underscores, starting with a letter or digit", name)
	}
	return nil
}
