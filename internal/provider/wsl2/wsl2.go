// Package wsl2 implements avar's Provider by driving Windows Subsystem for
// Linux.
//
// It is the only place in avar that knows WSL exists. Everything above the
// Provider interface speaks of machines, selectors and mounts; this package
// translates those into `wsl.exe` invocations and translates its output back
// (REQ-17.3, REQ-18.14, design §3.6).
//
// Four properties of that translation are deliberate, and three of them are
// where this backend differs from the Lima one rather than merely restating it.
//
// A machine is a WSL distribution avar imported. avar never adopts, starts,
// stops, exports or unregisters a distribution the user installed themselves:
// ownership is the reserved avr- name prefix and avar's own record together,
// checked before anything is done, and Status filters rather than annotates, so
// a user's Ubuntu is invisible to avar rather than listed and skipped (REQ-18.7,
// PROP-6). This is a stronger promise here than on macOS, because a Windows
// developer's WSL is far more likely to be somewhere they already live.
//
// No shell is ever involved on the host side. Every invocation is an argv passed
// to deps.Runner, so a path containing a space or a quote is an argument and can
// never become syntax. Where a guest shell is genuinely needed — provisioning is
// a sequence of file writes that no argv can express — the script travels
// base64-encoded, as a single token with no space, quote or newline in it, so
// nothing between here and the guest can reinterpret it.
//
// Terminating means terminating one distribution. `wsl --shutdown` stops the
// whole WSL virtual machine and with it every distribution the user has open,
// which would make `avr stop` throw away somebody's unrelated work. avar uses
// `wsl --terminate <name>` and never the global form (REQ-18.7).
//
// avar's own state directory holds the distributions it imports, so removing an
// environment reclaims its disk and removing avar removes them all.
package wsl2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/olamide226/avar/internal/deps"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// guestUserFallback is the Linux account name avar uses when the Windows
// account name has nothing usable in it — an account named entirely in a
// non-Latin script, for instance. It is not a failure case: the name only has to
// be a valid Linux user that is not root (REQ-1.4).
const guestUserFallback = "avar"

// guestUserMaxLen is the conventional limit useradd enforces.
const guestUserMaxLen = 32

// unsafeUserChars is everything a portable Linux user name may not contain.
var unsafeUserChars = regexp.MustCompile(`[^a-z0-9_-]`)

// Records is avar's own registry of the machines it created.
//
// Ownership is two conditions, not one: the avr- prefix says a name is shaped
// like avar's, and the record says avar actually made it (REQ-5.4, REQ-18.7,
// PROP-6). It is an interface rather than a *state.Store so that the provider
// depends on the two questions it needs answered instead of on the state
// package, which also keeps the ownership rule testable without a state
// directory on disk.
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
	// WSL is the verified WSL installation from internal/deps. Its Path is the
	// executable avar runs, so the binary that passed the version gate is the
	// binary that gets driven.
	WSL deps.WSL

	// Runner executes wsl.exe. It is required: avar has one subprocess
	// abstraction and this package uses it rather than reaching for os/exec
	// itself.
	Runner deps.Runner

	// Records is avar's machine registry, used for the second half of the
	// ownership check. A nil Records reduces ownership to the name prefix
	// alone, which is what the reconciler needs while it is still deciding
	// which distributions to adopt; production wiring passes the state store.
	Records Records

	// DistrosDir is where imported distributions are stored, normally
	// <State_Dir>\distros. It is required: a distribution imported outside
	// avar's own directory could not be removed with confidence, and its disk
	// could not be reclaimed by removing avar.
	DistrosDir string

	// LogsDir is where provisioning logs are written, normally
	// <State_Dir>\logs. Provisioning failures name the log file, so this is
	// required.
	LogsDir string

	// SnapshotsDir is where captured environment states are kept, normally
	// <State_Dir>\snapshots. It is required: a snapshot is a copy of a whole
	// disk, and one written outside avar's own state directory could not be
	// reclaimed by removing the environment it belongs to.
	SnapshotsDir string

	// GuestUser overrides the Linux account avar creates in each distribution.
	// Empty means one derived from the Windows account name, which is what
	// production uses; tests pin it so a generated provisioning script is the
	// same on every developer's machine.
	GuestUser string

	// HostArch pins the host architecture. A zero value is probed. Tests pin
	// it so that the architecture refusal can be exercised from either side.
	HostArch types.Arch
}

// Provider drives WSL 2 through wsl.exe.
//
// It holds no mutable state: what WSL knows is read fresh per call (see view),
// so two concurrent avr invocations cannot disagree because one of them cached.
type Provider struct {
	wsl          string
	runner       deps.Runner
	records      Records
	distrosDir   string
	logsDir      string
	snapshotsDir string
	guestUser    string
	hostArch     types.Arch

	// shared is this invocation's picture of what WSL has. It is built on
	// first use and dropped by forget() whenever avar changes something, so
	// one invocation asks WSL its three listing questions once rather than
	// once per operation (REQ-17.1). A Provider is per-invocation, so this
	// never outlives the process.
	//
	// It is not guarded by a mutex because a Provider serves one invocation on
	// one goroutine: cmd.App builds it behind a sync.Once and every command
	// drives it in sequence.
	shared *view
}

