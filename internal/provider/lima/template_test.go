package lima

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// updateGolden rewrites the expected configurations instead of comparing
// against them: `go test ./internal/provider/lima/ -update`. The generated
// files are committed so that a change to the template, an image pin or a
// resource default shows up in review as a diff of the machine a user will
// actually get, rather than as a change to Go code whose effect a reviewer has
// to imagine.
var updateGolden = flag.Bool("update", false, "rewrite the golden Lima configurations")

// testHost is the host every test sizes machines against, so that generated
// configuration does not depend on whatever machine the tests run on.
var testHost = HostResources{CPUs: 10, MemoryGB: 32, Arch: types.ArchARM64}

func TestInstanceTemplate_Parses(t *testing.T) {
	if instanceTemplate == nil {
		t.Fatal("the embedded instance template did not parse")
	}
}

func TestRenderInstanceConfig_Golden_REQ_1_2(t *testing.T) {
	// Every case pins the host explicitly, so the generated configuration is a
	// function of its inputs alone and the golden files do not depend on the
	// machine running the tests.
	appleSilicon := HostResources{CPUs: 10, MemoryGB: 32, Arch: types.ArchARM64}
	intelMac := HostResources{CPUs: 8, MemoryGB: 16, Arch: types.ArchAMD64}

	cases := []struct {
		golden string
		host   HostResources
		spec   provider.MachineSpec
	}{
		{
			// The default environment on the default host: native arm64, so
			// vz + VirtioFS + Rosetta, with two registered projects.
			golden: "ubuntu-24.04-arm64-vz.yaml",
			host:   appleSilicon,
			spec: provider.MachineSpec{
				Name:     "avr-ubuntu-24.04-arm64",
				Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64},
				Kind:     types.KindShared,
				Mounts:   shares("/Users/dev/code/web", "/Users/dev/code/api"),
			},
		},
		{
			// `avr --arch amd64` on Apple silicon: a whole emulated machine,
			// no Rosetta, and SSHFS because QEMU has no VirtioFS (REQ-4.6).
			golden: "ubuntu-24.04-amd64-qemu.yaml",
			host:   appleSilicon,
			spec: provider.MachineSpec{
				Name:     "avr-ubuntu-24.04-amd64",
				Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchAMD64},
				Kind:     types.KindShared,
				Mounts:   shares("/Users/dev/code/api"),
			},
		},
		{
			// An Intel host running its own architecture: vz and VirtioFS, but
			// no Rosetta, which only exists to translate x86_64 into arm64.
			golden: "ubuntu-24.04-amd64-vz-intel.yaml",
			host:   intelMac,
			spec: provider.MachineSpec{
				Name:     "avr-ubuntu-24.04-amd64",
				Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchAMD64},
				Kind:     types.KindShared,
				Mounts:   shares("/Users/dev/code/api"),
			},
		},
		{
			golden: "debian-12-arm64-vz.yaml",
			host:   appleSilicon,
			spec: provider.MachineSpec{
				Name:     "avr-debian-12-arm64",
				Selector: types.EnvironmentSelector{Distro: types.DistroDebian, Version: "12", Arch: types.ArchARM64},
				Kind:     types.KindShared,
				Mounts:   shares("/Users/dev/code/api"),
			},
		},
		{
			golden: "fedora-42-arm64-vz.yaml",
			host:   appleSilicon,
			spec: provider.MachineSpec{
				Name:     "avr-fedora-42-arm64",
				Selector: types.EnvironmentSelector{Distro: types.DistroFedora, Version: "42", Arch: types.ArchARM64},
				Kind:     types.KindShared,
				Mounts:   shares("/Users/dev/code/api"),
			},
		},
		{
			// A base machine has no project registered yet, and a small host
			// gets the floor rather than a quarter of very little (REQ-17.4).
			golden: "ubuntu-24.04-arm64-no-mounts-small-host.yaml",
			host:   HostResources{CPUs: 2, MemoryGB: 8, Arch: types.ArchARM64},
			spec: provider.MachineSpec{
				Name:     "avr-base-ubuntu-24.04-arm64",
				Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64},
				Kind:     types.KindBase,
			},
		},
		{
			// The caller sized this one, so the host-proportional defaults do
			// not apply: MachineSpec says a non-zero value is the caller's
			// decision.
			golden: "ubuntu-22.04-arm64-explicit-resources.yaml",
			host:   appleSilicon,
			spec: provider.MachineSpec{
				Name:     "avr-prj-3fa9c2b1de",
				Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "22.04", Arch: types.ArchARM64, Isolated: true},
				Kind:     types.KindIsolated,
				Mounts:   shares("/Users/dev/code/api"),
				CPUs:     2,
				MemoryGB: 3,
				DiskGB:   40,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.golden, func(t *testing.T) {
			got, err := renderInstanceConfig(tc.spec, tc.host)
			if err != nil {
				t.Fatalf("renderInstanceConfig: %v", err)
			}
			compareGolden(t, tc.golden, got)
		})
	}
}

