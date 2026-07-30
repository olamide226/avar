package resolve

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/state"
	"github.com/olamide226/avar/internal/types"
)

// The real store satisfies the narrow interface the resolver asks for. This
// assertion lives in the test binary on purpose: production code in
// internal/resolve must not import internal/state, or declaring the interface
// here would buy nothing.
var _ Store = (*state.Store)(nil)

// --- Test doubles ---------------------------------------------------------

// fixedNow is the fake store's clock. Records carry timestamps, and the
// resolver's determinism is only observable if the store around it is
// deterministic too.
var fixedNow = time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)

// fakeStore is the whole of what the resolver needs from avar's state, with no
// filesystem and no lock. It hashes paths the same way internal/state does, so
// the machine names these tests assert on are the names production would
// produce (cross-checked in TestResolve_AgainstRealStateStore_REQ_11_2).
type fakeStore struct {
	projects map[string]types.ProjectRecord

	projectsErr error
	updateErr   error

	ensured []string
	updated []string
}

func newFakeStore(recs ...types.ProjectRecord) *fakeStore {
	f := &fakeStore{projects: map[string]types.ProjectRecord{}}
	for _, rec := range recs {
		f.projects[rec.ID] = rec
	}
	return f
}

func (f *fakeStore) Projects() ([]types.ProjectRecord, error) {
	if f.projectsErr != nil {
		return nil, f.projectsErr
	}
	out := make([]types.ProjectRecord, 0, len(f.projects))
	for _, rec := range f.projects {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (f *fakeStore) EnsureProject(path string) (types.ProjectRecord, error) {
	f.ensured = append(f.ensured, path)
	id := fakeID(path)
	rec, ok := f.projects[id]
	if !ok {
		rec = types.ProjectRecord{ID: id, Path: path, CreatedAt: fixedNow}
	}
	rec.LastUsedAt = fixedNow
	f.projects[id] = rec
	return rec, nil
}

func (f *fakeStore) UpdateProject(id string, mutate func(*types.ProjectRecord)) (types.ProjectRecord, error) {
	f.updated = append(f.updated, id)
	if f.updateErr != nil {
		return types.ProjectRecord{}, f.updateErr
	}
	rec, ok := f.projects[id]
	if !ok {
		return types.ProjectRecord{}, fmt.Errorf("no project record %s", id)
	}
	mutate(&rec)
	f.projects[id] = rec
	return rec, nil
}

// fakeID mirrors state.ProjectID's hashing without touching the filesystem.
func fakeID(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}

// projectAt builds a recorded project, optionally with remembered choices.
func projectAt(path string, mutate ...func(*types.ProjectRecord)) types.ProjectRecord {
	rec := types.ProjectRecord{ID: fakeID(path), Path: path, CreatedAt: fixedNow, LastUsedAt: fixedNow}
	for _, m := range mutate {
		m(&rec)
	}
	return rec
}

// arm64Host resolves as if avar were running on Apple Silicon, so that results
// do not depend on the machine the tests run on.
func arm64Host() Options { return Options{HostArch: types.ArchARM64} }

// --- Defaults and the precedence chain ------------------------------------

func TestResolve_DefaultEnvironmentNeedsNoUserInput_REQ_1_5(t *testing.T) {
	store := newFakeStore()

	got, err := Resolve("/Users/dev/code/app", cli.Selector{}, store, arm64Host())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	want := types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64}
	if got.Selector != want {
		t.Errorf("selector = %+v, want %+v", got.Selector, want)
	}
	if got.MachineName != "avr-ubuntu-24.04-arm64" {
		t.Errorf("machine name = %q, want %q", got.MachineName, "avr-ubuntu-24.04-arm64")
	}
	if got.Kind != types.KindShared {
		t.Errorf("kind = %q, want %q", got.Kind, types.KindShared)
	}
	if got.GuestCwd != "/Users/dev/code/app" {
		t.Errorf("guest cwd = %q, want the host directory", got.GuestCwd)
	}
	if got.Project.Path != "/Users/dev/code/app" {
		t.Errorf("project path = %q, want the invocation directory registered on first use", got.Project.Path)
	}
	if got.Emulated {
		t.Error("host-native architecture reported as emulated")
	}
	if len(store.ensured) != 1 {
		t.Errorf("EnsureProject calls = %v, want exactly one (first-use registration)", store.ensured)
	}
}

func TestResolve_PrecedenceTable_REQ_4_1_REQ_4_2(t *testing.T) {
	const dir = "/Users/dev/code/app"

	tests := []struct {
		name        string
		selector    cli.Selector
		recorded    []types.ProjectRecord
		config      Preference
		hostArch    types.Arch
		wantEnv     types.EnvironmentSelector
		wantMachine string
	}{
		{
			name:        "built-in defaults when no layer has an opinion",
			hostArch:    types.ArchARM64,
			wantEnv:     types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64},
			wantMachine: "avr-ubuntu-24.04-arm64",
		},
		{
			name:        "built-in default architecture follows the host",
			hostArch:    types.ArchAMD64,
			wantEnv:     types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchAMD64},
			wantMachine: "avr-ubuntu-24.04-amd64",
		},
		{
			name:        "configuration beats the built-in defaults",
			config:      Preference{Distro: types.DistroDebian, Arch: types.ArchAMD64},
			hostArch:    types.ArchARM64,
			wantEnv:     types.EnvironmentSelector{Distro: types.DistroDebian, Version: "13", Arch: types.ArchAMD64},
			wantMachine: "avr-debian-13-amd64",
		},
		{
			name: "the project's remembered environment beats configuration",
			recorded: []types.ProjectRecord{projectAt(dir, func(rec *types.ProjectRecord) {
				rec.Selector = &types.EnvironmentSelector{Distro: types.DistroFedora, Arch: types.ArchAMD64}
			})},
			config:      Preference{Distro: types.DistroDebian, Arch: types.ArchARM64},
			hostArch:    types.ArchARM64,
			wantEnv:     types.EnvironmentSelector{Distro: types.DistroFedora, Version: "43", Arch: types.ArchAMD64},
			wantMachine: "avr-fedora-43-amd64",
		},
		{
			name:     "explicit flags beat everything below them",
			selector: cli.Selector{Distro: types.DistroUbuntu, Arch: types.ArchARM64},
			recorded: []types.ProjectRecord{projectAt(dir, func(rec *types.ProjectRecord) {
				rec.Selector = &types.EnvironmentSelector{Distro: types.DistroFedora, Arch: types.ArchAMD64}
			})},
			config:      Preference{Distro: types.DistroDebian, Arch: types.ArchAMD64},
			hostArch:    types.ArchAMD64,
			wantEnv:     types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64},
			wantMachine: "avr-ubuntu-24.04-arm64",
		},
		{
			name:        "an explicit version pins the release (REQ-4.2)",
			selector:    cli.Selector{Distro: types.DistroDebian, DistroVersion: "13"},
			hostArch:    types.ArchARM64,
			wantEnv:     types.EnvironmentSelector{Distro: types.DistroDebian, Version: "13", Arch: types.ArchARM64},
			wantMachine: "avr-debian-13-arm64",
		},
		{
			name:     "a named distribution takes its own pinned version, never a lower layer's",
			selector: cli.Selector{Distro: types.DistroFedora},
			recorded: []types.ProjectRecord{projectAt(dir, func(rec *types.ProjectRecord) {
				rec.Selector = &types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04"}
			})},
			hostArch:    types.ArchARM64,
			wantEnv:     types.EnvironmentSelector{Distro: types.DistroFedora, Version: "43", Arch: types.ArchARM64},
			wantMachine: "avr-fedora-43-arm64",
		},
		{
			name:        "--arch alone keeps the distribution from the layer below",
			selector:    cli.Selector{Arch: types.ArchAMD64},
			config:      Preference{Distro: types.DistroDebian},
			hostArch:    types.ArchARM64,
			wantEnv:     types.EnvironmentSelector{Distro: types.DistroDebian, Version: "13", Arch: types.ArchAMD64},
			wantMachine: "avr-debian-13-amd64",
		},
		{
			name:        "values are normalised so they can name a machine",
			config:      Preference{Distro: types.Distro("Ubuntu"), Arch: types.Arch(" ARM64 ")},
			hostArch:    types.ArchAMD64,
			wantEnv:     types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64},
			wantMachine: "avr-ubuntu-24.04-arm64",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore(tc.recorded...)

			got, err := Resolve(dir, tc.selector, store, Options{HostArch: tc.hostArch, Config: tc.config})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Selector != tc.wantEnv {
				t.Errorf("selector = %+v, want %+v", got.Selector, tc.wantEnv)
			}
			if got.MachineName != tc.wantMachine {
				t.Errorf("machine name = %q, want %q", got.MachineName, tc.wantMachine)
			}
			if err := types.ValidateMachineName(got.MachineName); err != nil {
				t.Errorf("resolved machine name is not one avar may manage: %v", err)
			}
		})
	}
}

