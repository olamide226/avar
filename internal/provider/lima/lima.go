// Package lima implements avar's Provider by driving Lima's command-line tool.
//
// It is the only place in avar that knows Lima exists. Everything above the
// Provider interface speaks of machines, selectors and mounts; this package
// translates those into `limactl` invocations and translates Lima's output back
// (REQ-17.3, design §3.5).
//
// Three properties of that translation are deliberate:
//
// Lima is driven as a subprocess rather than imported as a library, which
// decouples avar from Lima's internal API and matches how other Lima frontends
// work (design §1). The cost is that avar parses `limactl` output, mitigated by
// the minimum-version gate in internal/deps and by fixture-driven tests.
//
// No shell is ever involved. Every invocation is an argv passed to
// deps.Runner, so a directory name containing a space, a quote or a semicolon is
// an argument and can never become syntax. The generated instance
// configuration quotes every interpolated value for the same reason.
//
// avar only ever touches machines it owns. Every machine-named operation
// validates the avr- prefix and, where avar's own records are available,
// membership in them, before doing anything at all; Status filters rather than
// annotates, so a Lima instance the user created themselves is invisible to avar
// (REQ-5.4, PROP-6).
package lima

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/olamide226/avar/internal/deps"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// Provider implements provider.Provider against a local Lima installation.
var _ provider.Provider = (*Provider)(nil)

// cleanupTimeout bounds the cleanup of a partially created machine. It runs on a
// context detached from the caller's, because the usual reason cleanup is needed
// is that the caller's context was cancelled — and a Ctrl-C that leaves a wedged
// half-created machine behind is exactly what PROP-7 forbids.
const cleanupTimeout = 2 * time.Minute

// createLogPerm and logsDirPerm keep provisioning logs private to the user. They
// carry image URLs and guest hostnames rather than secrets, but avar's state
// directory is the user's alone.
const (
	createLogPerm = 0o600
	logsDirPerm   = 0o700
	configPerm    = 0o600
)

// Records is avar's own registry of the machines it created.
//
// Ownership is two conditions, not one: the avr- prefix says a name is shaped
// like avar's, and the record says avar actually made it (REQ-5.4, PROP-6). This
// is an interface rather than a *state.Store so that the provider depends on the
// two questions it needs answered instead of on the state package — which also
// keeps the ownership rule testable without a state directory on disk.
//
// *state.Store satisfies it as written.
type Records interface {
	// Machine reports avar's record of one machine, and whether there is one.
	Machine(name string) (types.MachineRecord, bool, error)
	// Machines reports every machine avar has a record of.
	Machines() ([]types.MachineRecord, error)
}

// Options are the collaborators a Provider needs.
type Options struct {
	// Lima is the verified Lima installation from internal/deps. Its Path is
	// the executable avar runs, so the binary that passed the version gate is
	// the binary that gets driven.
	Lima deps.Lima

	// Runner executes limactl. It is required: avar has exactly one subprocess
	// abstraction (deps.Runner) and this package uses it rather than reaching
	// for os/exec itself.
	Runner deps.Runner

	// Records is avar's machine registry, used for the second half of the
	// ownership check. A nil Records reduces ownership to the name prefix
	// alone, which is what the reconciler needs while it is still deciding
	// which instances to adopt; production wiring passes the state store.
	Records Records

	// LogsDir is where provisioning logs are written, normally
	// ~/.avr/logs. Provisioning failures name the log file, so this is
	// required.
	LogsDir string

	// Host pins the host's resources for machine sizing. A zero field is
	// probed on the create path; tests pin it to keep generated
	// configuration deterministic.
	Host HostResources
}

// Provider drives Lima through limactl.
//
// It holds no mutable state: everything Lima knows is read fresh per call
// (see view), so two concurrent avr invocations cannot disagree because one of
// them cached.
type Provider struct {
	limactl        string
	runner         deps.Runner
	records        Records
	logsDir        string
	host           HostResources
	reapHostAgents func(context.Context, string, string) error
}

