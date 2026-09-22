package update

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// ChecksumsAsset is the name of the file a release publishes its checksums in.
const ChecksumsAsset = "checksums.txt"

// ParseChecksums reads the checksums.txt a release publishes: one line per
// file, a lower-case SHA-256 in hexadecimal, two spaces, the file's name.
//
// The reader is strict, and deliberately so. A lenient one turns a line it
// cannot read into a file it has no checksum for, which is the same outcome as
// a release that never published one — and avar's answer to "no checksum" has
// to be a refusal, not a shrug (docs/lessons.md, "A lenient reader turns every
// mistake into a setting that silently does not apply").
func ParseChecksums(body []byte) (map[string]string, error) {
	sums := make(map[string]string)
	for i, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		sum, name, ok := strings.Cut(line, "  ")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("line %d is not \"<sha256>  <file>\": %q", i+1, line)
		}
		if _, err := hex.DecodeString(sum); err != nil || len(sum) != sha256.Size*2 {
			return nil, fmt.Errorf("line %d does not start with a SHA-256 checksum: %q", i+1, sum)
		}
		sums[name] = strings.ToLower(sum)
	}
	if len(sums) == 0 {
		return nil, fmt.Errorf("it lists no files, so nothing in this release can be verified")
	}
	return sums, nil
}

// ChecksumFor returns the checksum a release recorded for one file.
//
// A file the checksums do not mention is an error rather than a file to trust:
// an unverifiable download and a bad download are the same thing to a program
// that is about to run it (REQ-19.5).
func ChecksumFor(sums map[string]string, name string) (string, error) {
	sum, ok := sums[name]
	if !ok {
		return "", fmt.Errorf("%s lists no checksum for %s, so avar cannot tell whether a download of it is the file this release published: %w", ChecksumsAsset, name, ErrChecksumMismatch)
	}
	return sum, nil
}
