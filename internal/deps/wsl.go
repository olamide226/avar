// WSL 2 is avar's Windows backend. This file locates wsl.exe, gates it on
// MinWSLVersion, and offers to install or update it (REQ-18.2, REQ-18.3).
//
// Three things make this a different job from the Lima one rather than the same
// job with different strings.
//
// The tool is always present, and that proves nothing. wsl.exe ships in System32
// on every Windows 11 installation, whether or not the platform behind it is
// installed, enabled, or current. "Is the file there" is therefore not a usable
// question; what avar asks is whether the tool can report a version it supports,
// which only a working, current WSL can do.
//
// Setup may not finish inside the invocation that started it. Enabling the
// platform can require a restart, and no amount of retrying inside one avr run
// will change that. That outcome is a distinct answer — ErrRestartRequired —
// rather than a failure, because the user's next step is to reboot and run the
// same command again, and avar must not have half-created anything in the
// meantime (REQ-18.3, PROP-13).
//
// The tool speaks UTF-16. See DecodeWSLOutput.

package deps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// wslBinary is the WSL command-line tool avar drives.
const wslBinary = "wsl.exe"

// MinWSLVersion is the oldest WSL release avar supports. It is the single point
// of change for the version gate: nothing else in avar hard-codes a WSL version.
//
// The floor is 2.0.0 for the same reason Lima's is 2.0.0 — it is the oldest
// release avar has evidence for — but here the number also draws a real line.
// WSL is delivered two ways: as an optional Windows component, which is what a
// machine that has never run `wsl --update` has, and as a Store package, which
// Microsoft has shipped since late 2023 and which is the only channel carrying a
// `wsl --version` at all. Every flag the WSL2Provider is built on belongs to the
// Store releases: --cd for the guest working directory, --import --version 2 for
// creating an avar-owned distribution, and --export --vhd for a snapshot. A
// build that cannot report its own version cannot have them, and guessing would
// move the failure from a sentence avar can explain to a subprocess error the
// user has to decode.
const MinWSLVersion = "2.0.0"

// minWSL is MinWSLVersion parsed once. A malformed constant is a programming
// error caught by TestMinWSLVersion_Parses before it can ship.
var minWSL = mustParseVersion(MinWSLVersion)

// MinWSL returns the minimum supported WSL version.
func MinWSL() Version { return minWSL }

// systemDirs are searched for wsl.exe when PATH does not name it. A PATH can be
// trimmed by a shell profile or inherited oddly from a launcher, and "WSL is
// right there and avar cannot see it" is not an acceptable answer to the user.
//
// The Windows system directory is the only location: wsl.exe is an
// operating-system component, not something a user installs somewhere of their
// choosing. avar's Windows builds are 64-bit, so System32 is the native
// directory and no Sysnative redirection applies.
func systemDirs() []string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return []string{filepath.Join(root, "System32")}
}

// ErrWSLNotFound reports that no wsl.exe could be located at all.
var ErrWSLNotFound = errors.New("wsl.exe not found on PATH or in the Windows system directory")

// ErrRestartRequired reports that WSL setup got as far as it can without the
// machine being restarted.
//
// It is deliberately not an installation failure. The install succeeded; the
// platform is not usable until Windows has restarted, and the user's next step
// is to reboot and run the same avr command again. Callers match on this to say
// exactly that, rather than reporting that something went wrong (REQ-18.3).
var ErrRestartRequired = errors.New("WSL needs Windows to restart before it can be used")

// ErrWSL1 reports a distribution registered as WSL 1.
//
// avar never converts one: `wsl --set-version` rewrites the distribution's
// entire filesystem in place and takes minutes. Doing that on the user's behalf,
// as a side effect of them asking for a shell, is not a decision avar gets to
// make (REQ-18.4, PROP-15).
var ErrWSL1 = errors.New("distribution is registered as WSL 1")

// wslInstallQuestion is the consent avar must have before installing anything,
// and it states the cost before the user agrees rather than after: this may
// prompt for administrator approval and may need a restart (REQ-18.3).
const wslInstallQuestion = "WSL is not installed, or is too old for avar to use.\n" +
	"avar can install it with `wsl --install --no-distribution`, which installs the\n" +
	"platform only and creates no Linux distribution of its own.\n" +
	"Windows may ask you to approve the change, and may need to restart afterwards.\n" +
	"Install it now?"