// New returns a Provider driving the given Lima installation.
func New(opts Options) (*Provider, error) {
	switch {
	case opts.Lima.Path == "":
		return nil, errors.New("creating the Lima provider: no limactl path was given; obtain one from deps.EnsureLima")
	case opts.Runner == nil:
		return nil, errors.New("creating the Lima provider: no command runner was given")
	case opts.LogsDir == "":
		return nil, errors.New("creating the Lima provider: no log directory was given; provisioning failures must be able to name a log file")
	}
	return &Provider{
		limactl:        opts.Lima.Path,
		runner:         opts.Runner,
		records:        opts.Records,
		logsDir:        opts.LogsDir,
		host:           opts.Host,
		reapHostAgents: reapHostAgents,
	}, nil
}

// EnsureMachine brings the machine spec describes into a running state,
// provisioning it first if Lima does not have it (REQ-1.2, REQ-1.3).
func (p *Provider) EnsureMachine(ctx context.Context, spec provider.MachineSpec, progress types.ProgressSink) error {
	// The name is avar's own, derived by the resolver, so a name that fails
	// here means the caller is confused about which machine it is asking for —
	// and acting on it could start a machine belonging to the user.
	if err := p.gate(ctx, spec.Name, ownershipPrefix); err != nil {
		return err
	}
	if err := p.checkAddressedToMe(spec); err != nil {
		return err
	}

	v := p.newView()
	inst, exists, err := v.lookup(ctx, spec.Name)
	if err != nil {
		return err
	}

	switch {
	case !exists:
		if spec.Kind == types.KindIsolated {
			return p.createIsolated(ctx, spec, progress)
		}
		return p.create(ctx, spec, progress)

	case inst.running():
		// The warm path. Nothing is verified, reconfigured or restarted, and
		// nothing is printed: this runs on every single avr invocation and the
		// whole latency budget is ~500 ms (REQ-1.2, REQ-1.3, REQ-17.1).
		return nil

	case inst.state() == types.StateBroken:
		// Lima cannot start it and avar must not destroy it: the disk may hold
		// work the user has not pushed anywhere. Reconciliation and `avr reset`
		// are the paths that delete, and both are the user's decision.
		return fmt.Errorf("starting your %s environment: Lima reports machine %s as broken; "+
			"run `avr status` to see what Lima says about it, and `limactl start %s` to see the underlying error",
			spec.Selector.Label(), spec.Name, spec.Name)

	default:
		return p.start(ctx, spec, progress)
	}
}

// start starts an existing machine (REQ-1.3).
func (p *Provider) start(ctx context.Context, spec provider.MachineSpec, progress types.ProgressSink) error {
	progress.Progress(types.ProgressEvent{
		Kind:    types.ProgressStarting,
		Machine: spec.Name,
		Message: fmt.Sprintf("Starting %s", spec.Selector.Label()),
	})
	if _, err := p.run(ctx, "start", "--tty=false", spec.Name); err != nil {
		// A machine that exists but will not start keeps whatever is installed
		// in it. avar reports and suggests; it does not delete (design §6).
		return fmt.Errorf("starting your %s environment (machine %s): %w; "+
			"run `avr status` to see its state", spec.Selector.Label(), spec.Name, err)
	}
	return nil
}

// checkAddressedToMe refuses a spec the resolver addressed to another backend.
//
// It cannot happen while avar has one backend, and it is cheap insurance for
// when it has two: a spec carries the provider the resolver chose from the host
// platform, and building a Lima machine from a spec that asked for a WSL
// distribution would produce something the caller never requested. An empty
// Provider means "whichever backend receives this", which is what a
// single-backend caller passes.
func (p *Provider) checkAddressedToMe(spec provider.MachineSpec) error {
	if spec.Provider == "" || spec.Provider == p.ID() {
		return nil
	}
	return fmt.Errorf("creating your %s environment: this machine is recorded against the %q backend and avar is running the %q backend on this host",
		spec.Selector.Label(), spec.Provider, p.ID())
}