// compareGolden compares generated output against its committed expectation.
func compareGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating the golden directory: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s (run `go test ./internal/provider/lima/ -update` to create it): %v", path, err)
	}
	if string(got) != string(want) {
		t.Errorf("generated configuration does not match %s.\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

func TestNewInstanceConfig_NativeArchGetsVZVirtioFSAndRosetta_REQ_4_6(t *testing.T) {
	cfg, err := newInstanceConfig(provider.MachineSpec{
		Name:     "avr-ubuntu-24.04-arm64",
		Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64},
	}, HostResources{CPUs: 10, MemoryGB: 32, Arch: types.ArchARM64})
	if err != nil {
		t.Fatalf("newInstanceConfig: %v", err)
	}
	if cfg.VMType != vmTypeVZ {
		t.Errorf("vmType = %q, want %q: a native guest runs on Apple's Virtualization framework", cfg.VMType, vmTypeVZ)
	}
	if cfg.MountType != mountTypeVirtioFS {
		t.Errorf("mountType = %q, want %q (REQ-6.1)", cfg.MountType, mountTypeVirtioFS)
	}
	if !cfg.Rosetta {
		t.Error("Rosetta is disabled: an arm64 guest on Apple silicon should be able to run x86_64 binaries without a second machine")
	}
}

func TestNewInstanceConfig_ForeignArchGetsEmulatedQEMU_REQ_4_6(t *testing.T) {
	cfg, err := newInstanceConfig(provider.MachineSpec{
		Name:     "avr-ubuntu-24.04-amd64",
		Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchAMD64},
	}, HostResources{CPUs: 10, MemoryGB: 32, Arch: types.ArchARM64})
	if err != nil {
		t.Fatalf("newInstanceConfig: %v", err)
	}
	if cfg.VMType != vmTypeQEMU {
		t.Errorf("vmType = %q, want %q: a foreign architecture has to be emulated", cfg.VMType, vmTypeQEMU)
	}
	if cfg.MountType != mountTypeReverseSSHFS {
		t.Errorf("mountType = %q, want %q: QEMU has no VirtioFS on macOS", cfg.MountType, mountTypeReverseSSHFS)
	}
	if cfg.Rosetta {
		t.Error("Rosetta is enabled on an emulated x86_64 guest, where it has nothing to translate")
	}
}

func TestNewInstanceConfig_IntelHostGetsNoRosetta(t *testing.T) {
	cfg, err := newInstanceConfig(provider.MachineSpec{
		Name:     "avr-ubuntu-24.04-amd64",
		Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchAMD64},
	}, HostResources{CPUs: 8, MemoryGB: 16, Arch: types.ArchAMD64})
	if err != nil {
		t.Fatalf("newInstanceConfig: %v", err)
	}
	if cfg.VMType != vmTypeVZ {
		t.Errorf("vmType = %q, want %q: x86_64 is native on an Intel Mac", cfg.VMType, vmTypeVZ)
	}
	if cfg.Rosetta {
		t.Error("Rosetta is enabled on an Intel host, where it does not exist")
	}
}

func TestRenderInstanceConfig_SharesProjectsWritableAtIdenticalPaths_REQ_6_1(t *testing.T) {
	out, err := renderInstanceConfig(provider.MachineSpec{
		Name:     "avr-ubuntu-24.04-arm64",
		Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64},
		Mounts:   shares("/Users/dev/code/api"),
	}, testHost)
	if err != nil {
		t.Fatalf("renderInstanceConfig: %v", err)
	}
	got := string(out)
	for _, want := range []string{
		`- location: "/Users/dev/code/api"`,
		`  mountPoint: "/Users/dev/code/api"`,
		`  writable: true`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated configuration is missing %q:\n%s", want, got)
		}
	}
}

