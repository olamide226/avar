// Package deps makes avar's Lima dependency invisible.
//
// avar drives Lima as a subprocess, so `limactl` is a hard requirement. This
// package locates it, gates on MinLimaVersion, and — only with the user's
// explicit consent — installs Lima with Homebrew. A user who has never heard of
// Lima should never have to read a README to get past a missing dependency.
//
// Nothing here builds a command line for a shell to interpret. Every subprocess
// is executed as an argv, so no path, version string, or tool output can turn
// into shell syntax.
package deps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// limactlBinary is the Lima CLI avar drives.
const limactlBinary = "limactl"

// homebrewBinDirs are the standard Homebrew binary directories, searched when
// limactl is not on PATH. A terminal or editor launched from the macOS GUI can
// inherit a PATH without them, and "Lima is installed but avar cannot see it"
// is not an acceptable answer to the user.
var homebrewBinDirs = []string{
	"/opt/homebrew/bin", // Apple silicon
	"/usr/local/bin",    // Intel
}

// installQuestion is the consent avar must have before installing anything.
const installQuestion = "Lima is not installed. avar needs it to run Linux environments.\nInstall it now with `brew install lima`?"

// ErrLimaNotFound reports that no limactl executable could be located.
var ErrLimaNotFound = errors.New("limactl not found on PATH or in the Homebrew binary directories")

// Lima describes a usable Lima installation.
type Lima struct {
	// Path is the absolute path to the limactl executable that was verified.
	// Callers execute this path rather than resolving "limactl" again, so the
	// binary avar checked is the binary avar runs.
	Path string
	// Version is what `limactl --version` reported.
	Version Version
}

// Runner executes an external program.
//
// Implementations receive a program path and an argv — never a command string —
// which is what keeps a shell out of avar's subprocess handling. Both methods
// must honour ctx by terminating the child rather than leaking it.
type Runner interface {
	// Output runs the program and returns its standard output.
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
	// Stream runs the program, copying its output to w as it arrives so a
	// long install shows progress instead of looking like a hang.
	Stream(ctx context.Context, w io.Writer, name string, args ...string) error
}

// Manager answers one question: is there a Lima that avar can use, and if not,
// what happens next.
//
// Every collaborator that touches the outside world is a field, so the whole
// component is testable without a subprocess, a terminal, or Homebrew. A zero
// Manager uses the real ones.
type Manager struct {
	// Runner executes limactl and brew. Defaults to a real exec runner.
	Runner Runner
	// Confirm asks the user a yes/no question. Defaults to a prompt on
	// stdin/Out. It must return false when the answer is no.
	Confirm func(ctx context.Context, question string) (bool, error)
	// Interactive reports whether there is a user to ask. Defaults to
	// "stdin is a terminal".
	Interactive func() bool
	// LookPath resolves an executable name. Defaults to exec.LookPath.
	LookPath func(file string) (string, error)
	// FallbackDirs are searched for limactl when LookPath fails. Defaults to
	// the Homebrew binary directories.
	FallbackDirs []string
	// Out receives the install prompt's progress and the installer's own
	// output. Defaults to io.Discard.
	Out io.Writer
}

// EnsureLima verifies avar's Lima dependency using the real PATH, the real
// filesystem, and the real terminal, streaming any install output to w.
func EnsureLima(ctx context.Context, w io.Writer) (Lima, error) {
	return (&Manager{Out: w}).EnsureLima(ctx)
}

// EnsureLima returns a Lima installation that avar can operate against.
//
// A compatible Lima is used as-is, silently. A missing Lima produces an offer
// to install it with Homebrew, which proceeds only after the user says yes. An
// unsupported version is refused outright: avar never auto-upgrades and never
// operates against a Lima below MinLimaVersion.
func (m *Manager) EnsureLima(ctx context.Context) (Lima, error) {
	if err := ctx.Err(); err != nil {
		return Lima{}, fmt.Errorf("checking avar's Lima dependency: %w", err)
	}

	lima, err := m.detect(ctx)
	switch {
	case err == nil:
		return m.gate(lima)
	case errors.Is(err, ErrLimaNotFound):
		return m.offerInstall(ctx)
	default:
		return Lima{}, err
	}
}

