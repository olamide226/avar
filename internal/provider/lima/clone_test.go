//go:build unix

// See shell_test.go: LimaProvider is a macOS backend, so its behaviour tests
// run where it runs.

package lima

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// isolatedMachine is a project-specific machine name, derived by the resolver
// from the project identity and the environment.
const testIsolatedMachine = "avr-prj-3fa9c2b1d0-ubuntu-24.04-arm64"

// testBaseMachine is the base machine name for the default Ubuntu 24.04 arm64
// environment.
const testBaseMachine = "avr-base-ubuntu-24.04-arm64"

// emptyListing returns a listing with no instances.
func emptyListing(t *testing.T) []byte {
	return fixture(t, "list-empty.json")
}

func TestBaseMachineName_EmbedsTheEnvironment_REQ_11_1(t *testing.T) {
	env := types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64}
	want := "avr-base-ubuntu-24.04-arm64"
	if got := baseMachineName(env); got != want {
		t.Errorf("baseMachineName(%v) = %q, want %q", env, got, want)
	}

	env = types.EnvironmentSelector{Distro: types.DistroFedora, Version: "42", Arch: types.ArchAMD64}
	want = "avr-base-fedora-42-amd64"
	if got := baseMachineName(env); got != want {
		t.Errorf("baseMachineName(%v) = %q, want %q", env, got, want)
	}

	// The base name must satisfy avar's ownership validation.
	if err := types.ValidateMachineName(testBaseMachine); err != nil {
		t.Errorf("base machine name %q fails ValidateMachineName: %v", testBaseMachine, err)
	}
}

// TestEnsureBase_CreatesWhenAbsent proves that the first isolation for a
// (distro, arch) pair provisions a pristine base, stops it, and leaves it in a
// cloneable state.
func TestEnsureBase_CreatesWhenAbsent_REQ_11_1(t *testing.T) {
	runner := newFakeRunner().listing(emptyListing(t))
	runner.streamOutput = "INFO[0000] Provisioning base\n"
	p := newTestProvider(t, runner, newFakeRecords())
	sink := &recordingSink{}

	spec := provider.MachineSpec{
		Name:     testIsolatedMachine,
		Selector: nativeSelector(),
		Kind:     types.KindIsolated,
	}
	if err := p.ensureBase(context.Background(), spec, testBaseMachine, sink); err != nil {
		t.Fatalf("ensureBase: %v", err)
	}

	argvs := runner.limactlArgvs()
	// The first call is `list --json` (from ensureBase's view lookup).
	// Then it provisions the base via create, then stops it.
	if len(argvs) < 2 {
		t.Fatalf("want at least 2 limactl calls (list + start), got %d: %v", len(argvs), argvs)
	}
	if argvs[0] != "limactl list --json" {
		t.Errorf("first call is not the listing: %v", argvs)
	}
	if !strings.Contains(argvs[1], "limactl start --name "+testBaseMachine) {
		t.Errorf("second call is not the base provisioning start: %v", argvs)
	}
	// The last call should be `stop <base>`.
	last := argvs[len(argvs)-1]
	if !strings.Contains(last, "limactl stop") || !strings.Contains(last, testBaseMachine) {
		t.Errorf("last call is not the stop of the base: %v", argvs)
	}

	// Progress should mention base creation.
	creating := sink.of(types.ProgressCreating)
	if len(creating) < 1 {
		t.Fatalf("want at least one Creating event, got %d: %v", len(creating), sink.kinds())
	}
	if !strings.Contains(creating[0].Message, "Preparing") {
		t.Errorf("Creating message does not mention preparing a base: %q", creating[0].Message)
	}
}

// TestEnsureBase_StopsARunningBase proves that an already-running base is
// stopped before it can be used for cloning — a running VM's disk is not safe
// to clone.
func TestEnsureBase_StopsARunningBase(t *testing.T) {
	// Build a fixture listing with the base machine in Running state.
	runningBase := []byte(`{"name":"avr-base-ubuntu-24.04-arm64","status":"Running","dir":"/tmp/base","vmType":"vz","arch":"aarch64","cpus":2,"memory":4294967296,"disk":107374182400,"config":{}}`)
	runner := newFakeRunner().listing(runningBase)
	p := newTestProvider(t, runner, newFakeRecords())
	sink := &recordingSink{}

	spec := provider.MachineSpec{
		Name:     testIsolatedMachine,
		Selector: nativeSelector(),
		Kind:     types.KindIsolated,
	}
	if err := p.ensureBase(context.Background(), spec, testBaseMachine, sink); err != nil {
		t.Fatalf("ensureBase: %v", err)
	}

	argvs := runner.limactlArgvs()
	// Should be: list, then stop. No provisioning.
	if len(argvs) < 2 {
		t.Fatalf("want at least 2 calls (list + stop), got %d: %v", len(argvs), argvs)
	}
	if argvs[0] != "limactl list --json" {
		t.Errorf("first call is not the listing: %v", argvs)
	}
	if argvs[1] != "limactl stop "+testBaseMachine {
		t.Errorf("second call is not stop: want %q, got %q", "limactl stop "+testBaseMachine, argvs[1])
	}
	// No additional calls — the base already exists, do not re-provision.
	if len(argvs) > 2 {
		t.Errorf("unexpected calls after stop: %v", argvs[2:])
	}
}

