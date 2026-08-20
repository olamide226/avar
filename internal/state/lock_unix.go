//go:build unix

package state

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openLockFile opens the lock file and takes flock(2)'s exclusive lock on it
// without blocking.
//
// flock is associated with the open file description rather than the process,
// which is what gives the guarantee fileLock documents: the kernel releases the
// lock when the descriptor is closed, including the close it performs for a
// process that was killed.
func openLockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, filePerm)
	if err != nil {
		return nil, fmt.Errorf("open avar state lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock avar state %s: %w", path, err)
	}
	return f, nil
}

// lockHeldByAnother reports whether a failed attempt should be retried.
//
// EWOULDBLOCK is the lock being held by somebody else, which is the condition
// the bounded wait exists for. EINTR is a signal arriving mid-call and says
// nothing about the lock at all, so it is retried rather than reported.
func lockHeldByAnother(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR)
}

// unlockFile releases the lock ahead of the close that would release it anyway,
// so that a failure to unlock is reported rather than hidden.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
