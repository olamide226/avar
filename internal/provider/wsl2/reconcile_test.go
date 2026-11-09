//go:build windows

package wsl2

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/state"
	"github.com/olamide226/avar/internal/types"
)

// These tests compose the backend the way App.Provider does in production: the
// real state store is the provider's Records, and reconciliation is given
// provider.ReconciliationView of that same provider. Only wsl.exe is faked.

// reconcileOnStartup runs crash recovery exactly as App.Provider does.
func reconcileOnStartup(t *testing.T, store *state.Store, p *Provider) state.Result {
	t.Helper()
	result, err := store.Reconcile(context.Background(), provider.ReconciliationView(p))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return result
}

func openStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening a temporary state directory: %v", err)
	}
	return store
}

// changeFor finds what reconciliation did about one machine.
func changeFor(result state.Result, machine string) (state.Change, bool) {
	for _, c := range result.Changes {
		if c.Machine == machine {
			return c, true
		}
	}
	return state.Change{}, false
}

// REQ-17.5, PROP-6: avar cannot prove it created an avr- distribution it has
// no record of — there is no operation journal, and a name is something anyone
// can type — so reconciliation reports it for the user to inspect and never
// mutates it (design §3.3). That holds whatever state WSL reports it in,
// including the WSL 1 registration that avar reads as broken and would
// otherwise delete.
func TestReconcile_ReportsAnUnrecordedDistributionAndNeverTouchesIt_REQ_17_5(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		machine string
		version int
		running bool
	}{
		{"shared, stopped", "avr-fedora-43-amd64", 2, false},
		{"shared, running", "avr-ubuntu-24.04-amd64", 2, true},
		{"WSL 1, which avar reads as broken", "avr-debian-13-amd64", 1, false},
		{"isolated", "avr-prj-3fa9c2b1d0-ubuntu-24.04-amd64", 2, true},
		{"a name avar would not choose", "avr-alpine-3.20-amd64", 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFakeWSL()
			f.register(tc.machine, tc.version, tc.running)
			f.register("Ubuntu", 2, true)
			store := openStore(t)
			p := newProvider(t, f, store)

			result := reconcileOnStartup(t, store, p)

			change, ok := changeFor(result, tc.machine)
			if !ok || change.Action != state.ActionLeft {
				t.Fatalf("Reconcile changes = %+v, want %s reported and left alone", result.Changes, tc.machine)
			}
			if len(result.Changes) != 1 {
				t.Errorf("Reconcile changes = %+v, want only %s; the user's Ubuntu is not avar's to mention", result.Changes, tc.machine)
			}
			if !strings.Contains(change.Manual, "wsl.exe --unregister "+tc.machine) {
				t.Errorf("Manual = %q, want the command that removes %s by hand", change.Manual, tc.machine)
			}
			if _, found, err := store.Machine(tc.machine); err != nil || found {
				t.Errorf("store.Machine(%s) = %v, %v; nothing unprovable may be recorded", tc.machine, found, err)
			}
			for _, verb := range []string{"--unregister", "--terminate", "--exec", "--set-version"} {
				if f.ranAny(verb) {
					t.Errorf("reconciliation ran wsl.exe %s on a distribution it cannot prove is avar's: %v", verb, f.argvs())
				}
			}
			if got, err := p.Status(context.Background()); err != nil || len(got) != 0 {
				t.Errorf("Status = %v, %v; want nothing, since none of it is in avar's records", got, err)
			}
		})
	}
}

// Recorded distributions reconcile as they always have: one WSL still has is
// left alone, and the record of one it no longer has is dropped.
func TestReconcile_RecordedDistributionsAreUnaffected_REQ_17_5(t *testing.T) {
	t.Parallel()

	const gone = "avr-fedora-43-amd64"
	f := newFakeWSL()
	f.register(testMachine, 2, true)
	store := openStore(t)
	for _, name := range []string{testMachine, gone} {
		if err := store.PutMachine(types.MachineRecord{
			Name:      name,
			Provider:  types.ProviderWSL2,
			Selector:  ubuntuSelector(),
			Kind:      types.KindShared,
			CreatedAt: time.Now().Add(-time.Hour).UTC(),
		}); err != nil {
			t.Fatalf("recording %s: %v", name, err)
		}
	}
	p := newProvider(t, f, store)

	result := reconcileOnStartup(t, store, p)

	if len(result.Changes) != 1 || result.Changes[0].Machine != gone || result.Changes[0].Action != state.ActionForgotten {
		t.Fatalf("Reconcile changes = %+v, want only %s forgotten", result.Changes, gone)
	}
	if _, found, _ := store.Machine(testMachine); !found {
		t.Errorf("the record of %s, which WSL still has, was dropped", testMachine)
	}
}

// REQ-17.1: the reconciler's listing is the invocation's listing. Reconciling
// and then serving a command asks WSL its three listing questions once.
func TestReconcile_AddsNoListingToTheWarmPath_REQ_17_1(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	store := openStore(t)
	if err := store.PutMachine(types.MachineRecord{
		Name: testMachine, Provider: types.ProviderWSL2, Selector: ubuntuSelector(), Kind: types.KindShared,
	}); err != nil {
		t.Fatal(err)
	}
	p := newProvider(t, f, store)

	reconcileOnStartup(t, store, p)
	if _, err := p.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}

	listings := 0
	for _, argv := range f.calls {
		if len(argv) > 1 && argv[1] == "--list" {
			listings++
		}
	}
	if listings != 3 {
		t.Errorf("wsl.exe --list ran %d times, want the one view's 3: %v", listings, f.argvs())
	}
}

// REQ-5.4, PROP-6: the unfiltered listing exists for reconciliation alone. The
// provider every command uses still lists only recorded environments, and still
// refuses to stop one it has no record of.
func TestStatus_StaysFilteredByRecordBesideDiscover_REQ_5_4(t *testing.T) {
	t.Parallel()

	const orphan = "avr-fedora-43-amd64"
	f := newFakeWSL()
	f.register(testMachine, 2, true)
	f.register(orphan, 2, true)
	f.register("Ubuntu", 2, true)
	p := newProvider(t, f, recorded(testMachine))

	got, err := p.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(got) != 1 || got[0].Name != testMachine {
		t.Errorf("Status = %v, want only the recorded %s", got, testMachine)
	}

	found, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	names := make([]string, 0, len(found))
	for _, m := range found {
		names = append(names, m.Name)
	}
	if len(names) != 2 || names[0] != orphan || names[1] != testMachine {
		t.Errorf("Discover = %v, want [%s %s] and never Ubuntu", names, orphan, testMachine)
	}

	if err := p.Stop(context.Background(), orphan, types.DiscardProgress); err == nil {
		t.Errorf("Stop(%s) succeeded on a distribution avar has no record of", orphan)
	}
	if f.ranAny("--terminate") {
		t.Errorf("avar stopped an unrecorded distribution: %v", f.argvs())
	}
}