// create provisions a machine from a generated configuration and starts it.
//
// Nothing half-created survives a failure: a create that fails, or that is
// interrupted, deletes the partial instance before returning, so the next
// invocation finds either a working machine or nothing at all (REQ-1.6, PROP-7).
// No record of the machine is written here — that is the caller's to write once
// this returns nil (design §4).
func (p *Provider) create(ctx context.Context, spec provider.MachineSpec, progress types.ProgressSink) error {
	// Pre-flight the mounts before provisioning: a project directory that is
	// not there is worth a clear error now rather than a machine that boots
	// with a missing share (REQ-6.5).
	mounts, err := normalizeMounts(spec.Mounts)
	if err != nil {
		return fmt.Errorf("creating your %s environment: %w", spec.Selector.Label(), err)
	}
	if err := checkHostDirs(mounts); err != nil {
		return fmt.Errorf("creating your %s environment: %w", spec.Selector.Label(), err)
	}

	host := p.hostResources(ctx)
	config, err := renderInstanceConfig(spec, host)
	if err != nil {
		return err
	}
	cpus, memoryGB, _ := resources(spec, host)

	progress.Progress(types.ProgressEvent{
		Kind:    types.ProgressCreating,
		Machine: spec.Name,
		Message: fmt.Sprintf("Creating your Linux development environment · %s · %s",
			spec.Selector.Label(), describeResources(cpus, memoryGB)),
	})
	if spec.Selector.Emulated() {
		// Said once, at provision time, before the slow part starts (REQ-4.6).
		progress.Progress(types.ProgressEvent{
			Kind:    types.ProgressWarning,
			Machine: spec.Name,
			Message: fmt.Sprintf("%s runs under CPU emulation because the host is %s. "+
				"Expect it to be markedly slower than a native environment; a native environment can run individual x86_64 binaries through Rosetta instead.",
				spec.Selector.Label(), types.HostArch()),
		})
	}

	configPath, cleanupConfig, err := writeTempConfig(spec.Name, config)
	if err != nil {
		return err
	}
	defer cleanupConfig()

	logPath, logFile, err := p.createLog(spec.Name)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()

	// Provisioning output goes to the log and, line by line, to the progress
	// sink so that a verbose run can show it. os/exec guarantees that a single
	// writer used for both stdout and stderr is written to by one goroutine at
	// a time, so the tee needs no lock of its own.
	lines := &progressLines{machine: spec.Name, sink: progress}
	defer lines.flush()

	runErr := p.runner.Stream(ctx, io.MultiWriter(logFile, lines), p.limactl,
		"start", "--name", spec.Name, "--tty=false", configPath)
	if runErr == nil {
		// The machine is up. Prove the shares actually landed rather than
		// trusting that Lima's exit code covered them (REQ-6.5).
		if err := p.verifyMounts(ctx, spec.Name, mounts); err != nil {
			return p.abandonCreate(ctx, spec, logPath, err)
		}
		return nil
	}
	return p.abandonCreate(ctx, spec, logPath, runErr)
}

// abandonCreate removes the partial machine and reports why the create failed
// (REQ-1.6, PROP-7).
func (p *Provider) abandonCreate(ctx context.Context, spec provider.MachineSpec, logPath string, cause error) error {
	cleanupCtx, cancel := detach(ctx)
	defer cancel()
	cleanupErr := p.deleteInstance(cleanupCtx, spec.Name)

	remainder := "The partially created machine has been removed, so running avr again starts from a clean slate."
	if cleanupErr != nil {
		remainder = fmt.Sprintf("avar could not remove the partially created machine (%v): run `limactl delete --force %s` before trying again.",
			cleanupErr, spec.Name)
	}

	// A cancelled context is the user pressing Ctrl-C, not a failure of Lima's.
	// It is reported as cancellation so the command layer can exit 130.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("creating your %s environment was interrupted. %s The provisioning log is at %s: %w",
			spec.Selector.Label(), remainder, logPath, ctxErr)
	}
	return fmt.Errorf("creating your %s environment (machine %s): %w. %s The full provisioning log is at %s — its tail usually names the cause, most often a failed image download or too little disk space",
		spec.Selector.Label(), spec.Name, cause, remainder, logPath)
}

