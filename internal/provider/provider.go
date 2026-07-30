// Package provider defines the boundary between avar and whatever actually
// runs Linux for it.
//
// Everything above this package — the command layer, the resolver, mount
// management — depends on these interfaces and never on a concrete backend, so
// that a second backend can be added without editing a line of command-layer
// code (REQ-17.3). Nothing here names or assumes a particular virtualization
// technology, product, or command-line tool: a backend that talks to a remote
// host over SSH must be able to satisfy these contracts as written.
//
// The operations are split by capability rather than gathered into one large
// interface. Provider is the core set every backend must be able to do at all;
// Snapshotter, SSHConfigProvider and PortDiagnoser describe abilities a backend
// either genuinely has or genuinely lacks. A caller that needs a capability
// type-asserts for it and tells the user plainly when the backend does not
// offer it, instead of every backend being forced to declare methods it can
// only stub out.
//
// Vocabulary comes from the glossary: a Machine is one Linux environment avar
// manages, a Selector is the (distro, version, arch, isolation) choice that
// determines which machine a command targets, and a Project is a host
// directory. Package types owns those shared definitions; this package owns
// only the descriptions of backend operations.
package provider

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/olamide226/avar/internal/types"
)

// Errors every implementation reports through the same sentinels, so callers
// can react to a condition instead of matching on message text.
var (
	// ErrNotOwned reports an attempt to operate on a machine avar does not
	// own. Implementations return it — wrapped, naming the machine — rather
	// than touching anything outside avar's own machines (REQ-5.4, PROP-6).
	ErrNotOwned = errors.New("machine is not managed by avar")

	// ErrMachineNotFound reports that the named machine does not exist.
	// Operations documented as fully idempotent (Delete) never return it.
	ErrMachineNotFound = errors.New("machine does not exist")

	// ErrMachineNotRunning reports that an operation needing a running machine
	// was pointed at one that is not running. Callers reach it only by
	// skipping EnsureMachine.
	ErrMachineNotRunning = errors.New("machine is not running")

	// ErrSnapshotNotFound reports an unknown snapshot name, so the caller can
	// list the names that do exist instead of echoing a backend error
	// (REQ-10.2).
	ErrSnapshotNotFound = errors.New("snapshot does not exist")
)

