//go:build windows

package state

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// replaceRetryFor and replaceRetryInterval bound the wait for a reader to let
// go of the file being replaced. A tenth of a second is far longer than a
// scanner needs to read a few kilobytes of JSON, and short enough that a
// genuine permission failure is still reported promptly.
const (
	replaceRetryFor      = 1 * time.Second
	replaceRetryInterval = 10 * time.Millisecond
)

// replaceFile moves tmp over path atomically and durably.
//
// Two things differ from the POSIX side, and both are Windows being Windows
// rather than avar choosing differently.
//
// Durability: the POSIX side flushes the directory after the rename, and there
// is no counterpart here. Asking for one is not a silent no-op but an outright
// failure — a Windows directory cannot be opened for synchronisation, and
// FlushFileBuffers on a directory handle returns ERROR_ACCESS_DENIED, which is
// how every one of avar's state writes failed the first time they ran on
// Windows. MOVEFILE_WRITE_THROUGH asks the same question the platform's own
// way: the call does not return until the replacement is on disk. os.Rename
// already passes MOVEFILE_REPLACE_EXISTING, so the write-through is the whole
// of what calling MoveFileEx directly buys (REQ-17.5, PROP-7).
//
// Open readers: replacing a file that another process has open fails, because
// Go opens files for reading without FILE_SHARE_DELETE. On POSIX, renaming over
// an open file is simply allowed. avar's own readers take the state lock and so
// cannot collide, but a virus scanner, a backup agent, or a developer with
// machines.json open in an editor is outside that discipline and is a normal
// condition on a Windows desktop rather than an error. The bounded retry turns
// a transient reader into a few milliseconds of delay; a real permission
// failure still surfaces, just a second later.
func replaceFile(tmp, path string) error {
	from, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return fmt.Errorf("read the temporary file name %s: %w", tmp, err)
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("read the destination name %s: %w", path, err)
	}

	deadline := time.Now().Add(replaceRetryFor)
	for {
		err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
		if err == nil || !replaceBlockedByReader(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(replaceRetryInterval)
	}
}

// replaceBlockedByReader reports whether a failed replace is somebody else
// holding the destination open, which is worth retrying, rather than a
// permission or path problem, which is not.
func replaceBlockedByReader(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
