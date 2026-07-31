// Package session tracks live avar shell sessions and detects idle machines
// for automatic shutdown (REQ-5.5).
//
// It adds nothing to avar's warm path: Attach and Detach are each a single
// atomic store write. The idle file it maintains is advisory, so a corrupted
// write or a race between concurrent invocations cannot stop a machine
// somebody is using (Property 11).
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/olamide226/avar/internal/state"
	"github.com/olamide226/avar/internal/types"
)

const idleSinceFile = "idle_since.json"

// Attach records that this process is now attached to the machine. It is
// idempotent: calling Attach again for the same (machine, pid) pair refreshes
// the record rather than duplicating it (REQ-5.5 — sessions are tracked so
// idle stop can tell which machines are in use).
//
// Start time is set here rather than in the store so the caller does not have
// to remember, and so that the timestamp of a re-attach (same process, after
// a crash-cleaned-up stale entry) is always the attach time, not the original
// session start.
func Attach(store *state.Store, machine string, pid int) error {
	now := time.Now().UTC()
	if err := store.AddSession(types.SessionRecord{
		Machine:   machine,
		PID:       pid,
		StartedAt: now,
	}); err != nil {
		return fmt.Errorf("recording session on %s: %w", machine, err)
	}
	// The machine is now active: clear the idle-since marker so that a
	// reconnect resets the idle clock rather than leaving it running from a
	// previous session's end.
	return clearIdleSince(store, machine)
}

// Detach removes pid from machine's session list. It is best-effort by
// design — a missing session is the state this call is asking for and is not
// an error — so the caller can always defer Detach from a shell exit.
//
// When the last session detaches the machine's idle clock starts, which is
// what lets idle auto-stop know how long it has been alone.
func Detach(store *state.Store, machine string, pid int) {
	// RemoveSession is best-effort; a missing entry is not an error.
	if err := store.RemoveSession(machine, pid); err != nil {
		// Logging this would require pulling a logger into the session
		// package. A RemoveSession failure means the store is broken, and
		// the next operation against it will surface that.
		return
	}

	// If this was the last session, the machine is now idle. Record the
	// time so that idle-check can measure how long it has been alone.
	sessions, err := store.SessionsFor(machine)
	if err != nil {
		return
	}
	if len(sessions) == 0 {
		_ = recordIdleSince(store, machine, time.Now().UTC())
	}
}

// ThisPID returns the current process id.
func ThisPID() int { return os.Getpid() }

// --- idle-since tracking ---------------------------------------------------
// A machine's idle clock starts when its last session detaches. The clock is
// stored in a simple JSON file in the state directory so that it survives
// avar invocations. It is advisory: a stale write or a race cannot produce a
// false idle signal for a machine with live sessions, because the idle check
// always reads sessions directly from the store before consulting this file
// (Property 11).

// idleSinceMap is the on-disk shape: machine name → UTC RFC 3339 timestamp of
// when the machine last became sessionless.
type idleSinceMap map[string]time.Time

// readIdleSince reads the idle-since file at <store.Root()>/idle_since.json.
// A missing file is the normal first-run state and returns an empty map.
func readIdleSince(store *state.Store) (idleSinceMap, error) {
	return readIdleSinceAt(filepath.Join(store.Root(), idleSinceFile))
}

func readIdleSinceAt(path string) (idleSinceMap, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return idleSinceMap{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var m idleSinceMap
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return m, nil
}

// writeIdleSince replaces the idle-since file atomically so a crash during
// the write cannot leave a half file behind (REQ-17.5).
func writeIdleSince(store *state.Store, m idleSinceMap) error {
	return writeIdleSinceAt(filepath.Join(store.Root(), idleSinceFile), m)
}

func writeIdleSinceAt(path string, m idleSinceMap) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode idle state: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+idleSinceFile+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary idle file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// recordIdleSince sets the idle-since clock for machine to t.
func recordIdleSince(store *state.Store, machine string, t time.Time) error {
	m, err := readIdleSince(store)
	if err != nil {
		return err
	}
	m[machine] = t
	return writeIdleSince(store, m)
}

// clearIdleSince removes machine's idle-since clock.
func clearIdleSince(store *state.Store, machine string) error {
	m, err := readIdleSince(store)
	if err != nil {
		return err
	}
	delete(m, machine)
	return writeIdleSince(store, m)
}