// wslUpdateQuestion is the consent for the lighter of the two actions: the
// platform is present and merely behind. `wsl --update` replaces the Store
// package and neither touches nor restarts a distribution the user has.
const wslUpdateQuestion = "WSL is installed but older than avar supports.\n" +
	"avar can update it with `wsl --update`, which updates the WSL platform and\n" +
	"leaves your distributions untouched.\n" +
	"Update it now?"

// WSL describes a usable WSL installation.
type WSL struct {
	// Path is the absolute path to the wsl.exe that was verified. Callers
	// execute this path rather than resolving "wsl.exe" again, so the binary
	// avar checked is the binary avar drives.
	Path string
	// Version is what `wsl --version` reported.
	Version Version
}

// WSLManager answers one question: is there a WSL that avar can use, and if
// not, what happens next.
//
// Every collaborator that touches the outside world is a field, so the whole
// component is testable without a subprocess, a terminal, or a working WSL —
// which matters more here than it does for Lima, since the states worth testing
// (absent, disabled, too old, restart pending) are states a developer's own
// machine is in only one of, and cannot be moved between cheaply. A zero
// WSLManager uses the real ones.
//
// It is a separate type from Manager rather than a mode of it. The two share the
// rule "ask before changing the user's machine" and nothing else: different
// tool, different setup commands, different search path, and a restart outcome
// that has no counterpart in `brew install`. Folding them together would let a
// Lima test reach a WSL field, and would put a host branch inside a component
// whose whole purpose is that a Windows invocation never mentions Lima
// (PROP-13).
type WSLManager struct {
	// Runner executes wsl.exe. Defaults to a real exec runner.
	Runner Runner
	// Confirm asks the user a yes/no question. Defaults to a prompt on
	// stdin/Out. It must return false when the answer is no.
	Confirm func(ctx context.Context, question string) (bool, error)
	// Interactive reports whether there is a user to ask. Defaults to
	// "stdin is a terminal".
	Interactive func() bool
	// LookPath resolves an executable name. Defaults to exec.LookPath.
	LookPath func(file string) (string, error)
	// FallbackDirs are searched for wsl.exe when LookPath fails. Defaults to
	// the Windows system directory.
	FallbackDirs []string
	// Out receives the setup prompt's progress and the tool's own output.
	// Defaults to io.Discard.
	Out io.Writer
}

// EnsureWSL verifies avar's WSL dependency using the real PATH, the real
// filesystem, and the real terminal, streaming any setup output to w.
func EnsureWSL(ctx context.Context, w io.Writer) (WSL, error) {
	return (&WSLManager{Out: w}).EnsureWSL(ctx)
}

// EnsureWSL returns a WSL installation that avar can operate against.
//
// A compatible WSL is used as-is, silently — that is the warm path of every
// Windows invocation and it costs one subprocess. Anything else is a question
// put to the user before avar changes their machine: a WSL that cannot report a
// version offers an install, one below the minimum offers an update, and neither
// proceeds without an explicit yes.
//
// Nothing is registered here whatever the outcome. A refusal, a declined prompt,
// and a restart-pending install all leave the machine as they found it apart
// from the platform setup the user agreed to (REQ-18.3, PROP-13).
func (m *WSLManager) EnsureWSL(ctx context.Context) (WSL, error) {
	if err := ctx.Err(); err != nil {
		return WSL{}, fmt.Errorf("checking avar's WSL dependency: %w", err)
	}

	path, err := m.findWSL()
	if err != nil {
		return WSL{}, err
	}

	version, err := m.probeVersion(ctx, path)
	switch {
	case err != nil:
		// wsl.exe is there but cannot report a version avar can read. That is
		// the optional component with no Store package behind it, or the
		// platform disabled — either way an install, not an update.
		return m.offerSetup(ctx, path, wslInstallQuestion, "install", "--install", "--no-distribution")
	case version.AtLeast(minWSL):
		return WSL{Path: path, Version: version}, nil
	default:
		return m.offerSetup(ctx, path, wslUpdateQuestion, "update", "--update")
	}
}

