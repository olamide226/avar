package session

import (
	"fmt"
	"time"

	"github.com/olamide226/avar/internal/state"
	"github.com/olamide226/avar/internal/types"
)

// DefaultIdleTimeout is the timeout when config.toml does not set one.
const DefaultIdleTimeout = 2 * time.Hour

// IdleTimeout is the idle timeout cfg sets, or DefaultIdleTimeout when it sets
// none. Zero means auto-stop is disabled (REQ-5.5), which IdleMachines enforces
// by returning nothing.
//
// There is deliberately no fallback here for a file that could not be read: a
// caller holding an error from state.Store.Config has no timeout to pass, and
// guessing the default would stop environments for a user who may have written
// "0".
func IdleTimeout(cfg state.Config) time.Duration {
	if !cfg.IdleTimeoutSet {
		return DefaultIdleTimeout
	}
	return cfg.IdleTimeout
}

// IdleMachines returns the names of the avar-managed machines that have been
// idle — no live sessions — for at least timeout. Property 11: a machine with
// live sessions is never in this set, regardless of elapsed time.
//
// The first time a sessionless machine is seen without an idle-since record,
// its idle clock is set to now, so a machine that lost its sessions to a
// crash gets a fresh timeout rather than being stopped immediately.
func IdleMachines(store *state.Store, timeout time.Duration) ([]string, error) {
	if timeout <= 0 {
		return nil, nil
	}

	// Sessions and machines are read in one transaction, not two. Read
	// separately, a session could attach between the two loads and its machine
	// would then look sessionless — Property 11 violated by a machine being
	// stopped while somebody is working in it.
	var (
		sessions []types.SessionRecord
		machines []types.MachineRecord
	)
	if err := store.Update(func(tx *state.Tx) error {
		sessions = tx.Sessions()
		machines = tx.Machines()
		return nil
	}); err != nil {
		return nil, fmt.Errorf("reading avar's session and machine records: %w", err)
	}

	// Live sessions: the definitive answer to "is this machine in use?"
	inUse := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		inUse[s.Machine] = true
	}

	since, err := readIdleSince(store)
	if err != nil {
		return nil, err
	}

	var (
		now   = time.Now().UTC()
		idle  []string
		dirty bool
	)
	for _, m := range machines {
		if inUse[m.Name] {
			continue // Property 11
		}

		t, ok := since[m.Name]
		if !ok {
			// First time this machine has been seen without sessions.
			// Start its idle clock now rather than treating "no record"
			// as "has been idle forever".
			since[m.Name] = now
			dirty = true
			continue
		}
		if now.Sub(t) >= timeout {
			idle = append(idle, m.Name)
		}
	}

	if dirty {
		if werr := writeIdleSince(store, since); werr != nil {
			// The write failure does not invalidate the idle set
			// computed above; the worst case is that the next check
			// re-initialises the missing timestamps.
			return idle, werr
		}
	}
	return idle, nil
}
