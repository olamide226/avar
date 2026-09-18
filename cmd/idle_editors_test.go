package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/session"
	"github.com/olamide226/avar/internal/types"
)

// An editor opened with `avr code`, `avr cursor` or `avr zed` holds no avar
// session: the command launches the editor and exits, and the editor then works
// in the guest through its own remote server. These flow tests are about the
// idle check noticing that, through the backend's EditorProber.

const (
	ubuntuIdle = "avr-ubuntu-24.04-arm64"
	fedoraIdle = "avr-fedora-42-arm64"
)

func vsCodeWindow() []provider.EditorConnection {
	return []provider.EditorConnection{{Editor: "VS Code", PID: 5944}}
}

// idleTimeout writes config.toml's idle_timeout.
func idleTimeout(t *testing.T, app *testApp, value string) {
	t.Helper()
	if err := os.WriteFile(app.store.ConfigPath(), []byte("idle_timeout = \""+value+"\"\n"), 0o600); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}
}

// backdateIdleClock sets a machine's idle clock as if its last session had
// detached ago. It writes the file the session package keeps, because the point
// is a machine that has been alone for longer than a test can wait.
func backdateIdleClock(t *testing.T, app *testApp, machine string, ago time.Duration) {
	t.Helper()
	path := filepath.Join(app.store.Root(), "idle_since.json")
	since := map[string]time.Time{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &since); err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
	}
	since[machine] = time.Now().UTC().Add(-ago)
	data, err := json.Marshal(since)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func stoppedMachines(f *fake.Fake) map[string]bool {
	stopped := map[string]bool{}
	for _, call := range f.CallsFor(fake.OpStop) {
		stopped[call.Machine] = true
	}
	return stopped
}

func probedMachines(f *fake.Fake) []string {
	var probed []string
	for _, call := range f.CallsFor(fake.OpConnectedEditors) {
		probed = append(probed, call.Machine)
	}
	return probed
}

// REQ-5.10, PROP-11: a machine with no avar session but a VS Code window
// connected is not stopped, however long its idle clock has run. Before this,
// the idle check read only avar's own session records, and stopped it.
func TestIdleCheck_LeavesAMachineWithAConnectedEditorRunning_REQ_5_10(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	idleAfterOneNanosecond(t, app)
	idleMachine(t, app, f, ubuntuIdle, ubuntu(), types.StateRunning)
	f.SetConnectedEditors(ubuntuIdle, vsCodeWindow())

	if err := runIdleCheck(context.Background(), app.App); err != nil {
		t.Fatalf("avr internal idle-check: %v", err)
	}

	if stoppedMachines(f)[ubuntuIdle] {
		t.Fatalf("idle check stopped an environment with a VS Code window connected: %v", f.Calls())
	}
	f.AssertMachineState(t, ubuntuIdle, types.StateRunning)
	if !strings.Contains(app.err.String(), "VS Code") {
		t.Errorf("a check run by hand did not say why it kept the environment:\n%s", app.err.String())
	}
}

// REQ-5.11, PROP-11: when avar cannot find out whether an editor is connected,
// it does not stop that machine. The others are still stopped, and the check
// exits non-zero so the host scheduler records that something went wrong.
func TestIdleCheck_StopsNothingItCouldNotAsk_REQ_5_11(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	idleTimeout(t, app, "1h")
	idleMachine(t, app, f, fedoraIdle, fedora(), types.StateRunning)
	backdateIdleClock(t, app, fedoraIdle, 3*time.Hour)
	idleMachine(t, app, f, ubuntuIdle, ubuntu(), types.StateRunning)
	backdateIdleClock(t, app, ubuntuIdle, 3*time.Hour)
	// Machines are asked in name order, so the first probe is Fedora's.
	f.FailNextOn(fake.OpConnectedEditors, errors.New("looking for editor windows connected to machine avr-fedora-42-arm64: exit status 255"))

	err := runIdleCheck(context.Background(), app.App)
	if err == nil || !strings.Contains(err.Error(), fedora().Label()) || !strings.Contains(err.Error(), "exit status 255") {
		t.Errorf("idle check returned %v, want an error naming %s and the cause", err, fedora().Label())
	}

	stopped := stoppedMachines(f)
	if stopped[fedoraIdle] {
		t.Errorf("stopped %s without knowing whether an editor was connected: %v", fedoraIdle, f.Calls())
	}
	if !stopped[ubuntuIdle] {
		t.Errorf("one failed probe kept %s running as well: %v", ubuntuIdle, f.Calls())
	}

	// The idle clock was left as it was, so the next check that gets an
	// answer — no editor — stops the machine then, not a timeout later.
	f.Reset()
	if err := runIdleCheck(context.Background(), app.App); err != nil {
		t.Fatalf("second idle check: %v", err)
	}
	if !stoppedMachines(f)[fedoraIdle] {
		t.Errorf("a failed probe restarted %s's idle clock: the next check did not stop it: %v", fedoraIdle, f.Calls())
	}
}