// findWSL resolves the wsl.exe executable, falling back to the Windows system
// directory when PATH does not name it.
func (m *WSLManager) findWSL() (string, error) {
	if path, err := m.lookPath()(wslBinary); err == nil {
		return path, nil
	}
	for _, dir := range m.fallbackDirs() {
		candidate := filepath.Join(dir, wslBinary)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", &WSLNotInstalledError{Reason: WSLReasonNotFound, Err: ErrWSLNotFound}
}

// probeVersion asks wsl.exe what it is.
//
// A failure to run is not distinguished from output with no version in it, and
// that is deliberate. The reasons wsl.exe cannot report a version — the optional
// component disabled, the Store package never installed, a build predating
// --version — have the same remedy and the same next step, and telling them
// apart would mean matching on sentences that Windows localizes.
func (m *WSLManager) probeVersion(ctx context.Context, path string) (Version, error) {
	out, err := m.runner().Output(ctx, path, "--version")
	if err != nil {
		return Version{}, fmt.Errorf("asking %s for its version: %w", path, err)
	}
	version, err := ParseVersion(DecodeWSLOutput(out))
	if err != nil {
		return Version{}, fmt.Errorf("reading the WSL version from %s --version: %w", path, err)
	}
	return version, nil
}

// offerSetup runs the consent-then-act-then-reverify flow shared by installing
// WSL and updating it.
//
// The re-probe at the end is the point of the function. `wsl --install` exiting
// zero is not proof that avar can drive WSL: on a machine where the platform was
// disabled the change does not take effect until Windows restarts. Asking the
// tool again is how avar tells "set up and ready" from "set up, restart pending"
// without reading a localized sentence (REQ-18.3).
func (m *WSLManager) offerSetup(ctx context.Context, path, question, action string, args ...string) (WSL, error) {
	// A script, a CI job, or a hook has nobody to answer the prompt. Waiting
	// would hang it, and acting anyway would be an unconsented change to the
	// machine, so the default is to decline and say what to run.
	if !m.interactive()() {
		return WSL{}, &WSLNotInstalledError{Reason: WSLReasonNonInteractive}
	}

	confirmed, err := m.confirm()(ctx, question)
	if err != nil {
		return WSL{}, fmt.Errorf("asking whether to %s WSL: %w", action, err)
	}
	if !confirmed {
		return WSL{}, &WSLNotInstalledError{Reason: WSLReasonDeclined}
	}
	if err := ctx.Err(); err != nil {
		return WSL{}, fmt.Errorf("setting WSL up with wsl %s: %w", strings.Join(args, " "), err)
	}

	fmt.Fprintf(m.out(), "Running wsl %s. This can take a few minutes.\n", strings.Join(args, " "))
	if err := m.runner().Stream(ctx, m.out(), path, args...); err != nil {
		return WSL{}, &WSLNotInstalledError{Reason: WSLReasonSetupFailed, Action: action, Err: err}
	}

	version, err := m.probeVersion(ctx, path)
	if err != nil {
		return WSL{}, &WSLRestartRequiredError{Err: err}
	}
	if !version.AtLeast(minWSL) {
		return WSL{}, &WSLVersionTooOldError{Found: version, Minimum: minWSL, Path: path}
	}
	return WSL{Path: path, Version: version}, nil
}

// Collaborator accessors: a zero WSLManager works, and a test can replace any
// one of these without supplying the others.

func (m *WSLManager) runner() Runner {
	if m.Runner != nil {
		return m.Runner
	}
	return execRunner{}
}

func (m *WSLManager) confirm() func(context.Context, string) (bool, error) {
	if m.Confirm != nil {
		return m.Confirm
	}
	return func(ctx context.Context, question string) (bool, error) {
		return promptYesNo(ctx, os.Stdin, m.out(), question)
	}
}

func (m *WSLManager) interactive() func() bool {
	if m.Interactive != nil {
		return m.Interactive
	}
	return stdinIsTerminal
}

func (m *WSLManager) lookPath() func(string) (string, error) {
	if m.LookPath != nil {
		return m.LookPath
	}
	return exec.LookPath
}

func (m *WSLManager) fallbackDirs() []string {
	if m.FallbackDirs != nil {
		return m.FallbackDirs
	}
	return systemDirs()
}

func (m *WSLManager) out() io.Writer {
	if m.Out != nil {
		return m.Out
	}
	return io.Discard
}

// WSLNotInstalledReason says why avar has no usable WSL and did not set one up.
type WSLNotInstalledReason string

const (
	// WSLReasonNotFound: wsl.exe could not be found at all, which on a supported
	// Windows host means something is wrong with the installation itself.
	WSLReasonNotFound WSLNotInstalledReason = "no-wsl"
	// WSLReasonDeclined: the user was asked and said no.
	WSLReasonDeclined WSLNotInstalledReason = "declined"
	// WSLReasonNonInteractive: there was nobody to ask for consent.
	WSLReasonNonInteractive WSLNotInstalledReason = "non-interactive"
	// WSLReasonSetupFailed: the install or update returned an error.
	WSLReasonSetupFailed WSLNotInstalledReason = "setup-failed"
)

// WSLNotInstalledError reports that avar cannot proceed because WSL is not
// usable. Its message carries the manual instructions, because this error is the
// last thing the user sees before avar exits non-zero.
type WSLNotInstalledError struct {
	Reason WSLNotInstalledReason
	// Action is "install" or "update", when the reason is a setup that did not
	// work out.
	Action string
	// Err is the underlying failure, where there was one.
	Err error
}

func (e *WSLNotInstalledError) Error() string {
	var headline string
	switch e.Reason {
	case WSLReasonNotFound:
		headline = "avar could not find wsl.exe, which is part of Windows itself."
	case WSLReasonDeclined:
		headline = "WSL 2 is required and was not set up."
	case WSLReasonNonInteractive:
		headline = "WSL 2 is not usable, and avar is not running on a terminal, so it cannot ask for permission to set it up."
	case WSLReasonSetupFailed:
		headline = fmt.Sprintf("`wsl --%s` failed: %v", e.Action, e.Err)
	default:
		headline = "WSL 2 is not usable."
	}
	return headline + "\n\n" + wslManualInstructions
}

func (e *WSLNotInstalledError) Unwrap() error { return e.Err }

// WSLRestartRequiredError reports that setup succeeded but the platform is not
// usable until Windows restarts.
//
// The instruction it carries is idempotent on purpose: running the same avr
// command after the restart is the whole of what the user has to do, because
// nothing was registered and nothing is half-finished (REQ-18.3, PROP-13).
type WSLRestartRequiredError struct {
	// Err is the re-probe that still could not read a version, kept so the
	// underlying condition stays inspectable.
	Err error
}

func (e *WSLRestartRequiredError) Error() string {
	return "WSL was set up, but Windows needs to restart before avar can use it.\n" +
		"\n" +
		"Restart Windows, then run the same avr command again.\n" +
		"Nothing has been created yet, so there is nothing to clean up first."
}

func (e *WSLRestartRequiredError) Unwrap() error { return ErrRestartRequired }

// WSLVersionTooOldError reports a WSL installation below MinWSLVersion that
// updating did not fix. avar refuses to operate against it rather than drive
// flags it may not have.
type WSLVersionTooOldError struct {
	Found   Version
	Minimum Version
	// Path is the wsl.exe that reported Found.
	Path string
}

func (e *WSLVersionTooOldError) Error() string {
	return fmt.Sprintf("WSL %s at %s is too old: avar needs WSL %s or newer.\n"+
		"\n"+
		"Update it, then run avr again:\n"+
		"\n"+
		"    wsl --update\n"+
		"\n"+
		"avar will not run against an unsupported WSL version.",
		e.Found, e.Path, e.Minimum)
}

// WSL1Error reports an avar-owned distribution registered as WSL 1, and carries
// the exact command that converts it.
//
// avar names the command and stops. The conversion rewrites the distribution's
// filesystem and takes minutes, so it is the user's decision, not a side effect
// of asking for a shell (REQ-18.4, PROP-15).
//
// It lives here rather than with the provider because it is a statement about a
// prerequisite, and because the remedy belongs beside the other remedies avar
// offers for the same dependency. The provider decides which distribution is in
// this state; what to say about it is settled once, here.
type WSL1Error struct {
	// Distribution is the registered name of the WSL 1 distribution.
	Distribution string
}

func (e *WSL1Error) Error() string {
	return fmt.Sprintf("the environment %s is registered as WSL 1, and avar needs WSL 2.\n"+
		"\n"+
		"Convert it, then run avr again:\n"+
		"\n"+
		"    wsl --set-version %s 2\n"+
		"\n"+
		"avar will not convert it for you: the conversion rewrites the whole\n"+
		"filesystem and takes several minutes.",
		e.Distribution, e.Distribution)
}

func (e *WSL1Error) Unwrap() error { return ErrWSL1 }

// wslManualInstructions is the fallback avar prints when it will not or cannot
// set WSL up itself. It names the exact commands to run.
const wslManualInstructions = "Set up WSL " + MinWSLVersion + " or newer, then run avr again:\n" +
	"\n" +
	"    wsl --install --no-distribution\n" +
	"    wsl --update\n" +
	"\n" +
	"Both may ask you to approve the change, and the first may need a restart.\n" +
	"Microsoft's instructions: https://learn.microsoft.com/windows/wsl/install"

// DecodeWSLOutput turns the bytes wsl.exe wrote into text, decoding UTF-16 when
// that is what it produced.
//
// It is exported because the WSL2Provider parses the same tool's output, and a
// second copy of this is how one of the two ends up reading distribution names
// as runs of NUL-separated characters.
//
// wsl.exe writes UTF-16LE when its output is redirected — which it always is,
// for avar — and it writes it with no byte-order mark, so nothing declares the
// encoding and the bytes have to be recognised. Verified against WSL 2.7.12,
// where `wsl --version` begins 57 00 53 00 4C 00: "WSL" in UTF-16LE. Some newer
// output is UTF-8, and is handled by the same function taking the other branch,
// which is why the encoding is detected rather than assumed.
//
// Detection is by NUL bytes rather than by any heuristic on the text, because
// that distinction is unambiguous: no output avar reads contains a NUL byte in
// UTF-8, and UTF-16LE text made of ASCII is half NUL bytes, all at odd offsets.
// A byte-order mark, where one is present, is authoritative and is consumed.
func DecodeWSLOutput(b []byte) string {
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		return decodeUTF16(b[2:], false)
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		return decodeUTF16(b[2:], true)
	case looksLikeUTF16LE(b):
		return decodeUTF16(b, false)
	default:
		return string(b)
	}
}