// gate accepts a detected Lima only if it meets the minimum version.
func (m *Manager) gate(lima Lima) (Lima, error) {
	if lima.Version.AtLeast(minLima) {
		return lima, nil
	}
	return Lima{}, &VersionTooOldError{Found: lima.Version, Minimum: minLima, Path: lima.Path}
}

// detect locates limactl and reads its version.
func (m *Manager) detect(ctx context.Context) (Lima, error) {
	path, err := m.findLimactl()
	if err != nil {
		return Lima{}, err
	}

	out, err := m.runner().Output(ctx, path, "--version")
	if err != nil {
		return Lima{}, fmt.Errorf("checking the installed Lima version with %s --version: %w; "+
			"if that command does not work by hand either, reinstall Lima with `brew reinstall lima`", path, err)
	}

	version, err := ParseVersion(string(out))
	if err != nil {
		return Lima{}, fmt.Errorf("reading the installed Lima version from %s --version: %w; "+
			"avar needs Lima %s or newer and will not guess", path, err, MinLimaVersion)
	}

	return Lima{Path: path, Version: version}, nil
}

// findLimactl resolves the limactl executable, falling back to the Homebrew
// binary directories when PATH does not name them.
func (m *Manager) findLimactl() (string, error) {
	if path, err := m.lookPath()(limactlBinary); err == nil {
		return path, nil
	}
	for _, dir := range m.fallbackDirs() {
		candidate := filepath.Join(dir, limactlBinary)
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	return "", ErrLimaNotFound
}

// offerInstall runs the missing-Lima flow: offer, confirm, install, re-verify.
func (m *Manager) offerInstall(ctx context.Context) (Lima, error) {
	brew, err := m.lookPath()("brew")
	if err != nil {
		return Lima{}, &NotInstalledError{Reason: ReasonNoHomebrew}
	}

	// A script, a CI job, or a hook has nobody to answer the prompt. Waiting
	// would hang it and installing anyway would be an unconsented change to
	// the machine, so the default is to decline and say what to run.
	if !m.interactive()() {
		return Lima{}, &NotInstalledError{Reason: ReasonNonInteractive}
	}

	confirmed, err := m.confirm()(ctx, installQuestion)
	if err != nil {
		return Lima{}, fmt.Errorf("asking whether to install Lima with Homebrew: %w", err)
	}
	if !confirmed {
		return Lima{}, &NotInstalledError{Reason: ReasonDeclined}
	}
	if err := ctx.Err(); err != nil {
		return Lima{}, fmt.Errorf("installing Lima with Homebrew: %w", err)
	}

	fmt.Fprintln(m.out(), "Installing Lima with Homebrew. This takes a minute.")
	if err := m.runner().Stream(ctx, m.out(), brew, "install", "lima"); err != nil {
		return Lima{}, &NotInstalledError{Reason: ReasonInstallFailed, Err: err}
	}

	// Homebrew exiting zero is not proof that avar can drive Lima: the formula
	// may have installed a version below the minimum, or into a directory avar
	// does not search. Re-run the full check rather than assume.
	lima, err := m.detect(ctx)
	if err != nil {
		return Lima{}, &NotInstalledError{Reason: ReasonInstallIncomplete, Err: err}
	}
	return m.gate(lima)
}

// Collaborator accessors: a zero Manager works, and a test can replace any one
// of these without supplying the others.

func (m *Manager) runner() Runner {
	if m.Runner != nil {
		return m.Runner
	}
	return execRunner{}
}

func (m *Manager) confirm() func(context.Context, string) (bool, error) {
	if m.Confirm != nil {
		return m.Confirm
	}
	return func(ctx context.Context, question string) (bool, error) {
		return promptYesNo(ctx, os.Stdin, m.out(), question)
	}
}

func (m *Manager) interactive() func() bool {
	if m.Interactive != nil {
		return m.Interactive
	}
	return stdinIsTerminal
}

func (m *Manager) lookPath() func(string) (string, error) {
	if m.LookPath != nil {
		return m.LookPath
	}
	return exec.LookPath
}

func (m *Manager) fallbackDirs() []string {
	if m.FallbackDirs != nil {
		return m.FallbackDirs
	}
	return homebrewBinDirs
}

func (m *Manager) out() io.Writer {
	if m.Out != nil {
		return m.Out
	}
	return io.Discard
}

// NotInstalledReason says why avar has no usable Lima and did not install one.
type NotInstalledReason string

const (
	// ReasonDeclined: the user was asked and said no.
	ReasonDeclined NotInstalledReason = "declined"
	// ReasonNoHomebrew: Homebrew is not available to install with.
	ReasonNoHomebrew NotInstalledReason = "no-homebrew"
	// ReasonNonInteractive: there was nobody to ask for consent.
	ReasonNonInteractive NotInstalledReason = "non-interactive"
	// ReasonInstallFailed: `brew install lima` returned an error.
	ReasonInstallFailed NotInstalledReason = "install-failed"
	// ReasonInstallIncomplete: the install reported success but avar still
	// cannot find or read a Lima installation.
	ReasonInstallIncomplete NotInstalledReason = "install-incomplete"
)

// NotInstalledError reports that avar cannot proceed because Lima is not
// installed. Its message carries the manual installation instructions, because
// this error is the last thing the user sees before avar exits non-zero.
type NotInstalledError struct {
	Reason NotInstalledReason
	// Err is the underlying failure, when the reason is an install that did
	// not work out.
	Err error
}

func (e *NotInstalledError) Error() string {
	var headline string
	switch e.Reason {
	case ReasonDeclined:
		headline = "Lima is required and was not installed."
	case ReasonNoHomebrew:
		headline = "Lima is not installed, and Homebrew is not available to install it with."
	case ReasonNonInteractive:
		headline = "Lima is not installed, and avar is not running on a terminal, so it cannot ask for permission to install it."
	case ReasonInstallFailed:
		headline = fmt.Sprintf("installing Lima with Homebrew failed: %v", e.Err)
	case ReasonInstallIncomplete:
		headline = fmt.Sprintf("Homebrew finished but avar still cannot use Lima: %v", e.Err)
	default:
		headline = "Lima is not installed."
	}
	return headline + "\n\n" + manualInstructions
}

func (e *NotInstalledError) Unwrap() error { return e.Err }

// manualInstructions is the fallback avar prints when it will not or cannot
// install Lima itself. It names the exact command to run.
const manualInstructions = "Install Lima " + MinLimaVersion + " or newer, then run avr again:\n" +
	"\n" +
	"    brew install lima\n" +
	"\n" +
	"Without Homebrew, follow https://lima-vm.io/docs/installation/\n" +
	"(Homebrew itself: https://brew.sh)"

// VersionTooOldError reports a Lima installation below MinLimaVersion. avar
// refuses to operate against it rather than risk misreading limactl's output.
type VersionTooOldError struct {
	Found   Version
	Minimum Version
	// Path is the limactl that reported Found.
	Path string
}

func (e *VersionTooOldError) Error() string {
	return fmt.Sprintf("Lima %s at %s is too old: avar needs Lima %s or newer.\n"+
		"\n"+
		"Upgrade it, then run avr again:\n"+
		"\n"+
		"    brew upgrade lima\n"+
		"\n"+
		"Without Homebrew, follow https://lima-vm.io/docs/installation/\n"+
		"avar will not run against an unsupported Lima version.",
		e.Found, e.Path, e.Minimum)
}
