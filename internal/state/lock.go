package state

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// ErrLockTimeout reports that the bounded wait for the state lock expired
// because another avr invocation still held it.
var ErrLockTimeout = errors.New("timed out waiting for the avar state lock")

// lockPollInterval is how often a waiting acquirer retries. Neither platform's
// primitive has a timed variant, so a bounded wait is a non-blocking attempt in
// a loop; 20 ms is short enough to be invisible on the warm path (REQ-17.1) and
// long enough not to spin.
const lockPollInterval = 20 * time.Millisecond

// fileLock is an advisory whole-file lock held for the duration of one state
// transaction.
//
// The lock is the open file itself rather than a record written into it, on
// both platforms and for the same reason: an avr killed mid-transaction has its
// descriptors closed by the operating system, so the lock cannot outlive the
// process that took it. A lock file holding a pid would need a liveness check
// and a stale-entry policy to reach the same guarantee (REQ-17.5).
//
// Re-entrancy: a second acquisition from the same process blocks exactly as
// another process would — it would self-deadlock. The store therefore never
// nests acquisitions: the lock is taken once per Store.Update, and every
// operation that needs to run inside a critical section takes a *Tx rather than
// re-acquiring the lock. The bounded wait is the backstop: a caller that does
// nest gets ErrLockTimeout naming the lock file instead of hanging forever.
type fileLock struct {
	path string
	file *os.File
}

// acquireLock takes the exclusive advisory lock on path, creating the lock
// file if needed, and waits at most timeout for a holder to release it. A
// non-positive timeout means "try once".
//
// How a platform makes an open file exclusive is openLockFile's business; this
// loop only has to tell "somebody else holds it" from "this failed" and it asks
// lockHeldByAnother which it is. Errors arrive fully framed from the platform,
// so nothing is wrapped twice on the way out.
func acquireLock(path string, timeout time.Duration) (*fileLock, error) {
	deadline := time.Now().Add(timeout)
	for {
		f, err := openLockFile(path)
		switch {
		case err == nil:
			return &fileLock{path: path, file: f}, nil
		case lockHeldByAnother(err):
			// Held by another invocation, or the attempt was interrupted.
			// Both are retryable.
		default:
			return nil, err
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("%w %s after %s: another avr invocation is still writing avar's state; wait for it to finish, or delete the lock file if no avr process is running: %w",
				ErrLockTimeout, path, timeout, err)
		}
		if remaining > lockPollInterval {
			remaining = lockPollInterval
		}
		time.Sleep(remaining)
	}
}

// release unlocks and closes the lock file. It is safe to call once; closing
// the descriptor would release the lock anyway, but unlocking explicitly keeps
// the intent visible and surfaces errors.
func (l *fileLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil

	unlockErr := unlockFile(f)
	closeErr := f.Close()
	if unlockErr != nil {
		return fmt.Errorf("release avar state lock %s: %w", l.path, unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close avar state lock %s: %w", l.path, closeErr)
	}
	return nil
}
