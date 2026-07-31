// Package mounts owns the decision of whether a project directory can appear
// inside a machine, what needs to happen to make it appear, and when doing so
// would interrupt other sessions.
//
// It is the component the command layer calls between ensuring the machine runs
// and attaching a shell to it. It is the only place that reads applied mounts,
// decides whether a restart is needed, and decides whether the restart is safe.
//
// It returns values and errors and never prints. User-visible output, including
// the live-session restart prompt, belongs to cmd/.
package mounts

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// MaxMounts caps how many project directories one machine shares at once.
//
// macOS's Virtualization.framework limits how many directory-share devices a
// virtual machine may have, and Lima uses one per mount plus one for the
// Rosetta share. Measured against Lima 2.2.0: nineteen project mounts start,
// twenty do not. The failure is a bare "Internal Virtualization error" during
// boot with nothing in it about directories at all, so a user who crossed the
// line would have no way to connect it to the project they had just entered —
// and no way back, because the machine cannot start to be repaired.
//
// Sixteen leaves headroom for the Rosetta share and for any device Lima adds
// in a later version. Past it the least recently used project is unshared. It
// stays a registered project and reappears the next time it is entered, paying
// the same one-time restart a project pays on its first visit.
const MaxMounts = 16

// Result reports what Ensure did or why it stopped.
type Result struct {
	// Unshared names the project directories dropped to stay within
	// MaxMounts, so the caller can say what happened. Empty in the ordinary
	// case.
	Unshared []string

	// SessionConflict is true when a mount change would restart a machine
	// that has other live avr sessions. The mounts were not modified — the
	// caller must either prompt the user (interactive) or abort.
	SessionConflict bool

	// SessionCount is the number of other live sessions on the machine. It
	// is set only when SessionConflict is true.
	SessionCount int

	// GuestCwd is the working directory for Shell, passed through unchanged.
	GuestCwd string
}

// Ensure makes the project directory visible inside the machine at the guest
// path the backend planned for it, adding it to whatever the machine already
// shares.
//
// When the project is already shared, Ensure returns immediately without
// changing anything — the warm path stays instant (REQ-17.1).
//
// sessions is the number of other live avr sessions on this machine. When it
// is > 0 and the mount has not yet been applied, the operation is refused
// rather than restarting the machine under live sessions (REQ-6.4). The
// Result reports the conflict so the caller can prompt the user and retry.
//
// mount is the mapping MapProjectPath returned for the project root directory.
// guestCwd is the directory Shell should start in, passed through unchanged.
//
// lastUsed maps a host project path to when it was last entered, and decides
// which projects are dropped when the machine is at MaxMounts. A path missing
// from it sorts oldest, which is the safe direction: a share avar has no
// record of having used is the one it should give up first.
func Ensure(ctx context.Context, p provider.Provider, machine string, mount types.MountSpec, guestCwd string, sessions int, lastUsed map[string]time.Time, progress types.ProgressSink) (Result, error) {
	applied, err := p.AppliedMounts(ctx, machine)
	if err != nil {
		return Result{}, fmt.Errorf("checking which directories are shared with your Linux environment: %w", err)
	}

	for _, m := range applied {
		if m.HostPath == mount.HostPath {
			// Already shared — the warm path. Nothing to change, so
			// nothing to restart and nothing to say (REQ-17.1).
			return Result{GuestCwd: guestCwd}, nil
		}
	}

	// The project is not yet shared. A restart is about to happen, and if
	// other sessions are attached to the machine that restart would
	// disconnect them. Stop here rather than surprise those sessions with
	// a lost terminal (REQ-6.4).
	if sessions > 0 {
		return Result{SessionConflict: true, SessionCount: sessions, GuestCwd: guestCwd}, nil
	}

	// Apply the complete desired set — existing mounts plus the new project.
	// Replace-not-append is what makes mount confinement checkable: the
	// guest can reach exactly the project roots registered to the machine,
	// at exactly the paths avar planned, and nothing else (PROP-5).
	desired := append([]types.MountSpec(nil), applied...)
	desired = append(desired, mount)

	// A machine that shares too many directories cannot be started at all, so
	// the set is capped here rather than discovered at boot (see MaxMounts).
	desired, unshared := capMounts(desired, mount.HostPath, lastUsed)

	if err := p.SetMounts(ctx, machine, desired, progress); err != nil {
		return Result{}, err
	}

	// Prove the share landed before handing the user a shell. Dropping
	// someone into an empty path is worse than failing outright (REQ-6.5).
	if err := verifyGuestPath(ctx, p, machine, mount); err != nil {
		return Result{}, err
	}

	return Result{GuestCwd: guestCwd, Unshared: unshared}, nil
}

// capMounts reduces a desired mount set to MaxMounts by dropping the least
// recently used projects, and reports which it dropped.
//
// keep is the project being entered right now and is never dropped: unsharing
// the directory the user is standing in would hand them a shell at a path that
// does not exist, which is the failure REQ-6.5 exists to prevent.
func capMounts(desired []types.MountSpec, keep string, lastUsed map[string]time.Time) ([]types.MountSpec, []string) {
	if len(desired) <= MaxMounts {
		return desired, nil
	}

	// Most recently used first, so the tail is what goes. The project being
	// entered sorts first unconditionally.
	order := append([]types.MountSpec(nil), desired...)
	slices.SortStableFunc(order, func(a, b types.MountSpec) int {
		switch {
		case a.HostPath == keep:
			return -1
		case b.HostPath == keep:
			return 1
		}
		return lastUsed[b.HostPath].Compare(lastUsed[a.HostPath])
	})

	dropped := make([]string, 0, len(order)-MaxMounts)
	for _, m := range order[MaxMounts:] {
		dropped = append(dropped, m.HostPath)
	}

	// Return the survivors in their original order so that an unchanged set
	// stays byte-identical and SetMounts still sees no change to make.
	kept := make([]types.MountSpec, 0, MaxMounts)
	for _, m := range desired {
		if !slices.Contains(dropped, m.HostPath) {
			kept = append(kept, m)
		}
	}
	slices.Sort(dropped)
	return kept, dropped
}

// verifyGuestPath checks in the guest that the directory is present at the
// planned guest path. The probe's output never reaches the user's terminal:
// ShellOpts.Stdout/Stderr redirect it into buffers discarded on success
// (REQ-6.5).
func verifyGuestPath(ctx context.Context, p provider.Provider, machine string, mount types.MountSpec) error {
	var stdout, stderr bytes.Buffer
	code, err := p.Shell(ctx, machine, provider.ShellOpts{
		Workdir: "/",
		Argv:    []string{"test", "-d", mount.GuestPath},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if err != nil {
		return fmt.Errorf("checking that %s is available in your Linux environment at %s: %w",
			mount.HostPath, mount.GuestPath, err)
	}
	if code != 0 {
		return fmt.Errorf("checking that %s is available in your Linux environment at %s: it is not a directory there; "+
			"macOS will not share a network volume or an external disk into a virtual machine — "+
			"move the project onto the internal disk, or run avr from a directory that is on it",
			mount.HostPath, mount.GuestPath)
	}
	return nil
}
