//go:build unix

package state

import (
	"errors"
	"syscall"
)

// processAlive probes a pid with signal 0.
//
// Signal 0 runs the kernel's existence and permission checks without delivering
// anything: nil means the process exists and we may signal it, EPERM means it
// exists but belongs to another user, ESRCH means it is gone.
//
// Non-positive pids are never probed — 0 and negative values address process
// groups, so signalling them would be both meaningless here and dangerous.
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
