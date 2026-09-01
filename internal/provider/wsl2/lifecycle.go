package wsl2

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

// cleanupTimeout bounds the removal of a partially created distribution. It runs
// on a context detached from the caller's, because the usual reason cleanup is
// needed is that the caller's context was cancelled — and a Ctrl-C that leaves a
// half-imported distribution registered is exactly what PROP-7 forbids.
const cleanupTimeout = 2 * time.Minute

// dirPerm and filePerm keep everything avar writes on Windows private to the
// user. A distribution's virtual disk is its whole filesystem, including
// whatever the user's projects put there, and a snapshot is a copy of one.
const (
	dirPerm  = 0o700
	filePerm = 0o600
)

// EnsureMachine brings the distribution described by spec into a running state,
// creating it first if it does not exist.
//
// The three cases are the contract's, and the middle one is where WSL differs
// from a virtual-machine backend: a registered distribution has no separate
// "start" step, because WSL starts one on demand the first time something runs
// inside it. Ensuring a stopped distribution is therefore running is a matter of
// running something trivial in it, which also proves it can be entered at all.
//
// The warm path — already running — does no work beyond the listing that
// established that, emits no progress, and in particular re-verifies nothing.
// That is the latency budget every Windows invocation is measured against
// (REQ-17.1).
func (p *Provider) EnsureMachine(ctx context.Context, spec provider.MachineSpec, progress types.ProgressSink) error {
	if err := p.gate(ctx, spec.Name, ownershipPrefix); err != nil {
		return err
	}
	if spec.Provider != "" && spec.Provider != p.ID() {
		return fmt.Errorf("environment %s is addressed to the %s backend, and this is %s", spec.Name, spec.Provider, p.ID())
	}
	if progress == nil {
		progress = types.DiscardProgress
	}
	// Refuse an impossible environment before anything is downloaded (REQ-18.6).
	entry, err := p.checkSupported(spec.Selector)
	if err != nil {
		return err
	}

	existing, found, err := p.view().lookup(ctx, spec.Name)
	if err != nil {
		return err
	}

	switch {
	case found && existing.WSLVersion == 1:
		// Never converted, never used, and never silently repaired: the
		// conversion is minutes of filesystem rewriting and the user's call
		// (REQ-18.4, PROP-15).
		return newWSL1Error(spec.Name)
	case found && existing.Running:
		return nil
	case found:
		progress.Progress(types.ProgressEvent{Kind: types.ProgressStarting, Machine: spec.Name})
		return p.start(ctx, spec.Name)
	default:
		return p.create(ctx, spec, entry, progress)
	}
}

// start brings a registered distribution up by running something trivial in it.
func (p *Provider) start(ctx context.Context, machine string) error {
	if _, err := p.run(ctx, "--distribution", machine, "--user", "root", "--exec", "/bin/true"); err != nil {
		return fmt.Errorf("starting environment %s: %w", machine, err)
	}
	p.forget()
	return nil
}

// create installs, provisions and verifies a new distribution, and leaves
// nothing behind if any of that fails.
//
// The cleanup on the failure path is not politeness. A distribution that was
// imported but not provisioned has no avar account, no marker and the Windows
// drives mounted; if it survived, the next invocation would find a registered
// distribution with avar's name and, having no record of the failure, could
// treat it as ready. Removing it means the next invocation finds either a
// working environment or nothing at all (REQ-1.6, REQ-18.12, PROP-7).
func (p *Provider) create(ctx context.Context, spec provider.MachineSpec, entry registryEntry, progress types.ProgressSink) error {
	if err := os.MkdirAll(p.distrosDir, dirPerm); err != nil {
		return fmt.Errorf("creating avar's environment directory %s: %w", p.distrosDir, err)
	}
	dir := p.installDir(spec.Name)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("creating the directory for environment %s at %s: %w", spec.Name, dir, err)
	}

	progress.Progress(types.ProgressEvent{
		Kind:    types.ProgressCreating,
		Machine: spec.Name,
		Message: fmt.Sprintf("%s %s · %s", spec.Selector.Distro, spec.Selector.Version, p.hostArch),
	})

	if err := p.install(ctx, spec.Name, entry, progress); err != nil {
		p.cleanupFailedCreate(spec.Name)
		return err
	}
	if err := p.provision(ctx, spec.Name, spec.Selector, entry); err != nil {
		p.cleanupFailedCreate(spec.Name)
		return err
	}
	return nil
}

