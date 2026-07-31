package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
)

// Every name the argv grammar will route must have a registered handler.
// Without this, adding a subcommand to the grammar and forgetting to handle it
// produces "internal error" at runtime instead of a test-time failure.
func TestDispatch_EveryGrammarSubcommandIsAccountedFor_REQ_2_5(t *testing.T) {
	for _, name := range cli.Subcommands() {
		switch name {
		case "help", "version":
			// Handled before dispatch: they are the subcommand spellings of
			// avar's own flags.
			continue
		}

		if _, registered := handlers[name]; !registered {
			t.Errorf("subcommand %q is in the argv grammar but has no handler", name)
		}
	}
}

func TestDispatch_RoutesSubcommandsToTheirHandler(t *testing.T) {
	const name = "status"

	original, had := handlers[name]
	t.Cleanup(func() {
		if had {
			handlers[name] = original
		} else {
			delete(handlers, name)
		}
	})

	called := false
	handlers[name] = func(_ context.Context, _ *App, inv cli.Invocation) error {
		called = true
		if inv.Subcommand != name {
			t.Errorf("handler received subcommand %q, want %q", inv.Subcommand, name)
		}
		return nil
	}

	inv := cli.Invocation{Mode: cli.ModeSubcommand, Subcommand: name}
	if err := dispatch(context.Background(), &App{}, inv); err != nil {
		t.Fatalf("dispatch returned an unexpected error: %v", err)
	}
	if !called {
		t.Error("the registered handler was not called")
	}
}

// A handler's error must reach the caller unchanged, because that is how a
// guest command's exit code becomes avar's own (REQ-1.7, REQ-2.2, PROP-3).
func TestDispatch_PropagatesHandlerErrorsUnchanged_PROP_3(t *testing.T) {
	original := guestHandler
	t.Cleanup(func() { guestHandler = original })

	want := Exit(42, nil)
	guestHandler = func(context.Context, *App, cli.Invocation) error { return want }

	err := dispatch(context.Background(), &App{}, cli.Invocation{Mode: cli.ModeGuestCommand, Guest: []string{"false"}})

	var exit *ExitCodeError
	if !errors.As(err, &exit) {
		t.Fatalf("dispatch returned %v, want an ExitCodeError", err)
	}
	if exit.Code != 42 {
		t.Errorf("exit code = %d, want 42", exit.Code)
	}
}

func TestDispatch_UnregisteredSubcommandNamesItsTask(t *testing.T) {
	if _, registered := handlers["snapshot"]; registered {
		t.Skip("snapshot now has a handler; this case no longer applies")
	}

	err := dispatch(context.Background(), &App{}, cli.Invocation{Mode: cli.ModeSubcommand, Subcommand: "snapshot"})
	if err == nil {
		t.Fatal("dispatch returned nil for an unimplemented subcommand, want an error")
	}
	if !strings.Contains(err.Error(), "task 15") {
		t.Errorf("error %q does not name the task that implements it", err)
	}
}

func TestRegisterSubcommand_RejectsNamesTheGrammarDoesNotKnow(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering a name outside the grammar's subcommand set did not panic")
		}
	}()
	registerSubcommand("definitely-not-a-subcommand", func(context.Context, *App, cli.Invocation) error { return nil })
}

func TestRegisterSubcommand_RejectsDuplicates(t *testing.T) {
	const name = "reset"

	original, had := handlers[name]
	t.Cleanup(func() {
		if had {
			handlers[name] = original
		} else {
			delete(handlers, name)
		}
	})

	noop := func(context.Context, *App, cli.Invocation) error { return nil }
	handlers[name] = noop

	defer func() {
		if recover() == nil {
			t.Error("registering the same subcommand twice did not panic")
		}
	}()
	registerSubcommand(name, noop)
}

func TestRegisteredSubcommands_AreSorted(t *testing.T) {
	got := registeredSubcommands()
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("registeredSubcommands() is not sorted: %v", got)
		}
	}
}
