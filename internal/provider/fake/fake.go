// Package fake provides an in-process implementation of the provider
// interfaces for tests.
//
// It exists so that command flows can be proven without a virtual machine
// (design §7): a test drives the real command code against a Fake, then asserts
// on the ordered sequence of operations the flow performed and the arguments it
// passed. Programming an outcome — an error from one operation, a guest exit
// code, a machine that is already running — is how a flow's error paths and
// warm paths become reachable in a unit test.
//
// A Fake keeps coherent state rather than merely recording calls: after
// EnsureMachine the machine is running, after Stop it is stopped, SetMounts is
// reflected by AppliedMounts, and Delete removes it. Sequences a real backend
// would refuse — attaching a shell to a machine that was never created,
// operating on a machine avar does not own — fail here too, because a fake that
// waves those through lets a test prove behaviour that cannot happen in
// production.
//
// A Fake is safe for concurrent use. Progress events are delivered to the sink
// while the Fake's lock is held, so a sink must not call back into the same
// Fake.
package fake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// The Fake implements every provider interface, so a flow that type-asserts for
// a capability finds it. A backend is free to implement only some of them; the
// test double implements all of them on purpose.
var (
	_ provider.Provider             = (*Fake)(nil)
	_ provider.Snapshotter          = (*Fake)(nil)
	_ provider.EditorTargetProvider = (*Fake)(nil)
	_ provider.NativeWorkspacer     = (*Fake)(nil)
	_ provider.PortDiagnoser        = (*Fake)(nil)
)

// ProviderID is the backend id the Fake reports. It is its own id rather than a
// real backend's: a flow test that records a machine records it against the
// provider that actually served it, and a test double claiming to be Lima would
// make an assertion about provider identity prove nothing.
const ProviderID = types.ProviderID("fake")

// GuestProjectRoot is where the Fake maps a project inside the guest.
//
// It is deliberately *not* the identity mapping Lima uses. The Fake is what
// command flows are proven against, and a double that shared a project at its
// host path could not catch the one mistake this whole boundary exists to
// prevent: a caller that assumes the host path is also the guest path, which is
// true on Lima and false on WSL (REQ-18.5, PROP-1). A flow that passes the
// Fake's guest path through to Shell works on both backends; one that passes a
// host path fails here rather than on a Windows user's machine.
const GuestProjectRoot = "/mnt/avr/projects"

// Op names a provider operation in a recorded call sequence.
type Op string

// The operations a Fake records. The values match the method names so that a
// failed assertion reads like the code under test.
const (
	OpEnsureMachine   Op = "EnsureMachine"
	OpShell           Op = "Shell"
	OpAppliedMounts   Op = "AppliedMounts"
	OpSetMounts       Op = "SetMounts"
	OpStop            Op = "Stop"
	OpDelete          Op = "Delete"
	OpStatus          Op = "Status"
	OpSnapshot        Op = "Snapshot"
	OpRestoreSnapshot Op = "RestoreSnapshot"
	OpListSnapshots   Op = "ListSnapshots"
	OpEditorTarget    Op = "EditorTarget"
	OpPortDiagnostics Op = "PortDiagnostics"

	OpScanNativeWorkspace  Op = "ScanNativeWorkspace"
	OpApplyNativeWorkspace Op = "ApplyNativeWorkspace"
)

// Call is one recorded operation with the arguments it was given and the result
// it produced. Only the fields relevant to Op are populated; the rest are zero.
type Call struct {
	// Op is which operation was called.
	Op Op
	// Machine is the machine name argument, empty for Status.
	Machine string
	// Spec is the argument to EnsureMachine.
	Spec provider.MachineSpec
	// Shell is the argument to Shell, captured before the Fake touched it so
	// that Env and Argv can be asserted verbatim.
	Shell provider.ShellOpts
	// Mounts is the desired set passed to SetMounts.
	Mounts []types.MountSpec
	// Snapshot is the snapshot name passed to Snapshot or RestoreSnapshot.
	Snapshot string
	// Workspace is the native workspace passed to ScanNativeWorkspace or
	// ApplyNativeWorkspace.
	Workspace types.NativeWorkspace
	// Sync is the synchronization passed to ApplyNativeWorkspace, captured
	// before the Fake applied it so a test can assert the exact file set and
	// direction a flow chose.
	Sync types.WorkspaceSync
	// ExitCode is what Shell returned.
	ExitCode int
	// Err is what the operation returned, nil on success. A call rejected by
	// the ownership guard is still recorded, with Err set, so a test can prove
	// the guard fired.
	Err error
}