func TestResolve_ConfigurationVersionWithoutDistroIsRefused(t *testing.T) {
	store := newFakeStore()

	_, err := Resolve("/Users/dev/code/app", cli.Selector{}, store, Options{
		HostArch: types.ArchARM64,
		Config:   Preference{Version: "24.04"},
	})
	if err == nil {
		t.Fatal("Resolve accepted a version with no distribution to attach it to")
	}
	if !strings.Contains(err.Error(), "24.04") {
		t.Errorf("error does not name the offending version: %v", err)
	}
}

// --- Isolation ------------------------------------------------------------

func TestResolve_IsolateTargetsAndRemembersProjectMachine_REQ_11_2(t *testing.T) {
	const dir = "/Users/dev/code/app"
	store := newFakeStore()

	first, err := Resolve(dir, cli.Selector{Isolate: true}, store, arm64Host())
	if err != nil {
		t.Fatalf("Resolve --isolate: %v", err)
	}
	wantName := "avr-prj-" + fakeID(dir)[:projectIDPrefixLen] + "-ubuntu-24.04-arm64"
	if first.MachineName != wantName {
		t.Errorf("machine name = %q, want %q", first.MachineName, wantName)
	}
	if first.Kind != types.KindIsolated {
		t.Errorf("kind = %q, want %q", first.Kind, types.KindIsolated)
	}
	if !first.Selector.Isolated {
		t.Error("resolved selector is not isolated")
	}
	if !first.Project.Isolated {
		t.Error("project record does not remember the isolation choice")
	}

	// REQ-11.2: a bare `avr` in the same project goes to the same machine.
	bare, err := Resolve(dir, cli.Selector{}, store, arm64Host())
	if err != nil {
		t.Fatalf("Resolve (bare, after --isolate): %v", err)
	}
	if !reflect.DeepEqual(bare, first) {
		t.Errorf("bare invocation resolved to %+v, want the remembered isolated target %+v", bare, first)
	}

	// Remembering happens once; re-asking does not rewrite the record.
	if _, err := Resolve(dir, cli.Selector{Isolate: true}, store, arm64Host()); err != nil {
		t.Fatalf("Resolve --isolate (second time): %v", err)
	}
	if len(store.updated) != 1 {
		t.Errorf("UpdateProject calls = %v, want exactly one", store.updated)
	}
}

