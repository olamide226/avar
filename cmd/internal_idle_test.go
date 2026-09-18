package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/session"
	"github.com/olamide226/avar/internal/types"
)

// idleMachine seeds an environment in the given state whose last session has
// already detached, under an idle timeout short enough that the next idle check
// finds it idle.
func idleMachine(t *testing.T, app *testApp, f *fake.Fake, name string, selector types.EnvironmentSelector, state types.MachineState) {
	t.Helper()
	seedMachine(t, f, name, selector, types.KindShared)
	f.SetMachineState(name, state)
	if err := app.store.PutMachine(types.MachineRecord{
		Name: name, Provider: fake.ProviderID, Selector: selector, Kind: types.KindShared,
	}); err != nil {
		t.Fatalf("recording %s: %v", name, err)
	}
	if err := session.Attach(app.store, name, os.Getpid()); err != nil {
		t.Fatalf("attaching a session to %s: %v", name, err)
	}
	session.Detach(app.store, name, os.Getpid())
}

func idleAfterOneNanosecond(t *testing.T, app *testApp) {
	t.Helper()
	if err := os.WriteFile(app.store.ConfigPath(), []byte("idle_timeout = \"1ns\"\n"), 0o600); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}
}

// Idle auto-stop asks every idle environment that is running or stopped to
// stop. A stopped one is included because Stop converges on stopped rather than
// performing a shutdown, and for a backend that can leave processes behind
// after the machine itself has stopped, converging is what releases them —
// which the idle check never did, so only an explicit `avr stop` ever cleaned
// up. A machine that is coming up or broken is still left alone.
func TestIdleCheck_ConvergesStoppedEnvironmentsToo_REQ_5_5(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	idleAfterOneNanosecond(t, app)

	fedora := types.EnvironmentSelector{Distro: types.DistroFedora, Version: "42", Arch: types.HostArch()}
	debian := types.EnvironmentSelector{Distro: types.DistroDebian, Version: "13", Arch: types.HostArch()}
	idleMachine(t, app, f, "avr-ubuntu-24.04-arm64", ubuntu(), types.StateRunning)
	idleMachine(t, app, f, "avr-fedora-42-arm64", fedora, types.StateStopped)
	idleMachine(t, app, f, "avr-debian-13-arm64", debian, types.StateBroken)

	f.SetStopWarning("avr-fedora-42-arm64", "Fedora 42 had a leftover process; avar ended it.")

	if err := runIdleCheck(context.Background(), app.App); err != nil {
		t.Fatalf("avr internal idle-check: %v", err)
	}

	stopped := map[string]bool{}
	for _, call := range f.CallsFor(fake.OpStop) {
		stopped[call.Machine] = true
	}
	for name, want := range map[string]bool{
		"avr-ubuntu-24.04-arm64": true,
		"avr-fedora-42-arm64":    true,
		"avr-debian-13-arm64":    false,
	} {
		if stopped[name] != want {
			t.Errorf("idle check asked %s to stop: %t, want %t (calls: %v)", name, stopped[name], want, f.Calls())
		}
	}
	f.AssertMachineState(t, "avr-ubuntu-24.04-arm64", types.StateStopped)

	// Nobody is watching a scheduled check, but one run by hand says what it did.
	if !strings.Contains(app.err.String(), "avar ended it") {
		t.Errorf("idle check discarded what the backend reported:\n%s", app.err.String())
	}
}

// Property 11 still holds with stopped environments included: one with a live
// session is not touched, whatever its state.
func TestIdleCheck_NeverTouchesAnEnvironmentWithALiveSession_PROP_11(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	idleAfterOneNanosecond(t, app)

	idleMachine(t, app, f, "avr-ubuntu-24.04-arm64", ubuntu(), types.StateStopped)
	if err := session.Attach(app.store, "avr-ubuntu-24.04-arm64", os.Getpid()); err != nil {
		t.Fatal(err)
	}

	if err := runIdleCheck(context.Background(), app.App); err != nil {
		t.Fatalf("avr internal idle-check: %v", err)
	}
	if n := f.Count(fake.OpStop); n != 0 {
		t.Errorf("idle check stopped an environment with a live session: %v", f.Calls())
	}
}

// launchctlCalls records what installLaunchdAgent asked launchctl to do, and
// answers `launchctl list <label>` as loaded or not.
type launchctlCalls struct {
	loaded bool
	calls  []string
}