// REQ-5.10: only machines the check is about to stop are asked. A machine with a
// session, one whose idle clock has not run out, and one that is already
// stopped cost no round trip into a guest.
func TestIdleCheck_ProbesOnlyMachinesAboutToBeStopped_REQ_5_10(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	idleTimeout(t, app, "1h")

	debian := types.EnvironmentSelector{Distro: types.DistroDebian, Version: "13", Arch: types.HostArch()}
	jammy := types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "22.04", Arch: types.HostArch()}

	// Idle past the timeout and running: the one to ask.
	idleMachine(t, app, f, ubuntuIdle, ubuntu(), types.StateRunning)
	backdateIdleClock(t, app, ubuntuIdle, 3*time.Hour)
	// Idle past the timeout but already stopped: converged, not asked.
	idleMachine(t, app, f, fedoraIdle, fedora(), types.StateStopped)
	backdateIdleClock(t, app, fedoraIdle, 3*time.Hour)
	// Running with a live session.
	idleMachine(t, app, f, "avr-debian-13-arm64", debian, types.StateRunning)
	if err := session.Attach(app.store, "avr-debian-13-arm64", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	// Running, idle for less than the timeout.
	idleMachine(t, app, f, "avr-ubuntu-22.04-arm64", jammy, types.StateRunning)
	backdateIdleClock(t, app, "avr-ubuntu-22.04-arm64", 10*time.Minute)

	if err := runIdleCheck(context.Background(), app.App); err != nil {
		t.Fatalf("avr internal idle-check: %v", err)
	}

	if probed := probedMachines(f); len(probed) != 1 || probed[0] != ubuntuIdle {
		t.Errorf("asked %v for editors, want only %s", probed, ubuntuIdle)
	}
	stopped := stoppedMachines(f)
	if !stopped[ubuntuIdle] || !stopped[fedoraIdle] || len(stopped) != 2 {
		t.Errorf("stopped %v, want %s and %s", stopped, ubuntuIdle, fedoraIdle)
	}
}

// REQ-5.10: a machine kept for its editor has its idle clock restarted, so when
// the window closes the machine gets a full timeout from when the editor was
// last seen — not a stop at the very next check because its clock started
// hours ago.
func TestIdleCheck_RestartsTheIdleClockOfAMachineKeptForAnEditor_REQ_5_10(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	idleTimeout(t, app, "1h")
	idleMachine(t, app, f, ubuntuIdle, ubuntu(), types.StateRunning)
	backdateIdleClock(t, app, ubuntuIdle, 3*time.Hour)
	f.SetConnectedEditors(ubuntuIdle, vsCodeWindow())

	if err := runIdleCheck(context.Background(), app.App); err != nil {
		t.Fatalf("first idle check: %v", err)
	}

	// The window closes. The next check is well inside an hour of the last
	// one that saw it.
	f.SetConnectedEditors(ubuntuIdle, nil)
	if err := runIdleCheck(context.Background(), app.App); err != nil {
		t.Fatalf("second idle check: %v", err)
	}

	if stoppedMachines(f)[ubuntuIdle] {
		t.Errorf("stopped the environment at the first check after its editor left, on an idle clock that started hours ago: %v", f.Calls())
	}
}

// REQ-5.10: `avr code` restarts the idle clock when it opens the editor. The
// editor's server has to install and connect before a check can see it — on a
// first connection that is a download — and a check that ran in between would
// stop the environment the user had just asked to open.
func TestCode_RestartsTheIdleClockWhenItOpensTheEditor_REQ_5_10(t *testing.T) {
	e := newEditorTest(t, "code")
	e.seedSSHTarget(t)
	if err := e.store.PutMachine(types.MachineRecord{
		Name: e.machine, Provider: fake.ProviderID, Selector: ubuntu(), Kind: types.KindShared,
	}); err != nil {
		t.Fatal(err)
	}
	idleTimeout(t, e.testApp, "1h")
	backdateIdleClock(t, e.testApp, e.machine, 3*time.Hour)

	if err := e.run(t, editorInvocation("code")); err != nil {
		t.Fatalf("avr code: %v", err)
	}
	if _, ok := e.launched(t); !ok {
		t.Fatal("`avr code` did not run the code launcher")
	}

	// VS Code is still connecting: nothing in the guest shows a window yet.
	if err := runIdleCheck(context.Background(), e.App); err != nil {
		t.Fatalf("avr internal idle-check: %v", err)
	}
	if stoppedMachines(e.f)[e.machine] {
		t.Errorf("the idle check stopped the environment `avr code` had just opened: %v", e.f.Calls())
	}
}
