package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/envpolicy"
	"github.com/olamide226/avar/internal/mounts"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/resolve"
	"github.com/olamide226/avar/internal/session"
	"github.com/olamide226/avar/internal/types"
	"github.com/olamide226/avar/internal/workspace"
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

	// --env-file is parsed before any machine work so that a missing or
	// malformed file fails the invocation before taking a slow path
	// (REQ-12.2).
	envFile, err := loadEnvFile(inv.EnvFile)
	if err != nil {
		return fmt.Errorf("--env-file %s: %w", inv.EnvFile, err)
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

	// Record this process as a live session on the machine so that idle
	// auto-stop never shuts it down while somebody is using it (REQ-5.5,
	// Property 11). Detach is deferred rather than placed after Shell
	// because a panic in Shell must also clean up the session record.
	detach := attachSession(app, target.MachineName)
	defer detach()

	code, err := p.Shell(ctx, target.MachineName, provider.ShellOpts{
		Workdir: guestCwd,
		Argv:    inv.Guest,
		Env: envpolicy.Compose(envpolicy.Input{
			Host:      envpolicy.HostEnviron(),
			Forwarded: inv.Env,
			EnvFile:   envFile,
			Allowlist: forwardEnv(app),
		}),
		TTY:             stdinIsTerminal(),
		ForwardSSHAgent: inv.SSHAgent,
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

// attachSession records a live session on machine for this process and
// returns a cleanup function that detaches it.
func attachSession(app *App, machine string) func() {
	store, err := app.Store()
	if err != nil {
		return func() {}
	}
	pid := session.ThisPID()
	if err := session.Attach(store, machine, pid); err != nil {
		return func() {}
	}
	return func() { session.Detach(store, machine, pid) }
}

// prepareEnvironment brings the target machine up and makes the project
// visible inside it, returning the mount and the directory the guest should
// start in.
func prepareEnvironment(ctx context.Context, app *App, p provider.Provider, target resolve.ResolvedTarget, progress types.ProgressSink) (string, error) {
	mount, guestCwd, err := p.MapProjectPath(target.Project.ID, target.Project.Path, target.HostCwd)
	if err != nil {
		return "", err
	}

	// The spec is addressed to the backend that is about to execute it. That
	// routing decision was already made by choosing p, so naming any other
	// backend here would be incoherent; the resolver's own view of which
	// backend owns this machine is what recordMachine stores durably.
	if err := p.EnsureMachine(ctx, provider.MachineSpec{
		Name:     target.MachineName,
		Provider: p.ID(),
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

	guestCwd, err = ensureMounted(ctx, app, p, target.MachineName, mount, guestCwd, progress)
	if err != nil {
		return "", err
	}

	// Said after the environment is ready and before the user is handed a
	// shell, so that it is the last thing on screen rather than something that
	// scrolls past behind provisioning output.
	adviseNativeWorkspace(app, p, target)

	return guestCwd, nil
}

// adviseNativeWorkspace tells a Windows user, once per project, that their
// project is on the slow side of the filesystem boundary (REQ-18.11).
//
// It is gated on the backend rather than on the host, because that is what the
// condition actually is: a project only crosses a filesystem boundary where the
// guest reaches it through one, which is true of WSL and not of Lima, where a
// project is shared at its own path and there is nothing to cross.
//
// Every failure is silent. This is advice on the way to a shell the user asked
// for, and advice that can fail the command is worse than no advice.
func adviseNativeWorkspace(app *App, p provider.Provider, target resolve.ResolvedTarget) {
	if p.ID() != types.ProviderWSL2 || target.Project.AdvisedNativeFS {
		return
	}
	advice, heavy := workspace.Detect(target.Project.Path)
	if !heavy {
		return
	}

	store, err := app.Store()
	if err != nil {
		return
	}
	// The dismissal is recorded before the message is printed, so that a user
	// who interrupts the invocation still does not see it a second time.
	if _, err := store.UpdateProject(target.Project.ID, func(rec *types.ProjectRecord) {
		rec.AdvisedNativeFS = true
	}); err != nil {
		return
	}
	fmt.Fprintln(app.Err, advice.Message(target.Project.Path))
}

// recordMachine writes avar's own record of a machine the backend has just
// confirmed is running.
func recordMachine(app *App, target resolve.ResolvedTarget, mount types.MountSpec) error {
	store, err := app.Store()
	if err != nil {
		return err
	}
	if err := store.PutMachine(types.MachineRecord{
		Name:      target.MachineName,
		Provider:  target.Provider,
		Selector:  target.Selector,
		Kind:      target.Kind,
		ProjectID: isolatedProjectID(target),
		Mounts:    []types.MountSpec{mount},
	}); err != nil {
		return err
	}

	// A new machine is the point at which idle stopping starts to matter, so
	// the periodic idle-check is scheduled here. What that means differs by
	// host and is ensureIdleScheduler's business, not this caller's.
	ensureIdleScheduler(app)
	return nil
}

// isolatedProjectID names the project an isolated machine serves, which the
// store requires and a shared machine must not carry.
func isolatedProjectID(target resolve.ResolvedTarget) string {
	if target.Kind == types.KindIsolated {
		return target.Project.ID
	}
	return ""
}

// ensureMounted delegates mount decisions to the mount management component and
// handles the interactive restart prompt when it would interrupt other sessions.
func ensureMounted(ctx context.Context, app *App, p provider.Provider, machine string, mount types.MountSpec, guestCwd string, progress types.ProgressSink) (string, error) {
	sessions, err := liveSessions(ctx, app, machine)
	if err != nil {
		return "", err
	}

	lastUsed := projectRecency(app)

	result, err := mounts.Ensure(ctx, p, machine, mount, guestCwd, sessions, lastUsed, progress)
	if err != nil {
		return "", err
	}
	reportUnshared(app, result.Unshared)
	if result.SessionConflict {
		if !stdinIsTerminal() {
			return "", fmt.Errorf("sharing %s with your Linux environment would restart it while %d other sessions are attached to it; run in an interactive terminal, or close the other sessions, and try again",
				mount.HostPath, result.SessionCount)
		}
		if !promptRestart(app, mount.HostPath, result.SessionCount) {
			return "", Exit(130, nil)
		}
		// Retry without sessions — the prompt was accepted.
		result, err = mounts.Ensure(ctx, p, machine, mount, guestCwd, 0, lastUsed, progress)
		if err != nil {
			return "", err
		}
		reportUnshared(app, result.Unshared)
	}
	return result.GuestCwd, nil
}

// projectRecency maps each registered project's host path to when it was last
// entered, which is what decides the order projects are unshared in when a
// machine is at its mount limit.
//
// A failure to read yields no recency rather than an error: the cap still
// applies, projects are simply dropped in whatever order the backend reports,
// and a shell the user asked for is not worth refusing over the ordering of an
// eviction they will not notice.
func projectRecency(app *App) map[string]time.Time {
	store, err := app.Store()
	if err != nil {
		return nil
	}
	projects, err := store.Projects()
	if err != nil {
		return nil
	}
	out := make(map[string]time.Time, len(projects))
	for _, p := range projects {
		out[p.Path] = p.LastUsedAt
	}
	return out
}

// reportUnshared tells the user which projects stopped being shared, because a
// directory silently vanishing from the guest would look like avar losing their
// files rather than making room.
func reportUnshared(app *App, unshared []string) {
	if len(unshared) == 0 {
		return
	}
	fmt.Fprintf(app.Err, "avr: this environment shares as many project directories as it can hold, so %s made room:\n",
		pluralize(len(unshared), "the least recently used one", "the least recently used ones"))
	for _, path := range unshared {
		fmt.Fprintf(app.Err, "       %s\n", path)
	}
	fmt.Fprintf(app.Err, "     Nothing was deleted. Run avr in one of them again to share it back.\n")
}

// liveSessions returns the number of other live avr sessions attached to the
// machine.
func liveSessions(ctx context.Context, app *App, machine string) (int, error) {
	store, err := app.Store()
	if err != nil {
		return 0, err
	}
	sessions, err := store.SessionsFor(machine)
	if err != nil {
		return 0, fmt.Errorf("checking for other sessions on %s: %w", machine, err)
	}
	return len(sessions), nil
}

// promptRestart asks the user whether they are willing to restart the machine
// despite other attached sessions, and returns true if they accept.
func promptRestart(app *App, hostPath string, sessionCount int) bool {
	s := "s"
	if sessionCount == 1 {
		s = ""
	}
	fmt.Fprintf(app.Err, "avr: sharing %s with your Linux environment will restart it.\n", hostPath)
	fmt.Fprintf(app.Err, "      %d other session%s %s attached to it and would be disconnected.\n",
		sessionCount, s, pluralize(sessionCount, "is", "are"))
	return app.confirmYesNo("      Continue? (y/N) ")
}

// pluralize returns singular if count is 1, plural otherwise.
func pluralize(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
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

// forwardEnv reads the standing forward_env grant from avar's configuration
// (REQ-12.4).
//
// It returns nothing rather than failing when the configuration cannot be
// read: forward_env is an optional convenience, and refusing to open a shell
// because a hand-edited file has a typo in it would be a poor trade. The
// consequence — a variable the user expected not arriving — is visible in the
// guest, whereas a shell that will not start is not obviously about this at
// all.
func forwardEnv(app *App) []string {
	store, err := app.Store()
	if err != nil {
		return nil
	}
	names, err := store.ConfigList("forward_env")
	if err != nil {
		return nil
	}
	return names
}

// loadEnvFile opens the file at path and parses it as envpolicy.ParseDotEnv
// would. An empty path is a no-op that returns a nil map. A non-empty path that
// names a missing file or one that cannot be parsed fails before any machine
// work, as REQ-12.2 requires.
func loadEnvFile(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open env file: %w", err)
	}
	defer f.Close()
	return envpolicy.ParseDotEnv(f)
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
