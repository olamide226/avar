package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/provider/fake"
)

// schtasksRecorder records what installScheduledTask asked schtasks to do, and
// fails every call when fail is set.
type schtasksRecorder struct {
	calls [][]string
	fail  bool
}

func (s *schtasksRecorder) run(args ...string) error {
	s.calls = append(s.calls, args)
	if s.fail {
		return errors.New("schtasks: access is denied")
	}
	return nil
}

// windowsInstall lays out what avar's Windows archive unpacks into a folder:
// avr.exe, and avrw.exe beside it unless withHelper is false. It returns the
// two paths, and a stamp path in a separate directory, as ensureScheduledTask
// keeps the stamp in avar's state directory rather than beside the binary.
func windowsInstall(t *testing.T, withHelper bool) (bin, helper, stamp string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "avr.exe")
	helper = filepath.Join(dir, windowlessHelper)
	touch(t, bin)
	if withHelper {
		touch(t, helper)
	}
	return bin, helper, filepath.Join(t.TempDir(), scheduledTaskStamp)
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}

// createHelperTask is the exact registration avar makes: the helper, and
// nothing else, as the task's program.
func createHelperTask(helper string) []string {
	return []string{
		"/Create",
		"/TN", "avar-idle-check",
		"/TR", `"` + helper + `"`,
		"/SC", "MINUTE",
		"/MO", strconv.Itoa(idleCheckMinutes),
		"/F",
	}
}

const idleNoticeText = "installed a background idle-check"
const helperMissingText = "idle auto-stop is off"

// REQ-18.16: the scheduled task runs avrw.exe, the GUI-subsystem helper, and not
// avr.exe. avr.exe is a console program, so a task running it in the user's
// session opened a console window on every run.
func TestScheduledTask_RunsTheWindowlessHelper_REQ_18_16(t *testing.T) {
	app := newTestApp(t, fake.New())
	bin, helper, stamp := windowsInstall(t, true)
	st := &schtasksRecorder{}

	installScheduledTask(app.App, stamp, bin, st.run)

	want := [][]string{createHelperTask(helper)}
	if !reflect.DeepEqual(st.calls, want) {
		t.Errorf("schtasks calls:\n got %q\nwant %q", st.calls, want)
	}
	if !strings.Contains(app.err.String(), idleNoticeText) {
		t.Errorf("the first registration did not tell the user:\n%s", app.err.String())
	}
}

// REQ-18.16: a task registered by an earlier avar runs avr.exe directly and
// opens a console window every time it fires. Its stamp names the binary, as
// every released avar wrote it, so the next `avr` replaces it with one that
// runs the helper, silently: this user was told about the check already.
func TestScheduledTask_ReplacesATaskThatRanAvrDirectly_REQ_18_16(t *testing.T) {
	for name, recorded := range map[string]func(bin string) string{
		"binary only":            func(bin string) string { return bin },
		"binary and interval":    func(bin string) string { return bin + "\nevery " + strconv.Itoa(idleCheckMinutes) + " minutes\n" },
		"binary, other interval": func(bin string) string { return bin + "\nevery 10 minutes\n" },
	} {
		t.Run(name, func(t *testing.T) {
			app := newTestApp(t, fake.New())
			bin, helper, stamp := windowsInstall(t, true)
			if err := os.WriteFile(stamp, []byte(recorded(bin)), 0o600); err != nil {
				t.Fatal(err)
			}
			st := &schtasksRecorder{}

			installScheduledTask(app.App, stamp, bin, st.run)

			want := [][]string{createHelperTask(helper)}
			if !reflect.DeepEqual(st.calls, want) {
				t.Errorf("schtasks calls:\n got %q\nwant %q", st.calls, want)
			}
			if strings.Contains(app.err.String(), idleNoticeText) {
				t.Errorf("replacing an existing task announced it as new:\n%s", app.err.String())
			}
		})
	}
}

