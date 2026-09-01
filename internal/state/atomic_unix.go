//go:build unix

package state

import (
	"fmt"
	"os"
	"path/filepath"
)

// replaceFile moves tmp over path and makes the move itself durable.
//
// rename(2) within a directory is atomic: a concurrent reader sees the old file
// or the new one and never a mixture. Atomic is not the same as durable,
// though — the bytes were fsynced before this is called, but the directory
// entry pointing at them has not been, so a crash could leave the entry unwritten
// and the new contents unreachable. Flushing the directory closes that gap
// (REQ-17.5, PROP-7).
func replaceFile(tmp, path string) error {
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// syncDir flushes a directory's own entries to disk.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s to flush its directory entry: %w", dir, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("flush directory %s: %w", dir, err)
	}
	return nil
}

// replaceBlockedByReader reports whether a file operation failed because
// somebody else has the file open. Nothing does on POSIX: renaming over an open
// file is allowed, and a reader keeps the file it opened. It exists so that the
// test proving atomicity to an outside observer reads the same on both hosts.
func replaceBlockedByReader(error) bool { return false }
