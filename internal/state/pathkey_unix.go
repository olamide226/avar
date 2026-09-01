//go:build unix

package state

// PathKey reduces a canonical POSIX path to the string a project's identity is
// hashed from.
//
// There is nothing to normalise, and the key is the path itself. A POSIX path is
// case-sensitive, has one separator, and has already been through EvalSymlinks
// by the time it gets here, so two paths that differ at all are two directories.
// Folding case would merge projects the filesystem keeps apart — `~/Code` and
// `~/code` really can both exist on a case-sensitive volume (REQ-11.2).
//
// It carries no prefix either, unlike the Windows key. That is not symmetry
// avar chose to break for its own sake: every project identity avar has ever
// written on macOS is the hash of this exact string, and adding anything to it
// would orphan every existing record and lose every project's remembered
// isolation for nothing gained. The Windows key has no such history to keep.
func PathKey(resolved string) string { return resolved }
