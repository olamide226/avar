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

// schtasksRecorder records what installScheduledTask asked schtasks to do. It
// fails every call when fail is set, and answers /Query as schtasks does for a
// task that does not exist when missing is set.
type schtasksRecorder struct {
	calls   [][]string
	fail    bool
	missing bool
}

func (s *schtasksRecorder) run(args ...string) error {
	s.calls = append(s.calls, args)
	if s.fail {
		return errors.New("schtasks: access is denied")
	}
	if s.missing && len(args) > 0 && args[0] == "/Query" {
		return errors.New("exit status 1: ERROR: The system cannot find the file specified.")
	}
	return nil
}

// queryTask is how avar asks whether the task it registered is still there.
var queryTask = []string{"/Query", "/TN", "avar-idle-check"}

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

			want := [][]string{queryTask, createHelperTask(helper)}
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

// REQ-5.9: the notice tells the user to delete the task to turn the check off.
// A registration that no longer matches, because avr.exe moved or an older
// avar made it, is replaced only if the task is still there: one the user
// deleted stays deleted, and avar remembers that so the next `avr` is one read
// again.
func TestScheduledTask_LeavesATaskTheUserDeleted_REQ_5_9(t *testing.T) {
	app := newTestApp(t, fake.New())
	bin, _, stamp := windowsInstall(t, true)
	if err := os.WriteFile(stamp, []byte(scheduledTaskStampContent(`C:\old\avr.exe`)), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &schtasksRecorder{missing: true}

	installScheduledTask(app.App, stamp, bin, st.run)

	if want := [][]string{queryTask}; !reflect.DeepEqual(st.calls, want) {
		t.Errorf("schtasks calls:\n got %q\nwant %q", st.calls, want)
	}
	if app.err.Len() != 0 {
		t.Errorf("respecting the user's deletion printed something:\n%s", app.err.String())
	}

	again := &schtasksRecorder{}
	installScheduledTask(app.App, stamp, bin, again.run)
	if len(again.calls) != 0 {
		t.Errorf("the next invocation ran schtasks for a task the user deleted: %q", again.calls)
	}
}

// REQ-17.1: the existence check is on the rare path only. A first registration,
// and one after avar itself removed the task, create it without asking.
func TestScheduledTask_QueriesOnlyWhenReplacingARegistration_REQ_17_1(t *testing.T) {
	for name, recorded := range map[string]func(bin string) string{
		"no stamp":       nil,
		"helper missing": helperMissingStampContent,
		"disabled":       func(string) string { return idleDisabledStamp },
	} {
		t.Run(name, func(t *testing.T) {
			app := newTestApp(t, fake.New())
			bin, helper, stamp := windowsInstall(t, true)
			if recorded != nil {
				if err := os.WriteFile(stamp, []byte(recorded(bin)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			st := &schtasksRecorder{missing: true}

			installScheduledTask(app.App, stamp, bin, st.run)

			if want := [][]string{createHelperTask(helper)}; !reflect.DeepEqual(st.calls, want) {
				t.Errorf("schtasks calls:\n got %q\nwant %q", st.calls, want)
			}
			if !strings.Contains(app.err.String(), idleNoticeText) {
				t.Errorf("registering again after avar had it off was not announced:\n%s", app.err.String())
			}
		})
	}
}

// REQ-5.9: idle_timeout = "0" turns idle stopping off, so the task that would
// do it is removed rather than left running to stop nothing; the user is told
// once.
func TestScheduledTask_IdleTimeoutZeroRemovesTheTask_REQ_5_9(t *testing.T) {
	app := newTestApp(t, fake.New())
	bin, _, stamp := windowsInstall(t, true)
	installScheduledTask(app.App, stamp, bin, (&schtasksRecorder{}).run)
	app.err.Reset()

	st := &schtasksRecorder{}
	removeScheduledTask(app.App, stamp, st.run)

	if want := [][]string{{"/Delete", "/TN", "avar-idle-check", "/F"}}; !reflect.DeepEqual(st.calls, want) {
		t.Errorf("schtasks calls:\n got %q\nwant %q", st.calls, want)
	}
	if !strings.Contains(app.err.String(), `idle_timeout = "0"`) {
		t.Errorf("the user was not told why the task was removed:\n%s", app.err.String())
	}

	app.err.Reset()
	again := &schtasksRecorder{}
	removeScheduledTask(app.App, stamp, again.run)
	if len(again.calls) != 0 || app.err.Len() != 0 {
		t.Errorf("the second invocation ran %q and printed %q; it should do neither", again.calls, app.err.String())
	}
}

// REQ-5.9: with idle stopping off and nothing registered, there is nothing to
// remove and nothing to say, and avar never removes a task it did not make.
func TestScheduledTask_IdleTimeoutZeroWithNothingRegistered_REQ_5_9(t *testing.T) {
	for name, recorded := range map[string]string{
		"no stamp":         "",
		"helper missing":   helperMissingStampContent(`C:\avr.exe`),
		"removed by user":  removedByUserStamp,
		"already disabled": idleDisabledStamp,
	} {
		t.Run(name, func(t *testing.T) {
			app := newTestApp(t, fake.New())
			stamp := filepath.Join(t.TempDir(), scheduledTaskStamp)
			if recorded != "" {
				if err := os.WriteFile(stamp, []byte(recorded), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			st := &schtasksRecorder{}

			removeScheduledTask(app.App, stamp, st.run)

			if len(st.calls) != 0 || app.err.Len() != 0 {
				t.Errorf("ran %q and printed %q; want neither", st.calls, app.err.String())
			}
		})
	}
}

// REQ-5.9: on macOS idle_timeout = "0" unloads and removes the agent, and
// says so once: with the plist gone there is nothing to say next time.
func TestLaunchdAgent_IdleTimeoutZeroRemovesTheAgent_REQ_5_9(t *testing.T) {
	app := newTestApp(t, fake.New())
	dir := t.TempDir()
	path := writePlist(t, dir, "/opt/homebrew/bin/avr")
	lc := &launchctlCalls{loaded: true}

	removeLaunchdAgent(app.App, dir, lc.run)

	if fileExists(path) {
		t.Error("the agent's plist is still there")
	}
	if want := []string{"list " + launchdLabel, "unload " + path}; !reflect.DeepEqual(lc.calls, want) {
		t.Errorf("launchctl calls:\n got %q\nwant %q", lc.calls, want)
	}
	if !strings.Contains(app.err.String(), `idle_timeout = "0"`) {
		t.Errorf("the user was not told why the agent was removed:\n%s", app.err.String())
	}

	app.err.Reset()
	again := &launchctlCalls{}
	removeLaunchdAgent(app.App, dir, again.run)
	if len(again.calls) != 0 || app.err.Len() != 0 {
		t.Errorf("the second invocation ran %q and printed %q; it should do neither", again.calls, app.err.String())
	}
}

// REQ-5.9: whether a check is wanted is read through the strict config API.
// "0" turns it off, the default and a real timeout keep it on, and a file that
// cannot be read changes nothing, since avar cannot tell what it says.
func TestIdleCheckWanted_FollowsIdleTimeout_REQ_5_9(t *testing.T) {
	for name, tc := range map[string]struct {
		config     string
		wanted, ok bool
	}{
		"no file":         {"", true, true},
		"a timeout":       {"idle_timeout = \"4h\"\n", true, true},
		"zero":            {"idle_timeout = \"0\"\n", false, true},
		"zero, no spaces": {"idle_timeout=\"0\"\n", false, true},
		"unreadable":      {"idle_timout = \"0\"\n", false, false},
	} {
		t.Run(name, func(t *testing.T) {
			app := newTestApp(t, fake.New())
			if tc.config != "" {
				if err := os.WriteFile(app.store.ConfigPath(), []byte(tc.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			wanted, ok := idleCheckWanted(app.App)
			if wanted != tc.wanted || ok != tc.ok {
				t.Errorf("idleCheckWanted = (%t, %t), want (%t, %t)", wanted, ok, tc.wanted, tc.ok)
			}
		})
	}
}

// REQ-18.17: `avar` is the same program as `avr`, so a run started under the
// alias finds the registration its other name made and leaves it alone.
//
// Windows ships avar.exe beside avr.exe, and os.Executable reports whichever
// name was typed. The stamp records that path, so without canonicalBinary the
// alias found a stamp naming avr.exe, took it for a registration to replace,
// and ran /Query and /Create — two subprocesses, on the path REQ-17.1 budgets
// at 500 ms — then wrote its own name back for the next `avr` to undo.
func TestScheduledTask_TheAliasLeavesTheRegistrationAlone_REQ_18_17(t *testing.T) {
	app := newTestApp(t, fake.New())
	bin, _, stamp := windowsInstall(t, true)
	alias := filepath.Join(filepath.Dir(bin), "avar.exe")
	touch(t, alias)

	installScheduledTask(app.App, stamp, canonicalBinary(bin), (&schtasksRecorder{}).run)
	app.err.Reset()

	again := &schtasksRecorder{}
	installScheduledTask(app.App, stamp, canonicalBinary(alias), again.run)

	if len(again.calls) != 0 {
		t.Errorf("`avar` re-registered the task `avr` had registered: %q", again.calls)
	}
	if app.err.Len() != 0 {
		t.Errorf("`avar` printed something about a registration that was current:\n%s", app.err.String())
	}
}

// REQ-18.17: an alias registers as the program it aliases, so both names of one
// installation keep one registration between them. A copy of the alias with no
// canonical binary beside it registers as itself: there is nothing else to
// name, and its own name is at least stable.
func TestCanonicalBinary_AnAliasRegistersAsAvr_REQ_18_17(t *testing.T) {
	dir := t.TempDir()
	lone := t.TempDir()
	for _, name := range []string{"avr", "avr.exe", "avar", "avar.exe", "avrw.exe"} {
		touch(t, filepath.Join(dir, name))
	}
	touch(t, filepath.Join(lone, "avar.exe"))

	cases := []struct {
		name string
		bin  string
		want string
	}{
		{"the alias, beside the canonical name", filepath.Join(dir, "avar"), filepath.Join(dir, "avr")},
		{"the alias on Windows", filepath.Join(dir, "avar.exe"), filepath.Join(dir, "avr.exe")},
		{"the alias however it is spelled", filepath.Join(dir, "AVAR.EXE"), filepath.Join(dir, "avr.exe")},
		{"the canonical name itself", filepath.Join(dir, "avr"), filepath.Join(dir, "avr")},
		{"the canonical name on Windows", filepath.Join(dir, "avr.exe"), filepath.Join(dir, "avr.exe")},
		{"the alias alone", filepath.Join(lone, "avar.exe"), filepath.Join(lone, "avar.exe")},
		{"any other program", filepath.Join(dir, "avrw.exe"), filepath.Join(dir, "avrw.exe")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := canonicalBinary(c.bin); got != c.want {
				t.Errorf("canonicalBinary(%q) = %q, want %q", c.bin, got, c.want)
			}
		})
	}
}