// install registers the distribution under avar's name, in avar's directory,
// without launching it.
//
// The output goes to two places at once. The progress sink carries it live,
// because downloading a root filesystem is minutes of silence otherwise; the log
// file keeps it after the invocation ends, because the output of a failed
// install is the only evidence of why it failed and it scrolls away (design §6).
func (p *Provider) install(ctx context.Context, machine string, entry registryEntry, progress types.ProgressSink) error {
	logPath, logFile, err := p.createLog(machine)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()

	// The decoder is passed rather than assumed: wsl.exe writes UTF-16, so a
	// line forwarded raw reaches the user as NUL-separated letters.
	lines := &provider.ProgressWriter{Machine: machine, Sink: progress, Decode: deps.DecodeWSLOutput}
	defer lines.Flush()

	base := []string{
		"--install", entry.Distro,
		"--name", machine,
		"--location", p.installDir(machine),
		"--no-launch",
	}
	out := io.MultiWriter(logFile, lines)

	err = p.installWithRetries(ctx, machine, base, out)

	// Whether it worked or not, WSL may now have a registration it did not have
	// before, so the picture avar holds of what exists is stale either way.
	p.forget()
	if err != nil {
		return fmt.Errorf("installing environment %s from WSL's %q distribution: %w; the full output is in %s", machine, entry.Distro, err, logPath)
	}
	return nil
}

// installAttempts and installRetryPause bound how hard avar tries.
//
// Three is enough for the failure this exists for and few enough that a machine
// with no network at all still fails promptly rather than after a minute of
// hopeful waiting. The pause is there so a retry is not simply the same instant
// re-tried.
const (
	installAttempts   = 3
	installRetryPause = 2 * time.Second
)

// installWithRetries runs the install, retrying a failure that left nothing
// behind.
//
// The reason is measured rather than defensive. `wsl --install` fetches a
// distribution list from raw.githubusercontent.com before it downloads anything,
// and on a corporate network that request is intermittently reset: on the host
// this backend was first provisioned against, the same URL failed once and
// succeeded on the next attempt, seconds apart. Failing the whole invocation on
// the first reset would make avar unusable on exactly the networks it is meant
// for, and would tell the user their distribution does not exist.
//
// --web-download is on every attempt, including the first, and that is a
// separate decision from the retry. It asks WSL to fetch the image directly
// rather than through the Store, which is what makes provisioning work at all on
// a machine that has no Store — every Windows Server install, and any machine
// whose policy disables it. That condition is deterministic and known in
// advance, unlike the reset, so making the first attempt the one that discovers
// it would mean every such machine pays a guaranteed failure plus
// installRetryPause, and is told "the download did not start; retrying" about
// something that will never start.
//
// Putting the flag only on the later attempts would not buy a shorter retry
// either: the reset happens while fetching the *distribution list*, which both
// channels do, so it is the second attempt that fixes it and not the flag.
//
// A retry only happens when nothing was registered. A failure *after* the
// distribution appears is not transient, and retrying it would fail with
// ERROR_ALREADY_EXISTS and bury the real cause under a second one.
func (p *Provider) installWithRetries(ctx context.Context, machine string, base []string, out io.Writer) error {
	args := append(append([]string(nil), base...), "--web-download")

	var err error
	for attempt := 1; attempt <= installAttempts; attempt++ {
		if err = p.runner.Stream(ctx, out, p.wsl, args...); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}

		p.forget()
		if _, registered, lookupErr := p.view().lookup(ctx, machine); lookupErr == nil && registered {
			return err
		}
		if attempt < installAttempts {
			fmt.Fprintf(out, "avar: the download did not start; retrying (%d of %d)\n", attempt+1, installAttempts)
			select {
			case <-ctx.Done():
				return err
			case <-time.After(installRetryPause):
			}
		}
	}
	return err
}

