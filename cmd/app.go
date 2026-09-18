package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/olamide226/avar/internal/browser"
	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/deps"
	"github.com/olamide226/avar/internal/projconfig"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/lima"
	"github.com/olamide226/avar/internal/provider/wsl2"
	"github.com/olamide226/avar/internal/resolve"
	"github.com/olamide226/avar/internal/state"
	"github.com/olamide226/avar/internal/types"
	"golang.org/x/term"
)

// App carries what every command needs and builds the expensive parts on
// demand.
//
// Construction is lazy because the cost is not uniform: `avr --help` must not
// probe for Lima, and `avr status` must not pay for a state directory it may
// not use. Each dependency is built at most once per invocation and never
// cached across invocations — avar is a short-lived process, and a stale view
// of the backend is worse than asking again.
type App struct {
	Version string
	Stdin   io.Reader
	Out     io.Writer
	Err     io.Writer

	once struct {
		store    sync.Once
		config   sync.Once
		provider sync.Once
	}
	store     *state.Store
	storeErr  error
	config    state.Config
	configErr error
	prov      provider.Provider
	provErr   error

	// terminal replaces the check for an interactive terminal when set, so
	// that flow tests can prove both what avar asks a person and what it
	// refuses to assume without one. Nil means the real stdin.
	terminal func() bool

	// buildBackend replaces backend construction when set: flow tests hand
	// the App an in-process provider through it, so the whole of Provider —
	// including the crash recovery that follows a successful build — runs
	// against a fake without going near a real one. Nil means the host's
	// real backend.
	buildBackend func(ctx context.Context) (provider.Provider, error)

	// scheduleIdleCheck replaces the host scheduler registration when set.
	// Registration writes into the user's home directory and runs launchctl or
	// schtasks against their real session, so a flow test that creates an
	// environment must never reach it: one once left a launchd agent pointing
	// at a deleted test binary on a developer's Mac. Nil means the real one.
	scheduleIdleCheck func(app *App)

	// browser replaces the host's browser launcher when set, so a flow test
	// can see what `avr open` would have opened without opening anything. Nil
	// means the host's own.
	browser browser.Opener
}

// newApp returns an App writing to the real streams.
func newApp(version string) *App {
	return &App{Version: version, Stdin: os.Stdin, Out: os.Stdout, Err: os.Stderr}
}

// interactive reports whether a person is at the terminal to answer a question.
//
// It asks whether stdin is a terminal rather than whether it is a character
// device. The difference matters for consent: /dev/null is a character device
// and not a terminal, and an approval must never be inferred from a stream no
// person is typing into.
func (a *App) interactive() bool {
	if a.terminal != nil {
		return a.terminal()
	}
	return isTerminal(os.Stdin)
}