// Stop shuts the machine down cleanly, releasing its memory (REQ-5.2).
func (p *Provider) Stop(ctx context.Context, machine string, progress types.ProgressSink) error {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return err
	}
	inst, err := p.newView().require(ctx, machine)
	if err != nil {
		return err
	}
	if !inst.running() {
		// Lima can report Stopped even when a host agent escaped its parent.
		// Reaping that exact agent is still part of converging on stopped.
		return p.reapHostAgents(ctx, p.limactl, machine)
	}
	progress.Progress(types.ProgressEvent{
		Kind:    types.ProgressStopping,
		Machine: machine,
		Message: fmt.Sprintf("Stopping %s", machine),
	})
	return p.stopMachine(ctx, machine, progress)
}

// stopMachine stops a machine, ending it outright if it will not shut down
// cleanly.
//
// Every path that stops a machine goes through here — the `avr stop` command,
// the restart a mount change needs, the pause around a snapshot, and preparing
// a base for cloning. They each used to call `limactl stop` directly, so a
// guest that hung on shutdown failed whichever operation happened to be
// running, which is how a wedged hostagent turned into a failing mount test
// with nothing in the message about shutdown at all.
//
// Escalating is safe here in a way it would not be for a general-purpose VM
// manager: the only state that matters is the project directory, which is a
// share on the host and never inside the guest's disk (PROP-10). It is
// announced rather than silent, because work in progress *inside* the guest
// can still be lost and the user should know that is what happened.
func (p *Provider) stopMachine(ctx context.Context, machine string, progress types.ProgressSink) error {
	if _, err := p.run(ctx, "stop", machine); err != nil {
		progress.Progress(types.ProgressEvent{
			Kind:    types.ProgressWarning,
			Machine: machine,
			Message: fmt.Sprintf("%s did not shut down cleanly; ending it. Unsaved work inside it may be lost — your project files on this Mac are not affected.", machine),
		})
		if _, forceErr := p.run(ctx, "stop", "--force", machine); forceErr != nil {
			return fmt.Errorf("stopping machine %s: %w (it did not stop cleanly, and ending it outright also failed: %v)", machine, err, forceErr)
		}
	}
	if err := p.reapHostAgents(ctx, p.limactl, machine); err != nil {
		return fmt.Errorf("removing orphaned Lima host agents for machine %s: %w", machine, err)
	}
	return nil
}

// Delete destroys the machine and everything inside it. Host project files are
// untouched, because they were shared and never copied (REQ-10.3, PROP-10).
//
// The ownership check here is the name prefix alone. Delete is also how avar
// cleans up after a failed create and how the reconciler removes a broken
// orphan, and in both cases there is deliberately no record to check: the record
// is written only after a machine is confirmed viable (design §4). Requiring one
// would make exactly the wedged state PROP-7 exists to prevent unrecoverable.
func (p *Provider) Delete(ctx context.Context, machine string) error {
	if err := p.gate(ctx, machine, ownershipPrefix); err != nil {
		return err
	}
	_, exists, err := p.newView().lookup(ctx, machine)
	if err != nil {
		return err
	}
	if !exists {
		// Fully idempotent: the caller of a cleanup path cannot know what
		// survived (REQ-1.6, REQ-17.5).
		return nil
	}
	return p.deleteInstance(ctx, machine)
}

