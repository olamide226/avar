//go:build windows

package state

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockWholeFile is the byte range the lock covers. Windows locks ranges rather
// than files, so "the whole file" is spelled as the largest range there is —
// which also covers the bytes a file this size does not have, since the lock
// file is empty and stays that way.
const lockWholeFile = ^uint32(0)

// openLockFile opens the lock file and takes an exclusive lock on it without
// blocking.
//
// LockFileEx is the closest Windows equivalent of flock(2), and the two
// properties avar depends on are the same: the lock belongs to the open handle
// rather than to the process, so the kernel drops it when the handle closes,
// including the close it performs for a process that was killed (REQ-17.5).
//
// The alternative — opening the file with no share mode at all, so that the
// open is itself the lock — would need no lock call, but it makes the file
// unopenable by anything else, and on Windows "anything else" routinely
// includes an antivirus scanner or the search indexer. A transient sharing
// violation from one of those is indistinguishable from another avr holding
// the lock, and avar would wait out its whole timeout for it. Locking a range
// of a normally-shared file leaves those readers alone.
func openLockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return nil, fmt.Errorf("open avar state lock %s: %w", path, err)
	}

	var overlapped windows.Overlapped
	err = windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		lockWholeFile,
		lockWholeFile,
		&overlapped,
	)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock avar state %s: %w", path, err)
	}
	return f, nil
}

// lockHeldByAnother reports whether a failed attempt should be retried.
//
// ERROR_LOCK_VIOLATION is LOCKFILE_FAIL_IMMEDIATELY declining to wait, which is
// the condition the bounded wait exists for. There is no EINTR counterpart to
// retry on: the call is not interruptible by a signal, so the Unix side's
// second retryable case has no equivalent here rather than a missing one.
func lockHeldByAnother(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

// unlockFile releases the lock ahead of the close that would release it anyway,
// so that a failure to unlock is reported rather than hidden.
func unlockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockWholeFile, lockWholeFile, &overlapped)
}