// New returns a Provider driving the given WSL installation.
func New(opts Options) (*Provider, error) {
	switch {
	case opts.WSL.Path == "":
		return nil, errors.New("creating the WSL backend: no wsl.exe path was given; obtain one from deps.EnsureWSL")
	case opts.Runner == nil:
		return nil, errors.New("creating the WSL backend: no command runner was given")
	case opts.DistrosDir == "":
		return nil, errors.New("creating the WSL backend: no directory was given for imported distributions; avar stores them under its own state directory so that removing an environment reclaims its disk")
	case opts.LogsDir == "":
		return nil, errors.New("creating the WSL backend: no log directory was given; provisioning failures must be able to name a log file")
	case opts.SnapshotsDir == "":
		return nil, errors.New("creating the WSL backend: no snapshot directory was given; avar keeps captured environment states under its own state directory")
	}

	guestUser := opts.GuestUser
	if guestUser == "" {
		guestUser = GuestUserName(windowsAccountName())
	}
	if err := validateGuestUser(guestUser); err != nil {
		return nil, fmt.Errorf("creating the WSL backend: %w", err)
	}

	hostArch := opts.HostArch
	if hostArch == "" {
		hostArch = types.HostArch()
	}

	return &Provider{
		wsl:          opts.WSL.Path,
		runner:       opts.Runner,
		records:      opts.Records,
		distrosDir:   opts.DistrosDir,
		logsDir:      opts.LogsDir,
		snapshotsDir: opts.SnapshotsDir,
		guestUser:    guestUser,
		hostArch:     hostArch,
	}, nil
}

// ID reports which backend this is.
func (p *Provider) ID() types.ProviderID { return types.ProviderWSL2 }

// GuestUserName turns a Windows account name into a Linux user name.
//
// The two namespaces do not agree: a Windows account can be "Ola Adebayo" or
// "CONTOSO\ola.adebayo" or written in a script with no Latin characters at all,
// and none of those is a portable Linux user name. The mapping lower-cases,
// keeps the domain-less part, replaces everything a Linux name may not contain,
// and falls back to a fixed name when nothing usable survives.
//
// The result does not have to equal the Windows name and cannot always: what
// REQ-1.4 asks for is a non-root account that belongs to the person at the
// keyboard, not a name that round-trips.
func GuestUserName(windows string) string {
	name := windows
	// A domain-qualified account is DOMAIN\user; the user half is the name.
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToLower(strings.TrimSpace(name))
	name = unsafeUserChars.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-_")
	if len(name) > guestUserMaxLen {
		name = strings.Trim(name[:guestUserMaxLen], "-_")
	}
	// A Linux user name may not begin with a digit or a dash, and may not be
	// root — avar's whole promise here is a non-root account (REQ-1.4).
	if name == "" || name == "root" || !isUserNameStart(name[0]) {
		return guestUserFallback
	}
	return name
}

func isUserNameStart(c byte) bool { return c >= 'a' && c <= 'z' || c == '_' }

// validateGuestUser rejects a name avar must not create an account for.
func validateGuestUser(name string) error {
	if name == "" {
		return errors.New("the guest account name is empty")
	}
	if name == "root" {
		return errors.New("the guest account may not be root: avar gives the user a non-root account with passwordless sudo")
	}
	if unsafeUserChars.MatchString(name) || !isUserNameStart(name[0]) || len(name) > guestUserMaxLen {
		return fmt.Errorf("%q is not a usable Linux account name", name)
	}
	return nil
}

// windowsAccountName reports the signed-in account, which is only ever a
// starting point for GuestUserName.
func windowsAccountName() string {
	if name := os.Getenv("USERNAME"); name != "" {
		return name
	}
	return guestUserFallback
}

// ownership says how thoroughly an operation checks that avar owns a machine.
type ownership int

const (
	// ownershipPrefix checks the name only. It is what the operations that
	// create a machine, or clean one up before its record exists, can check.
	ownershipPrefix ownership = iota
	// ownershipRecord additionally requires the machine to be in avar's own
	// registry, which is what every operation on an existing machine uses.
	ownershipRecord
)

// gate refuses to act on anything avar does not own.
//
// It runs before any subprocess, so a name avar does not own produces an error
// and no side effect whatsoever — the property that keeps a developer's own
// Ubuntu distribution untouched no matter what avar is asked to do (REQ-18.7,
// PROP-6).
func (p *Provider) gate(ctx context.Context, machine string, mode ownership) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("operating on environment %s: %w", machine, err)
	}
	if types.ValidateMachineName(machine) != nil {
		return fmt.Errorf("%w: %s; avar only manages environments it created", provider.ErrNotOwned, machine)
	}
	if mode == ownershipRecord && p.records != nil {
		_, ok, err := p.records.Machine(machine)
		if err != nil {
			return fmt.Errorf("checking whether avar owns environment %s: %w", machine, err)
		}
		if !ok {
			return fmt.Errorf("%w: %s is not in avar's records; avar will not touch a WSL distribution it did not create", provider.ErrNotOwned, machine)
		}
	}
	return nil
}

// run executes wsl.exe with an argv and returns its decoded standard output.
func (p *Provider) run(ctx context.Context, args ...string) (string, error) {
	out, err := p.runner.Output(ctx, p.wsl, args...)
	return deps.DecodeWSLOutput(out), err
}

// installDir is where a distribution's virtual disk lives.
func (p *Provider) installDir(machine string) string {
	return filepath.Join(p.distrosDir, machine)
}

// What this backend is, stated where the compiler checks it.
//
// The core contract is the claim the whole Provider boundary was built for: a
// second backend satisfying it without a line of command-layer code changing
// (REQ-17.3, REQ-18.14). If it stops compiling, that claim has stopped being
// true.
//
// The three optional ones are claims about what WSL can do, and they are all
// yes: it can export and import a distribution's disk, VS Code can attach to a
// distribution, and a guest port's reachability can be probed. A backend that
// could not do one of these would say so by leaving the assertion out rather
// than by stubbing the methods (design §3.0).
var (
	_ provider.Provider             = (*Provider)(nil)
	_ provider.Snapshotter          = (*Provider)(nil)
	_ provider.EditorTargetProvider = (*Provider)(nil)
	_ provider.PortDiagnoser        = (*Provider)(nil)
)