// String renders the call the way an assertion failure should read.
func (c Call) String() string {
	var b strings.Builder
	b.WriteString(string(c.Op))
	b.WriteString("(")
	switch c.Op {
	case OpEnsureMachine:
		fmt.Fprintf(&b, "%s, %s", c.Spec.Name, c.Spec.Selector.Label())
	case OpShell:
		fmt.Fprintf(&b, "%s, workdir=%s, argv=%v, tty=%t", c.Machine, c.Shell.Workdir, c.Shell.Argv, c.Shell.TTY)
	case OpSetMounts:
		fmt.Fprintf(&b, "%s, %v", c.Machine, types.MountHostPaths(c.Mounts))
	case OpSnapshot, OpRestoreSnapshot:
		fmt.Fprintf(&b, "%s, %s", c.Machine, c.Snapshot)
	case OpScanNativeWorkspace:
		fmt.Fprintf(&b, "%s, %s", c.Machine, c.Workspace.Path)
	case OpApplyNativeWorkspace:
		fmt.Fprintf(&b, "%s, %s, %s, copy=%v, delete=%v",
			c.Machine, c.Workspace.Path, c.Sync.Direction, c.Sync.Copy, c.Sync.Delete)
	case OpStatus:
	default:
		b.WriteString(c.Machine)
	}
	b.WriteString(")")
	if c.Op == OpShell {
		fmt.Fprintf(&b, " = %d", c.ExitCode)
	}
	if c.Err != nil {
		fmt.Fprintf(&b, " err=%v", c.Err)
	}
	return b.String()
}

// machine is the Fake's model of one environment.
type machine struct {
	selector  types.EnvironmentSelector
	kind      types.MachineKind
	state     types.MachineState
	mounts    []types.MountSpec
	snapshots []provider.SnapshotInfo
	cpus      int
	memoryGB  float64
	diskGB    float64
	// restarts counts the times the Fake had to cycle the machine, so a test
	// can prove a flow did not restart when it had no need to (REQ-6.4).
	restarts int
	// foreign marks a machine avar does not own, seeded only by
	// AddForeignMachine.
	foreign bool
}

// snapshotEpoch is the base for synthesised snapshot timestamps. Snapshots are
// stamped at fixed one-minute intervals so that listings are deterministic.
var snapshotEpoch = time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

// Default resources reported for a machine created without explicit sizing,
// standing in for a backend's host-proportional defaults (REQ-17.4).
const (
	defaultCPUs     = 4
	defaultMemoryGB = 8.0
	defaultDiskGB   = 100.0
)

// Fake is an in-process provider that records what it was asked to do.
//
// Configure it with the Set*/Fail*/Add* methods before exercising the code
// under test, then assert with the Assert* helpers or read the recording with
// Calls, Ops and Events. Every zero value is a sane default: a fresh Fake owns
// no machines, fails nothing, and returns exit code 0 from Shell.
type Fake struct {
	mu       sync.Mutex
	calls    []Call
	events   []types.ProgressEvent
	machines map[string]*machine

	stickyErr map[Op]error
	queuedErr map[Op][]error

	exitCode int
	stdout   string
	stderr   string

	editorTargets map[string]provider.EditorTarget
	portDiags     map[string][]provider.PortDiagnostic

	// workspaces models the two copies of each project a native workspace
	// keeps, keyed by the workspace's guest path. See native.go.
	workspaces map[string]*nativeWorkspace

	snapshotSeq int
}

// New returns a Fake that owns no machines and fails nothing.
func New() *Fake {
	return &Fake{
		machines:      make(map[string]*machine),
		stickyErr:     make(map[Op]error),
		queuedErr:     make(map[Op][]error),
		editorTargets: make(map[string]provider.EditorTarget),
		portDiags:     make(map[string][]provider.PortDiagnostic),
		workspaces:    make(map[string]*nativeWorkspace),
	}
}

// --- programming outcomes -------------------------------------------------

