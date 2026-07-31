package session

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/olamide226/avar/internal/state"
	"github.com/olamide226/avar/internal/types"
)

// DefaultIdleTimeout is the timeout when config.toml does not set one.
const DefaultIdleTimeout = 2 * time.Hour

const configKey = "idle_timeout"

// IdleTimeout reads config.toml's idle_timeout from the store's state
// directory and returns the parsed duration and a boolean that reports
// whether auto-stop is disabled.
//
// The config value is a Go duration string (e.g. "2h", "30m"). "0" means
// disabled — the caller should never stop machines. A missing or unreadable
// config returns the default (2h). Parsing is intentionally simple rather
// than pulling in a TOML parser: idle_timeout is the only key we own right
// now, and a purpose-built line reader is both correct and smaller.
// A zero return means auto-stop is disabled (REQ-5.5). There is no separate
// "disabled" flag and no error: every failure to read or parse falls back to
// the default rather than refusing to check idle state at all, and a caller
// that has the duration already knows disabled means "do not stop anything" —
// IdleMachines enforces exactly that.
func IdleTimeout(store *state.Store) time.Duration {
	return idleTimeoutAt(store.ConfigPath())
}

func idleTimeoutAt(configPath string) time.Duration {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return DefaultIdleTimeout
	}

	val := stripQuotes(parseTOMLKey(string(data), configKey))
	if val == "" {
		return DefaultIdleTimeout
	}

	// A non-positive duration — "0", "0s", "-1h" — disables auto-stop.
	d, err := time.ParseDuration(val)
	if err != nil {
		return DefaultIdleTimeout
	}
	if d <= 0 {
		return 0
	}
	return d
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

// --- manual TOML parsing ---------------------------------------------------
// Parsing only the one key avar owns means we do not need a full TOML parser
// or a dependency on one. The format is simple: `key = "value"` or `key =
// value`, one per line, with optional whitespace. Comments are not handled
// because config.toml is machine-written by avar's own future `avr config`
// (task 22); neither are dotted keys, inline tables, or arrays, which
// config.toml does not use.

// parseTOMLKey finds the value for key in a simple TOML-like document.
// Returns "" when the key is absent.
func parseTOMLKey(doc, key string) string {
	prefix := key + " = "
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		// Skip blank lines and table headers.
		if line == "" || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		return strings.TrimSpace(line[len(prefix):])
	}
	return ""
}

// stripQuotes removes a single pair of matching double or single quotes.
func stripQuotes(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[0] == s[len(s)-1] {
		s = s[1 : len(s)-1]
	}
	return s
}
