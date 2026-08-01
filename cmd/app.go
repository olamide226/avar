package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/deps"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/lima"
	"github.com/olamide226/avar/internal/resolve"
	"github.com/olamide226/avar/internal/state"
	"github.com/olamide226/avar/internal/types"
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
		provider sync.Once
	}
	store    *state.Store
	storeErr error
	prov     provider.Provider
	provErr  error

	// buildBackend replaces backend construction when set: flow tests hand
	// the App an in-process provider through it, so the whole of Provider —
	// including the crash recovery that follows a successful build — runs
	// against a fake without going near a real one. Nil means the host's
	// real backend.
	buildBackend func(ctx context.Context) (provider.Provider, error)
}

// newApp returns an App writing to the real streams.
func newApp(version string) *App {
	return &App{Version: version, Stdin: os.Stdin, Out: os.Stdout, Err: os.Stderr}
}

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
	default:
		// Unreachable while HostProviderID is the only source of id, and
		// deliberately loud if a future host is routed without a backend.
		return nil, fmt.Errorf("internal error: no backend built for provider %q", id)
	}
}

// Resolve maps the current directory and the invocation's selector flags onto
// the machine this invocation targets.
func (a *App) Resolve(inv cli.Invocation) (resolve.ResolvedTarget, error) {
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

	return resolve.Resolve(id, cwd, inv.Selector, store, resolve.Options{})
}