// FailOn makes every subsequent call to op return err. Pass a nil err to stop
// failing.
func (f *Fake) FailOn(op Op, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.stickyErr, op)
		return
	}
	f.stickyErr[op] = err
}

// FailNextOn makes only the next call to op return err. Repeated calls queue up,
// which is how a flow that retries can be given a failure followed by a
// success. Queued failures are consumed before any FailOn error.
func (f *Fake) FailNextOn(op Op, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queuedErr[op] = append(f.queuedErr[op], err)
}

// SetExitCode programs the exit code Shell reports for the guest process. It is
// returned with a nil error: a non-zero guest status is not a provider failure
// (PROP-3).
func (f *Fake) SetExitCode(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exitCode = code
}

// SetShellOutput programs what Shell writes to redirected guest streams, so a
// flow that probes the guest and reads its output can be tested. Nothing is
// written when the caller left the stream nil (an attached session).
func (f *Fake) SetShellOutput(stdout, stderr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stdout, f.stderr = stdout, stderr
}

// AddMachine seeds an existing machine in the given state, so a test can start
// from a warm or stopped environment instead of provisioning one. It panics on
// a name avar does not own: seeding one would let a test prove behaviour the
// real backend refuses (REQ-5.4, PROP-6). Use AddForeignMachine for that.
func (f *Fake) AddMachine(name string, selector types.EnvironmentSelector, kind types.MachineKind, state types.MachineState) {
	if err := types.ValidateMachineName(name); err != nil {
		panic(fmt.Sprintf("fake.AddMachine: %v", err))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.machines[name] = &machine{
		selector: selector,
		kind:     kind,
		state:    state,
		cpus:     defaultCPUs,
		memoryGB: defaultMemoryGB,
		diskGB:   defaultDiskGB,
	}
}

// AddForeignMachine seeds a machine avar does not own — one the user created
// themselves. It exists so a test can prove that Status never lists it and that
// no operation will touch it (REQ-5.4, PROP-6).
func (f *Fake) AddForeignMachine(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.machines[name] = &machine{state: types.StateRunning, foreign: true}
}

// RemoveMachine deletes a machine from the Fake without recording a call,
// standing in for a machine that disappeared behind avar's back.
func (f *Fake) RemoveMachine(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.machines, name)
}

// SetMachineState forces a seeded machine's state. It panics if the machine was
// never added, because silently creating one would hide a test's own mistake.
func (f *Fake) SetMachineState(name string, state types.MachineState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.machines[name]
	if !ok {
		panic(fmt.Sprintf("fake.SetMachineState: %s was never added", name))
	}
	m.state = state
}

// SetSnapshots programs the snapshots a machine holds. It panics if the machine
// was never added.
func (f *Fake) SetSnapshots(name string, snaps []provider.SnapshotInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.machines[name]
	if !ok {
		panic(fmt.Sprintf("fake.SetSnapshots: %s was never added", name))
	}
	m.snapshots = append([]provider.SnapshotInfo(nil), snaps...)
}

// SetEditorTarget programs the target EditorTarget returns for a machine, which
// is how a test covers a backend that hands back SSH material as well as one
// that does not. GuestPath is filled in from the call.
func (f *Fake) SetEditorTarget(name string, target provider.EditorTarget) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.editorTargets[name] = target
}

// SetPortDiagnostics programs the port diagnostics reported for a machine.
func (f *Fake) SetPortDiagnostics(name string, diags []provider.PortDiagnostic) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.portDiags[name] = append([]provider.PortDiagnostic(nil), diags...)
}

// --- inspection -----------------------------------------------------------

// Calls returns the recorded operations in the order they happened.
func (f *Fake) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Ops returns just the operation names in order, which is what most flow
// assertions are about.
func (f *Fake) Ops() []Op {
	f.mu.Lock()
	defer f.mu.Unlock()
	ops := make([]Op, len(f.calls))
	for i, c := range f.calls {
		ops[i] = c.Op
	}
	return ops
}

// CallsFor returns the recorded calls to one operation, in order.
func (f *Fake) CallsFor(op Op) []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Call
	for _, c := range f.calls {
		if c.Op == op {
			out = append(out, c)
		}
	}
	return out
}

// Count reports how many times an operation was called.
func (f *Fake) Count(op Op) int {
	return len(f.CallsFor(op))
}

