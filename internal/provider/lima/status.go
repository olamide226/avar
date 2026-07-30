package lima

import (
	"context"
	"fmt"
	"strings"

	"github.com/olamide226/avar/internal/types"
)

// Machine-name prefixes that carry meaning. The resolver derives every machine
// name deterministically, which is what lets a listing say something about a
// machine avar has no record of.
const (
	// isolatedNamePrefix marks a per-project machine: avr-prj-<hash>.
	isolatedNamePrefix = types.MachineNamePrefix + "prj-"
	// baseNamePrefix marks a pristine machine cloned to create isolated ones:
	// avr-base-<distro>-<version>-<arch>.
	baseNamePrefix = types.MachineNamePrefix + "base-"
)

// Status reports one entry per machine avar owns, ordered by name.
//
// It filters rather than annotates. A Lima instance whose name is not avar's
// does not appear in the result at all: `avr status` and `avr stop --all` are
// built directly on this, and neither may ever reach a machine the user created
// themselves (REQ-5.1, REQ-5.4, PROP-6).
//
// The filter is the name prefix alone, deliberately, even though the mutating
// operations additionally require a record. A machine avar created but had not
// finished recording is exactly the damage a crash mid-create leaves behind, and
// reconciliation exists to adopt it — which it can only do if a listing shows it.
// Listing is how avar discovers damage; mutating is how it acts on it, and only
// the latter needs the stricter guard.
//
// Backend truth and avar's own records answer different halves of each entry.
// Lima knows the state, the size and the applied mounts; only avar knows which
// environment the machine was created to provide, because a Lima instance
// carries no selector. Where there is no record, as much as the deterministic
// machine name allows is derived and the rest is left zero — a caller can act on
// "I do not know" but not on a guess.
//
// Sessions is left at zero: live avar sessions are avar's own bookkeeping, not
// something the backend can see.
func (p *Provider) Status(ctx context.Context) ([]types.MachineStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("listing the machines avar manages: %w", err)
	}

	records, err := p.ownedRecords()
	if err != nil {
		return nil, err
	}

	instances, err := p.newView().all(ctx)
	if err != nil {
		return nil, err
	}

	// An empty slice rather than nil: "avar owns nothing yet" is a real answer
	// the command layer turns into onboarding text (REQ-5.3).
	out := make([]types.MachineStatus, 0, len(instances))
	for _, inst := range instances {
		if types.ValidateMachineName(inst.Name) != nil {
			continue
		}
		status := types.MachineStatus{
			Name:     inst.Name,
			State:    inst.state(),
			CPUs:     inst.CPUs,
			MemoryGB: gibibytes(inst.Memory),
			DiskGB:   gibibytes(inst.Disk),
			DiskUsed: diskUsedGB(inst.Dir),
			Mounts:   inst.mounts(),
			VMType:   inst.VMType,
		}
		if record, known := records[inst.Name]; known {
			status.Selector = record.Selector
			status.Kind = record.Kind
		} else {
			status.Selector, status.Kind = describeUnrecorded(inst)
		}
		out = append(out, status)
	}
	// view.all is already ordered by name, so the result is too.
	return out, nil
}

// describeUnrecorded says what can be known about a machine avar has no record
// of, from its deterministic name and from what Lima reports.
//
// Nothing is guessed. A per-project machine's name is a hash, so its
// environment is genuinely unknowable from a listing and the selector stays
// zero; an unrecognised distro or version stays zero for the same reason.
func describeUnrecorded(inst instance) (types.EnvironmentSelector, types.MachineKind) {
	name := inst.Name
	switch {
	case strings.HasPrefix(name, isolatedNamePrefix):
		// avr-prj-<hash>: the project it belongs to is not recoverable from
		// the name, and neither is the environment.
		return types.EnvironmentSelector{Isolated: true, Arch: archFromLima(inst.Arch)}, types.KindIsolated
	case strings.HasPrefix(name, baseNamePrefix):
		return selectorFromName(strings.TrimPrefix(name, baseNamePrefix), inst.Arch), types.KindBase
	default:
		return selectorFromName(strings.TrimPrefix(name, types.MachineNamePrefix), inst.Arch), types.KindShared
	}
}

// selectorFromName reads a shared machine's name, which the resolver builds as
// <distro>-<version>-<arch>.
//
// The architecture Lima reports is the fallback, since it is backend truth
// rather than an inference from a string.
func selectorFromName(name, limaArch string) types.EnvironmentSelector {
	selector := types.EnvironmentSelector{Arch: archFromLima(limaArch)}

	archAt := strings.LastIndex(name, "-")
	if archAt < 0 {
		return selector
	}
	if arch, err := types.ParseArch(name[archAt+1:]); err == nil {
		selector.Arch = arch
	}

	versionAt := strings.Index(name[:archAt], "-")
	if versionAt < 0 {
		return selector
	}
	distro := name[:versionAt]
	switch types.Distro(distro) {
	case types.DistroUbuntu, types.DistroDebian, types.DistroFedora:
		selector.Distro = types.Distro(distro)
		selector.Version = name[versionAt+1 : archAt]
	}
	return selector
}

// archFromLima translates Lima's architecture vocabulary back into avar's,
// reporting an empty value for an architecture avar does not support rather
// than a wrong one.
func archFromLima(arch string) types.Arch {
	switch arch {
	case limaArchAARCH64:
		return types.ArchARM64
	case limaArchX8664:
		return types.ArchAMD64
	default:
		return ""
	}
}

// ownedRecords indexes avar's machine records by name.
//
// A nil Records means the caller has not given the provider access to avar's
// registry — the reconciler's situation, where the records are what is being
// decided.
func (p *Provider) ownedRecords() (map[string]types.MachineRecord, error) {
	if p.records == nil {
		return nil, nil
	}
	records, err := p.records.Machines()
	if err != nil {
		return nil, fmt.Errorf("reading avar's machine records: %w", err)
	}
	byName := make(map[string]types.MachineRecord, len(records))
	for _, record := range records {
		byName[record.Name] = record
	}
	return byName, nil
}
