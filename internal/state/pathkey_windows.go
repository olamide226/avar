//go:build windows

package state

import (
	"path/filepath"
	"strings"
)

// pathKeyPrefix labels the vocabulary a key is written in.
//
// It is part of the hashed input so that a key can never be mistaken for one
// written by the other platform. Nothing today can produce both — a state
// directory belongs to one machine — but a key is a stored identity, and an
// identity with no namespace in it is one that cannot be told apart later.
const pathKeyPrefix = "windows:"

// PathKey reduces a canonical Windows path to the string a project's identity is
// hashed from.
//
// Windows filesystems are case-insensitive and case-preserving, and its path
// syntax accepts both separators. `C:\Code\App`, `c:/code/app` and
// `C:\Code\App\` are one directory, and a user who reaches the same project by
// different routes — a shortcut, a shell that lower-cases, a script that builds
// a path by joining with forward slashes — must get the same environment and the
// same remembered choices, not a second project record with a second machine
// behind it (REQ-18.13, PROP-14).
//
// The normalisation is: separators to backslashes, redundant segments removed,
// a trailing separator dropped except on a drive root where it is part of the
// name, and the whole thing lower-cased.
//
// Only the key is normalised. ProjectRecord.Path keeps the spelling the user's
// filesystem gave, because that is what avar shows them and what it shares into
// the guest; a lower-cased path would be correct to Windows and wrong on screen.
func PathKey(resolved string) string {
	key := filepath.Clean(resolved)
	key = filepath.FromSlash(key)

	// filepath.Clean leaves a drive root as `C:\`, where the separator is part
	// of the name, and strips it everywhere else — so there is nothing more to
	// trim, and trimming anyway would turn `C:\` into `C:`, which names the
	// drive's current directory rather than its root.

	// Case folding is the whole point, and simple lower-casing is the right
	// rule for it here: Windows compares paths with an uppercase mapping over
	// the whole of Unicode, but avar only needs two spellings of one path to
	// agree with each other, which any consistent fold gives.
	return pathKeyPrefix + strings.ToLower(key)
}