func TestResolve_SharedOverrideDoesNotPersist_REQ_11_3(t *testing.T) {
	const dir = "/Users/dev/code/app"
	store := newFakeStore(projectAt(dir, func(rec *types.ProjectRecord) { rec.Isolated = true }))

	override, err := Resolve(dir, cli.Selector{Shared: true}, store, arm64Host())
	if err != nil {
		t.Fatalf("Resolve --shared: %v", err)
	}
	if override.MachineName != "avr-ubuntu-24.04-arm64" {
		t.Errorf("machine name = %q, want the shared machine", override.MachineName)
	}
	if override.Kind != types.KindShared || override.Selector.Isolated {
		t.Errorf("--shared did not resolve to the shared machine: kind %q, isolated %v", override.Kind, override.Selector.Isolated)
	}
	if len(store.updated) != 0 {
		t.Errorf("--shared wrote to the project record (%v); it is a one-invocation override", store.updated)
	}

	// The remembered default survives the override.
	after, err := Resolve(dir, cli.Selector{}, store, arm64Host())
	if err != nil {
		t.Fatalf("Resolve (bare, after --shared): %v", err)
	}
	if !after.Selector.Isolated {
		t.Error("the project's remembered isolation was lost by an --shared invocation")
	}
}

func TestResolve_ContradictoryIsolationFlagsAreRefused(t *testing.T) {
	store := newFakeStore()

	if _, err := Resolve("/Users/dev/code/app", cli.Selector{Isolate: true, Shared: true}, store, arm64Host()); err == nil {
		t.Fatal("Resolve silently picked a winner between --isolate and --shared")
	}
}