func (l *launchctlCalls) run(args ...string) error {
	l.calls = append(l.calls, strings.Join(args, " "))
	if len(args) > 0 && args[0] == "list" && !l.loaded {
		return errors.New("Could not find service")
	}
	return nil
}

func writePlist(t *testing.T, dir, bin string) string {
	t.Helper()
	path := filepath.Join(dir, launchdPlist)
	if err := os.WriteFile(path, []byte(launchdPlistContent(bin)), 0o644); err != nil {
		t.Fatalf("writing a plist: %v", err)
	}
	return path
}

func readPlist(t *testing.T, path string) string {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the plist: %v", err)
	}
	return string(got)
}

// REQ-5.5: an agent that points at a binary which is no longer this one, left
// by an upgrade that moved avr or by anything else, is rewritten and reloaded.
// Before this, an existing plist was trusted whatever it said, so an agent
// pointing at a deleted binary failed every ten minutes for good and idle
// auto-stop silently never ran again.
func TestLaunchdAgent_RepairsAnAgentPointingAtAnotherBinary_REQ_5_5(t *testing.T) {
	app := newTestApp(t, fake.New())
	dir := t.TempDir()
	path := writePlist(t, dir, "/private/var/folders/go-build123/b001/cmd.test")
	lc := &launchctlCalls{loaded: true}

	installLaunchdAgent(app.App, dir, "/opt/homebrew/bin/avr", lc.run)

	if got := readPlist(t, path); got != launchdPlistContent("/opt/homebrew/bin/avr") {
		t.Errorf("the plist still points elsewhere:\n%s", got)
	}
	want := []string{"list " + launchdLabel, "unload " + path, "load " + path}
	if strings.Join(lc.calls, "|") != strings.Join(want, "|") {
		t.Errorf("launchctl calls = %q, want %q", lc.calls, want)
	}
	if strings.Contains(app.err.String(), "installed a background idle-check") {
		t.Error("repairing an agent announced it again, as though it were new")
	}
}

// An agent the user unloaded is left unloaded when it is repaired: the file is
// corrected so a later login runs the right binary, but avar does not turn back
// on something the user turned off.
func TestLaunchdAgent_RepairsWithoutReloadingAnAgentTheUserUnloaded_REQ_5_5(t *testing.T) {
	app := newTestApp(t, fake.New())
	dir := t.TempDir()
	path := writePlist(t, dir, "/opt/homebrew/Caskroom/avar/0.2.1/avr")
	lc := &launchctlCalls{loaded: false}

	installLaunchdAgent(app.App, dir, "/opt/homebrew/Caskroom/avar/0.8.0/avr", lc.run)

	if got := readPlist(t, path); got != launchdPlistContent("/opt/homebrew/Caskroom/avar/0.8.0/avr") {
		t.Errorf("the plist was not corrected:\n%s", got)
	}
	for _, call := range lc.calls {
		if strings.HasPrefix(call, "load") {
			t.Errorf("reloaded an agent the user had unloaded: %q", lc.calls)
		}
	}
}

// REQ-17.1: this runs on every `avr`, warm or cold, so an agent that is already
// right costs one read and no launchctl at all.
func TestLaunchdAgent_LeavesACurrentAgentAlone_REQ_17_1(t *testing.T) {
	app := newTestApp(t, fake.New())
	dir := t.TempDir()
	writePlist(t, dir, "/opt/homebrew/bin/avr")
	lc := &launchctlCalls{loaded: true}

	installLaunchdAgent(app.App, dir, "/opt/homebrew/bin/avr", lc.run)

	if len(lc.calls) != 0 {
		t.Errorf("launchctl was run for an agent that was already current: %q", lc.calls)
	}
}

// The first installation writes, loads, and says what it did, once.
func TestLaunchdAgent_InstallsAndAnnouncesTheFirstTime_REQ_5_5(t *testing.T) {
	app := newTestApp(t, fake.New())
	dir := filepath.Join(t.TempDir(), "LaunchAgents")
	lc := &launchctlCalls{}

	installLaunchdAgent(app.App, dir, "/opt/homebrew/bin/avr", lc.run)

	path := filepath.Join(dir, launchdPlist)
	if got := readPlist(t, path); got != launchdPlistContent("/opt/homebrew/bin/avr") {
		t.Errorf("the plist is not the expected agent:\n%s", got)
	}
	if strings.Join(lc.calls, "|") != "load "+path {
		t.Errorf("launchctl calls = %q, want only a load", lc.calls)
	}
	if !strings.Contains(app.err.String(), "installed a background idle-check") {
		t.Error("the first installation did not tell the user")
	}
}