// TestEnsureBase_NoOpWhenStopped proves that a base already in the correct
// state costs nothing.
func TestEnsureBase_NoOpWhenStopped(t *testing.T) {
	stoppedBase := []byte(`{"name":"avr-base-ubuntu-24.04-arm64","status":"Stopped","dir":"/tmp/base","vmType":"vz","arch":"aarch64","cpus":2,"memory":4294967296,"disk":107374182400,"config":{}}`)
	runner := newFakeRunner().listing(stoppedBase)
	p := newTestProvider(t, runner, newFakeRecords())
	sink := &recordingSink{}

	spec := provider.MachineSpec{
		Name:     testIsolatedMachine,
		Selector: nativeSelector(),
		Kind:     types.KindIsolated,
	}
	if err := p.ensureBase(context.Background(), spec, testBaseMachine, sink); err != nil {
		t.Fatalf("ensureBase: %v", err)
	}

	argvs := runner.limactlArgvs()
	// Only the listing — no stop, no provisioning.
	if len(argvs) != 1 || argvs[0] != "limactl list --json" {
		t.Errorf("want exactly [list], got %v", argvs)
	}
	if len(sink.kinds()) != 0 {
		t.Errorf("the stopped-base path emitted progress: %v", sink.kinds())
	}
}

// TestCreateFromBase_ClonesAndStarts proves that an isolated machine is
// produced by cloning the base, starting the clone, and verifying the mounts.
func TestCreateFromBase_ClonesAndStarts_REQ_11_1(t *testing.T) {
	// The base exists and is stopped — the warm isolation path.
	stoppedBase := []byte(`{"name":"avr-base-ubuntu-24.04-arm64","status":"Stopped","dir":"/tmp/base","vmType":"vz","arch":"aarch64","cpus":2,"memory":4294967296,"disk":107374182400,"config":{}}`)
	runner := newFakeRunner().listing(stoppedBase)
	runner.streamOutput = "INFO[0000] Starting via clone\n"
	p := newTestProvider(t, runner, newFakeRecords())
	sink := &recordingSink{}

	project := t.TempDir()
	spec := provider.MachineSpec{
		Name:     testIsolatedMachine,
		Selector: nativeSelector(),
		Kind:     types.KindIsolated,
		Mounts:   shares(project),
	}

	logPath := filepath.Join(t.TempDir(), "create.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	defer logFile.Close()

	mounts, err := normalizeMounts(spec.Mounts)
	if err != nil {
		t.Fatalf("normalizeMounts: %v", err)
	}

	if err := p.createFromBase(context.Background(), spec, mounts, logPath, logFile, sink); err != nil {
		t.Fatalf("createFromBase: %v", err)
	}

	argvs := runner.limactlArgvs()
	// Sequence: list (ensureBase), clone, edit (add mounts), shell (verify mount -d), shell (verify mount -w)
	if len(argvs) < 4 {
		t.Fatalf("want at least 4 limactl calls (list + clone + edit + shell...), got %d: %v", len(argvs), argvs)
	}
	if argvs[0] != "limactl list --json" {
		t.Errorf("first call is not the listing: %v", argvs)
	}
	wantClone := "limactl clone " + testBaseMachine + " " + testIsolatedMachine
	if argvs[1] != wantClone {
		t.Errorf("clone call: want %q, got %q", wantClone, argvs[1])
	}
	// The third call is the edit that adds project mounts.
	if !strings.HasPrefix(argvs[2], "limactl edit "+testIsolatedMachine+" --set .mounts = ") {
		t.Errorf("edit call: want limactl edit %s --set .mounts = [...], got %q", testIsolatedMachine, argvs[2])
	}

	// The streamed start call is the fourth total call (not in limactlArgvs).
	fullArgvs := runner.argvs()
	if len(fullArgvs) < 5 {
		t.Fatalf("want at least 5 total calls (list + clone + edit + start + shell...), got %d: %v", len(fullArgvs), fullArgvs)
	}

	// Check the progress events: should have a Creating event naming the
	// clone.
	creating := sink.of(types.ProgressCreating)
	if len(creating) < 1 {
		t.Fatalf("want at least one Creating event, got %d: %v", len(creating), sink.kinds())
	}
	if !strings.Contains(creating[0].Message, "from clone") {
		t.Errorf("Creating message does not mention cloning: %q", creating[0].Message)
	}
}

// TestEnsureMachine_IsolatedCreatesFromBase proves that the isolated path is
// entered automatically when the spec carries KindIsolated.
func TestEnsureMachine_IsolatedCreatesViaClone_REQ_11_1(t *testing.T) {
	project := t.TempDir()

	// First listing: target doesn't exist. Second listing (ensureBase): base
	// doesn't exist — triggers base creation.
	runner := newFakeRunner().listing(
		emptyListing(t), // First: target doesn't exist
		emptyListing(t), // Second: base doesn't exist, create it
	)
	runner.streamOutput = "INFO[0000] Provisioning\n"
	p := newTestProvider(t, runner, newFakeRecords())
	sink := &recordingSink{}

	err := p.EnsureMachine(context.Background(), provider.MachineSpec{
		Name:     testIsolatedMachine,
		Selector: nativeSelector(),
		Kind:     types.KindIsolated,
		Mounts:   shares(project),
	}, sink)
	if err != nil {
		t.Fatalf("EnsureMachine (isolated): %v", err)
	}

	argvs := runner.limactlArgvs()
	// We expect: list (ensureMachine), list (ensureBase), start (create base),
	// stop (base), clone, edit (add mounts), start (streamed), shell (verify), shell (verify).
	if len(argvs) < 6 {
		t.Fatalf("want at least 6 limactl calls, got %d: %v", len(argvs), argvs)
	}
	if argvs[0] != "limactl list --json" {
		t.Errorf("first call is not listing: %v", argvs)
	}
	if argvs[1] != "limactl list --json" {
		t.Errorf("second call is not listing (base check): %v", argvs)
	}

	// Find the clone and edit calls.
	foundClone := false
	foundEdit := false
	for _, a := range argvs {
		if strings.HasPrefix(a, "limactl clone") {
			foundClone = true
		}
		if strings.HasPrefix(a, "limactl edit") {
			foundEdit = true
		}
	}
	if !foundClone {
		t.Errorf("no clone call in argvs: %v", argvs)
	}
	if !foundEdit {
		t.Errorf("no edit call in argvs: %v", argvs)
	}
}

// TestEnsureMachine_IsolatedFallsBackOnCloneFailure proves that when cloning
// fails the provider falls through to full provisioning instead of returning
// an error.
func TestEnsureMachine_IsolatedFallsBackOnCloneFailure_REQ_11_1(t *testing.T) {
	project := t.TempDir()

	// The base exists and is stopped, so the listing shows it ready.
	stoppedBase := []byte(`{"name":"avr-base-ubuntu-24.04-arm64","status":"Stopped","dir":"/tmp/base","vmType":"vz","arch":"aarch64","cpus":2,"memory":4294967296,"disk":107374182400,"config":{}}`)
	// First listing (ensureMachine): target doesn't exist (empty).
	// Second listing (ensureBase): base exists (stopped).
	runner := newFakeRunner().listing(emptyListing(t), stoppedBase)
	// Fail the clone command, but let everything else succeed.
	runner.failOn("clone", os.ErrNotExist)
	runner.streamOutput = "INFO[0000] Provisioning from scratch\n"
	p := newTestProvider(t, runner, newFakeRecords())
	sink := &recordingSink{}

	err := p.EnsureMachine(context.Background(), provider.MachineSpec{
		Name:     testIsolatedMachine,
		Selector: nativeSelector(),
		Kind:     types.KindIsolated,
		Mounts:   shares(project),
	}, sink)
	if err != nil {
		t.Fatalf("EnsureMachine (isolated fallback): %v", err)
	}

	argvs := runner.limactlArgvs()
	// The clone call should have been attempted.
	foundClone := false
	foundConfigStart := false
	for _, a := range argvs {
		if strings.HasPrefix(a, "limactl clone") {
			foundClone = true
		}
		if strings.Contains(a, configPathPlaceholder) {
			foundConfigStart = true
		}
	}
	if !foundClone {
		t.Errorf("clone was never attempted: %v", argvs)
	}
	if !foundConfigStart {
		t.Errorf("fallback to config-based provisioning did not happen: %v", argvs)
	}
}

// A size in the spec reaches the clone. Cloning copies the base's configuration
// verbatim, so before this the clone silently had the base's size whatever the
// spec asked for — the defect that stays invisible until a caller passes a size
// (design §3.11).
func TestCreateFromBase_AppliesTheSpecSize_REQ_15_1(t *testing.T) {
	stoppedBase := []byte(`{"name":"avr-base-ubuntu-24.04-arm64","status":"Stopped","dir":"/tmp/base","vmType":"vz","arch":"aarch64","cpus":4,"memory":8589934592,"disk":107374182400,"config":{}}`)

	for _, tc := range []struct {
		name     string
		mounts   bool
		cpus     int
		memoryGB float64
		want     string
	}{
		{"size and mounts in one edit", true, 6, 12, "--set .cpus = 6 --set .memory = \"12GiB\" --tty=false"},
		{"a fractional size is written in MiB", true, 0, 1.5, "--set .memory = \"1536MiB\" --tty=false"},
		{"a size with no mounts still edits", false, 2, 0, "limactl edit " + testIsolatedMachine + " --set .cpus = 2 --tty=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := newFakeRunner().listing(stoppedBase)
			p := newTestProvider(t, runner, newFakeRecords())

			spec := provider.MachineSpec{
				Name:     testIsolatedMachine,
				Selector: nativeSelector(),
				Kind:     types.KindIsolated,
				CPUs:     tc.cpus,
				MemoryGB: tc.memoryGB,
			}
			if tc.mounts {
				spec.Mounts = shares(t.TempDir())
			}
			mounts, err := normalizeMounts(spec.Mounts)
			if err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(t.TempDir(), "create.log")
			logFile, err := os.Create(logPath)
			if err != nil {
				t.Fatal(err)
			}
			defer logFile.Close()

			if err := p.createFromBase(context.Background(), spec, mounts, logPath, logFile, &recordingSink{}); err != nil {
				t.Fatalf("createFromBase: %v", err)
			}

			var edit string
			for _, a := range runner.limactlArgvs() {
				if strings.HasPrefix(a, "limactl edit") {
					if edit != "" {
						t.Fatalf("more than one edit of the clone: %q and %q", edit, a)
					}
					edit = a
				}
			}
			if !strings.Contains(edit, tc.want) {
				t.Errorf("edit of the clone = %q, want it to contain %q", edit, tc.want)
			}
		})
	}
}

