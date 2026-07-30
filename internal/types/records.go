package types

import (
	"fmt"
	"regexp"
	"time"
)

// MachineNamePattern constrains machine names. The "avr-" prefix is also
// avar's ownership marker: avar refuses to operate on anything without it.
var MachineNamePattern = regexp.MustCompile(`^avr-[a-z0-9.-]+$`)

// MachineNamePrefix marks a machine as created and owned by avar.
const MachineNamePrefix = "avr-"

// ValidateMachineName rejects names avar must not act on.
func ValidateMachineName(name string) error {
	if !MachineNamePattern.MatchString(name) {
		return fmt.Errorf("%q is not an avar-managed machine name", name)
	}
	return nil
}

// MachineKind distinguishes the roles a machine plays.
type MachineKind string

const (
	// KindShared is the long-lived machine every project uses by default.
	KindShared MachineKind = "shared"
	// KindIsolated is a per-project machine created by --isolate.
	KindIsolated MachineKind = "isolated"
	// KindBase is a pristine, never-entered machine cloned to create
	// isolated machines quickly.
	KindBase MachineKind = "base"
)

// ProjectRecord is what avar remembers about a host directory it has been run
// from. Records are created on first use and are never removed automatically.
type ProjectRecord struct {
	// ID is the SHA-256 of the resolved absolute path (see state.ProjectID).
	ID string `json:"id"`
	// Path is the resolved absolute path: both the display name and the
	// mount source.
	Path string `json:"path"`
	// Isolated records that this project defaults to its own machine.
	Isolated bool `json:"isolated"`
	// Selector, when set, overrides global distro/arch defaults for this
	// project.
	Selector   *EnvironmentSelector `json:"selector,omitempty"`
	CreatedAt  time.Time            `json:"created_at"`
	LastUsedAt time.Time            `json:"last_used_at"`
}

// MachineRecord is avar's record of a machine it created. It is written only
// after the machine is confirmed viable and deleted only after the machine is
// gone, so the record never describes a machine that never worked.
type MachineRecord struct {
	Name      string              `json:"name"`
	Selector  EnvironmentSelector `json:"selector"`
	Kind      MachineKind         `json:"kind"`
	ProjectID string              `json:"project_id,omitempty"`
	// Mounts are the host project roots registered to this machine.
	Mounts    []string  `json:"mounts"`
	CreatedAt time.Time `json:"created_at"`
	// VMType records the backend's virtualization mode for status output.
	VMType string `json:"vm_type,omitempty"`
}

// SessionRecord tracks a live attachment so that idle shutdown never stops a
// machine somebody is using.
type SessionRecord struct {
	Machine   string    `json:"machine"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
}

// MachineState is the lifecycle state of a machine as the backend reports it.
type MachineState string

const (
	StateRunning MachineState = "running"
	StateStopped MachineState = "stopped"
	StateBroken  MachineState = "broken"
	StateUnknown MachineState = "unknown"
)

// MachineStatus is a point-in-time view of a machine, combining backend truth
// with avar's own record.
type MachineStatus struct {
	Name     string
	Selector EnvironmentSelector
	Kind     MachineKind
	State    MachineState
	CPUs     int
	MemoryGB float64
	DiskGB   float64
	DiskUsed float64
	Mounts   []string
	VMType   string
	// Sessions is the number of live avar sessions attached.
	Sessions int
}
