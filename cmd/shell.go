package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/envpolicy"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/resolve"
	"github.com/olamide226/avar/internal/types"
)

func init() { registerGuest(runGuest) }

// runGuest is `avr` and `avr <command>`: the whole product in one path.
//
// The two differ only in whether there is an argv, so they share everything —
// resolving the environment, creating it if this is the first time, making the
// project visible inside it, and attaching. Splitting them would mean two
// chances for the shell and the one-shot path to drift apart.
func runGuest(ctx context.Context, app *App, inv cli.Invocation) error {
	target, err := app.Resolve(inv)
	if err != nil {
		return err
	}

	p, err := app.Provider(ctx)
	if err != nil {
		return err
	}

	// Provisioning is the one slow thing a user waits through, and the only
	// part of this path that may be interrupted. An interactive session must
	// not be: once the guest holds the terminal, Ctrl-C belongs to it
	// (REQ-3.3), which is why the disposition is installed here and removed
	// before attaching rather than in main.
	setupCtx, stopSetup := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)

	guestCwd, err := prepareEnvironment(setupCtx, app, p, target, progressTo(app.Err))
	stopSetup()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			// The conventional status for a command the user interrupted,
			// so a script sees the same thing it would from any other.
			return Exit(130, nil)
		}
		return err
	}

	code, err := p.Shell(ctx, target.MachineName, provider.ShellOpts{
		Workdir: guestCwd,
		Argv:    inv.Guest,
		Env:     envpolicy.Compose(envpolicy.Input{Host: envpolicy.HostEnviron()}),
		TTY:     stdinIsTerminal(),
	})
	if err != nil {
		return err
	}
	if code != 0 {
		// The guest already said whatever it had to say on its own stderr.
		// avar adds nothing and simply carries the status out (REQ-2.2).
		return Exit(code, nil)
	}
	return nil
}

// prepareEnvironment brings the target machine up and makes the project
// visible inside it, returning the mount and the directory the guest should
// start in.
func prepareEnvironment(ctx context.Context, app *App, p provider.Provider, target resolve.ResolvedTarget, progress types.ProgressSink) (string, error) {
	mount, guestCwd, err := p.MapProjectPath(target.Project.ID, target.Project.Path, target.HostCwd)
	if err != nil {
		return "", err
	}

	if err := p.EnsureMachine(ctx, provider.MachineSpec{
		Name:     target.MachineName,
		Provider: target.Provider,
		Selector: target.Selector,
		Kind:     target.Kind,
		Mounts:   []types.MountSpec{mount},
	}, progress); err != nil {
		return "", err
	}

	// The record is written only now, because the backend has confirmed the
	// machine started (design §4). Until it exists avar treats the machine as
	// one it does not own, which is what makes an interrupted create safe to
	// clean up — and it is why this cannot be folded into EnsureMachine: the
	// provider is given read-only access to avar's records precisely so a
	// backend cannot invent ownership for itself.
	if err := recordMachine(app, target, mount); err != nil {
		return "", err
	}

	// Registering the project is task 9's subject; this is the smallest
	// correct version of it, kept here so the shell path works end to end.
	// Task 9 replaces it with internal/mounts, which also handles the
	// restart prompt when other sessions are attached (REQ-6.4).
	if err := ensureMounted(ctx, p, target.MachineName, mount, progress); err != nil {
		return "", err
	}

	return guestCwd, nil
}

// recordMachine writes avar's own record of a machine the backend has just
// confirmed is running.
func recordMachine(app *App, target resolve.ResolvedTarget, mount types.MountSpec) error {
	store, err := app.Store()
	if err != nil {
		return err
	}
	return store.PutMachine(types.MachineRecord{
		Name:      target.MachineName,
		Provider:  target.Provider,
		Selector:  target.Selector,
		Kind:      target.Kind,
		ProjectID: isolatedProjectID(target),
		Mounts:    []types.MountSpec{mount},
	})
}

// isolatedProjectID names the project an isolated machine serves, which the
// store requires and a shared machine must not carry.
func isolatedProjectID(target resolve.ResolvedTarget) string {
	if target.Kind == types.KindIsolated {
		return target.Project.ID
	}
	return ""
}

// ensureMounted adds the project to the machine's shares if it is not already
// there, leaving the shares it already had alone.
func ensureMounted(ctx context.Context, p provider.Provider, machine string, mount types.MountSpec, progress types.ProgressSink) error {
	applied, err := p.AppliedMounts(ctx, machine)
	if err != nil {
		return err
	}
	for _, m := range applied {
		if m.HostPath == mount.HostPath {
			return nil
		}
	}
	// SetMounts takes the complete desired set, so the existing shares are
	// carried over: passing only the new one would unshare every other
	// project registered to this machine.
	return p.SetMounts(ctx, machine, append(applied, mount), progress)
}

// stdinIsTerminal reports whether the guest should get a pseudo-terminal.
//
// The rule is the host's stdin, not the presence of a command: `avr` with no
// arguments in a pipeline is still not interactive, and `avr npm test` typed at
// a prompt still is (REQ-2.3, PROP-8).
func stdinIsTerminal() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}

// progressTo renders progress events for a human.
//
// Everything goes to stderr, never stdout: `avr <command> | consumer` must
// deliver the guest's output and nothing of avar's own, and a provisioning
// notice appearing in a pipeline would corrupt it (REQ-2.3).
func progressTo(w io.Writer) types.ProgressSink {
	return types.ProgressFunc(func(e types.ProgressEvent) {
		switch e.Kind {
		case types.ProgressLog:
			// Backend chatter, useful when something is wrong and noise
			// otherwise.
			return
		case types.ProgressWarning:
			fmt.Fprintf(w, "avr: %s\n", e.Message)
		default:
			fmt.Fprintf(w, "%s\n", e.Message)
		}
	})
}
