package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
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
}

// newApp returns an App writing to the real streams.
func newApp(version string) *App {
	return &App{Version: version, Out: os.Stdout, Err: os.Stderr}
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
// Host routing happens before the dependency check so that an unsupported host
// is told so plainly, rather than being sent to install Lima and discovering
// the same thing more slowly (REQ-18.1, REQ-17.6).
func (a *App) Provider(ctx context.Context) (provider.Provider, error) {
	a.once.provider.Do(func() {
		id, err := provider.HostProviderID()
		if err != nil {
			a.provErr = err
			return
		}

		store, err := a.Store()
		if err != nil {
			a.provErr = err
			return
		}

		switch id {
		case types.ProviderLima:
			limactl, err := deps.EnsureLima(ctx, a.Err)
			if err != nil {
				a.provErr = err
				return
			}
			p, err := lima.New(lima.Options{
				Lima:    limactl,
				Runner:  deps.NewRunner(),
				Records: store,
				LogsDir: store.LogsDir(),
			})
			if err != nil {
				a.provErr = fmt.Errorf("prepare the Lima backend: %w", err)
				return
			}
			a.prov = p
		default:
			// Unreachable while HostProviderID is the only source of id, and
			// deliberately loud if a future host is routed without a backend.
			a.provErr = fmt.Errorf("internal error: no backend built for provider %q", id)
		}
	})
	return a.prov, a.provErr
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