// Provider is the set of operations every avar backend must implement. It is
// the whole of avar's dependency on the outside world for machine lifecycle,
// file sharing, and command execution.
//
// Contract obligations that apply to every method:
//
//   - The context comes first and is honoured. A cancelled context aborts the
//     operation promptly, leaves no half-finished machine behind (PROP-7), and
//     returns an error wrapping ctx.Err().
//   - Any machine name argument is validated with types.ValidateMachineName
//     before anything is done with it. A name avar does not own yields an
//     error wrapping ErrNotOwned and no side effects whatsoever. Where the
//     implementation is given access to avar's records, membership in those
//     records is checked too: the prefix and the record together are what
//     "avar owns this" means (REQ-5.4, PROP-6).
//   - Implementations return values and errors; they never write to the
//     terminal. User-visible progress goes through the types.ProgressSink
//     parameter of the methods that take one, and the presentation layer
//     decides how to render it. The one exception is the guest's own standard
//     streams, which Shell connects straight through.
//   - Errors name what was being attempted, wrap the underlying cause with %w,
//     and where the user can act, say what to do next.
type Provider interface {
	// EnsureMachine brings the machine described by spec into a running state,
	// creating it first if it does not exist.
	//
	// This is the first call of nearly every avr invocation, so it must be
	// idempotent (REQ-1.2, REQ-1.3):
	//
	//   - Machine absent: provision it from spec, then start it.
	//   - Machine present but not running: start it.
	//   - Machine already running: do nothing, emit no progress events, return
	//     nil. This is the warm path the latency budget is measured on, so it
	//     must not restart, reconfigure, or re-verify anything the backend can
	//     avoid (REQ-17.1).
	//
	// On a nil return the machine is running, has the mounts spec asked for,
	// and offers a non-root guest account matching the host username with
	// passwordless privilege escalation (REQ-1.4). The caller may record the
	// machine as viable only after that nil return.
	//
	// On failure nothing half-created survives: a partially provisioned
	// machine is removed before returning, so the next invocation either finds
	// a working machine or finds nothing and provisions cleanly (REQ-1.6,
	// PROP-7).
	//
	// Progress is reported as it goes, because provisioning is the one slow
	// path a user sits and waits through: types.ProgressCreating when a
	// machine is being provisioned, types.ProgressStarting when an existing
	// one is being started, and — when spec.Selector.Emulated() is true — a
	// single types.ProgressWarning about the cost of emulation before the work
	// begins (REQ-4.6). progress is never nil; pass types.DiscardProgress to
	// ignore events.
	EnsureMachine(ctx context.Context, spec MachineSpec, progress types.ProgressSink) error

	// Shell runs a command in the guest, or attaches an interactive login
	// shell when opts.Argv is empty, and reports the exit code the guest
	// process finished with.
	//
	// The machine must already be running: Shell creates and starts nothing,
	// returning ErrMachineNotFound or ErrMachineNotRunning instead. Call
	// EnsureMachine first.
	//
	// exitCode and err are independent, and conflating them is the easiest way
	// to break avar's contract with the shell that invoked it:
	//
	//   - A guest process that ran and exited non-zero is a SUCCESS for Shell.
	//     Return (that code, nil). `avr false` exits 1 and `avr sh -c 'exit
	//     42'` exits 42 (REQ-1.7, REQ-2.2, PROP-3). Never turn a non-zero
	//     guest status into a Go error, and never add commentary of avar's own
	//     about it — the guest already said whatever it had to say on its own
	//     stderr.
	//   - err is for failing to run the command at all: the machine went away,
	//     the transport broke, the context was cancelled. exitCode is then
	//     meaningless and the caller reports the error instead.
	//
	// Shell streams the guest's standard streams without introducing
	// buffering-induced reordering (REQ-2.3), allocates a guest pseudo-terminal
	// if and only if opts.TTY, and forwards interrupt, terminate, and
	// window-resize notifications to the guest (REQ-2.4, REQ-3.1, REQ-3.3,
	// PROP-8).
	//
	// Shell deliberately takes no types.ProgressSink and must stay silent. By
	// the time the guest is attached avar has nothing left to say, and
	// anything it printed would interleave with the guest's own output.
	Shell(ctx context.Context, machine string, opts ShellOpts) (exitCode int, err error)

	// AppliedMounts reports the host directories currently shared into the
	// machine, in a stable order.
	//
	// It is a read-only query: it changes nothing, emits no progress, and does
	// not require the machine to be running. The result is what the backend
	// has actually applied, not what avar's records expect — comparing the two
	// is how a caller decides whether the current project still needs
	// registering (REQ-6.4).
	AppliedMounts(ctx context.Context, machine string) ([]string, error)

	// SetMounts makes dirs the machine's set of shared host directories,
	// replacing whatever was there.
	//
	// dirs is the complete desired set, not an addition: every entry is an
	// absolute host directory, shared writable at the identical path inside
	// the guest (REQ-6.1), and anything absent from dirs stops being shared.
	// Replace-not-append is what makes mount confinement checkable — the guest
	// can reach exactly the project roots avar registered to that machine and
	// nothing else, never the home directory and never an unregistered sibling
	// (REQ-6.3, REQ-9.3, REQ-9.4, PROP-5). Callers therefore pass the existing
	// set plus the new project.
	//
	// It is idempotent: when dirs already equals the applied set, SetMounts
	// changes nothing, emits no progress, and in particular does not restart
	// the machine — returning to a known project has to stay instant
	// (REQ-17.1).
	//
	// Where a backend can only change file sharing by restarting the machine,
	// SetMounts performs that restart inside the call and explains the
	// one-time delay first through a types.ProgressMounting event, so the user
	// knows why an otherwise instant command took ten seconds (REQ-6.4). On a
	// nil return the machine is running again and every entry in dirs is
	// readable and writable in the guest; implementations verify that rather
	// than assume it, because dropping the user into a shell at an empty path
	// is worse than failing outright (REQ-6.5).
	SetMounts(ctx context.Context, machine string, dirs []string, progress types.ProgressSink) error

	// Stop shuts the machine down cleanly, releasing its memory (REQ-5.2), and
	// reports it with a types.ProgressStopping event.
	//
	// It converges on a state rather than performing an action: stopping a
	// machine that is already stopped returns nil and emits nothing. An
	// unknown machine is ErrMachineNotFound, so a caller can distinguish
	// "already stopped" from "there was never an environment here". Stop never
	// destroys anything inside the guest.
	Stop(ctx context.Context, machine string, progress types.ProgressSink) error

	// Delete destroys the machine and everything installed inside it. Host
	// files are untouched: project directories were shared, never copied, so
	// deleting a machine cannot lose a user's work (REQ-10.3, PROP-10).
	//
	// Delete is fully idempotent — deleting a machine that does not exist
	// returns nil — because it is also how avar cleans up after a failed
	// create and after a crash, where the caller cannot know what survived
	// (REQ-1.6, REQ-17.5, PROP-7).
	//
	// Delete never asks the user anything. Confirmation belongs to the command
	// layer (REQ-10.3).
	Delete(ctx context.Context, machine string) error

	// Status reports one entry per machine avar owns, ordered by name so that
	// output and tests are deterministic.
	//
	// Implementations must filter, not merely annotate. A machine the user
	// created outside avar — one whose name fails types.ValidateMachineName,
	// or which is absent from avar's own records — must not appear in the
	// result at all, because `avr status` and `avr stop --all` are built
	// directly on it and must never reach a machine avar did not create
	// (REQ-5.1, REQ-5.4, PROP-6). An empty slice and a nil error is the
	// correct answer when avar owns nothing yet (REQ-5.3).
	//
	// types.MachineStatus.Sessions counts live avar sessions, which is avar's
	// own bookkeeping rather than backend truth; providers may leave it zero
	// for the caller to fill in.
	Status(ctx context.Context) ([]types.MachineStatus, error)
}