// --- Machine naming: REQ-4.3 and PROP-2 -----------------------------------

func TestResolve_SharedMachinePerSelector_REQ_4_3(t *testing.T) {
	const dir = "/Users/dev/code/app"

	seen := map[string]types.EnvironmentSelector{}
	for _, env := range SupportedEnvironments() {
		sel := cli.Selector{Distro: env.Distro, DistroVersion: env.Version, Arch: env.Arch}

		got, err := Resolve(dir, sel, newFakeStore(), Options{HostArch: types.ArchARM64})
		if err != nil {
			t.Fatalf("Resolve %+v: %v", env, err)
		}
		if got.Selector != env {
			t.Errorf("selector = %+v, want %+v", got.Selector, env)
		}
		if got.Kind != types.KindShared {
			t.Errorf("%s: kind = %q, want %q", got.MachineName, got.Kind, types.KindShared)
		}
		if err := types.ValidateMachineName(got.MachineName); err != nil {
			t.Errorf("%+v: %v", env, err)
		}
		if other, clash := seen[got.MachineName]; clash {
			t.Errorf("%+v and %+v both map to machine %q; each environment must have its own", env, other, got.MachineName)
		}
		seen[got.MachineName] = env
	}

	if len(seen) != len(SupportedEnvironments()) {
		t.Fatalf("got %d distinct machine names for %d supported environments", len(seen), len(SupportedEnvironments()))
	}
}

func TestResolve_DeterministicTarget_PROP_2(t *testing.T) {
	const dir = "/Users/dev/code/app"

	cases := []struct {
		name     string
		cwd      string
		selector cli.Selector
		recorded []types.ProjectRecord
		opts     Options
	}{
		{name: "defaults", cwd: dir, opts: arm64Host()},
		{name: "explicit environment", cwd: dir, selector: cli.Selector{Distro: types.DistroFedora, Arch: types.ArchAMD64}, opts: arm64Host()},
		{name: "isolated", cwd: dir, selector: cli.Selector{Isolate: true}, opts: arm64Host()},
		{
			name:     "shared override of a remembered isolated project",
			cwd:      dir,
			selector: cli.Selector{Shared: true},
			recorded: []types.ProjectRecord{projectAt(dir, func(rec *types.ProjectRecord) { rec.Isolated = true })},
			opts:     arm64Host(),
		},
		{
			name:     "nested subdirectory of a recorded project",
			cwd:      filepath.Join(dir, "services", "api"),
			recorded: []types.ProjectRecord{projectAt(dir)},
			opts:     arm64Host(),
		},
		{name: "configuration layer", cwd: dir, opts: Options{HostArch: types.ArchAMD64, Config: Preference{Distro: types.DistroDebian}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore(tc.recorded...)

			first, err := Resolve(tc.cwd, tc.selector, store, tc.opts)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			// Repeated against the same (now warmed) state, and against state
			// that never saw the first call: the answer may not depend on
			// either.
			for i := 0; i < 20; i++ {
				again, err := Resolve(tc.cwd, tc.selector, store, tc.opts)
				if err != nil {
					t.Fatalf("Resolve (repeat %d): %v", i, err)
				}
				if !reflect.DeepEqual(again, first) {
					t.Fatalf("repeat %d resolved to %+v, want %+v", i, again, first)
				}
			}
			fresh, err := Resolve(tc.cwd, tc.selector, newFakeStore(tc.recorded...), tc.opts)
			if err != nil {
				t.Fatalf("Resolve (fresh store): %v", err)
			}
			if !reflect.DeepEqual(fresh, first) {
				t.Fatalf("fresh store resolved to %+v, want %+v", fresh, first)
			}
		})
	}
}

