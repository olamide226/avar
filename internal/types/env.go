// Package types holds the vocabulary shared by avar's packages: environment
// selectors, persisted records, and provider contracts.
//
// It exists to keep internal/state, internal/resolve and internal/provider
// free of import cycles. It contains data definitions and their validation
// only — no I/O, no subprocess execution, no terminal output.
package types

import (
	"fmt"
	"runtime"
	"strings"
)

// Arch is a guest CPU architecture.
type Arch string

const (
	ArchARM64 Arch = "arm64"
	ArchAMD64 Arch = "amd64"
)

// HostArch reports the architecture of the machine avar is running on.
func HostArch() Arch {
	if runtime.GOARCH == "amd64" {
		return ArchAMD64
	}
	return ArchARM64
}

// Native reports whether a guest of this architecture can run without CPU
// emulation on the current host.
func (a Arch) Native() bool { return a == HostArch() }

// ParseArch validates a user-supplied architecture name.
func ParseArch(s string) (Arch, error) {
	switch Arch(strings.ToLower(strings.TrimSpace(s))) {
	case ArchARM64:
		return ArchARM64, nil
	case ArchAMD64:
		return ArchAMD64, nil
	default:
		return "", fmt.Errorf("unsupported architecture %q: supported values are %s, %s", s, ArchARM64, ArchAMD64)
	}
}

// Distro identifies a Linux distribution family.
type Distro string

const (
	DistroUbuntu Distro = "ubuntu"
	DistroDebian Distro = "debian"
	DistroFedora Distro = "fedora"
)

// EnvironmentSelector is the user's choice of operating environment. It is the
// unit avar maps onto machines: distinct selectors never share a machine.
type EnvironmentSelector struct {
	Distro  Distro `json:"distro"`
	Version string `json:"version"`
	Arch    Arch   `json:"arch"`

	// Isolated selects a per-project machine rather than the shared machine
	// for (Distro, Version, Arch).
	Isolated bool `json:"isolated"`
}

// Label renders the selector for human-facing output, e.g. "Ubuntu 24.04 · arm64".
func (s EnvironmentSelector) Label() string {
	name := string(s.Distro)
	if name != "" {
		name = strings.ToUpper(name[:1]) + name[1:]
	}
	return fmt.Sprintf("%s %s · %s", name, s.Version, s.Arch)
}

// Emulated reports whether running this selector requires CPU emulation, which
// carries a substantial performance cost worth warning the user about.
func (s EnvironmentSelector) Emulated() bool { return !s.Arch.Native() }
