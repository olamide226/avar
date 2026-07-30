package types

import (
	"runtime"
	"strings"
	"testing"
)

func TestParseArch_RejectsUnsupportedAndListsOptions_REQ_4_4(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    Arch
		wantErr bool
	}{
		{name: "arm64", input: "arm64", want: ArchARM64},
		{name: "amd64", input: "amd64", want: ArchAMD64},
		{name: "uppercase is accepted", input: "ARM64", want: ArchARM64},
		{name: "surrounding space is trimmed", input: "  amd64 ", want: ArchAMD64},
		{name: "x86_64 alias is not supported", input: "x86_64", wantErr: true},
		{name: "empty", input: "", wantErr: true},
		{name: "riscv", input: "riscv64", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseArch(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseArch(%q) = %q, want error", tc.input, got)
				}
				// The error must name the supported values so the user can
				// act on it without reading the docs.
				for _, want := range []string{string(ArchARM64), string(ArchAMD64)} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention supported value %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseArch(%q) returned unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseArch(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestHostArch_MatchesRuntime(t *testing.T) {
	t.Parallel()

	want := ArchARM64
	if runtime.GOARCH == "amd64" {
		want = ArchAMD64
	}
	if got := HostArch(); got != want {
		t.Errorf("HostArch() = %q, want %q for GOARCH %q", got, want, runtime.GOARCH)
	}
	if !HostArch().Native() {
		t.Error("the host architecture must report itself as native")
	}
}

func TestSelectorEmulated_OnlyForForeignArch_REQ_4_6(t *testing.T) {
	t.Parallel()

	native := EnvironmentSelector{Distro: DistroUbuntu, Version: "24.04", Arch: HostArch()}
	if native.Emulated() {
		t.Error("a host-architecture selector must not be reported as emulated")
	}

	foreign := ArchAMD64
	if HostArch() == ArchAMD64 {
		foreign = ArchARM64
	}
	if !(EnvironmentSelector{Distro: DistroUbuntu, Version: "24.04", Arch: foreign}).Emulated() {
		t.Errorf("selector for %q on host %q must be reported as emulated", foreign, HostArch())
	}
}

func TestSelectorLabel_IsHumanReadable(t *testing.T) {
	t.Parallel()

	got := EnvironmentSelector{Distro: DistroUbuntu, Version: "24.04", Arch: ArchARM64}.Label()
	if want := "Ubuntu 24.04 · arm64"; got != want {
		t.Errorf("Label() = %q, want %q", got, want)
	}
}

func TestValidateMachineName_OnlyAcceptsAvarOwnedNames_REQ_5_4(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		machine string
		wantErr bool
	}{
		{name: "shared machine", machine: "avr-ubuntu-24.04-arm64"},
		{name: "isolated machine", machine: "avr-prj-3fa9c2b1d0"},
		{name: "base machine", machine: "avr-base-ubuntu-24.04-arm64"},
		{name: "user's own lima machine", machine: "default", wantErr: true},
		{name: "colima", machine: "colima", wantErr: true},
		{name: "prefix must be at the start", machine: "my-avr-machine", wantErr: true},
		{name: "prefix alone is not a name", machine: "avr-", wantErr: true},
		{name: "uppercase is rejected", machine: "avr-Ubuntu", wantErr: true},
		{name: "path traversal is rejected", machine: "avr-../../etc", wantErr: true},
		{name: "empty", machine: "", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateMachineName(tc.machine)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateMachineName(%q) = nil, want error", tc.machine)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateMachineName(%q) = %v, want nil", tc.machine, err)
			}
		})
	}
}