func TestResolve_NoMachineNameCollisions_PROP_2(t *testing.T) {
	owner := map[string]string{}

	claim := func(t *testing.T, name, by string) {
		t.Helper()
		if err := types.ValidateMachineName(name); err != nil {
			t.Errorf("%s: %v", by, err)
		}
		if prev, clash := owner[name]; clash {
			t.Errorf("machine %q is claimed by both %s and %s", name, prev, by)
		}
		owner[name] = by
	}

	// Every shared environment in the matrix.
	for _, env := range SupportedEnvironments() {
		sel := cli.Selector{Distro: env.Distro, DistroVersion: env.Version, Arch: env.Arch}
		got, err := Resolve("/Users/dev/code/app", sel, newFakeStore(), arm64Host())
		if err != nil {
			t.Fatalf("Resolve %+v: %v", env, err)
		}
		claim(t, got.MachineName, "shared "+env.Label())
	}

	// Every (project, environment) pair an isolated machine can be asked for.
	// Enumerating the matrix here rather than one environment per project is
	// the point: an isolated environment is identified by its project *and* the
	// environment it was derived from (REQ-11.1), so a project asking for two
	// distributions must get two machines. A naming scheme that dropped the
	// environment would fail this loop on its second iteration.
	for i := 0; i < 500; i++ {
		dir := fmt.Sprintf("/Users/dev/code/project-%d", i)
		for _, env := range SupportedEnvironments() {
			sel := cli.Selector{Distro: env.Distro, DistroVersion: env.Version, Arch: env.Arch, Isolate: true}
			got, err := Resolve(dir, sel, newFakeStore(), arm64Host())
			if err != nil {
				t.Fatalf("Resolve isolated %s: %v", dir, err)
			}
			claim(t, got.MachineName, fmt.Sprintf("isolated %s %s", dir, env.Label()))
		}
	}
}

func TestResolve_IsolatedEnvironmentsAreDistinctPerDistro_REQ_4_2_REQ_11_1(t *testing.T) {
	// One project, two distributions, both isolated. REQ-11.1 derives an
	// isolated environment from a clean base image of the *selected* (distro,
	// arch), and REQ-4.2 says --distro targets a machine of that distribution —
	// so these must be two machines. Were they one, the second invocation would
	// find the first machine already running and hand the user Ubuntu when they
	// asked for Fedora, with nothing in the output to say so.
	const dir = "/Users/dev/code/app"
	store := newFakeStore()

	ubuntu, err := Resolve(dir, cli.Selector{Distro: types.DistroUbuntu, Isolate: true}, store, arm64Host())
	if err != nil {
		t.Fatalf("Resolve --isolate --distro ubuntu: %v", err)
	}
	fedora, err := Resolve(dir, cli.Selector{Distro: types.DistroFedora, Isolate: true}, store, arm64Host())
	if err != nil {
		t.Fatalf("Resolve --isolate --distro fedora: %v", err)
	}

	if ubuntu.MachineName == fedora.MachineName {
		t.Fatalf("both isolated environments resolved to %q; --distro fedora would silently enter the Ubuntu machine", ubuntu.MachineName)
	}
	if ubuntu.Project.ID != fedora.Project.ID {
		t.Errorf("the two environments belong to different projects (%s, %s); they are the same directory", ubuntu.Project.ID, fedora.Project.ID)
	}

	hash := fakeID(dir)[:projectIDPrefixLen]
	if want := "avr-prj-" + hash + "-ubuntu-24.04-arm64"; ubuntu.MachineName != want {
		t.Errorf("ubuntu machine = %q, want %q", ubuntu.MachineName, want)
	}
	if want := "avr-prj-" + hash + "-fedora-43-arm64"; fedora.MachineName != want {
		t.Errorf("fedora machine = %q, want %q", fedora.MachineName, want)
	}

	// The architecture is part of the identity for the same reason.
	amd64, err := Resolve(dir, cli.Selector{Distro: types.DistroUbuntu, Arch: types.ArchAMD64, Isolate: true}, store, arm64Host())
	if err != nil {
		t.Fatalf("Resolve --isolate --distro ubuntu --arch amd64: %v", err)
	}
	if amd64.MachineName == ubuntu.MachineName {
		t.Errorf("both architectures resolved to %q", amd64.MachineName)
	}
}

