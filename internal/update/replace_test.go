package update

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// memFS is the filesystem replacement sees, as a map from path to contents,
// with a hook that fails a chosen call.
//
// It exists because the half of this code that matters only ever runs on
// Windows — where a running image cannot be overwritten — and because every
// failure worth handling is a failure of one of these four calls. A double
// makes each of them, and the undo for each, an ordinary test on any machine.
// Nothing here touches a real file, and nothing replaces a real binary.
type memFS struct {
	files map[string]string
	// failOn makes one call fail: "rename <old> -> <new>" or
	// "remove <name>".
	failOn string
	calls  []string
}

func newMemFS(files map[string]string) *memFS {
	copied := make(map[string]string, len(files))
	for name, body := range files {
		copied[name] = body
	}
	return &memFS{files: copied}
}

func (m *memFS) record(call string) error {
	m.calls = append(m.calls, call)
	if m.failOn != "" && call == m.failOn {
		return fmt.Errorf("simulated failure: %s", call)
	}
	return nil
}

func (m *memFS) Rename(oldpath, newpath string) error {
	if err := m.record(fmt.Sprintf("rename %s -> %s", oldpath, newpath)); err != nil {
		return err
	}
	body, ok := m.files[oldpath]
	if !ok {
		return fmt.Errorf("rename %s: no such file", oldpath)
	}
	delete(m.files, oldpath)
	m.files[newpath] = body
	return nil
}

func (m *memFS) Remove(name string) error {
	if err := m.record("remove " + name); err != nil {
		return err
	}
	if _, ok := m.files[name]; !ok {
		return fmt.Errorf("remove %s: no such file", name)
	}
	delete(m.files, name)
	return nil
}

func (m *memFS) Exists(name string) bool {
	_, ok := m.files[name]
	return ok
}