// LastCall returns the most recent call to an operation.
func (f *Fake) LastCall(op Op) (Call, bool) {
	calls := f.CallsFor(op)
	if len(calls) == 0 {
		return Call{}, false
	}
	return calls[len(calls)-1], true
}

// Events returns the progress events the Fake emitted, in order, so progress
// rendering can be tested against what a real provider would report.
func (f *Fake) Events() []types.ProgressEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]types.ProgressEvent(nil), f.events...)
}

// MachineState reports a machine's current state, or types.StateUnknown if the
// Fake has no such machine.
func (f *Fake) MachineState(name string) types.MachineState {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.machines[name]
	if !ok {
		return types.StateUnknown
	}
	return m.state
}

// Mounts reports a machine's currently applied file shares.
func (f *Fake) Mounts(name string) []types.MountSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.machines[name]
	if !ok {
		return nil
	}
	return append([]types.MountSpec(nil), m.mounts...)
}

// Restarts reports how many times the Fake cycled a machine, which is how a
// test proves a mount change restarted once and a revisit not at all (REQ-6.4).
func (f *Fake) Restarts(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.machines[name]
	if !ok {
		return 0
	}
	return m.restarts
}

// Reset clears the recording and the programmed outcomes but keeps the
// machines, for a test that exercises a second invocation against the same
// environment.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
	f.events = nil
	f.stickyErr = make(map[Op]error)
	f.queuedErr = make(map[Op][]error)
	f.exitCode = 0
	f.stdout, f.stderr = "", ""
}

// --- Provider -------------------------------------------------------------

// ID reports the Fake's own backend id.
func (f *Fake) ID() types.ProviderID { return ProviderID }

// MapProjectPath maps a project root to a deterministic guest root and appends
// the working directory's relative suffix.
//
// It is not recorded as a call. The contract makes it a pure function —
// deterministic, no mutation, no I/O — so there is nothing about it a flow test
// could assert by counting invocations; what a flow has to get right is that
// the guest path it eventually passes to Shell is the one the provider planned,
// and that is asserted on the Shell call itself.
func (f *Fake) MapProjectPath(projectID, hostRoot, hostCwd string) (types.MountSpec, string, error) {
	if strings.TrimSpace(projectID) == "" {
		return types.MountSpec{}, "", fmt.Errorf("mapping %s into the guest: no project identity was given", hostRoot)
	}
	if !filepath.IsAbs(hostRoot) || !filepath.IsAbs(hostCwd) {
		return types.MountSpec{}, "", fmt.Errorf("mapping a project into the guest: the project root and the current directory must both be absolute (got %q and %q)", hostRoot, hostCwd)
	}
	root, cwd := filepath.Clean(hostRoot), filepath.Clean(hostCwd)

	rel, err := filepath.Rel(root, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return types.MountSpec{}, "", fmt.Errorf("mapping %s into the guest: it is not inside the project directory %s", cwd, root)
	}

	guestRoot := path.Join(GuestProjectRoot, projectID)
	guestCwd := guestRoot
	if rel != "." {
		guestCwd = path.Join(guestRoot, filepath.ToSlash(rel))
	}
	mount := types.MountSpec{ProjectID: projectID, HostPath: root, GuestPath: guestRoot, Writable: true}
	if err := mount.Validate(); err != nil {
		return types.MountSpec{}, "", fmt.Errorf("mapping %s into the guest: %w", root, err)
	}
	return mount, guestCwd, nil
}

// EnsureMachine creates the machine if absent, starts it if not running, and
// does nothing at all when it is already running (REQ-1.2, REQ-1.3).
func (f *Fake) EnsureMachine(ctx context.Context, spec provider.MachineSpec, progress types.ProgressSink) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := Call{Op: OpEnsureMachine, Machine: spec.Name, Spec: cloneSpec(spec)}
	call.Err = f.ensureMachine(ctx, spec, progress)
	f.calls = append(f.calls, call)
	return call.Err
}