func TestResolve_LongestMachineNameIsUsable_PROP_2(t *testing.T) {
	// The isolated form is the long end of the matrix: prefix, reserved token,
	// ten hex characters, and the whole environment. Asserted rather than
	// assumed, since a name avar's own pattern rejects would make every later
	// operation refuse to touch the machine.
	const hostnameLabelLimit = 63

	longest := ""
	for _, env := range SupportedEnvironments() {
		env.Isolated = true
		name, err := machineName(env, strings.Repeat("f", 64))
		if err != nil {
			t.Fatalf("machineName(%+v): %v", env, err)
		}
		if err := types.ValidateMachineName(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if len(name) > len(longest) {
			longest = name
		}
	}

	if len(longest) > hostnameLabelLimit {
		t.Errorf("longest machine name %q is %d characters, over the %d-character hostname label limit", longest, len(longest), hostnameLabelLimit)
	}
	t.Logf("longest machine name in the supported matrix: %q (%d characters)", longest, len(longest))
}

func TestMatrix_PinnedVersionIsSupported(t *testing.T) {
	for _, d := range SupportedDistros() {
		pinned, ok := PinnedVersion(d)
		if !ok {
			t.Errorf("%s has no pinned version", d)
			continue
		}
		versions := SupportedVersions(d)
		found := false
		for _, v := range versions {
			if v == pinned {
				found = true
			}
		}
		if !found {
			t.Errorf("%s pins %q, which is not among its supported versions %v", d, pinned, versions)
		}
		if len(SupportedArches(d, pinned)) == 0 {
			t.Errorf("%s %s supports no architecture", d, pinned)
		}
	}
}

func TestMatrix_ReservesTheIsolatedNameToken(t *testing.T) {
	// A distribution named "prj" would let a shared machine collide with a
	// project's isolated machine (PROP-2).
	for _, d := range SupportedDistros() {
		if string(d) == isolatedNameToken {
			t.Errorf("distribution %q collides with the reserved isolated-machine token", d)
		}
	}
}

// --- Unsupported combinations: REQ-4.4 ------------------------------------

func TestResolve_UnsupportedCombinationListsSupportedValues_REQ_4_4(t *testing.T) {
	const dir = "/Users/dev/code/app"

	t.Run("unsupported version", func(t *testing.T) {
		_, err := Resolve(dir, cli.Selector{Distro: types.DistroUbuntu, DistroVersion: "18.04"}, newFakeStore(), arm64Host())
		if err == nil {
			t.Fatal("Resolve accepted a version avar does not support")
		}
		if !errors.Is(err, ErrUnsupportedEnvironment) {
			t.Errorf("error does not wrap ErrUnsupportedEnvironment: %v", err)
		}
		for _, v := range SupportedVersions(types.DistroUbuntu) {
			if !strings.Contains(err.Error(), v) {
				t.Errorf("error does not list supported version %q: %v", v, err)
			}
		}
	})

	t.Run("unsupported distribution reaching the resolver", func(t *testing.T) {
		// internal/cli rejects an unknown --distro name, so this case arrives
		// through a configuration layer instead.
		_, err := Resolve(dir, cli.Selector{}, newFakeStore(), Options{
			HostArch: types.ArchARM64,
			Config:   Preference{Distro: types.Distro("arch")},
		})
		if err == nil {
			t.Fatal("Resolve accepted a distribution avar does not support")
		}
		if !errors.Is(err, ErrUnsupportedEnvironment) {
			t.Errorf("error does not wrap ErrUnsupportedEnvironment: %v", err)
		}
		for _, d := range SupportedDistros() {
			if !strings.Contains(err.Error(), string(d)) {
				t.Errorf("error does not list supported distribution %q: %v", d, err)
			}
		}
	})

	t.Run("unsupported architecture for a release", func(t *testing.T) {
		// No release in the matrix is single-architecture today, so the check
		// itself is exercised directly: it is what stops a future
		// architecture-limited release from being promised.
		err := checkSupported(types.DistroUbuntu, "24.04", types.Arch("riscv64"))
		if err == nil {
			t.Fatal("checkSupported accepted an architecture the release does not offer")
		}
		if !errors.Is(err, ErrUnsupportedEnvironment) {
			t.Errorf("error does not wrap ErrUnsupportedEnvironment: %v", err)
		}
		for _, a := range SupportedArches(types.DistroUbuntu, "24.04") {
			if !strings.Contains(err.Error(), string(a)) {
				t.Errorf("error does not list supported architecture %q: %v", a, err)
			}
		}
	})
}

// --- Emulation reporting: REQ-4.6 -----------------------------------------

func TestResolve_ReportsEmulationWithoutWarning_REQ_4_6(t *testing.T) {
	tests := []struct {
		host         types.Arch
		guest        types.Arch
		wantEmulated bool
	}{
		{host: types.ArchARM64, guest: types.ArchARM64, wantEmulated: false},
		{host: types.ArchARM64, guest: types.ArchAMD64, wantEmulated: true},
		{host: types.ArchAMD64, guest: types.ArchAMD64, wantEmulated: false},
		{host: types.ArchAMD64, guest: types.ArchARM64, wantEmulated: true},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s_on_%s", tc.guest, tc.host), func(t *testing.T) {
			got, err := Resolve("/Users/dev/code/app", cli.Selector{Arch: tc.guest}, newFakeStore(), Options{HostArch: tc.host})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Emulated != tc.wantEmulated {
				t.Errorf("Emulated = %v, want %v", got.Emulated, tc.wantEmulated)
			}
		})
	}
}