// A spec with no size changes nothing about the clone beyond its mounts.
func TestCreateFromBase_NoSizeNoMountsNoEdit(t *testing.T) {
	stoppedBase := []byte(`{"name":"avr-base-ubuntu-24.04-arm64","status":"Stopped","dir":"/tmp/base","vmType":"vz","arch":"aarch64","cpus":4,"memory":8589934592,"disk":107374182400,"config":{}}`)
	runner := newFakeRunner().listing(stoppedBase)
	p := newTestProvider(t, runner, newFakeRecords())
	logPath := filepath.Join(t.TempDir(), "create.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()

	spec := provider.MachineSpec{Name: testIsolatedMachine, Selector: nativeSelector(), Kind: types.KindIsolated}
	if err := p.createFromBase(context.Background(), spec, nil, logPath, logFile, &recordingSink{}); err != nil {
		t.Fatalf("createFromBase: %v", err)
	}
	for _, a := range runner.limactlArgvs() {
		if strings.HasPrefix(a, "limactl edit") {
			t.Errorf("an unsized, unmounted clone was edited: %q", a)
		}
	}
}

// The base is shared by every isolated environment of its (distro, arch), so it
// is created at the defaults whatever the first isolated spec asked for.
// Otherwise the first project to isolate would size the base, and through it
// every later clone that asked for no size of its own.
func TestEnsureBase_IsCreatedAtTheDefaultSize_REQ_15_1(t *testing.T) {
	runner := newFakeRunner().listing(emptyListing(t))
	p := newTestProvider(t, runner, newFakeRecords())

	spec := provider.MachineSpec{
		Name:     testIsolatedMachine,
		Selector: nativeSelector(),
		Kind:     types.KindIsolated,
		CPUs:     7,
		MemoryGB: 13,
	}
	if err := p.ensureBase(context.Background(), spec, testBaseMachine, &recordingSink{}); err != nil {
		t.Fatalf("ensureBase: %v", err)
	}

	config := runner.configWritten
	if config == "" {
		t.Fatal("no base configuration was written, so this test would check nothing")
	}
	if strings.Contains(config, "cpus: 7") || strings.Contains(config, "13GiB") {
		t.Errorf("the base took the isolated spec's size:\n%s", config)
	}
	if !strings.Contains(config, "cpus: 4") || !strings.Contains(config, `"8GiB"`) {
		t.Errorf("the base is not at the host-proportional defaults (4 CPU, 8GiB on the test host):\n%s", config)
	}
}
