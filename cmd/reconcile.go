package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/olamide226/avar/internal/state"
)

// reconcile converges avar's machine records on the machines the backend
// actually has, before the invocation that built the backend acts on them
// (REQ-17.5). It runs from App.Provider — once per invocation, and only on
// paths that touch a backend at all.
//
// Two postures are deliberate, and reportChanges and the absence of a return
// value encode them:
//
//   - Reporting is selective. Destroying an environment is always told
//     (design §6), and a machine avar could not place is told so it never
//     sits invisible. An adoption or a dropped record is silent: the repair
//     restored exactly the state the user already thought they had, and the
//     crash that made it necessary was reported when it happened (REQ-1.6).
//   - Failure never stops the command. A repair mechanism that bricks the
//     tool when it cannot repair is worse than one that reports and
//     proceeds: the records reconciliation maintains are advisory to the
//     command paths, which re-derive what they need from the backend itself
//     and will surface a persistent problem with more context than this one
//     warning can carry. Repairs completed before the failure are still
//     reported and still stand — the reconciler returns a partial Result
//     rather than discarding it.
func reconcile(ctx context.Context, app *App, store *state.Store, backend state.Backend) {
	result, err := store.Reconcile(ctx, backend)
	reportChanges(app.Err, result.Changes)
	if err != nil {
		fmt.Fprintf(app.Err, "avr: crash recovery did not finish and will be retried on the next run: %v\n", err)
	}
}

// reportChanges tells the user about the repairs they need to know about, and
// only those, so the overwhelmingly common consistent case prints nothing
// (REQ-17.1). Everything goes to stderr: stdout belongs to the command the
// user actually ran (REQ-2.3).
func reportChanges(w io.Writer, changes []state.Change) {
	for _, c := range changes {
		switch c.Action {
		case state.ActionDeleted, state.ActionLeft:
			// Change.Reason was written for this: one user-facing
			// sentence, no backend command, and no machine name the
			// user is expected to type (REQ-1.5).
			fmt.Fprintf(w, "avr: %s\n", c.Reason)
		default:
			// Adopted and forgotten restore the user's existing mental
			// model rather than changing anything in it, so they are
			// silent.
		}
	}
}
