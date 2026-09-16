package projconfig

import (
	"reflect"
	"testing"

	"github.com/olamide226/avar/internal/types"
)

func TestInstallCommands_PerDistribution_REQ_15_1(t *testing.T) {
	apt := func(args ...string) []string {
		return append([]string{"sudo", "-n", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get"}, args...)
	}
	for _, tc := range []struct {
		distro types.Distro
		want   [][]string
	}{
		{types.DistroUbuntu, [][]string{apt("update"), apt("install", "-y", "ripgrep", "g++")}},
		{types.DistroDebian, [][]string{apt("update"), apt("install", "-y", "ripgrep", "g++")}},
		{types.DistroFedora, [][]string{{"sudo", "-n", "dnf", "install", "-y", "ripgrep", "g++"}}},
	} {
		t.Run(string(tc.distro), func(t *testing.T) {
			got, err := InstallCommands(tc.distro, []string{"ripgrep", "g++"})
			if err != nil {
				t.Fatalf("InstallCommands: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("InstallCommands = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInstallCommands_RefusesWhatParseWouldRefuse(t *testing.T) {
	for _, name := range []string{"-y", "--allow-downgrades", "./x.deb", "/tmp/x.rpm", "jq=1.6", "a b", ""} {
		if _, err := InstallCommands(types.DistroUbuntu, []string{"jq", name}); err == nil {
			t.Errorf("InstallCommands accepted %q", name)
		}
	}
	if _, err := InstallCommands("arch", []string{"jq"}); err == nil {
		t.Error("InstallCommands invented a package manager for an unknown distribution")
	}
}

func TestInstallCommands_NothingToInstallRunsNothing(t *testing.T) {
	got, err := InstallCommands(types.DistroUbuntu, nil)
	if err != nil || got != nil {
		t.Errorf("InstallCommands(nil) = %q, %v; want nothing", got, err)
	}
}

func TestConfig_PackagesApplyOnlyToTheirDistribution_REQ_15_1(t *testing.T) {
	c := Config{Distro: "Ubuntu", Packages: []string{"jq"}}
	if !c.PackagesApplyTo(types.DistroUbuntu) {
		t.Error("packages for Ubuntu do not apply to Ubuntu")
	}
	if c.PackagesApplyTo(types.DistroFedora) {
		t.Error("packages for Ubuntu apply to Fedora")
	}
	if (Config{Distro: types.DistroUbuntu}).PackagesApplyTo(types.DistroUbuntu) {
		t.Error("a file with no packages reports packages that apply")
	}
}

func TestConfig_ResourceDeclaration(t *testing.T) {
	for _, tc := range []struct {
		c    Config
		want string
	}{
		{Config{}, ""},
		{Config{CPUs: 4}, "cpus=4"},
		{Config{MemoryMiB: 8192}, "memory=8GiB"},
		{Config{CPUs: 2, MemoryMiB: 1536}, "cpus=2 memory=1536MiB"},
	} {
		if got := tc.c.ResourceDeclaration(); got != tc.want {
			t.Errorf("%+v.ResourceDeclaration() = %q, want %q", tc.c, got, tc.want)
		}
	}
	if got := (Config{MemoryMiB: 1536}).MemoryGB(); got != 1.5 {
		t.Errorf("MemoryGB = %v, want 1.5", got)
	}
}