// schtasksCalls records what installScheduledTask asked schtasks to do.
type schtasksCalls struct{ calls [][]string }

func (s *schtasksCalls) run(args ...string) error {
	s.calls = append(s.calls, args)
	return nil
}

// modifier returns the /MO value of the one /Create call, or "".
func (s *schtasksCalls) modifier(t *testing.T) string {
	t.Helper()
	if len(s.calls) != 1 {
		t.Fatalf("schtasks ran %d times, want exactly one /Create: %q", len(s.calls), s.calls)
	}
	args := s.calls[0]
	for i, a := range args {
		if a == "/MO" && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("the /Create call has no /MO: %q", args)
	return ""
}

// REQ-5.5: the idle check runs every 30 minutes. A task registered by an older
// avar at the old interval, whose stamp names the same binary, is registered
// again at the new one on the next invocation; the binary alone must not decide
// that the task is current.
func TestScheduledTask_ReregistersATaskAtTheOldInterval_REQ_5_5(t *testing.T) {
	app := newTestApp(t, fake.New())
	stamp := filepath.Join(t.TempDir(), scheduledTaskStamp)
	bin := `C:\Users\ola\AppData\Local\Microsoft\WinGet\Links\avr.exe`
	// What avar up to this change wrote: the binary path and nothing else.
	if err := os.WriteFile(stamp, []byte(bin), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &schtasksCalls{}

	installScheduledTask(app.App, stamp, bin, st.run)

	if got := st.modifier(t); got != "30" {
		t.Errorf("the task was registered every %s minutes, want 30", got)
	}
	if strings.Contains(app.err.String(), "installed a background idle-check") {
		t.Error("re-registering an existing task announced it again, as though it were new")
	}
}

// REQ-17.1: this runs on every environment-creating invocation, so a task that
// is already current costs one file read and no schtasks at all.
func TestScheduledTask_LeavesACurrentTaskAlone_REQ_17_1(t *testing.T) {
	app := newTestApp(t, fake.New())
	stamp := filepath.Join(t.TempDir(), scheduledTaskStamp)
	bin := `C:\Program Files\avar\avr.exe`
	first := &schtasksCalls{}
	installScheduledTask(app.App, stamp, bin, first.run)

	again := &schtasksCalls{}
	installScheduledTask(app.App, stamp, bin, again.run)

	if len(again.calls) != 0 {
		t.Errorf("schtasks ran for a task that was already current: %q", again.calls)
	}
}

// The first registration runs every 30 minutes and tells the user, once.
func TestScheduledTask_RegistersEveryThirtyMinutesTheFirstTime_REQ_5_5(t *testing.T) {
	app := newTestApp(t, fake.New())
	stamp := filepath.Join(t.TempDir(), scheduledTaskStamp)
	st := &schtasksCalls{}

	installScheduledTask(app.App, stamp, `C:\avr.exe`, st.run)

	if got := st.modifier(t); got != "30" {
		t.Errorf("the task was registered every %s minutes, want 30", got)
	}
	if !strings.Contains(app.err.String(), "every 30 minutes") {
		t.Errorf("the notice does not say how often the check runs:\n%s", app.err.String())
	}
}

// REQ-5.5: on macOS a launchd agent written by an older avar at the old
// interval is rewritten at the new one. The plist's content is compared, so
// the interval is part of what makes it current.
func TestLaunchdAgent_RewritesAnAgentAtTheOldInterval_REQ_5_5(t *testing.T) {
	app := newTestApp(t, fake.New())
	dir := t.TempDir()
	path := filepath.Join(dir, launchdPlist)
	old := strings.Replace(launchdPlistContent("/opt/homebrew/bin/avr"),
		fmt.Sprintf("<integer>%d</integer>", idleCheckMinutes*60), "<integer>600</integer>", 1)
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	lc := &launchctlCalls{loaded: true}

	installLaunchdAgent(app.App, dir, "/opt/homebrew/bin/avr", lc.run)

	got := readPlist(t, path)
	if !strings.Contains(got, "<key>StartInterval</key>\n\t<integer>1800</integer>") {
		t.Errorf("the agent does not run every 1800 seconds:\n%s", got)
	}
}
