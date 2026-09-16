package cmd

import (
	"context"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/types"
)

// REQ-11.3: `avr isolate off --yes` deletes the project's isolated machine and
// forgets it, as `avr destroy` does. Deleting the machine but keeping its record
// leaves avar describing an environment that no longer exists until some later
// reconciliation notices.
func TestIsolateOff_Yes_DeletesAndForgetsTheMachine_REQ_11_3(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)

	// The project exists, and defaults to its own environment.
	if _, err := app.Resolve(cli.Invocation{}); err != nil {
		t.Fatalf("resolving the project: %v", err)
	}
	_, rec, _, ok, err := currentProject(app.App)
	if err != nil || !ok {
		t.Fatalf("finding the project record: ok=%v err=%v", ok, err)
	}
	if _, err := app.store.UpdateProject(rec.ID, func(r *types.ProjectRecord) { r.Isolated = true }); err != nil {
		t.Fatalf("isolating the project: %v", err)
	}

	target, err := app.Resolve(cli.Invocation{Selector: cli.Selector{Isolate: true}})
	if err != nil {
		t.Fatalf("resolving the isolated environment: %v", err)
	}
	seedMachine(t, f, target.MachineName, target.Selector, types.KindIsolated)
	if err := app.store.PutMachine(types.MachineRecord{
		Name: target.MachineName, Provider: fake.ProviderID, Selector: target.Selector, Kind: types.KindIsolated, ProjectID: rec.ID,
	}); err != nil {
		t.Fatalf("recording the isolated machine: %v", err)
	}

	if err := runIsolate(context.Background(), app.App, cli.Invocation{
		Mode: cli.ModeSubcommand, Subcommand: "isolate", SubcommandArgs: []string{"off", "--yes"},
	}); err != nil {
		t.Fatalf("avr isolate off --yes: %v", err)
	}

	f.AssertCalled(t, fake.OpDelete)
	machines, err := app.store.Machines()
	if err != nil {
		t.Fatalf("reading machine records: %v", err)
	}
	for _, m := range machines {
		if m.Name == target.MachineName {
			t.Errorf("%s was deleted but avar still has a record of it", m.Name)
		}
	}
}
