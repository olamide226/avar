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

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// Result reports what Ensure did or why it stopped.
type Result struct {
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
func Ensure(ctx context.Context, p provider.Provider, machine string, mount types.MountSpec, guestCwd string, sessions int, progress types.ProgressSink) (Result, error) {
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
	if err := p.SetMounts(ctx, machine, desired, progress); err != nil {
		return Result{}, err
	}

	// Prove the share landed before handing the user a shell. Dropping
	// someone into an empty path is worse than failing outright (REQ-6.5).
	if err := verifyGuestPath(ctx, p, machine, mount); err != nil {
		return Result{}, err
	}

	return Result{GuestCwd: guestCwd}, nil
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