// Snapshotter is implemented by backends that can capture and restore a
// machine's state (REQ-10.1, REQ-10.2, REQ-10.4).
//
// It is separate from Provider because snapshot support is a real difference
// between backends rather than an implementation detail: a backend that
// attaches to a machine somebody else administers has no snapshots to offer. A
// backend without them should say so by not implementing this interface, not
// by being forced to declare three methods that return "unsupported".
type Snapshotter interface {
	// Snapshot captures the machine's current state under name and returns
	// once the snapshot is durable (REQ-10.1).
	//
	// The machine may be running. Where the backend cannot capture a running
	// machine, Snapshot stops it, captures, and starts it again within the
	// call, reporting each transition through progress so the pause is
	// explained; the machine is left in the state it was found in. A name that
	// already exists is an error — deciding whether to replace it is the
	// caller's business, not the backend's.
	Snapshot(ctx context.Context, machine, name string, progress types.ProgressSink) error

	// RestoreSnapshot returns the machine to the state captured under name
	// (REQ-10.2).
	//
	// An unknown name is ErrSnapshotNotFound with no side effects, so the
	// caller can list the available names instead. As with Snapshot, any
	// stop/start the backend needs is performed inside the call, reported
	// through progress, and leaves the machine in the state it was found in.
	// Work done in the guest since the snapshot is destroyed; host project
	// files are not affected.
	RestoreSnapshot(ctx context.Context, machine, name string, progress types.ProgressSink) error

	// ListSnapshots reports the snapshots held for the machine, newest last,
	// with the timestamps `avr snapshot` displays (REQ-10.4). It is a
	// read-only query: it changes nothing and emits no progress.
	ListSnapshots(ctx context.Context, machine string) ([]SnapshotInfo, error)
}

// SSHConfigProvider is implemented by backends that can describe how to reach
// a machine over SSH, which is what editor integration needs (REQ-13.1).
//
// It is separate from Provider because a backend may execute commands in a
// guest through a transport that has no SSH endpoint to hand out at all, in
// which case `avr code` must be able to say so rather than receive a
// half-truth.
type SSHConfigProvider interface {
	// SSHConfig returns an OpenSSH client configuration stanza that reaches
	// the machine, suitable for writing into avar's own SSH configuration file
	// (REQ-13.3).
	//
	// The machine must be running, since the stanza describes a live endpoint;
	// otherwise ErrMachineNotRunning. The returned text is data, not a file
	// path, and the caller owns where it lands — implementations never write
	// to the user's SSH configuration.
	SSHConfig(ctx context.Context, machine string) (string, error)
}

// PortDiagnoser is implemented by backends that can explain the state of
// automatic port forwarding.
//
// Forwarding itself is not a Provider operation: a guest port becomes
// reachable on the host without avar asking, and is released the same way
// (REQ-7.1, REQ-7.3), so there is nothing to call — only something to report.
// This exists so that a port which could not be published because the host
// port was already taken is discoverable in avar's diagnostics instead of
// silently missing (REQ-7.2).
type PortDiagnoser interface {
	// PortDiagnostics reports what the backend knows about the machine's
	// forwarded ports, ordered by guest port. It is a read-only query and
	// never fails merely because forwarding is broken — an unforwardable port
	// is data in the result, not an error.
	PortDiagnostics(ctx context.Context, machine string) ([]PortDiagnostic, error)
}