// --- Project resolution: REQ-6.6 ------------------------------------------

func TestResolve_NestedDirectoryResolvesToItsProject_REQ_6_6(t *testing.T) {
	const root = "/Users/dev/code/app"
	nested := filepath.Join(root, "services", "api")

	store := newFakeStore(projectAt(root, func(rec *types.ProjectRecord) { rec.Isolated = true }))

	got, err := Resolve(nested, cli.Selector{}, store, arm64Host())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Project.Path != root {
		t.Errorf("project path = %q, want the recorded project %q", got.Project.Path, root)
	}
	if got.Project.ID != fakeID(root) {
		t.Errorf("project id = %q, want the recorded project's identity", got.Project.ID)
	}
	if got.GuestCwd != nested {
		t.Errorf("guest cwd = %q, want the directory avr was run from %q", got.GuestCwd, nested)
	}
	// The subdirectory inherits the project's remembered isolation rather than
	// silently becoming a separate, shared project.
	if got.MachineName != "avr-prj-"+fakeID(root)[:projectIDPrefixLen]+"-ubuntu-24.04-arm64" {
		t.Errorf("machine name = %q, want the project's isolated machine", got.MachineName)
	}
	if len(store.projects) != 1 {
		t.Errorf("recorded %d projects, want the one that already existed", len(store.projects))
	}
}

func TestResolve_NearestRecordedProjectWins_REQ_6_6(t *testing.T) {
	const outer = "/Users/dev/code/mono"
	inner := filepath.Join(outer, "packages", "web")
	deep := filepath.Join(inner, "src")

	store := newFakeStore(projectAt(outer), projectAt(inner))

	got, err := Resolve(deep, cli.Selector{}, store, arm64Host())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Project.Path != inner {
		t.Errorf("project path = %q, want the nearest enclosing recorded project %q", got.Project.Path, inner)
	}
}

func TestResolve_SiblingPrefixIsNotAProject(t *testing.T) {
	// "/Users/dev/code/app" must not capture "/Users/dev/code/application".
	store := newFakeStore(projectAt("/Users/dev/code/app"))

	got, err := Resolve("/Users/dev/code/application", cli.Selector{}, store, arm64Host())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Project.Path != "/Users/dev/code/application" {
		t.Errorf("project path = %q, want the invocation directory registered as its own project", got.Project.Path)
	}
}

func TestResolve_RejectsADirectoryItCannotCompare(t *testing.T) {
	tests := []struct{ name, cwd string }{
		{name: "empty", cwd: ""},
		{name: "blank", cwd: "   "},
		{name: "relative", cwd: "code/app"},
		{name: "dot", cwd: "."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Resolve(tc.cwd, cli.Selector{}, newFakeStore(), arm64Host()); err == nil {
				t.Fatalf("Resolve(%q) succeeded; the resolver needs an absolute, symlink-resolved directory", tc.cwd)
			}
		})
	}
}

func TestResolve_UnresolvedDirectoryIsRefusedRatherThanMounted(t *testing.T) {
	// A store whose canonical path is not the directory it was asked about
	// stands in for a caller that passed an unresolved (symlinked) cwd:
	// continuing would share one directory and enter another (REQ-6.5).
	store := &misreportingStore{}

	_, err := Resolve("/tmp/link/app", cli.Selector{}, store, arm64Host())
	if err == nil {
		t.Fatal("Resolve accepted a project record that does not contain the invocation directory")
	}
	if !strings.Contains(err.Error(), "/private/tmp/real/app") {
		t.Errorf("error does not name the path avar actually recorded: %v", err)
	}
}

type misreportingStore struct{}

func (misreportingStore) Projects() ([]types.ProjectRecord, error) { return nil, nil }

