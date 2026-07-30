//go:build unix

package lima

import (
	"os"
	"path/filepath"
	"syscall"
)

// diffDiskFile is the machine's writable disk image inside its Lima instance
// directory. It is sparse: its apparent size is the disk the guest believes it
// has, while the blocks actually allocated are what the machine costs the host.
const diffDiskFile = "diffdisk"

// blockSize is the unit st_blocks counts in, fixed at 512 bytes by POSIX
// regardless of the filesystem's own block size.
const blockSize = 512

// diskUsedGB reports how much host disk the machine's image is really taking, in
// gibibytes, which is the "disk usage" `avr status` shows (REQ-5.1).
//
// An unreadable or unrecognised instance directory reports zero rather than an
// error: a status listing that fails because one number could not be read is
// worse than a status listing with one number missing.
func diskUsedGB(instanceDir string) float64 {
	if instanceDir == "" {
		return 0
	}
	info, err := os.Stat(filepath.Join(instanceDir, diffDiskFile))
	if err != nil {
		return 0
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return gibibytes(stat.Blocks * blockSize)
}