// REQ-17.1: registration is checked on every environment-creating `avr`, so a
// current task costs one read of the stamp and nothing else: no schtasks, and
// not even a look for the helper. Removing the helper after registration and
// hearing nothing is how the test knows the helper was not looked for.
func TestScheduledTask_CurrentTaskCostsOneRead_REQ_17_1(t *testing.T) {
	app := newTestApp(t, fake.New())
	bin, helper, stamp := windowsInstall(t, true)
	installScheduledTask(app.App, stamp, bin, (&schtasksRecorder{}).run)
	if err := os.Remove(helper); err != nil {
		t.Fatal(err)
	}
	app.err.Reset()

	again := &schtasksRecorder{}
	installScheduledTask(app.App, stamp, bin, again.run)

	if len(again.calls) != 0 {
		t.Errorf("schtasks ran for a task that was already current: %q", again.calls)
	}
	if app.err.Len() != 0 {
		t.Errorf("a current task printed something:\n%s", app.err.String())
	}
}

// REQ-18.16: without the helper avar registers nothing, rather than a task that
// opens a window on the user's desktop every time it runs, and says once that
// idle auto-stop is off and how to turn it on.
func TestScheduledTask_WithoutTheHelperRegistersNothingAndSaysSoOnce_REQ_18_16(t *testing.T) {
	app := newTestApp(t, fake.New())
	bin, _, stamp := windowsInstall(t, false)
	st := &schtasksRecorder{}

	installScheduledTask(app.App, stamp, bin, st.run)

	if len(st.calls) != 0 {
		t.Errorf("schtasks ran with no helper to register: %q", st.calls)
	}
	msg := app.err.String()
	for _, want := range []string{helperMissingText, windowlessHelper, "avr stop"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not mention %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, idleNoticeText) {
		t.Errorf("avar announced a check it did not install:\n%s", msg)
	}

	app.err.Reset()
	again := &schtasksRecorder{}
	installScheduledTask(app.App, stamp, bin, again.run)
	if len(again.calls) != 0 || app.err.Len() != 0 {
		t.Errorf("the second invocation ran %q and printed %q; it should do neither", again.calls, app.err.String())
	}
}