// Glob matches the one shape Sweep asks for, <dir><separator>*<suffix>, and
// does it by prefix and suffix rather than with filepath.Match.
//
// filepath.Match is host-specific: on a Unix host a backslash in the pattern
// is an escape, so the Windows paths these tests are written in would match
// nothing, and the double would report that a sweep found nothing to do on
// exactly the host where sweeping matters.
// It also reads both separators as one, because these tests are written in
// Windows paths whatever host runs them, and the filepath.Join in Sweep builds
// its pattern with the host's separator.
func (m *memFS) Glob(pattern string) ([]string, error) {
	star := strings.Index(pattern, "*")
	if star < 0 {
		return nil, fmt.Errorf("the double only understands a pattern with one *: %q", pattern)
	}
	slashes := func(path string) string { return strings.ReplaceAll(path, `\`, "/") }
	prefix, suffix := slashes(pattern[:star]), pattern[star+1:]
	var out []string
	for name := range m.files {
		middle, ok := strings.CutPrefix(slashes(name), prefix)
		if !ok || !strings.HasSuffix(middle, suffix) {
			continue
		}
		if strings.Contains(strings.TrimSuffix(middle, suffix), "/") {
			continue // a glob does not cross into a subdirectory
		}
		out = append(out, name)
	}
	slices.Sort(out)
	return out, nil
}

// contents renders the filesystem as a sorted list, for comparing one state
// with another.
func (m *memFS) contents() string {
	var lines []string
	for name, body := range m.files {
		lines = append(lines, name+"="+body)
	}
	slices.Sort(lines)
	return strings.Join(lines, " ")
}

const (
	unixBin     = "/usr/local/bin/avr"
	winBin      = `C:\Tools\avar\avr.exe`
	winHelper   = `C:\Tools\avar\avrw.exe`
	winInstall  = `C:\Tools\avar`
	oldContents = "the installed avar"
	newContents = "the new avar"
)

func windowsInstall() *memFS {
	return newMemFS(map[string]string{
		winBin:                  oldContents,
		winHelper:               "the installed avrw",
		winBin + NewSuffix:      newContents,
		winHelper + NewSuffix:   "the new avrw",
		winInstall + `\LICENSE`: "Apache-2.0",
	})
}

// macOS renames the new binary over the installed one. The rename is atomic
// and the running process keeps the file it already opened, so there is never
// a moment with no avr installed.
func TestReplace_RenamesTheNewBinaryIntoPlace_REQ_19_6(t *testing.T) {
	fs := newMemFS(map[string]string{unixBin: oldContents, unixBin + NewSuffix: newContents})

	result, err := Replace(fs, StyleRename, []string{unixBin})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if fs.files[unixBin] != newContents {
		t.Errorf("%s = %q, want the new binary", unixBin, fs.files[unixBin])
	}
	if len(fs.files) != 1 {
		t.Errorf("the install directory holds %s, want only the installed binary", fs.contents())
	}
	if len(result.Aside) != 0 {
		t.Errorf("a direct rename left %v aside, and there is nothing to leave", result.Aside)
	}
	if !slices.Equal(result.Replaced, []string{unixBin}) {
		t.Errorf("replaced %v, want %v", result.Replaced, []string{unixBin})
	}
}

// A direct rename cannot be undone, so it is allowed for one binary only: a
// second could be attempted only after the first had become irreversible.
func TestReplace_RefusesMoreThanOneDirectRename_REQ_19_6(t *testing.T) {
	fs := newMemFS(map[string]string{winBin: oldContents, winHelper: "helper"})
	if _, err := Replace(fs, StyleRename, []string{winBin, winHelper}); err == nil {
		t.Error("Replace accepted two targets for a style that cannot undo the first")
	}
	if len(fs.calls) != 0 {
		t.Errorf("it did %v before refusing", fs.calls)
	}
}

// Windows cannot overwrite the image of a running program, so each installed
// file is renamed aside first, and both programs move as one transaction.
func TestReplace_MovesTheRunningBinaryAside_REQ_19_6_REQ_19_7(t *testing.T) {
	fs := windowsInstall()

	result, err := Replace(fs, StyleAside, []string{winBin, winHelper})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if fs.files[winBin] != newContents || fs.files[winHelper] != "the new avrw" {
		t.Errorf("after the update the install holds %s, want both new programs", fs.contents())
	}
	if fs.files[winBin+AsideSuffix] != oldContents {
		t.Errorf("the previous avr.exe is %q, want it kept aside until a later run deletes it", fs.files[winBin+AsideSuffix])
	}
	if !slices.Equal(result.Aside, []string{winBin + AsideSuffix, winHelper + AsideSuffix}) {
		t.Errorf("aside = %v, want both previous programs", result.Aside)
	}

	// Every target is moved aside before any new file is moved in, which
	// is what "both or neither" rests on: the first failure to put a new
	// file in place still has every previous file to put back.
	firstIn := slices.Index(fs.calls, fmt.Sprintf("rename %s -> %s", winBin+NewSuffix, winBin))
	lastAside := slices.Index(fs.calls, fmt.Sprintf("rename %s -> %s", winHelper, winHelper+AsideSuffix))
	if firstIn < 0 || lastAside < 0 || lastAside > firstIn {
		t.Errorf("calls = %v, want every file moved aside before any is moved in", fs.calls)
	}
}

// A file left aside by an earlier update is in the way of this one, so it is
// removed first. When it cannot be (Windows still has it mapped), the rename
// fails and says what to do.
func TestReplace_ClearsALeftoverFromAnEarlierUpdate(t *testing.T) {
	fs := windowsInstall()
	fs.files[winBin+AsideSuffix] = "an avar from two releases ago"

	if _, err := Replace(fs, StyleAside, []string{winBin, winHelper}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if fs.files[winBin+AsideSuffix] != oldContents {
		t.Errorf("the aside file is %q, want the binary this update replaced", fs.files[winBin+AsideSuffix])
	}
}

// An install that never had avrw.exe — somebody who copied only avr.exe out of
// the zip — gains it, rather than having the whole update refused.
func TestReplace_AddsAProgramTheInstallDidNotHave_REQ_19_7(t *testing.T) {
	fs := newMemFS(map[string]string{
		winBin:                oldContents,
		winBin + NewSuffix:    newContents,
		winHelper + NewSuffix: "the new avrw",
	})

	result, err := Replace(fs, StyleAside, []string{winBin, winHelper})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if fs.files[winHelper] != "the new avrw" {
		t.Errorf("avrw.exe = %q, want it installed beside avr.exe", fs.files[winHelper])
	}
	if !slices.Equal(result.Aside, []string{winBin + AsideSuffix}) {
		t.Errorf("aside = %v, want only the file that was there", result.Aside)
	}
}

// PROP-26's third clause: whatever fails, the binaries installed afterwards
// are exactly the ones installed before, and avr.exe and avrw.exe never come
// from different releases.
func TestProp_FailedReplacementLeavesWhatWasInstalled_PROP_26(t *testing.T) {
	before := windowsInstall().contents()

	// Every call a replacement of two Windows programs makes, in order.
	steps := []string{
		fmt.Sprintf("remove %s", winBin+AsideSuffix),
		fmt.Sprintf("rename %s -> %s", winBin, winBin+AsideSuffix),
		fmt.Sprintf("remove %s", winHelper+AsideSuffix),
		fmt.Sprintf("rename %s -> %s", winHelper, winHelper+AsideSuffix),
		fmt.Sprintf("rename %s -> %s", winBin+NewSuffix, winBin),
		fmt.Sprintf("rename %s -> %s", winHelper+NewSuffix, winHelper),
	}

	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			fs := windowsInstall()
			fs.failOn = step

			_, err := Replace(fs, StyleAside, []string{winBin, winHelper})
			removingALeftover := strings.HasPrefix(step, "remove ")
			if removingALeftover {
				// Removing an aside that is not there is expected
				// and ignored: the rename that follows is what
				// reports a file Windows will not let go of.
				if err != nil {
					t.Fatalf("Replace failed on a leftover that was never there: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Replace reported success though %q failed", step)
			}

			// The staged files are still there — the caller
			// discards them — so compare only what is installed.
			for _, target := range []string{winBin, winHelper} {
				fs.files[target+NewSuffix] = map[string]string{winBin: newContents, winHelper: "the new avrw"}[target]
			}
			if got := fs.contents(); got != before {
				t.Errorf("after a failure at %q the install holds\n  %s\nwant what was there before\n  %s", step, got, before)
			}
			if errors.Is(err, ErrPartiallyReplaced) {
				t.Errorf("the undo itself failed, and only the failing step was meant to fail: %v", err)
			}
		})
	}
}

// When the undo cannot put a file back, the user has to repair it by hand, so
// that outcome is its own error rather than one more wrapped cause.
func TestReplace_SaysSoWhenItCannotPutTheBinaryBack_REQ_19_6(t *testing.T) {
	fs := windowsInstall()
	// The new avr.exe cannot be moved in, and the previous one cannot be
	// moved back either.
	fs.failOn = fmt.Sprintf("rename %s -> %s", winBin+NewSuffix, winBin)
	failBack := fmt.Sprintf("rename %s -> %s", winBin+AsideSuffix, winBin)

	// memFS fails one call; failing the undo as well needs a second, so
	// swap the hook once the first failure has happened.
	fs2 := &failTwice{memFS: fs, second: failBack}
	_, err := Replace(fs2, StyleAside, []string{winBin, winHelper})
	if !errors.Is(err, ErrPartiallyReplaced) {
		t.Fatalf("Replace = %v, want ErrPartiallyReplaced", err)
	}
	if !strings.Contains(err.Error(), winBin+AsideSuffix) {
		t.Errorf("error = %v, want it to name the file the user has to move back", err)
	}
}

// failTwice fails the memFS's own failOn call and then one more named call, so
// a test can make the undo fail as well as the step it undoes.
type failTwice struct {
	*memFS
	second string
	failed bool
}

func (f *failTwice) Rename(oldpath, newpath string) error {
	call := fmt.Sprintf("rename %s -> %s", oldpath, newpath)
	if f.failed && call == f.second {
		f.calls = append(f.calls, call)
		return fmt.Errorf("simulated failure: %s", call)
	}
	err := f.memFS.Rename(oldpath, newpath)
	if err != nil {
		f.failed = true
	}
	return err
}

// Sweep is what finally deletes the file a Windows replacement left aside. It
// runs from a later invocation, never from the one that made it, because that
// one may still have the old image mapped.
func TestSweep_RemovesWhatEarlierUpdatesLeft_REQ_19_7(t *testing.T) {
	fs := newMemFS(map[string]string{
		winBin:                  newContents,
		winBin + AsideSuffix:    oldContents,
		winHelper:               "the new avrw",
		winHelper + NewSuffix:   "a staged file a failed update left",
		winInstall + `\LICENSE`: "Apache-2.0",
	})

	removed := Sweep(fs, winInstall)
	if len(removed) != 2 {
		t.Errorf("swept %v, want the aside binary and the staged file", removed)
	}
	if fs.files[winBin] != newContents || fs.files[winHelper] != "the new avrw" {
		t.Errorf("the sweep changed the installed programs: %s", fs.contents())
	}
	if _, ok := fs.files[winInstall+`\LICENSE`]; !ok {
		t.Error("the sweep removed a file that is not avar's leftovers")
	}
	if len(Sweep(fs, winInstall)) != 0 {
		t.Error("a second sweep found something to remove, so the first did not finish")
	}
}
