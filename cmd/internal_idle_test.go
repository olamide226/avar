package cmd

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/session"
	"github.com/olamide226/avar/internal/types"
)

// idleMachine seeds an environment in the given state whose last session has
// already detached, under an idle timeout short enough that the next idle check
// finds it idle.
func idleMachine(t *testing.T, app *testApp, f *fake.Fake, name string, selector types.EnvironmentSelector, state types.MachineState) {
	t.Helper()
	seedMachine(t, f, name, selector, types.KindShared)
	f.SetMachineState(name, state)
	if err := app.store.PutMachine(types.MachineRecord{
		Name: name, Provider: fake.ProviderID, Selector: selector, Kind: types.KindShared,
	}); err != nil {
		t.Fatalf("recording %s: %v", name, err)
	}
	if err := session.Attach(app.store, name, os.Getpid()); err != nil {
		t.Fatalf("attaching a session to %s: %v", name, err)
	}
	session.Detach(app.store, name, os.Getpid())
}

func idleAfterOneNanosecond(t *testing.T, app *testApp) {
	t.Helper()
	if err := os.WriteFile(app.store.ConfigPath(), []byte("idle_timeout = \"1ns\"\n"), 0o600); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}
}

// Idle auto-stop asks every idle environment that is running or stopped to
// stop. A stopped one is included because Stop converges on stopped rather than
// performing a shutdown, and for a backend that can leave processes behind
// after the machine itself has stopped, converging is what releases them —
// which the idle check never did, so only an explicit `avr stop` ever cleaned
// up. A machine that is coming up or broken is still left alone.
func TestIdleCheck_ConvergesStoppedEnvironmentsToo_REQ_5_5(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	idleAfterOneNanosecond(t, app)

	fedora := types.EnvironmentSelector{Distro: types.DistroFedora, Version: "42", Arch: types.HostArch()}
	debian := types.EnvironmentSelector{Distro: types.DistroDebian, Version: "13", Arch: types.HostArch()}
	idleMachine(t, app, f, "avr-ubuntu-24.04-arm64", ubuntu(), types.StateRunning)
	idleMachine(t, app, f, "avr-fedora-42-arm64", fedora, types.StateStopped)
	idleMachine(t, app, f, "avr-debian-13-arm64", debian, types.StateBroken)

	f.SetStopWarning("avr-fedora-42-arm64", "Fedora 42 had a leftover process; avar ended it.")

	if err := runIdleCheck(context.Background(), app.App); err != nil {
		t.Fatalf("avr internal idle-check: %v", err)
	}

	stopped := map[string]bool{}
	for _, call := range f.CallsFor(fake.OpStop) {
		stopped[call.Machine] = true
	}
	for name, want := range map[string]bool{
		"avr-ubuntu-24.04-arm64": true,
		"avr-fedora-42-arm64":    true,
		"avr-debian-13-arm64":    false,
	} {
		if stopped[name] != want {
			t.Errorf("idle check asked %s to stop: %t, want %t (calls: %v)", name, stopped[name], want, f.Calls())
		}
	}
	f.AssertMachineState(t, "avr-ubuntu-24.04-arm64", types.StateStopped)

	// Nobody is watching a scheduled check, but one run by hand says what it did.
	if !strings.Contains(app.err.String(), "avar ended it") {
		t.Errorf("idle check discarded what the backend reported:\n%s", app.err.String())
	}
}

// Property 11 still holds with stopped environments included: one with a live
// session is not touched, whatever its state.
func TestIdleCheck_NeverTouchesAnEnvironmentWithALiveSession_PROP_11(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	idleAfterOneNanosecond(t, app)

	idleMachine(t, app, f, "avr-ubuntu-24.04-arm64", ubuntu(), types.StateStopped)
	if err := session.Attach(app.store, "avr-ubuntu-24.04-arm64", os.Getpid()); err != nil {
		t.Fatal(err)
	}

	if err := runIdleCheck(context.Background(), app.App); err != nil {
		t.Fatalf("avr internal idle-check: %v", err)
	}
	if n := f.Count(fake.OpStop); n != 0 {
		t.Errorf("idle check stopped an environment with a live session: %v", f.Calls())
	}
}
