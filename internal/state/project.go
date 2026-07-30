package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ProjectID is a project's stable identity: the SHA-256 (hex) of its resolved
// absolute path.
//
// Every spelling of the same directory — relative, trailing slash, "..", or
// through a symlink — resolves to one path and therefore to one id. That is
// what makes a project's remembered choices (isolation, selector overrides)
// stick across invocations from different working directories.
func ProjectID(path string) (string, error) {
	resolved, err := ResolveProjectPath(path)
	if err != nil {
		return "", err
	}
	return hashPath(resolved), nil
}

// ResolveProjectPath turns a user-supplied path into the canonical form avar
// stores and mounts: absolute, with every symlink resolved.
//
// The directory must already exist. A project's identity is the identity of a
// real directory on disk, and there is no way to canonicalise a path that does
// not exist yet: guessing would let two spellings of the same future directory
// hash differently.
func ResolveProjectPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("resolve project directory: no path given")
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve project directory %q: %w", path, err)
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolve project directory %s: no such directory; avr works in a directory that exists on the host: %w", abs, err)
		}
		return "", fmt.Errorf("resolve project directory %s: %w", abs, err)
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect project directory %s: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("resolve project directory %s: not a directory; a project is a directory avr is run from", resolved)
	}
	return resolved, nil
}

func hashPath(resolved string) string {
	sum := sha256.Sum256([]byte(resolved))
	return hex.EncodeToString(sum[:])
}
