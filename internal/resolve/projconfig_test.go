package resolve

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/projconfig"
	"github.com/olamide226/avar/internal/state"
	"github.com/olamide226/avar/internal/types"
)

// fileAt is a project-configuration reader that finds cfg in dir and nothing
// anywhere else, recording every directory it was asked about.
type fileAt struct {
	dir   string
	cfg   projconfig.Config
	err   error
	asked []string
}

func (f *fileAt) read(projectDir string) (projconfig.Config, error) {
	f.asked = append(f.asked, projectDir)
	if projectDir != f.dir {
		return projconfig.Config{}, nil
	}
	if f.err != nil {
		return projconfig.Config{}, f.err
	}
	return f.cfg, nil
}

func withFile(opts Options, f *fileAt) Options {
	opts.ProjectConfig = f.read
	return opts
}

// The file's place in the precedence chain: below everything the user chose on
// this host, above avar's own defaults.
func TestResolve_ProjectConfigPrecedence_REQ_15_1(t *testing.T) {
	dir := hostPath("/Users/dev/code/app")
	file := projconfig.Config{Path: dir + "/.avr.toml", Distro: types.DistroFedora, Arch: types.ArchAMD64}

	tests := []struct {
		name     string
		selector cli.Selector
		recorded []types.ProjectRecord
		config   Preference
		wantEnv  types.EnvironmentSelector
	}{
		{
			name:    "the file beats the built-in defaults",
			wantEnv: types.EnvironmentSelector{Distro: types.DistroFedora, Version: "43", Arch: types.ArchAMD64},
		},
		{
			name:    "the file beats avar's global configuration",
			config:  Preference{Distro: types.DistroDebian, Arch: types.ArchARM64},
			wantEnv: types.EnvironmentSelector{Distro: types.DistroFedora, Version: "43", Arch: types.ArchAMD64},
		},
		{
			name: "the project's remembered environment beats the file",
			recorded: []types.ProjectRecord{projectAt(dir, func(rec *types.ProjectRecord) {
				rec.Selector = &types.EnvironmentSelector{Distro: types.DistroDebian, Arch: types.ArchARM64}
			})},
			wantEnv: types.EnvironmentSelector{Distro: types.DistroDebian, Version: "13", Arch: types.ArchARM64},
		},
		{
			name:     "flags beat the file",
			selector: cli.Selector{Distro: types.DistroUbuntu, Arch: types.ArchARM64},
			wantEnv:  types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64},
		},
		{
			name:     "a flag for one setting leaves the file's other setting in force",
			selector: cli.Selector{Arch: types.ArchARM64},
			wantEnv:  types.EnvironmentSelector{Distro: types.DistroFedora, Version: "43", Arch: types.ArchARM64},
		},
		{
			name:     "a flag naming a distribution does not inherit the file's version",
			selector: cli.Selector{Distro: types.DistroDebian},
			wantEnv:  types.EnvironmentSelector{Distro: types.DistroDebian, Version: "13", Arch: types.ArchAMD64},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fileAt{dir: dir, cfg: file}
			opts := withFile(Options{HostArch: types.ArchARM64, Config: tc.config}, reader)

			got, err := Resolve(types.ProviderLima, dir, tc.selector, newFakeStore(tc.recorded...), opts)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Selector != tc.wantEnv {
				t.Errorf("selector = %+v, want %+v", got.Selector, tc.wantEnv)
			}
			if !reflect.DeepEqual(got.Config, file) {
				t.Errorf("target carries config %+v, want the file as read %+v", got.Config, file)
			}
		})
	}
}

func TestResolve_ProjectConfigVersionPinsTheRelease_REQ_15_1(t *testing.T) {
	dir := hostPath("/Users/dev/code/app")
	reader := &fileAt{dir: dir, cfg: projconfig.Config{Path: "x", Distro: types.DistroDebian, Version: "13"}}

	got, err := Resolve(types.ProviderLima, dir, cli.Selector{}, newFakeStore(), withFile(arm64Host(), reader))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.MachineName != "avr-debian-13-arm64" {
		t.Errorf("machine name = %q, want avr-debian-13-arm64", got.MachineName)
	}
}

// The file is read from the project's directory — the nearest recorded
// directory, which is what is shared into the guest — and from nowhere else:
// not from the working directory, and not from any parent.
func TestResolve_ProjectConfigIsReadFromTheProjectDirectoryOnly_REQ_15_1(t *testing.T) {
	root := hostPath("/Users/dev/code/app")
	nested := filepath.Join(root, "services", "api")
	reader := &fileAt{dir: root, cfg: projconfig.Config{Path: "x", Distro: types.DistroFedora}}

	got, err := Resolve(types.ProviderLima, nested, cli.Selector{}, newFakeStore(projectAt(root)), withFile(arm64Host(), reader))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !reflect.DeepEqual(reader.asked, []string{root}) {
		t.Errorf("the reader was asked about %v, want only the project directory %s", reader.asked, root)
	}
	if got.Selector.Distro != types.DistroFedora {
		t.Errorf("a nested invocation did not apply its project's file: %+v", got.Selector)
	}

	// With nothing recorded, the directory avr runs in is the project, and a
	// file in a parent is not read.
	parentOnly := &fileAt{dir: root, cfg: projconfig.Config{Path: "x", Distro: types.DistroFedora}}
	got, err = Resolve(types.ProviderLima, nested, cli.Selector{}, newFakeStore(), withFile(arm64Host(), parentOnly))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !reflect.DeepEqual(parentOnly.asked, []string{nested}) {
		t.Errorf("the reader was asked about %v, want only %s", parentOnly.asked, nested)
	}
	if got.Selector.Distro != types.DistroUbuntu {
		t.Errorf("a file in a parent of the project was applied: %+v", got.Selector)
	}
}

