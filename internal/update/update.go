// Package update decides how a copy of avar was installed and, where avar owns
// the binary, what it takes to replace it with the current release.
//
// Everything here is a decision rather than an effect: which install this is,
// which archive belongs to a host, whether a download is the file the release
// says it is, and in what order files move. The host arrives through two small
// interfaces — an HTTP Doer and a FileOps — and through the paths the caller
// hands in, so that every path, including the Windows one, is exercised on any
// machine without a network, without a real install, and without replacing
// anything (design §3.12, REQ-19).
//
// The package prints nothing and reads no global state. cmd/update.go owns the
// output, as it does for every command.
package update

import (
	"fmt"
	"strings"
)

// Method is how this copy of avar was installed, which decides whether avar
// may replace the binary or must hand the job to whoever owns it.
type Method int

const (
	// MethodArchive is a binary unpacked from a release archive, or built
	// and placed by hand. Nothing else claims to own the file, so avar
	// replaces it itself (REQ-19.4).
	MethodArchive Method = iota
	// MethodHomebrewCask is a binary Homebrew staged in its Caskroom and
	// linked onto the PATH. Homebrew owns the file and records a version
	// for it (REQ-19.2).
	MethodHomebrewCask
	// MethodWinget is a binary the Windows Package Manager unpacked into
	// its package directory, usually reached through a link in WinGet\Links.
	// winget owns the file and records a version for it (REQ-19.3).
	MethodWinget
)

// String names the method for diagnostics and test failure messages.
func (m Method) String() string {
	switch m {
	case MethodHomebrewCask:
		return "homebrew-cask"
	case MethodWinget:
		return "winget"
	case MethodArchive:
		return "archive"
	default:
		return fmt.Sprintf("method(%d)", int(m))
	}
}

// Install describes the copy of avar that is running.
type Install struct {
	// Method is who owns the binary.
	Method Method
	// Path is the file avar would replace: the resolved path rather than
	// the one the user typed, because that is the file on disk.
	Path string
	// Name is what the owning manager knows the package by — the cask name
	// or the winget package identifier. It is empty for MethodArchive.
	Name string
}

// Manager is the name of the package manager that owns this installation, for
// a message that has to say who to go to. It is empty for MethodArchive.
func (i Install) Manager() string {
	switch i.Method {
	case MethodHomebrewCask:
		return "Homebrew"
	case MethodWinget:
		return "winget"
	default:
		return ""
	}
}

// UpgradeCommand is the command that updates an installation its package
// manager owns; ok is false for one avar updates itself.
//
// avar prints this and never runs it. On Windows it could not: `winget
// upgrade` replaces avr.exe, and Windows cannot replace the image of a running
// program, so winget started from inside `avr update` would be asked to
// overwrite its own parent. Both managers can also need elevation and ask
// questions of their own, and when one fails its own message is the one that
// names the fix (design §3.12).
func (i Install) UpgradeCommand() (argv []string, ok bool) {
	switch i.Method {
	case MethodHomebrewCask:
		return []string{"brew", "upgrade", "--cask", i.Name}, true
	case MethodWinget:
		return []string{"winget", "upgrade", i.Name}, true
	default:
		return nil, false
	}
}

// Names a path segment has to match for the manager that owns it to be
// recognised, and the package names used when the path names the manager but
// not the package: a link in WinGet\Links names the command, not the package
// it came from.
const (
	defaultCask     = "avar"
	defaultWingetID = "olamide226.avar"
	caskroomSegment = "Caskroom"
	wingetSegment   = "WinGet"
	packagesSegment = "Packages"
	linksSegment    = "Links"
)

// Detect reads which install this is from the path of the running executable.
//
// It is given both the path avar was invoked as and that path with its links
// resolved, and believes the resolved one first, because both package managers
// keep the real file somewhere else and link to it: Homebrew links
// <prefix>/bin/avr into <prefix>/Caskroom/avar/<version>/, and winget links
// ...\WinGet\Links\avr.exe into ...\WinGet\Packages\olamide226.avar_.../.
// Matching the invoked path alone calls both of them a manual install and
// overwrites a file its manager believes it owns.
func Detect(goos, invoked, resolved string) Install {
	if resolved == "" {
		resolved = invoked
	}
	for _, path := range []string{resolved, invoked} {
		if path == "" {
			continue
		}
		if name, ok := caskName(goos, path); ok {
			return Install{Method: MethodHomebrewCask, Path: resolved, Name: name}
		}
		if name, ok := wingetPackage(goos, path); ok {
			return Install{Method: MethodWinget, Path: resolved, Name: name}
		}
	}
	return Install{Method: MethodArchive, Path: resolved}
}

// caskName reports whether a path lies inside a Homebrew Caskroom, and which
// cask staged it: .../Caskroom/<cask>/<version>/avr.
func caskName(goos, path string) (string, bool) {
	if goos != "darwin" {
		return "", false
	}
	parts := segments(path)
	for i, part := range parts {
		if !strings.EqualFold(part, caskroomSegment) {
			continue
		}
		if i+1 < len(parts) {
			return parts[i+1], true
		}
		return defaultCask, true
	}
	return "", false
}

// wingetPackage reports whether a path lies inside the Windows Package
// Manager's own directories, and which package identifier it carries.
//
// Two shapes count. ...\WinGet\Packages\<id>_<source hash>\avr.exe is where
// winget unpacks a portable package, and the identifier is the part of that
// folder name before the first underscore. ...\WinGet\Links\avr.exe is the
// link winget puts on the PATH; it names the command rather than the package,
// so the identifier falls back to avar's own.
func wingetPackage(goos, path string) (string, bool) {
	if goos != "windows" {
		return "", false
	}
	parts := segments(path)
	for i, part := range parts {
		if !strings.EqualFold(part, wingetSegment) || i+1 >= len(parts) {
			continue
		}
		switch {
		case strings.EqualFold(parts[i+1], packagesSegment):
			if i+2 < len(parts) {
				if id, _, found := strings.Cut(parts[i+2], "_"); found && id != "" {
					return id, true
				}
			}
			return defaultWingetID, true
		case strings.EqualFold(parts[i+1], linksSegment):
			return defaultWingetID, true
		}
	}
	return "", false
}

// segments splits a path on both separators, so a Windows path reads the same
// way on any host. That matters for the tests as much as for the code: these
// tables run everywhere, and a Windows install is diagnosed from a macOS
// developer's machine as often as from Windows itself.
func segments(path string) []string {
	parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