// looksLikeUTF16LE reports whether b is UTF-16LE text with no byte-order mark,
// by counting the NUL that follows every U+0000–U+00FF character in that
// encoding. An odd-length buffer cannot be UTF-16 at all, and valid UTF-8 with
// no NUL in it is taken at face value.
//
// The count is a proportion rather than a requirement that every odd byte be
// NUL, because that stricter form only holds while the text is entirely
// Latin-1. A single character outside it breaks the run: 名 is U+540D, which
// encodes to 8D 54, so its odd byte is 0x54 and detection would fail for the
// whole buffer — falling through to string(b), which is the NUL-interleaved
// garbage this function exists to prevent. That is not hypothetical for the
// caller that matters: `wsl --list --quiet` is nothing but distribution names,
// and a user may name their own in any script.
//
// Comparing the two parities is what separates UTF-16LE from UTF-16BE, which
// puts its NULs at the even offsets instead. Both are still unambiguous against
// UTF-8, which is the property actually relied on: the output avar reads
// carries no NUL byte at all when it is UTF-8, so it can never reach the
// threshold.
func looksLikeUTF16LE(b []byte) bool {
	if len(b) < 2 || len(b)%2 != 0 {
		return false
	}
	if utf8.Valid(b) && !bytes.ContainsRune(b, 0) {
		return false
	}
	var odd, even int
	for i := 0; i+1 < len(b); i += 2 {
		if b[i] == 0 {
			even++
		}
		if b[i+1] == 0 {
			odd++
		}
	}
	pairs := len(b) / 2
	return odd*10 >= pairs*utf16NULThreshold && odd > even
}

// utf16NULThreshold is the proportion of code units, in tenths, that must carry
// a NUL at the odd offset for a buffer to be read as UTF-16LE. Three in ten is
// far below what any realistic wsl.exe output produces — even a list of
// entirely non-Latin distribution names reaches half, because the CRLF ending
// every line is two code units that are themselves Latin-1 — and far above the
// zero that UTF-8 output produces.
const utf16NULThreshold = 3

// decodeUTF16 decodes b as UTF-16 in the given byte order.
func decodeUTF16(b []byte, bigEndian bool) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
			continue
		}
		units = append(units, uint16(b[i+1])<<8|uint16(b[i]))
	}
	return string(utf16.Decode(units))
}