func (misreportingStore) EnsureProject(string) (types.ProjectRecord, error) {
	const canonical = "/private/tmp/real/app"
	return types.ProjectRecord{ID: fakeID(canonical), Path: canonical}, nil
}

func (misreportingStore) UpdateProject(string, func(*types.ProjectRecord)) (types.ProjectRecord, error) {
	return types.ProjectRecord{}, errors.New("not expected")
}

// --- Store failures -------------------------------------------------------

func TestResolve_ReportsStoreFailures(t *testing.T) {
	t.Run("listing projects", func(t *testing.T) {
		store := newFakeStore()
		store.projectsErr = errors.New("projects.json is not valid JSON")

		_, err := Resolve("/Users/dev/code/app", cli.Selector{}, store, arm64Host())
		if err == nil || !strings.Contains(err.Error(), "projects.json") {
			t.Fatalf("error = %v, want the underlying cause wrapped", err)
		}
	})

	t.Run("remembering isolation", func(t *testing.T) {
		store := newFakeStore()
		store.updateErr = errors.New("state directory is read-only")

		_, err := Resolve("/Users/dev/code/app", cli.Selector{Isolate: true}, store, arm64Host())
		if err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("error = %v, want the underlying cause wrapped", err)
		}
	})

	t.Run("no store at all", func(t *testing.T) {
		if _, err := Resolve("/Users/dev/code/app", cli.Selector{}, nil, arm64Host()); err == nil {
			t.Fatal("Resolve accepted a nil store")
		}
	})
}

// --- Against the real state store -----------------------------------------

// TestResolve_AgainstRealStateStore_REQ_11_2 runs the same flows against
// internal/state rather than the fake, which is what proves the narrow Store
// interface is the real thing's shape and that the identities these tests
// assert on are the identities production computes.
func TestResolve_AgainstRealStateStore_REQ_11_2(t *testing.T) {
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open state directory: %v", err)
	}

	// The temporary directory is reached through a symlink on macOS, so
	// canonicalise it the way the command layer will.
	root, err := state.ResolveProjectPath(t.TempDir())
	if err != nil {
		t.Fatalf("resolve project path: %v", err)
	}
	nested := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create %s: %v", nested, err)
	}

	id, err := state.ProjectID(root)
	if err != nil {
		t.Fatalf("project id: %v", err)
	}
	if id != fakeID(root) {
		t.Fatalf("the fake store hashes paths differently from internal/state (%s vs %s)", fakeID(root), id)
	}

	isolated, err := Resolve(root, cli.Selector{Isolate: true}, st, arm64Host())
	if err != nil {
		t.Fatalf("Resolve --isolate: %v", err)
	}
	wantName := "avr-prj-" + id[:projectIDPrefixLen] + "-ubuntu-24.04-arm64"
	if isolated.MachineName != wantName {
		t.Fatalf("machine name = %q, want %q", isolated.MachineName, wantName)
	}

	// REQ-11.2 across processes: the choice is in the state directory, not in
	// memory, so a second store over the same directory sees it.
	reopened, err := state.Open(st.Root())
	if err != nil {
		t.Fatalf("reopen state directory: %v", err)
	}
	bare, err := Resolve(nested, cli.Selector{}, reopened, arm64Host())
	if err != nil {
		t.Fatalf("Resolve (bare, nested, reopened store): %v", err)
	}
	if bare.MachineName != wantName {
		t.Errorf("nested bare invocation resolved to %q, want the project's remembered machine %q", bare.MachineName, wantName)
	}
	if bare.Project.Path != root {
		t.Errorf("project path = %q, want %q", bare.Project.Path, root)
	}
	if bare.GuestCwd != nested {
		t.Errorf("guest cwd = %q, want %q", bare.GuestCwd, nested)
	}

	// REQ-11.3: the override does not touch what was remembered.
	shared, err := Resolve(root, cli.Selector{Shared: true}, st, arm64Host())
	if err != nil {
		t.Fatalf("Resolve --shared: %v", err)
	}
	if shared.MachineName != "avr-ubuntu-24.04-arm64" {
		t.Errorf("machine name = %q, want the shared machine", shared.MachineName)
	}
	rec, ok, err := st.Project(id)
	if err != nil || !ok {
		t.Fatalf("read project record: ok=%v err=%v", ok, err)
	}
	if !rec.Isolated {
		t.Error("--shared cleared the project's remembered isolation")
	}
}