func (f *Fake) ensureMachine(ctx context.Context, spec provider.MachineSpec, progress types.ProgressSink) error {
	if err := f.gate(ctx, OpEnsureMachine, spec.Name); err != nil {
		return err
	}

	if spec.Provider != "" && spec.Provider != f.ID() {
		return fmt.Errorf("creating machine %s: it is addressed to the %q backend, not %q", spec.Name, spec.Provider, f.ID())
	}
	mounts, err := types.NormalizeMounts(spec.Mounts)
	if err != nil {
		return fmt.Errorf("creating machine %s: %w", spec.Name, err)
	}

	m, exists := f.machines[spec.Name]
	switch {
	case !exists:
		m = &machine{
			selector: spec.Selector,
			kind:     spec.Kind,
			mounts:   mounts,
			cpus:     orInt(spec.CPUs, defaultCPUs),
			memoryGB: orFloat(spec.MemoryGB, defaultMemoryGB),
			diskGB:   orFloat(spec.DiskGB, defaultDiskGB),
		}
		f.machines[spec.Name] = m
		f.emit(progress, types.ProgressEvent{
			Kind:    types.ProgressCreating,
			Machine: spec.Name,
			Message: fmt.Sprintf("Creating your Linux development environment · %s", spec.Selector.Label()),
		})
		if spec.Selector.Emulated() {
			f.emit(progress, types.ProgressEvent{
				Kind:    types.ProgressWarning,
				Machine: spec.Name,
				Message: fmt.Sprintf("%s runs under CPU emulation and will be noticeably slower than the host architecture", spec.Selector.Label()),
			})
		}
		m.state = types.StateRunning

	case m.foreign:
		return fmt.Errorf("%w: %s", provider.ErrNotOwned, spec.Name)

	case m.state == types.StateBroken:
		return fmt.Errorf("machine %s is broken and cannot be started", spec.Name)

	case m.state != types.StateRunning:
		f.emit(progress, types.ProgressEvent{
			Kind:    types.ProgressStarting,
			Machine: spec.Name,
			Message: fmt.Sprintf("Starting %s", spec.Selector.Label()),
		})
		m.state = types.StateRunning

	default:
		// Already running: the warm path is a silent no-op (REQ-1.2, REQ-1.3).
	}
	return nil
}

// Shell reports the programmed exit code for a guest process on a running
// machine. A non-zero code comes back with a nil error (PROP-3).
func (f *Fake) Shell(ctx context.Context, name string, opts provider.ShellOpts) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := Call{Op: OpShell, Machine: name, Shell: cloneShellOpts(opts)}
	call.ExitCode, call.Err = f.shell(ctx, name, opts)
	f.calls = append(f.calls, call)
	return call.ExitCode, call.Err
}

func (f *Fake) shell(ctx context.Context, name string, opts provider.ShellOpts) (int, error) {
	if err := f.gate(ctx, OpShell, name); err != nil {
		return 0, err
	}
	if opts.TTY && (opts.Stdin != nil || opts.Stdout != nil || opts.Stderr != nil) {
		return 0, errors.New("attaching a shell: ShellOpts.TTY cannot be combined with redirected streams")
	}
	if _, err := f.running(name); err != nil {
		return 0, err
	}
	if opts.Stdout != nil && f.stdout != "" {
		if _, err := io.WriteString(opts.Stdout, f.stdout); err != nil {
			return 0, fmt.Errorf("writing guest stdout: %w", err)
		}
	}
	if opts.Stderr != nil && f.stderr != "" {
		if _, err := io.WriteString(opts.Stderr, f.stderr); err != nil {
			return 0, fmt.Errorf("writing guest stderr: %w", err)
		}
	}
	return f.exitCode, nil
}

// AppliedMounts reports the machine's applied file shares, sorted.
func (f *Fake) AppliedMounts(ctx context.Context, name string) ([]types.MountSpec, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := Call{Op: OpAppliedMounts, Machine: name}
	var mounts []types.MountSpec
	if err := f.gate(ctx, OpAppliedMounts, name); err != nil {
		call.Err = err
	} else if m, err := f.owned(name); err != nil {
		call.Err = err
	} else {
		mounts = append([]types.MountSpec(nil), m.mounts...)
	}
	f.calls = append(f.calls, call)
	return mounts, call.Err
}