// REQ-18.16: an upgrade that arrives without the helper finds an existing task
// that runs avr.exe directly. Leaving it would keep the window appearing, so
// it is deleted, and the user is told idle auto-stop is off.
func TestScheduledTask_WithoutTheHelperDeletesTheConsoleTask_REQ_18_16(t *testing.T) {
	app := newTestApp(t, fake.New())
	bin, _, stamp := windowsInstall(t, false)
	if err := os.WriteFile(stamp, []byte(bin), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &schtasksRecorder{}

	installScheduledTask(app.App, stamp, bin, st.run)

	want := [][]string{{"/Delete", "/TN", "avar-idle-check", "/F"}}
	if !reflect.DeepEqual(st.calls, want) {
		t.Errorf("schtasks calls:\n got %q\nwant %q", st.calls, want)
	}
	if !strings.Contains(app.err.String(), helperMissingText) {
		t.Errorf("the user was not told idle auto-stop is off:\n%s", app.err.String())
	}
}

// REQ-18.16: putting the helper back turns idle auto-stop on at the next `avr`,
// and says so, because the last thing avar said was that it was off.
func TestScheduledTask_RegistersOnceTheHelperArrives_REQ_18_16(t *testing.T) {
	app := newTestApp(t, fake.New())
	bin, helper, stamp := windowsInstall(t, false)
	installScheduledTask(app.App, stamp, bin, (&schtasksRecorder{}).run)
	touch(t, helper)
	app.err.Reset()

	st := &schtasksRecorder{}
	installScheduledTask(app.App, stamp, bin, st.run)

	want := [][]string{createHelperTask(helper)}
	if !reflect.DeepEqual(st.calls, want) {
		t.Errorf("schtasks calls:\n got %q\nwant %q", st.calls, want)
	}
	if !strings.Contains(app.err.String(), idleNoticeText) {
		t.Errorf("turning idle auto-stop on after saying it was off was not announced:\n%s", app.err.String())
	}
}

// REQ-5.5: a registration schtasks refused is not remembered as done, so the
// next `avr` tries again rather than trusting a task that does not exist.
func TestScheduledTask_FailedRegistrationIsRetried_REQ_5_5(t *testing.T) {
	app := newTestApp(t, fake.New())
	bin, helper, stamp := windowsInstall(t, true)
	installScheduledTask(app.App, stamp, bin, (&schtasksRecorder{fail: true}).run)

	st := &schtasksRecorder{}
	installScheduledTask(app.App, stamp, bin, st.run)

	want := [][]string{createHelperTask(helper)}
	if !reflect.DeepEqual(st.calls, want) {
		t.Errorf("schtasks calls after a failed registration:\n got %q\nwant %q", st.calls, want)
	}
}

// REQ-5.5: winget unpacks the archive into its own package folder and links
// avr.exe onto PATH from another, and os.Executable may report either. The
// helper is found beside the real file when it is not beside the link.
func TestScheduledTask_FindsTheHelperBesideALinkedBinary_REQ_18_16(t *testing.T) {
	app := newTestApp(t, fake.New())
	packageDir, linksDir := t.TempDir(), t.TempDir()
	real := filepath.Join(packageDir, "avr.exe")
	touch(t, real)
	touch(t, filepath.Join(packageDir, windowlessHelper))
	link := filepath.Join(linksDir, "avr.exe")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	// The same comparison the code will make: on macOS t.TempDir sits under
	// /var, itself a link, so the resolved helper path is the one to expect.
	resolvedDir, err := filepath.EvalSymlinks(packageDir)
	if err != nil {
		t.Fatal(err)
	}
	st := &schtasksRecorder{}

	installScheduledTask(app.App, filepath.Join(t.TempDir(), scheduledTaskStamp), link, st.run)

	want := [][]string{createHelperTask(filepath.Join(resolvedDir, windowlessHelper))}
	if !reflect.DeepEqual(st.calls, want) {
		t.Errorf("schtasks calls:\n got %q\nwant %q", st.calls, want)
	}
}

// REQ-5.5: the host scheduler is never pointed at a binary in the temporary
// directory, because the registration would outlive the file: a test binary,
// a helper a test built, `go run`, or an avr.exe opened straight out of a zip.
// This test is such a binary, so it calls the real entry point. Its home and
// PATH are its own, so that if the guard fails, what it registers lands in the
// test's directory and no launchctl or schtasks is found to load it.
func TestIdleScheduler_NeverRegistersATemporaryBinary_REQ_5_5(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !withinDir(self, os.TempDir()) {
		t.Skipf("the test binary %s is not in the temporary directory, so it cannot stand in for one", self)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", "")
	app := newTestApp(t, fake.New())

	ensureIdleScheduler(app.App)

	for _, registration := range []string{
		filepath.Join(home, "Library", "LaunchAgents", launchdPlist),
		filepath.Join(app.store.Root(), scheduledTaskStamp),
	} {
		if fileExists(registration) {
			t.Errorf("a binary in the temporary directory registered the idle check: %s exists", registration)
		}
	}
	if !strings.Contains(app.err.String(), "temporary") {
		t.Errorf("the user was not told why idle auto-stop is not set up:\n%s", app.err.String())
	}
}

// withinDir is what the guard decides with; its own cases are the boundaries.
func TestWithinDir(t *testing.T) {
	dir := t.TempDir()
	for path, want := range map[string]bool{
		filepath.Join(dir, "avr"):        true,
		filepath.Join(dir, "sub", "avr"): true,
		dir:                              true,
		dir + "-sibling" + string(filepath.Separator) + "avr": false,
		filepath.Dir(dir): false,
	} {
		if got := withinDir(path, dir); got != want {
			t.Errorf("withinDir(%q, %q) = %t, want %t", path, dir, got, want)
		}
	}
}