// A file that cannot be read fails the invocation before anything about the
// project changes — in particular before --isolate is remembered.
func TestResolve_UnreadableProjectConfigChangesNothing_REQ_15_1(t *testing.T) {
	dir := hostPath("/Users/dev/code/app")
	broken := errors.New("line 3: unknown key")
	reader := &fileAt{dir: dir, err: broken}
	store := newFakeStore()

	_, err := Resolve(types.ProviderLima, dir, cli.Selector{Isolate: true}, store, withFile(arm64Host(), reader))
	if !errors.Is(err, broken) {
		t.Fatalf("Resolve = %v, want the reader's error", err)
	}
	if len(store.updated) != 0 {
		t.Errorf("isolation was remembered despite the unreadable file: updates %v", store.updated)
	}
}

// A value the user never typed has to be traced to where it came from, and it
// must still fail as an unsupported environment so the exit status matches
// the flag's (REQ-4.4).
func TestResolve_UnsupportedProjectConfigNamesTheFile_REQ_15_1(t *testing.T) {
	dir := hostPath("/Users/dev/code/app")
	path := dir + "/.avr.toml"

	for _, tc := range []struct {
		name  string
		cfg   projconfig.Config
		sel   cli.Selector
		names string
	}{
		{"unknown distribution", projconfig.Config{Path: path, Distro: "arch"}, cli.Selector{}, "distro is set in"},
		{"unknown release", projconfig.Config{Path: path, Distro: types.DistroFedora, Version: "12"}, cli.Selector{}, "distro is set in"},
		{"unknown architecture", projconfig.Config{Path: path, Arch: "riscv64"}, cli.Selector{}, "arch is set in"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(types.ProviderLima, dir, tc.sel, newFakeStore(), withFile(arm64Host(), &fileAt{dir: dir, cfg: tc.cfg}))
			if !errors.Is(err, ErrUnsupportedEnvironment) {
				t.Fatalf("Resolve = %v, want ErrUnsupportedEnvironment", err)
			}
			if !strings.Contains(err.Error(), tc.names) || !strings.Contains(err.Error(), path) {
				t.Errorf("error does not say the file chose the value: %v", err)
			}
		})
	}

	// When a flag chose the unsupported value, the file is not blamed.
	_, err := Resolve(types.ProviderLima, dir, cli.Selector{Distro: types.DistroFedora, DistroVersion: "12"}, newFakeStore(),
		withFile(arm64Host(), &fileAt{dir: dir, cfg: projconfig.Config{Path: path, Arch: types.ArchARM64}}))
	if err == nil || strings.Contains(err.Error(), path) {
		t.Errorf("Resolve = %v, want an error that does not blame the file", err)
	}
}

// PROP-24, first clause: a project without a file resolves exactly as it did
// before project configuration existed, for every input the resolver takes.
func TestProp_ZeroConfigurationInvariance_PROP_24(t *testing.T) {
	root := hostPath("/Users/dev/code/app")
	nested := filepath.Join(root, "web")

	selectors := []cli.Selector{
		{},
		{Isolate: true},
		{Shared: true},
		{Arch: types.ArchAMD64},
		{Distro: types.DistroFedora},
		{Distro: types.DistroDebian, DistroVersion: "13", Arch: types.ArchARM64},
		{Distro: types.DistroFedora, DistroVersion: "12"}, // unsupported: errors must match too
	}
	records := [][]types.ProjectRecord{
		nil,
		{projectAt(root)},
		{projectAt(root, func(rec *types.ProjectRecord) { rec.Isolated = true })},
		{projectAt(root, func(rec *types.ProjectRecord) {
			rec.Selector = &types.EnvironmentSelector{Distro: types.DistroDebian, Arch: types.ArchAMD64}
		})},
	}
	options := []Options{
		{HostArch: types.ArchARM64},
		{HostArch: types.ArchAMD64},
		{HostArch: types.ArchARM64, Config: Preference{Distro: types.DistroDebian}},
	}

	checked := 0
	for _, cwd := range []string{root, nested} {
		for _, sel := range selectors {
			for _, recs := range records {
				for _, opts := range options {
					without, errWithout := Resolve(types.ProviderLima, cwd, sel, newFakeStore(recs...), opts)

					absent := &fileAt{dir: "nowhere"}
					with, errWith := Resolve(types.ProviderLima, cwd, sel, newFakeStore(recs...), withFile(opts, absent))

					if (errWithout == nil) != (errWith == nil) || (errWith != nil && errWith.Error() != errWithout.Error()) {
						t.Fatalf("cwd=%s sel=%+v records=%v opts=%+v: errors differ: %v vs %v", cwd, sel, recs, opts, errWithout, errWith)
					}
					if !reflect.DeepEqual(with, without) {
						t.Fatalf("cwd=%s sel=%+v records=%v opts=%+v: a project without a file resolved to\n%+v\nwant\n%+v", cwd, sel, recs, opts, with, without)
					}
					checked++
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("the property checked no inputs")
	}
}

// End to end through the real reader and the real store: the file on disk
// selects the environment.
func TestResolve_ProjectConfigAgainstRealFiles_REQ_15_1(t *testing.T) {
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, err := state.ResolveProjectPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, projconfig.FileName), []byte("distro = \"fedora\"\narch = \"amd64\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(types.ProviderLima, root, cli.Selector{}, st, Options{HostArch: types.ArchARM64, ProjectConfig: projconfig.Load})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.MachineName != "avr-fedora-43-amd64" {
		t.Errorf("machine name = %q, want avr-fedora-43-amd64", got.MachineName)
	}
	if !got.Emulated {
		t.Error("an amd64 environment chosen by the file on an arm64 host is not reported as emulated")
	}
}