// SetMounts replaces the machine's shared directories, restarting it only when
// the set actually changed (REQ-6.4, PROP-5).
func (f *Fake) SetMounts(ctx context.Context, name string, mounts []types.MountSpec, progress types.ProgressSink) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := Call{Op: OpSetMounts, Machine: name, Mounts: append([]types.MountSpec(nil), mounts...)}
	call.Err = f.setMounts(ctx, name, mounts, progress)
	f.calls = append(f.calls, call)
	return call.Err
}

func (f *Fake) setMounts(ctx context.Context, name string, mounts []types.MountSpec, progress types.ProgressSink) error {
	if err := f.gate(ctx, OpSetMounts, name); err != nil {
		return err
	}
	m, err := f.owned(name)
	if err != nil {
		return err
	}
	want, err := types.NormalizeMounts(mounts)
	if err != nil {
		return fmt.Errorf("sharing directories with machine %s: %w", name, err)
	}
	if types.EqualMounts(m.mounts, want) {
		// Nothing to apply, so nothing to restart and nothing to say.
		return nil
	}
	f.emit(progress, types.ProgressEvent{
		Kind:    types.ProgressMounting,
		Machine: name,
		Message: fmt.Sprintf("Adding %s to your Linux environment — one-time restart", strings.Join(types.MountHostPaths(added(m.mounts, want)), ", ")),
	})
	m.mounts = want
	if m.state == types.StateRunning {
		m.restarts++
	}
	return nil
}

// Stop stops a running machine and does nothing to one already stopped.
func (f *Fake) Stop(ctx context.Context, name string, progress types.ProgressSink) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := Call{Op: OpStop, Machine: name}
	call.Err = f.stop(ctx, name, progress)
	f.calls = append(f.calls, call)
	return call.Err
}

func (f *Fake) stop(ctx context.Context, name string, progress types.ProgressSink) error {
	if err := f.gate(ctx, OpStop, name); err != nil {
		return err
	}
	m, err := f.owned(name)
	if err != nil {
		return err
	}
	if m.state != types.StateRunning {
		return nil
	}
	f.emit(progress, types.ProgressEvent{
		Kind:    types.ProgressStopping,
		Machine: name,
		Message: fmt.Sprintf("Stopping %s", m.selector.Label()),
	})
	m.state = types.StateStopped
	return nil
}

// Delete removes the machine and is idempotent: deleting one that does not
// exist succeeds (REQ-1.6, REQ-17.5).
func (f *Fake) Delete(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := Call{Op: OpDelete, Machine: name}
	call.Err = f.delete(ctx, name)
	f.calls = append(f.calls, call)
	return call.Err
}

func (f *Fake) delete(ctx context.Context, name string) error {
	if err := f.gate(ctx, OpDelete, name); err != nil {
		return err
	}
	if m, ok := f.machines[name]; ok && m.foreign {
		return fmt.Errorf("%w: %s", provider.ErrNotOwned, name)
	}
	delete(f.machines, name)
	return nil
}

// Status reports the avar-owned machines, ordered by name. Machines avar does
// not own are filtered out entirely (REQ-5.4, PROP-6).
func (f *Fake) Status(ctx context.Context) ([]types.MachineStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := Call{Op: OpStatus}
	var out []types.MachineStatus
	if err := f.gate(ctx, OpStatus, ""); err != nil {
		call.Err = err
	} else {
		names := make([]string, 0, len(f.machines))
		for name, m := range f.machines {
			if m.foreign || types.ValidateMachineName(name) != nil {
				continue
			}
			names = append(names, name)
		}
		sort.Strings(names)
		out = make([]types.MachineStatus, 0, len(names))
		for _, name := range names {
			m := f.machines[name]
			out = append(out, types.MachineStatus{
				Name:     name,
				Provider: f.ID(),
				Selector: m.selector,
				Kind:     m.kind,
				State:    m.state,
				CPUs:     m.cpus,
				MemoryGB: m.memoryGB,
				DiskGB:   m.diskGB,
				Mounts:   append([]types.MountSpec(nil), m.mounts...),
				Runtime:  runtimeOf(m.selector),
			})
		}
	}
	f.calls = append(f.calls, call)
	return out, call.Err
}

// --- Snapshotter ----------------------------------------------------------

