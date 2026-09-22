package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The suffixes a replacement works with, both beside the installed binary.
//
// Neither is a name a shell would find: on Windows a file called avr.exe.avr-old
// is not an executable PATH will run, and on macOS neither is on the PATH under
// the binary's own name.
const (
	// NewSuffix marks a verified binary that is written but not yet in
	// place.
	NewSuffix = ".avr-new"
	// AsideSuffix marks the binary a Windows replacement moved out of the
	// way, which a later run deletes.
	AsideSuffix = ".avr-old"
)

// FileOps is the whole of the filesystem that replacement uses.
//
// It is an interface because the interesting half of this code only ever runs
// on Windows, where a running image cannot be overwritten, and because every
// failure worth handling is a failure of one of these four calls. With a
// double in front of them the Windows ordering, and the undo for each step
// that can fail, are ordinary unit tests on any machine.
type FileOps interface {
	Rename(oldpath, newpath string) error
	Remove(name string) error
	Exists(name string) bool
	Glob(pattern string) ([]string, error)
}

// OSFileOps is the real filesystem.
type OSFileOps struct{}

func (OSFileOps) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }
func (OSFileOps) Remove(name string) error             { return os.Remove(name) }
func (OSFileOps) Glob(pattern string) ([]string, error) {
	return filepath.Glob(pattern)
}

// Exists reports whether a path is there, treating any error as absent, which
// is the right answer for a file whose only question is whether it has to be
// moved out of the way.
func (OSFileOps) Exists(name string) bool {
	_, err := os.Stat(name)
	return err == nil
}

// Style is how a host puts a new binary in place of one that may be running.
type Style int

const (
	// StyleRename renames the new file over the installed one. rename(2)
	// is atomic and a running process keeps the file it already opened, so
	// there is never a moment with no avr on the host.
	StyleRename Style = iota
	// StyleAside renames the installed file out of the way first, because
	// Windows refuses to replace the image of a running program. The file
	// left aside is deleted by a later run, not by this one, which may
	// still have it mapped.
	StyleAside
)

// StyleFor is the replacement style a host needs.
func StyleFor(goos string) Style {
	if goos == "windows" {
		return StyleAside
	}
	return StyleRename
}

// Result records what a replacement did, so the command layer can say it.
type Result struct {
	// Replaced are the installed paths that now hold the new binary.
	Replaced []string
	// Aside are the files the replacement moved out of the way and left
	// for a later run to delete.
	Aside []string
}

// ErrPartiallyReplaced reports a replacement that failed and could not be
// undone completely. It is the one outcome a user has to repair by hand, so it
// is a sentinel a caller can recognise rather than one more wrapped error.
var ErrPartiallyReplaced = errors.New("avar could not put the previous binary back")

// Replace puts each target's staged file (target + NewSuffix) in place of the
// target.
//
// With StyleAside every target is moved aside before any new file is moved in,
// and a failure at any point is undone: files already in place are removed and
// every file moved aside is renamed back. That is what makes "both or neither"
// true of avr.exe and avrw.exe, which have to come from the same release or
// the idle check breaks (REQ-19.7, #100).
//
// StyleRename accepts exactly one target. A direct rename cannot be undone —
// the file it overwrote is gone — so a second one could only ever be attempted
// after the first had become irreversible.
func Replace(ops FileOps, style Style, targets []string) (Result, error) {
	if len(targets) == 0 {
		return Result{}, errors.New("replace nothing: no binary was named")
	}
	if style == StyleRename && len(targets) > 1 {
		return Result{}, fmt.Errorf("replace %d binaries with one rename each: a rename that overwrites cannot be undone, so more than one of them cannot be all-or-nothing", len(targets))
	}

	var done Result
	fail := func(err error) (Result, error) {
		if undoErr := undo(ops, done); undoErr != nil {
			return done, fmt.Errorf("%w: %w, and putting things back failed: %w", ErrPartiallyReplaced, err, undoErr)
		}
		return Result{}, err
	}

	if style == StyleAside {
		for _, target := range targets {
			if !ops.Exists(target) {
				// Nothing is installed under this name — an
				// install that never had avrw.exe, for
				// instance. There is nothing to move aside,
				// and the new file is still put in place.
				continue
			}
			aside := target + AsideSuffix
			// A leftover from an earlier update is in the way.
			// Removing it can fail because it is still mapped, in
			// which case the rename below fails too and says so.
			_ = ops.Remove(aside)
			if err := ops.Rename(target, aside); err != nil {
				return fail(fmt.Errorf("move %s aside: %w (another avar may still be running; close it and try again)", filepath.Base(target), err))
			}
			done.Aside = append(done.Aside, aside)
		}
	}

	for _, target := range targets {
		if err := ops.Rename(target+NewSuffix, target); err != nil {
			return fail(fmt.Errorf("put the new %s in place: %w", filepath.Base(target), err))
		}
		done.Replaced = append(done.Replaced, target)
	}
	return done, nil
}

// undo reverses a partial replacement: the files already in place are removed,
// then every file moved aside is renamed back, so what is installed afterwards
// is exactly what was installed before.
func undo(ops FileOps, done Result) error {
	var errs []error
	for _, target := range done.Replaced {
		if err := ops.Remove(target); err != nil {
			errs = append(errs, fmt.Errorf("remove the new %s: %w", target, err))
		}
	}
	for _, aside := range done.Aside {
		target := aside[:len(aside)-len(AsideSuffix)]
		if err := ops.Rename(aside, target); err != nil {
			errs = append(errs, fmt.Errorf("put %s back as %s: %w", aside, target, err))
		}
	}
	return errors.Join(errs...)
}

// Discard removes the staged files a failed update left beside the installed
// binaries, so a later run does not find a half-prepared update it knows
// nothing about.
func Discard(ops FileOps, targets []string) {
	for _, target := range targets {
		_ = ops.Remove(target + NewSuffix)
	}
}

// Sweep deletes what earlier updates left in a directory: the binary a Windows
// replacement moved aside, and any staged file a failed one left behind.
//
// It runs at the start of `avr update` and from the scheduled idle check,
// which is the one avar that runs regularly and never on the warm path
// REQ-17.1 budgets. A file that is still mapped refuses to be deleted, and
// that is fine — it is inert, and the next sweep gets it.
func Sweep(ops FileOps, dir string) []string {
	var removed []string
	for _, suffix := range []string{AsideSuffix, NewSuffix} {
		matches, err := ops.Glob(filepath.Join(dir, "*"+suffix))
		if err != nil {
			continue
		}
		for _, match := range matches {
			if err := ops.Remove(match); err == nil {
				removed = append(removed, match)
			}
		}
	}
	return removed
}
