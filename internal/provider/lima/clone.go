package lima

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// createIsolated brings a per-project environment into existence by cloning a
// pristine base machine, falling back to full provisioning when the clone path
// cannot execute (REQ-11.1).
//
// It is the entry point called by ensureMachine when spec.Kind is KindIsolated
// and the machine does not exist yet. Mount validation happens first so that a
// missing project directory fails fast before any work begins.
func (p *Provider) createIsolated(ctx context.Context, spec provider.MachineSpec, progress types.ProgressSink) error {
	mounts, err := normalizeMounts(spec.Mounts)
	if err != nil {
		return fmt.Errorf("creating your %s environment: %w", spec.Selector.Label(), err)
	}
	if err := checkHostDirs(mounts); err != nil {
		return fmt.Errorf("creating your %s environment: %w", spec.Selector.Label(), err)
	}

	// Open the provisioning log early so that both clone and fallback paths
	// write to the same file.
	logPath, logFile, err := p.createLog(spec.Name)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()

	// Attempt clone-based creation. On failure, fall back to full
	// provisioning so the user still gets a working machine (REQ-11.1).
	if err := p.createFromBase(ctx, spec, mounts, logPath, logFile, progress); err == nil {
		return nil
	}

	// Clone path failed — provision from scratch.
	return p.create(ctx, spec, progress)
}

// baseNameToken is the segment that distinguishes a base machine from a shared
// or isolated one. Base machines are never entered: they exist only to be
// cloned.
const baseNameToken = "base-"

// baseMachineName derives the name of the pristine, stopped base machine for a
// given environment. The name carries avar's ownership prefix, the base token,
// and the environment triple, e.g. "avr-base-ubuntu-24.04-arm64".
func baseMachineName(env types.EnvironmentSelector) string {
	return types.MachineNamePrefix + baseNameToken + string(env.Distro) + "-" + env.Version + "-" + string(env.Arch)
}

// createFromBase provisions a new machine by cloning a pristine base, with a
// fallback to full provisioning when the clone path fails.
//
// The base machine itself is created on demand: the first isolate for a
// (distro, arch) pair provisions a fresh shared-configuration machine as the
// base, stops it and records it. Subsequent isolates clone that stopped base
// in seconds rather than paying a full cold-start each time (design §3.5).
//
// On a nil return the machine is running and every mount is present at its
// guest path. On failure the half-created instance is removed before returning,
// so the next invocation finds either a working machine or nothing at all
// (REQ-1.6, PROP-7).
func (p *Provider) createFromBase(ctx context.Context, spec provider.MachineSpec, mounts []types.MountSpec, logPath string, logFile *os.File, progress types.ProgressSink) error {
	base := baseMachineName(spec.Selector)

	if err := p.ensureBase(ctx, spec, base, progress); err != nil {
		return fmt.Errorf("creating your %s environment: preparing the base machine for cloning: %w", spec.Selector.Label(), err)
	}

	progress.Progress(types.ProgressEvent{
		Kind:    types.ProgressCreating,
		Machine: spec.Name,
		Message: fmt.Sprintf("Creating your Linux development environment · %s · from clone",
			spec.Selector.Label()),
	})
	if spec.Selector.Emulated() {
		progress.Progress(types.ProgressEvent{
			Kind:    types.ProgressWarning,
			Machine: spec.Name,
			Message: fmt.Sprintf("%s runs under CPU emulation because the host is %s. "+
				"Expect it to be markedly slower than a native environment.",
				spec.Selector.Label(), types.HostArch()),
		})
	}

	// Step 1: clone the base into the target.
	// "limactl clone <source> --name <target>" copies the disk image, which is
	// fast for a stopped machine (coordination doc §Environment facts).
	if _, err := p.run(ctx, "clone", base, spec.Name); err != nil {
		return p.abandonCreate(ctx, spec, logPath,
			fmt.Errorf("cloning the %s environment from the base machine %s: %w",
				spec.Selector.Label(), base, err))
	}

	// Step 2: add the project mounts to the clone's configuration. The base
	// machine was provisioned without any project shares, so the clone
	// inherits an empty mount list.  The edit happens while the clone is
	// still stopped — no restart is needed.
	if len(mounts) > 0 {
		if _, err := p.run(ctx, "edit", spec.Name, "--set", mountsExpression(mounts), "--tty=false"); err != nil {
			return p.abandonCreate(ctx, spec, logPath,
				fmt.Errorf("configuring shared directories for the cloned %s environment: %w",
					spec.Selector.Label(), err))
		}
	}

	// Step 3: start the clone.
	startLog := io.MultiWriter(logFile, &progressLines{machine: spec.Name, sink: progress})
	if err := p.runner.Stream(ctx, startLog, p.limactl, "start", "--tty=false", spec.Name); err != nil {
		return p.abandonCreate(ctx, spec, logPath,
			fmt.Errorf("starting the cloned %s environment (machine %s): %w",
				spec.Selector.Label(), spec.Name, err))
	}

	// Step 4: prove the shares landed (REQ-6.5).
	if err := p.verifyMounts(ctx, spec.Name, mounts); err != nil {
		return p.abandonCreate(ctx, spec, logPath, err)
	}

	return nil
}

// ensureBase makes certain a pristine base machine exists for the given
// environment, creating and stopping one if it does not.
//
// The base machine carries KindBase: it is provisioned identically to a shared
// machine but is given no mounts, is never entered, and exists exclusively as a
// clone source. It is stopped immediately after provisioning so that its disk
// image is closed and cloneable.
func (p *Provider) ensureBase(ctx context.Context, spec provider.MachineSpec, base string, progress types.ProgressSink) error {
	v := p.newView()
	inst, exists, err := v.lookup(ctx, base)
	if err != nil {
		return err
	}
	if exists {
		// The base already exists. If it is running, stop it — the base
		// must be stopped so that its disk image is closed and cloneable
		// (Lima clones from a stopped instance's disk).
		if inst.running() {
			if err := p.stopMachine(ctx, base, progress); err != nil {
				return fmt.Errorf("stopping the base machine %s before cloning: %w", base, err)
			}
		}
		return nil
	}

	// Provision the base as a bare machine: same (distro, arch) image, no
	// mounts, no project. KindBase tells the backend this machine exists only
	// to be cloned.
	//
	// Nothing records it here, and that is deliberate rather than an omission:
	// the provider holds read-only access to avar's records precisely so that
	// a backend cannot invent ownership for itself (design §4). The base is
	// adopted by reconciliation on the next invocation, which is what gives it
	// the record PROP-6 wants — see TestReconcile_AdoptsAnUnrecordedBaseMachine.
	// Until then it is held by its name prefix, which only avar creates.
	baseSpec := spec
	baseSpec.Name = base
	baseSpec.Kind = types.KindBase
	baseSpec.Mounts = nil
	baseSpec.Provider = "" // Accept whichever backend receives this.

	progress.Progress(types.ProgressEvent{
		Kind:    types.ProgressCreating,
		Machine: base,
		Message: fmt.Sprintf("Preparing a pristine %s environment for isolated projects",
			spec.Selector.Label()),
	})

	// create provisions from the generated config. With no mounts the machine
	// is a bare Linux image — nothing project-specific lives in it.
	if err := p.create(ctx, baseSpec, progress); err != nil {
		return fmt.Errorf("provisioning the base machine %s: %w", base, err)
	}

	// Stop the base so its disk image is closed and cloneable.
	if err := p.stopMachine(ctx, base, progress); err != nil {
		return fmt.Errorf("stopping the base machine %s after provisioning: %w", base, err)
	}

	return nil
}