// Snapshot captures a named snapshot, cycling a running machine as a real
// backend would (REQ-10.1).
func (f *Fake) Snapshot(ctx context.Context, name, snapshot string, progress types.ProgressSink) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := Call{Op: OpSnapshot, Machine: name, Snapshot: snapshot}
	call.Err = f.snapshot(ctx, name, snapshot, progress)
	f.calls = append(f.calls, call)
	return call.Err
}

func (f *Fake) snapshot(ctx context.Context, name, snapshot string, progress types.ProgressSink) error {
	if err := f.gate(ctx, OpSnapshot, name); err != nil {
		return err
	}
	m, err := f.owned(name)
	if err != nil {
		return err
	}
	if findSnapshot(m.snapshots, snapshot) >= 0 {
		return fmt.Errorf("capturing snapshot %q of %s: a snapshot with that name already exists", snapshot, name)
	}
	f.cycle(m, name, progress)
	f.snapshotSeq++
	m.snapshots = append(m.snapshots, provider.SnapshotInfo{
		Name:      snapshot,
		CreatedAt: snapshotEpoch.Add(time.Duration(f.snapshotSeq) * time.Minute),
	})
	return nil
}

// RestoreSnapshot restores a named snapshot, reporting ErrSnapshotNotFound for
// an unknown name (REQ-10.2).
func (f *Fake) RestoreSnapshot(ctx context.Context, name, snapshot string, progress types.ProgressSink) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := Call{Op: OpRestoreSnapshot, Machine: name, Snapshot: snapshot}
	call.Err = f.restoreSnapshot(ctx, name, snapshot, progress)
	f.calls = append(f.calls, call)
	return call.Err
}

func (f *Fake) restoreSnapshot(ctx context.Context, name, snapshot string, progress types.ProgressSink) error {
	if err := f.gate(ctx, OpRestoreSnapshot, name); err != nil {
		return err
	}
	m, err := f.owned(name)
	if err != nil {
		return err
	}
	if findSnapshot(m.snapshots, snapshot) < 0 {
		return fmt.Errorf("%w: %q on %s", provider.ErrSnapshotNotFound, snapshot, name)
	}
	f.cycle(m, name, progress)
	return nil
}

// ListSnapshots reports the machine's snapshots, oldest first.
func (f *Fake) ListSnapshots(ctx context.Context, name string) ([]provider.SnapshotInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := Call{Op: OpListSnapshots, Machine: name}
	var out []provider.SnapshotInfo
	if err := f.gate(ctx, OpListSnapshots, name); err != nil {
		call.Err = err
	} else if m, err := f.owned(name); err != nil {
		call.Err = err
	} else {
		out = append([]provider.SnapshotInfo(nil), m.snapshots...)
	}
	f.calls = append(f.calls, call)
	return out, call.Err
}

// --- EditorTargetProvider -------------------------------------------------

// EditorTarget returns the programmed target, or a synthesised one, for a
// running machine.
//
// The synthesised target carries no SSH material, so a flow tested against a
// bare Fake is a flow that works on a backend an editor reaches without SSH
// (REQ-18.10, PROP-17). A test that needs the SSH-bearing shape programs one.
func (f *Fake) EditorTarget(ctx context.Context, name, guestPath string) (provider.EditorTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := Call{Op: OpEditorTarget, Machine: name}
	var target provider.EditorTarget
	if err := f.gate(ctx, OpEditorTarget, name); err != nil {
		call.Err = err
	} else if _, err := f.running(name); err != nil {
		call.Err = err
	} else if programmed, ok := f.editorTargets[name]; ok {
		target = programmed
		target.GuestPath = guestPath
	} else {
		target = provider.EditorTarget{Authority: string(f.ID()) + "+" + name, GuestPath: guestPath}
	}
	f.calls = append(f.calls, call)
	return target, call.Err
}

// --- PortDiagnoser --------------------------------------------------------

// PortDiagnostics returns the programmed diagnostics for a machine.
func (f *Fake) PortDiagnostics(ctx context.Context, name string) ([]provider.PortDiagnostic, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := Call{Op: OpPortDiagnostics, Machine: name}
	var out []provider.PortDiagnostic
	if err := f.gate(ctx, OpPortDiagnostics, name); err != nil {
		call.Err = err
	} else if _, err := f.owned(name); err != nil {
		call.Err = err
	} else {
		out = append([]provider.PortDiagnostic(nil), f.portDiags[name]...)
	}
	f.calls = append(f.calls, call)
	return out, call.Err
}

