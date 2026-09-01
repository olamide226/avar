package state

import (
	"github.com/olamide226/avar/internal/types"
)

// processAlive reports whether pid names a process that still exists.
//
// Each platform answers it with the cheapest probe that does not disturb the
// process: a signal that is never delivered on Unix, a handle that is opened
// and immediately closed on Windows. Both treat "the process exists but belongs
// to another user" as alive, because it does.
//
// Pid reuse is not detectable either way: if the operating system has recycled
// a crashed session's pid, that session looks live until the new process exits.
// avar accepts that. Sessions are removed on clean exit, so the window only
// opens after a crash, and the only consequence is that idle auto-stop leaves a
// machine running longer than necessary (never that it stops a machine
// somebody is using, which is the property that matters).

// usableSessions returns the entries of in that avar may still act on.
//
// Two kinds of entry are dropped, both silently, because neither is an error
// condition: an entry whose pid is dead (the expected residue of a crashed or
// kill -9'd avr) and an entry naming a machine outside avar's namespace, which
// avar must never act on regardless of how it got into the file (REQ-5.4).
func usableSessions(in []types.SessionRecord) []types.SessionRecord {
	out := make([]types.SessionRecord, 0, len(in))
	for _, s := range in {
		if types.ValidateMachineName(s.Machine) != nil {
			continue
		}
		if !processAlive(s.PID) {
			continue
		}
		out = append(out, s)
	}
	return out
}
