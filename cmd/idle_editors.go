package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/session"
	"github.com/olamide226/avar/internal/state"
	"github.com/olamide226/avar/internal/types"
)

// keptForEditor decides whether a running environment the idle check is about
// to stop has an editor window connected to it, which avar's own session
// records cannot show: `avr code`, `avr cursor` and `avr zed` launch the editor
// and exit.
//
// It keeps the environment whenever it cannot say no. An editor found restarts
// the idle clock, so the environment gets a full timeout from the last check
// that saw the window rather than being stopped at the first check after it
// closes. A probe that fails keeps the environment too, and is returned as an
// error, because stopping on an unanswered question is how somebody's editor
// loses its machine (PROP-11).
func keptForEditor(ctx context.Context, app *App, prober provider.EditorProber, store *state.Store, m types.MachineStatus) (bool, error) {
	windows, err := prober.ConnectedEditors(ctx, m.Name)
	if err != nil {
		return true, fmt.Errorf("idle check left %s running, because it could not tell whether an editor is connected to it (the next check asks again): %w",
			m.Selector.Label(), err)
	}
	if len(windows) == 0 {
		return false, nil
	}

	fmt.Fprintf(app.Err, "avr: kept %s running: %s connected to it\n", m.Selector.Label(), editorsConnected(windows))
	if err := session.RestartIdleClock(store, m.Name); err != nil {
		return true, fmt.Errorf("idle check kept %s running for its editor, but could not restart its idle clock: %w", m.Selector.Label(), err)
	}
	return true, nil
}

// editorsConnected names the editors with a window connected, each once, in the
// order first seen: "VS Code is", "VS Code and Zed are".
func editorsConnected(windows []provider.EditorConnection) string {
	var names []string
	seen := map[string]bool{}
	for _, w := range windows {
		if !seen[w.Editor] {
			seen[w.Editor] = true
			names = append(names, w.Editor)
		}
	}
	if len(names) == 1 {
		return names[0] + " is"
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1] + " are"
}
