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
	Name string `json:"name"`
	// Provider is the backend that created the machine. A record without it
	// cannot be acted on safely, because "start this machine" means a different
	// operation to each backend; schema v2 stamps pre-existing records with
	// ProviderLima, which is the only backend that could have written them.
	Provider  ProviderID          `json:"provider"`
	Selector  EnvironmentSelector `json:"selector"`
	Kind      MachineKind         `json:"kind"`
	ProjectID string              `json:"project_id,omitempty"`
	// Mounts are the host project roots registered to this machine, each with
	// the guest path the provider planned for it.
	Mounts    []MountSpec `json:"mounts"`
	CreatedAt time.Time   `json:"created_at"`
	// Runtime records how the backend actually runs this machine — Lima's
	// virtualization mode, or the WSL version — for status output. It is a
	// backend-opaque string: avar displays it and never branches on it.
	Runtime string `json:"runtime,omitempty"`
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
//
// It deliberately carries no ProjectID. A machine's project is avar's own
// bookkeeping: nothing a backend can see, and nothing it can be asked. An
// isolated machine's name embeds a truncated hash of the project identity, but
// matching on a truncation would be a guess, and a machine filed under the
// wrong project would have the wrong directory mounted into it. Reconciliation
// therefore leaves an isolated orphan alone rather than adopting it on that
// evidence (state.recordFor, design §3.5). Provider is the opposite case and is
// present for exactly that reason: a backend always knows which backend it is.
type MachineStatus struct {
	Name     string
	Provider ProviderID
	Selector EnvironmentSelector
	Kind     MachineKind
	State    MachineState
	CPUs     int
	MemoryGB float64
	DiskGB   float64
	DiskUsed float64
	// Mounts are the file shares the backend has actually applied, host path
	// and guest path both, since on some backends they differ.
	Mounts []MountSpec
	// Runtime is the backend's own word for how it runs this machine, shown in
	// status output and never interpreted.
	Runtime string
	// Sessions is the number of live avar sessions attached.
	Sessions int
}