// provision configures the guest, restarts it so the configuration takes
// effect, and verifies the result.
func (p *Provider) provision(ctx context.Context, machine string, sel types.EnvironmentSelector, entry registryEntry) error {
	script, err := p.buildProvisionScript(machine, sel, entry)
	if err != nil {
		return err
	}
	if _, err := p.run(ctx, guestShellArgv(machine, script)...); err != nil {
		return fmt.Errorf("setting up environment %s: %w", machine, err)
	}

	// /etc/wsl.conf is read when a distribution starts, and this one has been
	// running since the moment the script ran. Terminating it — this one, never
	// `wsl --shutdown` — is what makes the mount and interop policy real rather
	// than merely written down.
	if err := p.terminate(ctx, machine); err != nil {
		return err
	}

	facts, err := p.readGuestFacts(ctx, machine)
	if err != nil {
		return fmt.Errorf("checking environment %s after setting it up: %w", machine, err)
	}
	if err := facts.check(machine, sel, entry, p.guestUser); err != nil {
		return err
	}

	// The version check comes last because it needs the distribution running,
	// which the read above has just ensured.
	v := p.view()
	d, err := v.require(ctx, machine)
	if err != nil {
		return err
	}
	if d.WSLVersion != 2 {
		return newWSL1Error(machine)
	}
	return nil
}

// readGuestFacts runs the verification script and parses what it reported.
func (p *Provider) readGuestFacts(ctx context.Context, machine string) (guestFacts, error) {
	script := fmt.Sprintf(verifyScript, markerPath, p.guestUser, GuestProjectRoot)
	out, err := p.run(ctx, guestShellArgv(machine, script)...)
	if err != nil {
		return guestFacts{}, err
	}
	return parseGuestFacts(out), nil
}

// cleanupFailedCreate removes whatever a failed create left registered.
//
// It runs on its own context: the common reason a create fails is that the
// caller's was cancelled, and cleanup that inherited the cancellation would do
// nothing at all.
func (p *Provider) cleanupFailedCreate(machine string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_ = p.Delete(ctx, machine)
}

// Stop terminates the distribution, releasing the memory its processes hold.
//
// It terminates this distribution and never the WSL virtual machine as a whole.
// `wsl --shutdown` would be one command instead of one per environment, and it
// would also kill every distribution the user has open — the Ubuntu they were
// working in, a database in a third — which is not something `avr stop` is
// allowed to do (REQ-5.2, REQ-18.7).
func (p *Provider) Stop(ctx context.Context, machine string, progress types.ProgressSink) error {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return err
	}
	if progress == nil {
		progress = types.DiscardProgress
	}

	v := p.view()
	d, err := v.require(ctx, machine)
	if err != nil {
		return err
	}
	if !d.Running {
		// Converging on a state rather than performing an action: a stopped
		// environment is already where Stop is trying to get it.
		return nil
	}

	progress.Progress(types.ProgressEvent{Kind: types.ProgressStopping, Machine: machine})
	return p.terminate(ctx, machine)
}

// terminate runs the one-distribution stop.
func (p *Provider) terminate(ctx context.Context, machine string) error {
	if _, err := p.run(ctx, "--terminate", machine); err != nil {
		return fmt.Errorf("stopping environment %s: %w", machine, err)
	}
	p.forget()
	return nil
}

// Delete unregisters the distribution and removes its disk.
//
// Host project files are untouched: a project is shared into the guest, never
// copied into it, so deleting an environment cannot lose a user's work
// (REQ-10.3, PROP-10, PROP-16).
//
// It is fully idempotent, because it is also how avar cleans up after a failed
// create and after a crash, where the caller cannot know what survived. The
// directory is removed only after the unregister succeeds: removing it first
// would leave WSL with a registration pointing at nothing, which is harder to
// recover from than either end state.
func (p *Provider) Delete(ctx context.Context, machine string) error {
	if err := p.gate(ctx, machine, ownershipPrefix); err != nil {
		return err
	}

	v := p.view()
	_, found, err := v.lookup(ctx, machine)
	if err != nil {
		return err
	}
	if found {
		if _, err := p.run(ctx, "--unregister", machine); err != nil {
			return fmt.Errorf("removing environment %s: %w", machine, err)
		}
		p.forget()
	}

	dir := p.installDir(machine)
	if err := os.RemoveAll(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing the files of environment %s at %s: %w", machine, dir, err)
	}
	return nil
}

