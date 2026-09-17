package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/olamide226/avar/internal/state"
	"github.com/olamide226/avar/internal/types"
)

func testStore(t *testing.T) *state.Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.Setenv(state.HomeEnv, dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv(state.HomeEnv) })
	store, err := state.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// testPID returns the current process id so that tests use a pid
// processAlive recognises. The store prunes sessions whose pid is dead, and
// pid 42 is never the test process.
func testPID() int { return os.Getpid() }

// startChild starts a process that sleeps until its stdin is closed. Its pid
// is alive for the lifetime of the test, giving Attach/Detach tests a second
// live pid when they need two distinct sessions.
func startChild(t *testing.T) int {
	t.Helper()
	// Using 'sleep' with a long duration; the child dies when the cmd is
	// garbage collected or when the test ends.
	cmd := exec.Command("sleep", "60")
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start child process for second session pid: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// writeMachine seeds the store with a machine record so that sessions can be
// added to it (the store refuses sessions on an unrecorded machine).
func writeMachine(t *testing.T, store *state.Store, name string, kind types.MachineKind) {
	t.Helper()
	err := store.PutMachine(types.MachineRecord{
		Name:     name,
		Provider: types.ProviderLima,
		Selector: types.EnvironmentSelector{Distro: "ubuntu", Version: "24.04", Arch: "arm64"},
		Kind:     kind,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAttach_RecordsSession(t *testing.T) {
	store := testStore(t)
	writeMachine(t, store, "avr-ubuntu-24.04-arm64", types.KindShared)

	pid := testPID()
	if err := Attach(store, "avr-ubuntu-24.04-arm64", pid); err != nil {
		t.Fatalf("Attach returned an unexpected error: %v", err)
	}

	sessions, err := store.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	if sessions[0].Machine != "avr-ubuntu-24.04-arm64" {
		t.Errorf("machine = %q, want avr-ubuntu-24.04-arm64", sessions[0].Machine)
	}
	if sessions[0].PID != pid {
		t.Errorf("pid = %d, want %d", sessions[0].PID, pid)
	}
	if sessions[0].StartedAt.IsZero() {
		t.Error("started_at was not set")
	}
}

func TestAttach_ClearsIdleSinceMarker(t *testing.T) {
	store := testStore(t)
	writeMachine(t, store, "avr-ubuntu-24.04-arm64", types.KindShared)

	// Pre-seed an idle-since marker.
	if err := recordIdleSince(store, "avr-ubuntu-24.04-arm64", time.Now().UTC().Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Attach should clear it.
	if err := Attach(store, "avr-ubuntu-24.04-arm64", testPID()); err != nil {
		t.Fatalf("Attach returned an unexpected error: %v", err)
	}

	since, err := readIdleSince(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := since["avr-ubuntu-24.04-arm64"]; ok {
		t.Error("idle-since marker was not cleared after Attach")
	}
}

func TestDetach_RemovesSession(t *testing.T) {
	store := testStore(t)
	writeMachine(t, store, "avr-ubuntu-24.04-arm64", types.KindShared)

	pid := testPID()
	if err := Attach(store, "avr-ubuntu-24.04-arm64", pid); err != nil {
		t.Fatal(err)
	}
	Detach(store, "avr-ubuntu-24.04-arm64", pid)

	sessions, err := store.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Errorf("want 0 sessions after Detach, got %d", len(sessions))
	}
}

func TestDetach_RecordsIdleSinceWhenLastSessionLeaves(t *testing.T) {
	store := testStore(t)
	writeMachine(t, store, "avr-ubuntu-24.04-arm64", types.KindShared)

	pid := testPID()
	if err := Attach(store, "avr-ubuntu-24.04-arm64", pid); err != nil {
		t.Fatal(err)
	}
	Detach(store, "avr-ubuntu-24.04-arm64", pid)

	since, err := readIdleSince(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := since["avr-ubuntu-24.04-arm64"]; !ok {
		t.Error("no idle-since marker recorded after last session detached")
	}
}

func TestDetach_DoesNotRecordIdleSinceWhenOtherSessionsRemain(t *testing.T) {
	store := testStore(t)
	writeMachine(t, store, "avr-ubuntu-24.04-arm64", types.KindShared)

	// Two sessions on the same machine with two different live PIDs.
	pid1 := testPID()
	pid2 := startChild(t)

	if err := Attach(store, "avr-ubuntu-24.04-arm64", pid1); err != nil {
		t.Fatal(err)
	}
	if err := Attach(store, "avr-ubuntu-24.04-arm64", pid2); err != nil {
		t.Fatal(err)
	}

	// Clear any idle-since marker set by the second attach.
	_ = clearIdleSince(store, "avr-ubuntu-24.04-arm64")

	// Detach pid1. pid2 is still alive, so the machine is not idle.
	Detach(store, "avr-ubuntu-24.04-arm64", pid1)

	since, err := readIdleSince(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := since["avr-ubuntu-24.04-arm64"]; ok {
		t.Error("idle-since marker was recorded even though sessions remain")
	}
}

func TestDetach_IsIdempotent(t *testing.T) {
	store := testStore(t)
	writeMachine(t, store, "avr-ubuntu-24.04-arm64", types.KindShared)

	pid := testPID()
	if err := Attach(store, "avr-ubuntu-24.04-arm64", pid); err != nil {
		t.Fatal(err)
	}
	Detach(store, "avr-ubuntu-24.04-arm64", pid)
	// Second Detach for the same session must not panic or error.
	Detach(store, "avr-ubuntu-24.04-arm64", pid)
}

// --- Idle detection --------------------------------------------------------

func TestIdleMachines_ExcludesMachineWithLiveSession_PROP_11(t *testing.T) {
	store := testStore(t)
	writeMachine(t, store, "avr-ubuntu-24.04-arm64", types.KindShared)

	if err := Attach(store, "avr-ubuntu-24.04-arm64", testPID()); err != nil {
		t.Fatal(err)
	}

	// Even with a very short timeout (which would make any sessionless
	// machine idle immediately), a machine with a live session is never
	// returned (Property 11).
	idle, err := IdleMachines(store, 1*time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range idle {
		if name == "avr-ubuntu-24.04-arm64" {
			t.Error("PROP-11 violation: machine with a live session was returned as idle")
		}
	}
}

func TestIdleMachines_ReturnsMachineIdleLongerThanTimeout(t *testing.T) {
	store := testStore(t)
	writeMachine(t, store, "avr-ubuntu-24.04-arm64", types.KindShared)

	// Seed an idle-since marker far in the past.
	if err := recordIdleSince(store, "avr-ubuntu-24.04-arm64", time.Now().UTC().Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}

	idle, err := IdleMachines(store, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range idle {
		if name == "avr-ubuntu-24.04-arm64" {
			found = true
		}
	}
	if !found {
		t.Error("machine idle for 3h was not returned with a 1h timeout")
	}
}

func TestIdleMachines_ExcludesMachineIdleLessThanTimeout(t *testing.T) {
	store := testStore(t)
	writeMachine(t, store, "avr-ubuntu-24.04-arm64", types.KindShared)

	// Seed an idle-since marker just a moment ago.
	if err := recordIdleSince(store, "avr-ubuntu-24.04-arm64", time.Now().UTC().Add(-1*time.Second)); err != nil {
		t.Fatal(err)
	}

	idle, err := IdleMachines(store, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range idle {
		if name == "avr-ubuntu-24.04-arm64" {
			t.Error("machine idle for 1s was returned with a 1h timeout")
		}
	}
}

func TestIdleMachines_ZeroTimeoutDisables(t *testing.T) {
	store := testStore(t)
	writeMachine(t, store, "avr-ubuntu-24.04-arm64", types.KindShared)

	if err := recordIdleSince(store, "avr-ubuntu-24.04-arm64", time.Now().UTC().Add(-10*time.Hour)); err != nil {
		t.Fatal(err)
	}

	idle, err := IdleMachines(store, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(idle) != 0 {
		t.Error("zero timeout returned machines: it should disable idle detection")
	}
}

func TestIdleMachines_InitialisesMissingIdleSince(t *testing.T) {
	store := testStore(t)
	writeMachine(t, store, "avr-ubuntu-24.04-arm64", types.KindShared)

	// No idle-since marker at all — the machine has never had a session.
	// IdleMachines should initialise the clock to now rather than treating
	// "no record" as "has been idle forever".

	idle, err := IdleMachines(store, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(idle) != 0 {
		t.Error("a machine with no idle-since record was returned as idle; it should start its clock now")
	}

	// The idle-since marker should now exist.
	since, err := readIdleSince(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := since["avr-ubuntu-24.04-arm64"]; !ok {
		t.Error("idle-since marker was not initialised")
	}
}

func TestIdleMachines_MultipleMachinesMixed(t *testing.T) {
	store := testStore(t)
	writeMachine(t, store, "avr-ubuntu-24.04-arm64", types.KindShared)
	writeMachine(t, store, "avr-fedora-41-arm64", types.KindShared)

	// ubuntu has a live session -> excluded (Property 11).
	if err := Attach(store, "avr-ubuntu-24.04-arm64", testPID()); err != nil {
		t.Fatal(err)
	}
	// fedora has been idle for 5 hours.
	if err := recordIdleSince(store, "avr-fedora-41-arm64", time.Now().UTC().Add(-5*time.Hour)); err != nil {
		t.Fatal(err)
	}

	idle, err := IdleMachines(store, 1*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	hasFedora := false
	hasUbuntu := false
	for _, name := range idle {
		switch name {
		case "avr-fedora-41-arm64":
			hasFedora = true
		case "avr-ubuntu-24.04-arm64":
			hasUbuntu = true
		}
	}
	if !hasFedora {
		t.Error("fedora idle for 5h was not returned")
	}
	if hasUbuntu {
		t.Error("PROP-11 violation: ubuntu with a live session was returned as idle")
	}
}

// --- Idle timeout ----------------------------------------------------------

// Reading config.toml is state.ParseConfig's, and tested there. What is left
// here is the default, and that a timeout the file sets to zero stays zero
// rather than becoming the default.
func TestIdleTimeout_DefaultsOnlyWhenTheFileSetsNone_REQ_5_5(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  state.Config
		want time.Duration
	}{
		{"no config.toml", state.Config{}, DefaultIdleTimeout},
		{"config.toml without idle_timeout", state.Config{Path: "config.toml", ForwardEnv: []string{"A"}}, DefaultIdleTimeout},
		{"a configured timeout", state.Config{IdleTimeout: 30 * time.Minute, IdleTimeoutSet: true}, 30 * time.Minute},
		{"disabled", state.Config{IdleTimeout: 0, IdleTimeoutSet: true}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IdleTimeout(tc.cfg); got != tc.want {
				t.Errorf("IdleTimeout(%+v) = %v, want %v", tc.cfg, got, tc.want)
			}
		})
	}
}

// --- Idle-since file round-trip -------------------------------------------

func TestIdleSinceFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, idleSinceFile)

	m := idleSinceMap{
		"avr-ubuntu-24.04-arm64": time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
	}
	if err := writeIdleSinceAt(path, m); err != nil {
		t.Fatal(err)
	}

	got, err := readIdleSinceAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if !got["avr-ubuntu-24.04-arm64"].Equal(m["avr-ubuntu-24.04-arm64"]) {
		t.Errorf("timestamp = %v, want %v", got["avr-ubuntu-24.04-arm64"], m["avr-ubuntu-24.04-arm64"])
	}
}

func TestIdleSinceFile_MissingIsEmpty(t *testing.T) {
	got, err := readIdleSinceAt(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}
