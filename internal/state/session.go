package state

import (
	"errors"
	"syscall"

	"github.com/olamide226/avar/internal/types"
)

// processAlive reports whether pid names a process that still exists.
//
// Signal 0 runs the kernel's existence and permission checks without
// delivering anything: nil means the process exists and we may signal it,
// EPERM means it exists but belongs to another user, ESRCH means it is gone.
// Non-positive pids are never probed — 0 and negative values address process
// groups, so signalling them would be both meaningless here and dangerous.
//
// Pid reuse is not detectable this way: if the kernel has recycled a crashed
// session's pid, that session looks live until the new process exits. avar
// accepts that. Sessions are removed on clean exit, so the window only opens
// after a crash, and the only consequence is that idle auto-stop leaves a
// machine running longer than necessary (never that it stops a machine
// somebody is using, which is the property that matters).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil:
		return true
	case errors.Is(err, syscall.EPERM):
		return true
	default:
		return false
	}
}

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