// isTerminal reports whether f is a terminal.
func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// confirmYesNo puts a yes/no question to the user and reports whether they
// agreed. Anything other than an explicit yes — including a closed input — is
// "no", so an unattended run never destroys anything by default.
//
// The question goes to the error stream because stdout belongs to the command
// the user actually ran (REQ-2.3), and the answer is read through App.Stdin
// rather than os.Stdin so that these paths can be driven by a test.
func (a *App) confirmYesNo(question string) bool {
	fmt.Fprint(a.Err, question)

	reply, err := bufio.NewReader(a.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(reply)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// requireConfirmer refuses a command that needs a typed confirmation when
// there is no terminal to type it at, saying what did not happen and how to
// run the command unattended.
//
// A reply on a pipe is not a confirmation: it was written before the summary
// it answers was printed, so nobody can have read what it agreed to. The
// refusal is an error rather than a quiet cancellation so that a script sees
// the command did nothing.
func (a *App) requireConfirmer(command, outcome string) error {
	if a.interactive() {
		return nil
	}
	return fmt.Errorf("`avr %s` asks you to type a confirmation first, and there is no terminal to type it at. %s "+
		"Run it from a terminal, or add --yes to go ahead without confirming", command, outcome)
}

// confirmByTyping asks the user to type an exact phrase before something
// irreversible happens, and reports whether they did.
//
// Typing the thing's own name rather than pressing a key is what makes the
// confirmation evidence of intent: it cannot be given by a stray return, and it
// cannot be given without having read what is about to happen.
//
// A closed or unreadable input is not a confirmation. That is the safe
// direction, and it is why this returns a bool rather than an error — there is
// nothing a caller could usefully do with the difference between "typed the
// wrong thing" and "typed nothing at all".
func (a *App) confirmByTyping(prompt, expected string) bool {
	fmt.Fprint(a.Out, prompt)

	line, err := bufio.NewReader(a.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	return strings.TrimSpace(line) == expected
}

// Browser returns what opens a web address on this computer (REQ-16.2).
func (a *App) Browser() browser.Opener {
	if a.browser != nil {
		return a.browser
	}
	return browser.System()
}

// Store opens avar's state directory.
func (a *App) Store() (*state.Store, error) {
	a.once.store.Do(func() {
		st, err := state.OpenDefault()
		if err != nil {
			a.storeErr = fmt.Errorf("open avar's state directory: %w", err)
			return
		}
		a.store = st
	})
	return a.store, a.storeErr
}

// Config reads the user's global config.toml, once per invocation, so that the
// check dispatch makes before running a command and the command's own use of a
// setting see the same file.
func (a *App) Config() (state.Config, error) {
	a.once.config.Do(func() {
		store, err := a.Store()
		if err != nil {
			a.configErr = err
			return
		}
		a.config, a.configErr = store.Config()
	})
	return a.config, a.configErr
}

// Provider returns the backend for this host, ensuring its dependencies first.
//
// A backend the App builds itself is reconciled against avar's records before
// it is handed out (see reconcile.go), so every command acts on records that
// already agree with the backend (REQ-17.5). Placing recovery here, on the
// path that first obtains the backend, is what keeps it lazy — `avr --help`
// never probes for one — and exactly once per invocation. A test that seeds
// prov directly marks this Once done and so skips construction and recovery
// alike; a test that wants recovery injects buildBackend instead.
func (a *App) Provider(ctx context.Context) (provider.Provider, error) {
	a.once.provider.Do(func() {
		p, err := a.backend(ctx)
		if err != nil {
			a.provErr = err
			return
		}

		// Construction already opened the store — the real backend takes it
		// as Records — so this returns the cached handle and cannot newly
		// fail; the check is here for the compiler, not for a real condition.
		store, err := a.Store()
		if err != nil {
			a.provErr = err
			return
		}

		reconcile(ctx, a, store, p)
		a.prov = p
	})
	return a.prov, a.provErr
}

// backend builds the backend for this host. Construction is the only thing
// that varies by host: everything above Provider is written against the
// provider.Provider contract and never against a concrete backend (REQ-17.3).
func (a *App) backend(ctx context.Context) (provider.Provider, error) {
	if a.buildBackend != nil {
		return a.buildBackend(ctx)
	}

	// Host routing happens before the dependency check so that an unsupported
	// host is told so plainly, rather than being sent to install Lima and
	// discovering the same thing more slowly (REQ-18.1, REQ-17.6).
	id, err := provider.HostProviderID()
	if err != nil {
		return nil, err
	}

	store, err := a.Store()
	if err != nil {
		return nil, err
	}

	switch id {
	case types.ProviderLima:
		limactl, err := deps.EnsureLima(ctx, a.Err)
		if err != nil {
			return nil, err
		}
		p, err := lima.New(lima.Options{
			Lima:    limactl,
			Runner:  deps.NewRunner(),
			Records: store,
			LogsDir: store.LogsDir(),
		})
		if err != nil {
			return nil, fmt.Errorf("prepare the Lima backend: %w", err)
		}
		return p, nil
	case types.ProviderWSL2:
		// Only the WSL check runs here, and it never mentions Lima, Docker
		// Desktop, or any other runtime: one host, one dependency (REQ-18.2,
		// PROP-13).
		wsl, err := deps.EnsureWSL(ctx, a.Err)
		if err != nil {
			return nil, err
		}
		p, err := wsl2.New(wsl2.Options{
			WSL:          wsl,
			Runner:       deps.NewRunner(),
			Records:      store,
			DistrosDir:   store.DistrosDir(),
			LogsDir:      store.LogsDir(),
			SnapshotsDir: store.SnapshotsDir(),
		})
		if err != nil {
			return nil, fmt.Errorf("prepare the WSL backend: %w", err)
		}
		return p, nil
	default:
		// Unreachable while HostProviderID is the only source of id, and
		// deliberately loud if a future host is routed without a backend.
		return nil, fmt.Errorf("internal error: no backend built for provider %q", id)
	}
}

// Resolve maps the current directory, the invocation's selector flags, and the
// project's .avr.toml if it has one, onto the machine this invocation targets.
// Every command that acts on "this environment" comes through here, which is
// what makes the file apply to all of them alike (design §3.11).
func (a *App) Resolve(inv cli.Invocation) (resolve.ResolvedTarget, error) {
	return a.resolve(inv, projconfig.Load)
}

// resolve is Resolve with the project-configuration reader chosen by the
// caller. Only `avr init` passes nil: it is the one command whose job is to
// create the file, and it refuses when one exists, so a file it cannot read
// must not stop it from saying so.
func (a *App) resolve(inv cli.Invocation, readConfig func(string) (projconfig.Config, error)) (resolve.ResolvedTarget, error) {
	id, err := provider.HostProviderID()
	if err != nil {
		return resolve.ResolvedTarget{}, err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return resolve.ResolvedTarget{}, fmt.Errorf("find the current directory: %w", err)
	}

	store, err := a.Store()
	if err != nil {
		return resolve.ResolvedTarget{}, err
	}

	return resolve.Resolve(id, cwd, inv.Selector, store, resolve.Options{ProjectConfig: readConfig})
}