// --- internals ------------------------------------------------------------

// gate applies what every operation owes the caller before doing any work:
// honour cancellation, refuse machines avar does not own, then yield any
// programmed failure. The ownership guard runs first so that a programmed error
// can never mask a name the real backend would have rejected (PROP-6).
func (f *Fake) gate(ctx context.Context, op Op, name string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if name != "" {
		if err := types.ValidateMachineName(name); err != nil {
			return fmt.Errorf("%w: %s", provider.ErrNotOwned, name)
		}
	}
	if queued := f.queuedErr[op]; len(queued) > 0 {
		f.queuedErr[op] = queued[1:]
		return queued[0]
	}
	return f.stickyErr[op]
}

// owned resolves a machine that must exist and must belong to avar.
func (f *Fake) owned(name string) (*machine, error) {
	m, ok := f.machines[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", provider.ErrMachineNotFound, name)
	}
	if m.foreign {
		return nil, fmt.Errorf("%w: %s", provider.ErrNotOwned, name)
	}
	return m, nil
}

// running resolves a machine that must additionally be running, which is what
// Shell and SSHConfig require of their caller.
func (f *Fake) running(name string) (*machine, error) {
	m, err := f.owned(name)
	if err != nil {
		return nil, err
	}
	if m.state != types.StateRunning {
		return nil, fmt.Errorf("%w: %s is %s", provider.ErrMachineNotRunning, name, m.state)
	}
	return m, nil
}

// cycle models the stop/start a backend needs in order to change a machine it
// cannot modify while running, leaving it in the state it was found in.
func (f *Fake) cycle(m *machine, name string, progress types.ProgressSink) {
	if m.state != types.StateRunning {
		return
	}
	f.emit(progress, types.ProgressEvent{
		Kind:    types.ProgressStopping,
		Machine: name,
		Message: fmt.Sprintf("Pausing %s", m.selector.Label()),
	})
	f.emit(progress, types.ProgressEvent{
		Kind:    types.ProgressStarting,
		Machine: name,
		Message: fmt.Sprintf("Resuming %s", m.selector.Label()),
	})
	m.restarts++
}

// emit records an event and forwards it to the sink. The sink is called with
// the Fake's lock held, so it must not call back into the same Fake.
func (f *Fake) emit(sink types.ProgressSink, event types.ProgressEvent) {
	f.events = append(f.events, event)
	if sink != nil {
		sink.Progress(event)
	}
}

func cloneSpec(spec provider.MachineSpec) provider.MachineSpec {
	spec.Mounts = append([]types.MountSpec(nil), spec.Mounts...)
	return spec
}

// cloneShellOpts snapshots the options so that a later mutation by the caller
// cannot rewrite history — Env in particular is asserted verbatim (PROP-4).
func cloneShellOpts(opts provider.ShellOpts) provider.ShellOpts {
	opts.Argv = append([]string(nil), opts.Argv...)
	if opts.Env != nil {
		env := make(map[string]string, len(opts.Env))
		for k, v := range opts.Env {
			env[k] = v
		}
		opts.Env = env
	}
	return opts
}

// added reports the entries of want whose host directory was not already
// shared.
func added(have, want []types.MountSpec) []types.MountSpec {
	present := make(map[string]struct{}, len(have))
	for _, m := range have {
		present[m.HostPath] = struct{}{}
	}
	var out []types.MountSpec
	for _, m := range want {
		if _, ok := present[m.HostPath]; !ok {
			out = append(out, m)
		}
	}
	return out
}

func findSnapshot(snaps []provider.SnapshotInfo, name string) int {
	for i, s := range snaps {
		if s.Name == name {
			return i
		}
	}
	return -1
}

// runtimeOf stands in for the backend-opaque runtime shown in status output.
// The Fake reports whether the environment is emulated, which is the only part
// of it avar reasons about.
func runtimeOf(selector types.EnvironmentSelector) string {
	if selector.Emulated() {
		return "emulated"
	}
	return "native"
}

func orInt(v, fallback int) int {
	if v == 0 {
		return fallback
	}
	return v
}

func orFloat(v, fallback float64) float64 {
	if v == 0 {
		return fallback
	}
	return v
}
