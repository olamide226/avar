//go:build windows

package state

import (
	"errors"

	"golang.org/x/sys/windows"
)

// processAlive opens a handle on the process and asks whether it has finished.
//
// Windows has no signal 0, and the obvious substitute is wrong: obtaining a
// handle alone does not prove a live process, because a handle keeps the
// process *object* alive after the process itself exits so that its exit code
// stays readable. The question therefore has to be asked of the object, and
// WaitForSingleObject with a zero timeout asks it without waiting — a process
// object becomes signalled when the process terminates, so WAIT_TIMEOUT means
// it has not.
//
// GetExitCodeProcess is the more direct-looking call and is the wrong one: it
// reports STILL_ACTIVE (259) for a running process and cannot distinguish that
// from a process that genuinely exited with status 259 — which avr does
// whenever a guest command does (PROP-3).
//
// PROCESS_QUERY_LIMITED_INFORMATION is deliberately the weaker right: it is
// granted across integrity levels where PROCESS_QUERY_INFORMATION is not, so a
// session started from an elevated terminal is still visible to an ordinary
// one. Non-positive pids are never probed, matching the Unix side, where they
// address process groups rather than processes.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// A process owned by another user is alive, which is the answer
		// kill(2) gives with EPERM. Anything else — most often
		// ERROR_INVALID_PARAMETER, for a pid that no longer exists — is not.
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	event, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		// The handle was obtained, so the process existed a moment ago and
		// avar has no evidence that it stopped. Reporting it alive keeps a
		// machine running that may still be in use, which is the safe
		// direction for idle auto-stop (PROP-11).
		return true
	}
	return event == uint32(windows.WAIT_TIMEOUT)
}