// deleteInstance removes a Lima instance.
//
// --force is deliberate. Delete's callers are "destroy this" paths — cleanup
// after a failed create, reconciliation of a broken instance, `avr reset` — and
// a graceful stop of a broken instance can hang indefinitely. Lima force-stops
// before deleting in any case; without the flag it refuses an instance that is
// not already stopped.
//
// Lima is itself idempotent here — `limactl delete --force` on an instance that
// does not exist warns and exits zero — but avar checks the listing first
// anyway, so that "there was nothing to delete" is a decision avar made rather
// than a behaviour it inherited.
func (p *Provider) deleteInstance(ctx context.Context, machine string) error {
	if _, err := p.run(ctx, "delete", "--force", machine); err != nil {
		return fmt.Errorf("deleting machine %s: %w; "+
			"`limactl delete --force %s` will remove it by hand", machine, err, machine)
	}
	return nil
}

// ownership says how much of PROP-6 an operation is able to enforce.
type ownership int

const (
	// ownershipPrefix checks the name only. It is what the operations that
	// legitimately run before or after a machine has a record use:
	// EnsureMachine may be creating the machine the record will describe, and
	// Delete may be removing one that never got a record at all.
	ownershipPrefix ownership = iota
	// ownershipRecord additionally requires the machine to be in avar's
	// records: the prefix and the record together are what "avar owns this"
	// means (REQ-5.4, PROP-6).
	ownershipRecord
)

// gate is what every machine-named operation owes its caller before it does
// anything: honour cancellation, then refuse a machine avar does not own, with
// no side effects whatsoever.
func (p *Provider) gate(ctx context.Context, machine string, mode ownership) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("operating on machine %s: %w", machine, err)
	}
	if err := types.ValidateMachineName(machine); err != nil {
		return fmt.Errorf("%w: %s; avar only manages machines it created", provider.ErrNotOwned, machine)
	}
	if mode == ownershipRecord && p.records != nil {
		_, ok, err := p.records.Machine(machine)
		if err != nil {
			return fmt.Errorf("checking whether avar owns machine %s: %w", machine, err)
		}
		if !ok {
			return fmt.Errorf("%w: %s is not in avar's records; avar will not touch a Lima machine it did not create", provider.ErrNotOwned, machine)
		}
	}
	return nil
}

// run executes limactl with an argv and returns its standard output.
func (p *Provider) run(ctx context.Context, args ...string) ([]byte, error) {
	return p.runner.Output(ctx, p.limactl, args...)
}

// createLog opens the provisioning log for a machine. Its path is named in the
// error a failed create returns, so the user has somewhere to look (design §6).
func (p *Provider) createLog(machine string) (string, *os.File, error) {
	if err := os.MkdirAll(p.logsDir, logsDirPerm); err != nil {
		return "", nil, fmt.Errorf("creating avar's log directory %s: %w", p.logsDir, err)
	}
	path := filepath.Join(p.logsDir, machine+"-create.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, createLogPerm)
	if err != nil {
		return "", nil, fmt.Errorf("opening the provisioning log %s: %w", path, err)
	}
	return path, file, nil
}

// writeTempConfig writes a generated instance configuration where limactl can
// read it, and returns a function that removes it.
//
// The file name matters: limactl treats an argument ending in .yaml as a
// configuration to create from rather than the name of an existing instance.
func writeTempConfig(machine string, config []byte) (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "avr-lima-")
	if err != nil {
		return "", nil, fmt.Errorf("creating a temporary directory for machine %s's configuration: %w", machine, err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	path = filepath.Join(dir, machine+".yaml")
	if err := os.WriteFile(path, config, configPerm); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("writing machine %s's configuration to %s: %w", machine, path, err)
	}
	return path, cleanup, nil
}

// detach returns a bounded context that outlives the cancellation of ctx, for
// cleanup work that has to happen precisely because ctx was cancelled.
func detach(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}
