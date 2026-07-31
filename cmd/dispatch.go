package cmd

import (
	"context"
	"fmt"
	"sort"

	"github.com/olamide226/avar/internal/cli"
)

// Handler runs one parsed invocation.
//
// Handlers return values and errors; the exit code comes from returning an
// ExitCodeError, which is how a guest command's status becomes avar's own
// (REQ-1.7, REQ-2.2).
type Handler func(ctx context.Context, app *App, inv cli.Invocation) error

// handlers holds the implementation of each avar subcommand, and guestHandler
// the implementation of the shell and one-shot paths.
//
// Commands register themselves from their own file rather than being wired into
// a switch here. That is what lets separate pieces of work add `avr status` and
// `avr stop` and the shell path without three changes colliding in one
// function, and it means a command's registration sits next to its code.
var (
	handlers     = map[string]Handler{}
	guestHandler Handler
)

// registerSubcommand wires a handler to an avar subcommand name.
//
// It panics on a duplicate or on a name the argv grammar does not know: both
// are programming errors that would otherwise surface as a command silently
// doing nothing, and both are caught by any test that builds the command tree.
func registerSubcommand(name string, h Handler) {
	if !cli.IsSubcommand(name) {
		panic(fmt.Sprintf("cmd: %q is not in the argv grammar's subcommand set", name))
	}
	if _, exists := handlers[name]; exists {
		panic(fmt.Sprintf("cmd: subcommand %q is registered twice", name))
	}
	handlers[name] = h
}

// registerGuest wires the handler for the interactive shell and one-shot
// command paths, which share everything except whether argv is empty.
func registerGuest(h Handler) {
	if guestHandler != nil {
		panic("cmd: the guest handler is registered twice")
	}
	guestHandler = h
}

// pendingSubcommand names the task that makes each unregistered subcommand
// real, so an early build answers specifically instead of failing blankly.
// Entries disappear as the commands land.
var pendingSubcommand = map[string]string{
	"isolate":  "task 17 (REQ-11.3)",
	"internal": "task 20 (REQ-5.5)",
}

// dispatch routes a parsed invocation to its registered handler.
func dispatch(ctx context.Context, app *App, inv cli.Invocation) error {
	switch inv.Mode {
	case cli.ModeShell, cli.ModeGuestCommand:
		if guestHandler == nil {
			return fmt.Errorf("not implemented yet: running commands in Linux lands with task 8 (REQ-1.1, REQ-2.1)")
		}
		return guestHandler(ctx, app, inv)

	case cli.ModeSubcommand:
		if h, ok := handlers[inv.Subcommand]; ok {
			return h(ctx, app, inv)
		}
		if owner, ok := pendingSubcommand[inv.Subcommand]; ok {
			return fmt.Errorf("not implemented yet: `avr %s` lands with %s", inv.Subcommand, owner)
		}
		// The grammar accepted a name nothing here knows about, so the two
		// have drifted apart.
		return fmt.Errorf("internal error: no handler for subcommand %q", inv.Subcommand)

	default:
		return fmt.Errorf("internal error: unknown invocation mode %s", inv.Mode)
	}
}

// registeredSubcommands lists the names with handlers, for tests.
func registeredSubcommands() []string {
	names := make([]string, 0, len(handlers))
	for name := range handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