// Status reports one entry per environment avar owns, ordered by name.
//
// It filters rather than annotates. A distribution the user installed
// themselves — one whose name is not avar's, or which is absent from avar's own
// records — does not appear at all, because `avr status` and `avr stop --all`
// are built directly on this and must never reach a distribution avar did not
// create (REQ-5.1, REQ-5.4, REQ-18.7, PROP-6).
func (p *Provider) Status(ctx context.Context) ([]types.MachineStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("listing avar's environments: %w", err)
	}

	distros, err := p.view().all(ctx)
	if err != nil {
		return nil, err
	}

	owned, err := p.ownedRecords()
	if err != nil {
		return nil, err
	}

	out := make([]types.MachineStatus, 0, len(distros))
	for _, d := range distros {
		if types.ValidateMachineName(d.Name) != nil {
			continue
		}
		record, ok := owned[d.Name]
		if !ok {
			continue
		}
		out = append(out, types.MachineStatus{
			Name:     d.Name,
			Provider: p.ID(),
			Selector: record.Selector,
			Kind:     record.Kind,
			State:    d.state(),
			Mounts:   record.Mounts,
			Runtime:  fmt.Sprintf("wsl%d", d.WSLVersion),
			DiskUsed: p.diskUsedGB(d.Name),
		})
	}
	return out, nil
}

// ownedRecords indexes avar's records by machine name.
//
// A nil Records means the caller is the reconciler, which is still deciding what
// avar owns and must see prefix-matching distributions that have no record yet.
// It is the one caller allowed that view, and it does not go through Status.
func (p *Provider) ownedRecords() (map[string]types.MachineRecord, error) {
	if p.records == nil {
		return nil, nil
	}
	records, err := p.records.Machines()
	if err != nil {
		return nil, fmt.Errorf("reading avar's environment records: %w", err)
	}
	owned := make(map[string]types.MachineRecord, len(records))
	for _, rec := range records {
		if rec.Provider == "" || rec.Provider == p.ID() {
			owned[rec.Name] = rec
		}
	}
	return owned, nil
}

// diskUsedGB reports how much disk a distribution's virtual disk occupies.
//
// It is a file size rather than a query to WSL, because WSL has no command that
// reports one, and the answer is right there: a distribution is one ext4.vhdx in
// the directory avar imported it into. A file avar cannot stat reports zero
// rather than failing, since disk usage is a status line and not a reason to
// refuse to print one.
func (p *Provider) diskUsedGB(machine string) float64 {
	var total int64
	entries, err := os.ReadDir(p.installDir(machine))
	if err != nil {
		return 0
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.IsDir() {
			continue
		}
		total += info.Size()
	}
	return float64(total) / (1 << 30)
}

// createLog opens the install log for a machine. Its path is named in the error
// a failed install returns, so the user has somewhere to look (design §6).
func (p *Provider) createLog(machine string) (string, *os.File, error) {
	if err := os.MkdirAll(p.logsDir, dirPerm); err != nil {
		return "", nil, fmt.Errorf("creating avar's log directory %s: %w", p.logsDir, err)
	}
	path := filepath.Join(p.logsDir, machine+"-create.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm)
	if err != nil {
		return "", nil, fmt.Errorf("opening the install log %s: %w", path, err)
	}
	return path, file, nil
}

// newWSL1Error reports an environment registered as WSL 1.
//
// The message is deps.WSL1Error's, because the remedy for a prerequisite belongs
// with the other remedies for the same prerequisite and must not be written
// twice. The error identifies as two things at once, and both are true: it is
// the WSL 1 condition, which the reconciler matches to leave the registration
// alone, and it is an unsupported capability, which is how the command layer
// already reports "this backend cannot do that with this environment" without
// knowing which backend answered (design §3.0, REQ-18.4, PROP-15).
func newWSL1Error(machine string) error { return &wsl1Error{machine: machine} }

type wsl1Error struct{ machine string }

func (e *wsl1Error) Error() string {
	return (&deps.WSL1Error{Distribution: e.machine}).Error()
}

func (e *wsl1Error) Unwrap() []error {
	return []error{deps.ErrWSL1, provider.ErrUnsupportedCapability}
}