// MachineSpec fully describes the machine a caller wants to exist. It is the
// resolver's decision stated in backend-neutral terms: no image references, no
// configuration file, no virtualization mode.
type MachineSpec struct {
	// Name is the machine's identity, derived deterministically by the
	// resolver and carrying avar's ownership prefix
	// (types.MachineNamePrefix). The user never sees or supplies it
	// (REQ-1.5).
	Name string

	// Selector is the environment this machine provides. The backend maps it
	// to a concrete OS image, and where Selector.Emulated() is true, to
	// whatever CPU emulation it has (REQ-4.1, REQ-4.2, REQ-4.6).
	Selector types.EnvironmentSelector

	// Kind is the role the machine plays, which a backend may use to decide
	// how to create it — a base machine is provisioned but never entered, so
	// it can be derived from cheaply later.
	Kind types.MachineKind

	// Mounts are the host project directories to share at identical guest
	// paths when the machine is created. Registering further projects later
	// goes through Provider.SetMounts.
	Mounts []string

	// CPUs, MemoryGB and DiskGB size a machine that is being created and are
	// ignored for one that already exists. Zero means "the backend's
	// conservative, host-proportional default", which keeps REQ-17.4's
	// defaults in one place instead of having every caller restate them.
	CPUs     int
	MemoryGB float64
	DiskGB   float64

	// DeriveFrom optionally names an existing machine to copy as the starting
	// point instead of provisioning from scratch, which is how a per-project
	// environment is created quickly from a pristine base (REQ-11.1).
	//
	// It is a hint, not a requirement: a backend that cannot derive machines,
	// or that finds the named machine missing, provisions normally instead —
	// the result must be indistinguishable either way. The named machine must
	// itself be avar-owned and is never modified.
	DeriveFrom string
}

// ShellOpts describes one guest execution.
type ShellOpts struct {
	// Workdir is the guest directory the process starts in. avar always sets
	// it to the host's current directory, which the identical-path mount makes
	// meaningful on both sides of the boundary (REQ-1.1, REQ-2.1, REQ-6.6,
	// PROP-1).
	Workdir string

	// Argv is the command to run, already split into arguments; nothing on the
	// way to the guest may re-parse or re-split it. Empty means an interactive
	// login shell (REQ-1.1).
	Argv []string

	// Env is the guest process environment, and it is a security boundary.
	//
	// It contains exactly the variables avar's policy allows through, and
	// implementations must pass exactly those. They must not merge, inherit,
	// or fall back to the host process environment, and must not add
	// convenience variables of their own. Any host variable outside the
	// minimal terminal allowlist that the user did not explicitly forward has
	// to be absent from the guest; otherwise tokens sitting in a shell profile
	// cross into Linux silently, which is precisely what avar promises does
	// not happen (REQ-9.1, REQ-12.4, PROP-4).
	//
	// Whatever the transport itself needs in order to work is the
	// implementation's own business and must not surface in the guest's
	// environment.
	Env map[string]string

	// TTY requests a guest pseudo-terminal. The caller sets it if and only if
	// the host's stdin is a terminal, so interactive programs get a terminal
	// and pipelines do not (REQ-2.3, PROP-8).
	TTY bool

	// Stdin, Stdout and Stderr optionally redirect the guest's standard
	// streams. A nil stream means the corresponding stream of the calling
	// process, which is what an attached session needs.
	//
	// Redirection is only meaningful for non-interactive execution: a
	// pseudo-terminal is bound to the real terminal's file descriptors, so
	// implementations must reject a non-nil stream combined with TTY rather
	// than silently ignoring one of the two. avar's own probes — verifying
	// that a mount really landed, for instance — redirect so that their output
	// never reaches the user's terminal (REQ-6.5).
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// ForwardSSHAgent asks for the host's SSH agent to be reachable from the
	// guest for this one execution and no longer, and is never set by default
	// (REQ-9.2, REQ-12.3, REQ-12.4). Phase 2.
	ForwardSSHAgent bool
}

// SnapshotInfo is one captured machine state, as `avr snapshot` lists it
// (REQ-10.4).
type SnapshotInfo struct {
	// Name is the identifier the user gave the snapshot and passes to restore.
	Name string
	// CreatedAt is when the snapshot was captured, in the host's clock.
	CreatedAt time.Time
}

// PortDiagnostic is what the backend knows about one guest port's forwarding
// (REQ-7.2).
type PortDiagnostic struct {
	// GuestPort is the port a guest process is listening on.
	GuestPort int
	// HostPort is the host port it was published on, which is normally the
	// same number. Zero when nothing was published.
	HostPort int
	// Forwarded reports whether the port is currently reachable from the host.
	Forwarded bool
	// Reason explains a false Forwarded in one human-readable sentence, e.g.
	// that the host port is already in use by another process. Empty when the
	// port is forwarded.
	Reason string
}