func TestRenderInstanceConfig_SharesNothingButRegisteredProjects_PROP_5(t *testing.T) {
	out, err := renderInstanceConfig(provider.MachineSpec{
		Name:     "avr-ubuntu-24.04-arm64",
		Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64},
		Mounts:   shares("/Users/dev/code/api"),
	}, testHost)
	if err != nil {
		t.Fatalf("renderInstanceConfig: %v", err)
	}
	got := string(out)
	// The home directory is what Lima's own templates share by default and is
	// precisely what avar must never share (REQ-6.3, REQ-9.3, REQ-9.4).
	for _, forbidden := range []string{
		`location: "~"`,
		`location: "/Users/dev"` + "\n",
		"loadDotSSHPubKeys: true",
		"forwardAgent: true",
	} {
		if strings.Contains(got, forbidden) {
			t.Errorf("generated configuration contains %q, which crosses the boundary avar promises to hold:\n%s", forbidden, got)
		}
	}
	if !strings.Contains(got, "propagateProxyEnv: false") {
		t.Error("proxy environment variables are propagated from the host, which is a host environment variable crossing into the guest (REQ-9.1)")
	}
}

func TestRenderInstanceConfig_MountsListIsEmptyNotAbsentWhenNoProjects(t *testing.T) {
	out, err := renderInstanceConfig(provider.MachineSpec{
		Name:     "avr-base-ubuntu-24.04-arm64",
		Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64},
	}, testHost)
	if err != nil {
		t.Fatalf("renderInstanceConfig: %v", err)
	}
	if !strings.Contains(string(out), "mounts: []") {
		t.Errorf("a machine with no registered project should share an explicitly empty list:\n%s", out)
	}
}

func TestRenderInstanceConfig_QuotesPathsThatWouldOtherwiseBeYAML(t *testing.T) {
	// A directory name is user data. If it could reach the configuration
	// unquoted it could add configuration of its own.
	hostile := `/Users/dev/code/we"ird: {a: b}` + "\n" + `mounts: [{location: "/"}]`
	out, err := renderInstanceConfig(provider.MachineSpec{
		Name:     "avr-ubuntu-24.04-arm64",
		Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64},
		Mounts:   shares(hostile),
	}, testHost)
	if err != nil {
		t.Fatalf("renderInstanceConfig: %v", err)
	}
	got := string(out)
	// The injected text must stay inside its quoted scalar: exactly one line
	// may begin a top-level mounts key, however many times the word appears
	// inside a value.
	var topLevelMountKeys int
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "mounts:") {
			topLevelMountKeys++
		}
	}
	if topLevelMountKeys != 1 {
		t.Errorf("a directory name introduced %d top-level mounts keys:\n%s", topLevelMountKeys, got)
	}
	if !strings.Contains(got, `\"ird`) || !strings.Contains(got, `\n`) {
		t.Errorf("the quote and newline in the directory name were not escaped:\n%s", got)
	}
}

func TestRenderInstanceConfig_UnsupportedEnvironmentNamesWhatIsSupported_REQ_4_4(t *testing.T) {
	_, err := renderInstanceConfig(provider.MachineSpec{
		Name:     "avr-ubuntu-18.04-arm64",
		Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "18.04", Arch: types.ArchARM64},
	}, testHost)
	if err == nil {
		t.Fatal("an environment avar has no pinned image for was accepted")
	}
	if !strings.Contains(err.Error(), "ubuntu:24.04") {
		t.Errorf("the error does not list what avar can provision: %v", err)
	}
}

func TestRenderInstanceConfig_RelativeMountIsRefused(t *testing.T) {
	_, err := renderInstanceConfig(provider.MachineSpec{
		Name:     "avr-ubuntu-24.04-arm64",
		Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64},
		Mounts:   shares("code/api"),
	}, testHost)
	if err == nil {
		t.Fatal("a relative directory was accepted, which cannot be shared at an identical absolute path")
	}
}
