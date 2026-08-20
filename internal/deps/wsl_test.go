package deps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// The WSL states worth testing are states a machine is in only one of, and
// moving a real machine between them costs a reboot. Everything below therefore
// drives WSLManager through its collaborators: no subprocess, no terminal, no
// WSL. What is asserted is the decision — which command avar ran, whether it
// asked first, and what it told the user — because that decision is the whole
// of this component.

// supportedWSL is a version above the pinned minimum, rendered the way Windows
// renders one: four components.
var supportedWSL = fmt.Sprintf("%d.%d.%d.0", minWSL.Major, minWSL.Minor+1, minWSL.Patch)

// tooOldWSL is a version below the pinned minimum. Deriving it keeps the tests
// honest when MinWSLVersion is raised.
var tooOldWSL = fmt.Sprintf("%d.9.9.0", minWSL.Major-1)

// versionOutput is what `wsl --version` writes, in the encoding it writes it in.
// Every field after the first is there because the parser has to walk past it:
// the Windows build number is itself version-shaped, so a parser that gave up on
// the first token would report the wrong number rather than none.
func versionOutput(version string) []byte {
	return utf16LE("WSL version: " + version + "\r\n" +
		"Kernel version: 6.18.33.2-2\r\n" +
		"WSLg version: 1.0.73.2\r\n" +
		"Windows version: 10.0.26200.8875\r\n")
}

// utf16LE encodes s the way wsl.exe writes to a redirected stream: UTF-16
// little-endian with no byte-order mark.
func utf16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(units)*2)
	for _, u := range units {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

// wslCall is one subprocess the manager ran.
type wslCall struct {
	kind string // "output" or "stream"
	argv []string
}

func (c wslCall) String() string { return c.kind + " " + strings.Join(c.argv, " ") }

// fakeWSL is the outside world for one test: a wsl.exe that answers `--version`
// however the test says, a user who answers the prompt however the test says,
// and a record of everything that was run.
type fakeWSL struct {
	// versionOut is what `wsl --version` writes. Nil means it writes nothing.
	versionOut []byte
	// versionErr is what `wsl --version` fails with. Non-nil models a WSL that
	// cannot report a version: disabled, or too old to have the flag.
	versionErr error
	// afterSetup replaces versionOut/versionErr once a setup command has run,
	// which is how a test says whether the install took effect immediately or
	// needs a restart.
	afterSetup    []byte
	afterSetupErr error
	// setupErr fails the install or update itself.
	setupErr error

	interactive   bool
	confirmAnswer bool
	confirmed     []string
	calls         []wslCall
	setupRan      bool
	out           strings.Builder
}

func (f *fakeWSL) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, wslCall{kind: "output", argv: append([]string{name}, args...)})
	if f.setupRan && (f.afterSetup != nil || f.afterSetupErr != nil) {
		return f.afterSetup, f.afterSetupErr
	}
	return f.versionOut, f.versionErr
}

func (f *fakeWSL) Stream(_ context.Context, w io.Writer, name string, args ...string) error {
	f.calls = append(f.calls, wslCall{kind: "stream", argv: append([]string{name}, args...)})
	f.setupRan = true
	if f.setupErr != nil {
		return f.setupErr
	}
	fmt.Fprintln(w, "installing")
	return nil
}

// manager wires the fake into a WSLManager, with a wsl.exe that exists on disk
// so the search does not become the thing under test.
func (f *fakeWSL) manager(t *testing.T) *WSLManager {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, wslBinary)
	if err := os.WriteFile(path, []byte("windows component"), 0o600); err != nil {
		t.Fatalf("writing the fake wsl.exe: %v", err)
	}

	return &WSLManager{
		Runner:       f,
		LookPath:     func(string) (string, error) { return "", errors.New("not on PATH") },
		FallbackDirs: []string{dir},
		Interactive:  func() bool { return f.interactive },
		Confirm: func(_ context.Context, question string) (bool, error) {
			f.confirmed = append(f.confirmed, question)
			return f.confirmAnswer, nil
		},
		Out: &f.out,
	}
}

// argvs renders every subprocess for a failure message.
func (f *fakeWSL) argvs() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.String())
	}
	return out
}

// ran reports whether any subprocess had all of args among its arguments.
func (f *fakeWSL) ran(args ...string) bool {
	for _, c := range f.calls {
		found := 0
		for _, want := range args {
			for _, got := range c.argv {
				if got == want {
					found++
					break
				}
			}
		}
		if found == len(args) {
			return true
		}
	}
	return false
}

// assertNoSetup fails if avar changed the machine.
func assertNoSetup(t *testing.T, f *fakeWSL) {
	t.Helper()
	for _, c := range f.calls {
		if c.kind == "stream" {
			t.Errorf("avar changed the machine without a usable consent: %s", c)
		}
	}
}

// REQ-18.2: a WSL that meets the minimum is used directly, with no prompt, no
// setup, and nothing but the version probe — this is the warm path of every
// Windows invocation.
func TestEnsureWSL_UsesACompatibleInstallWithoutPrompting_REQ_18_2(t *testing.T) {
	t.Parallel()

	f := &fakeWSL{versionOut: versionOutput(supportedWSL), interactive: true}
	m := f.manager(t)
	m.Confirm = func(context.Context, string) (bool, error) {
		t.Error("the user was prompted even though WSL is usable")
		return false, nil
	}

	wsl, err := m.EnsureWSL(context.Background())
	if err != nil {
		t.Fatalf("EnsureWSL returned an unexpected error: %v", err)
	}
	if wsl.Version.String() != strings.TrimSuffix(supportedWSL, ".0") {
		t.Errorf("Version = %s, want the reported %s", wsl.Version, supportedWSL)
	}
	if filepath.Base(wsl.Path) != wslBinary {
		t.Errorf("Path = %q, want the wsl.exe that was verified", wsl.Path)
	}
	if len(f.calls) != 1 {
		t.Errorf("the warm path ran %d subprocesses, want exactly the version probe: %v", len(f.calls), f.argvs())
	}
	assertNoSetup(t, f)
}

// The version comes from the WSL line and not from the Windows build number two
// lines below it, which is also version-shaped and much larger.
func TestEnsureWSL_ReadsTheWSLVersionNotTheWindowsBuild_REQ_18_2(t *testing.T) {
	t.Parallel()

	f := &fakeWSL{versionOut: versionOutput(supportedWSL), interactive: true}
	wsl, err := f.manager(t).EnsureWSL(context.Background())
	if err != nil {
		t.Fatalf("EnsureWSL: %v", err)
	}
	if wsl.Version.Major >= 10 {
		t.Errorf("Version = %s, which is the Windows build number, not the WSL version", wsl.Version)
	}
}

// REQ-18.3: a WSL that cannot report a version is offered an install that
// creates no distribution, and the offer says what it will cost before the user
// agrees to it.
func TestEnsureWSL_OffersAPlatformOnlyInstallAndSaysWhatItCosts_REQ_18_3(t *testing.T) {
	t.Parallel()

	f := &fakeWSL{
		versionErr:    errors.New("exit status 1"),
		afterSetup:    versionOutput(supportedWSL),
		interactive:   true,
		confirmAnswer: true,
	}
	m := f.manager(t)

	if _, err := m.EnsureWSL(context.Background()); err != nil {
		t.Fatalf("EnsureWSL after a confirmed install: %v", err)
	}

	if len(f.confirmed) != 1 {
		t.Fatalf("the user was asked %d times, want once: %v", len(f.confirmed), f.confirmed)
	}
	question := f.confirmed[0]
	for _, want := range []string{"approve", "restart", "--no-distribution"} {
		if !strings.Contains(question, want) {
			t.Errorf("the install offer does not mention %q, so the user agrees without knowing:\n%s", want, question)
		}
	}
	// --no-distribution is what keeps setup from creating a Linux environment
	// avar does not own and did not ask for (REQ-18.7).
	if !f.ran("--install", "--no-distribution") {
		t.Errorf("want `wsl --install --no-distribution`, got %v", f.argvs())
	}
}

// REQ-18.3: an install is re-verified rather than believed. A setup command that
// exits zero on a machine where the platform was disabled has not made WSL
// usable — Windows has to restart first — and that is a distinct answer with a
// distinct instruction, not a failure.
func TestEnsureWSL_RestartPendingIsItsOwnAnswer_REQ_18_3(t *testing.T) {
	t.Parallel()

	f := &fakeWSL{
		versionErr:    errors.New("exit status 1"),
		afterSetupErr: errors.New("exit status 1"),
		interactive:   true,
		confirmAnswer: true,
	}

	_, err := f.manager(t).EnsureWSL(context.Background())
	if err == nil {
		t.Fatal("EnsureWSL succeeded although WSL still cannot report a version")
	}
	if !errors.Is(err, ErrRestartRequired) {
		t.Fatalf("error %v is not an ErrRestartRequired, so a caller cannot tell a pending restart from a failure", err)
	}
	for _, want := range []string{"restart", "run the same avr command again", "nothing to clean up"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("the message does not tell the user to %q:\n%s", want, err)
		}
	}
	if !f.ran("--install") {
		t.Errorf("the install never ran: %v", f.argvs())
	}
}

// REQ-18.3: a WSL that is present but behind is offered the lighter action, and
// is told apart from an absent one — updating a platform the user has is not the
// same request as installing one they do not.
func TestEnsureWSL_OffersAnUpdateWhenTheVersionIsBehind_REQ_18_3(t *testing.T) {
	t.Parallel()

	f := &fakeWSL{
		versionOut:    versionOutput(tooOldWSL),
		afterSetup:    versionOutput(supportedWSL),
		interactive:   true,
		confirmAnswer: true,
	}

	wsl, err := f.manager(t).EnsureWSL(context.Background())
	if err != nil {
		t.Fatalf("EnsureWSL after a confirmed update: %v", err)
	}
	if !wsl.Version.AtLeast(minWSL) {
		t.Errorf("Version = %s, want the version reported after the update", wsl.Version)
	}
	if !f.ran("--update") {
		t.Errorf("want `wsl --update`, got %v", f.argvs())
	}
	if f.ran("--install") {
		t.Errorf("a present-but-old WSL was reinstalled rather than updated: %v", f.argvs())
	}
	if len(f.confirmed) != 1 || strings.Contains(f.confirmed[0], "not installed") {
		t.Errorf("the update offer reads like an install offer:\n%s", strings.Join(f.confirmed, "\n"))
	}
}

// REQ-18.3: an update that leaves WSL below the minimum is refused outright.
// avar never operates against a version it has no evidence for.
func TestEnsureWSL_RefusesAVersionAnUpdateDidNotFix_REQ_18_3(t *testing.T) {
	t.Parallel()

	f := &fakeWSL{
		versionOut:    versionOutput(tooOldWSL),
		afterSetup:    versionOutput(tooOldWSL),
		interactive:   true,
		confirmAnswer: true,
	}

	_, err := f.manager(t).EnsureWSL(context.Background())
	if err == nil {
		t.Fatal("EnsureWSL succeeded against a WSL below the minimum")
	}
	var tooOld *WSLVersionTooOldError
	if !errors.As(err, &tooOld) {
		t.Fatalf("error %v is not a WSLVersionTooOldError", err)
	}
	if !strings.Contains(err.Error(), MinWSLVersion) || !strings.Contains(err.Error(), "wsl --update") {
		t.Errorf("the message names neither the minimum nor the command to run:\n%s", err)
	}
}

// REQ-18.3: declining leaves the machine exactly as it was, and the message
// says what to run by hand.
func TestEnsureWSL_DeclinedSetupChangesNothing_REQ_18_3(t *testing.T) {
	t.Parallel()

	f := &fakeWSL{
		versionErr:    errors.New("exit status 1"),
		interactive:   true,
		confirmAnswer: false,
	}

	_, err := f.manager(t).EnsureWSL(context.Background())
	if err == nil {
		t.Fatal("EnsureWSL succeeded although the user declined")
	}
	assertNoSetup(t, f)
	for _, want := range []string{"wsl --install --no-distribution", "wsl --update", MinWSLVersion} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not name %q:\n%s", want, err)
		}
	}
}

// A script, a hook, or a CI job has nobody to answer the prompt. Installing
// anyway would be an unconsented change to somebody's machine.
func TestEnsureWSL_NonInteractiveNeverInstalls_REQ_18_3(t *testing.T) {
	t.Parallel()

	f := &fakeWSL{versionErr: errors.New("exit status 1"), interactive: false, confirmAnswer: true}

	_, err := f.manager(t).EnsureWSL(context.Background())
	if err == nil {
		t.Fatal("EnsureWSL succeeded with no WSL and nobody to ask")
	}
	assertNoSetup(t, f)
	if len(f.confirmed) != 0 {
		t.Errorf("a prompt was written with no terminal to read the answer from: %v", f.confirmed)
	}
}

// A setup command that fails is reported as a failure, with what the tool said,
// and is not confused with a pending restart.
func TestEnsureWSL_ReportsAFailedSetup_REQ_18_3(t *testing.T) {
	t.Parallel()

	f := &fakeWSL{
		versionErr:    errors.New("exit status 1"),
		setupErr:      errors.New("Error code: Wsl/InstallDistro/0x80070005"),
		interactive:   true,
		confirmAnswer: true,
	}

	_, err := f.manager(t).EnsureWSL(context.Background())
	if err == nil {
		t.Fatal("EnsureWSL succeeded although the install failed")
	}
	if errors.Is(err, ErrRestartRequired) {
		t.Error("a failed install was reported as a pending restart")
	}
	if !strings.Contains(err.Error(), "0x80070005") {
		t.Errorf("the message drops what the tool said:\n%s", err)
	}
}

// wsl.exe is part of Windows, so a host without it is broken rather than
// unconfigured — and avar must say so instead of offering to install something
// with a tool that is not there.
func TestEnsureWSL_MissingWSLExeIsReportedNotInstalled_REQ_18_3(t *testing.T) {
	t.Parallel()

	f := &fakeWSL{interactive: true, confirmAnswer: true}
	m := f.manager(t)
	m.FallbackDirs = []string{t.TempDir()}

	_, err := m.EnsureWSL(context.Background())
	if !errors.Is(err, ErrWSLNotFound) {
		t.Fatalf("error = %v, want it to wrap ErrWSLNotFound", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("avar ran something although it never found wsl.exe: %v", f.argvs())
	}
}

// A cancelled context stops before anything is asked or run: Ctrl-C at the
// prompt must not leave an install running.
func TestEnsureWSL_CancelledContextStopsBeforeActing(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := &fakeWSL{versionErr: errors.New("exit status 1"), interactive: true, confirmAnswer: true}
	if _, err := f.manager(t).EnsureWSL(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want a cancelled context", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("avar ran something after the context was cancelled: %v", f.argvs())
	}
}

// No avar subprocess is ever a shell or a command string: every recorded call is
// an argv whose program is the verified wsl.exe and whose arguments are separate
// elements.
func TestEnsureWSL_NeverInvokesAShell(t *testing.T) {
	t.Parallel()

	f := &fakeWSL{
		versionErr:    errors.New("exit status 1"),
		afterSetup:    versionOutput(supportedWSL),
		interactive:   true,
		confirmAnswer: true,
	}
	m := f.manager(t)
	if _, err := m.EnsureWSL(context.Background()); err != nil {
		t.Fatalf("EnsureWSL returned an unexpected error: %v", err)
	}
	if len(f.calls) == 0 {
		t.Fatal("expected the setup flow to run subprocesses")
	}

	for _, c := range f.calls {
		if filepath.Base(c.argv[0]) != wslBinary {
			t.Errorf("subprocess is not the verified wsl.exe: %s", c)
		}
		for _, arg := range c.argv {
			if strings.ContainsAny(arg, "|&;<>$`\n") || arg == "-c" {
				t.Errorf("argument %q looks like shell syntax: %s", arg, c)
			}
		}
	}
}

// PROP-13: a Windows invocation's dependency work mentions WSL and nothing else.
// Naming Lima, Docker Desktop, or Hyper-V in a message a Windows user reads
// would send them to install a runtime avar does not use.
func TestEnsureWSL_NeverMentionsAnotherRuntime_PROP_13(t *testing.T) {
	t.Parallel()

	f := &fakeWSL{versionErr: errors.New("exit status 1"), interactive: true, confirmAnswer: false}
	_, err := f.manager(t).EnsureWSL(context.Background())
	if err == nil {
		t.Fatal("EnsureWSL succeeded although the user declined")
	}

	haystack := strings.ToLower(err.Error() + "\n" + strings.Join(f.confirmed, "\n") + "\n" + f.out.String())
	for _, forbidden := range []string{"lima", "limactl", "homebrew", "brew", "docker", "hyper-v", "virtualbox"} {
		if strings.Contains(haystack, forbidden) {
			t.Errorf("a Windows dependency message mentions %q:\n%s", forbidden, haystack)
		}
	}
}

// REQ-18.4: a WSL 1 registration is refused with the exact conversion command
// and is never converted automatically.
func TestWSL1Error_NamesTheConversionAndRefusesToRunIt_REQ_18_4(t *testing.T) {
	t.Parallel()

	err := error(&WSL1Error{Distribution: "avr-ubuntu-24.04-amd64"})
	if !errors.Is(err, ErrWSL1) {
		t.Fatal("a WSL1Error does not identify itself, so a caller cannot react to the condition")
	}
	if !strings.Contains(err.Error(), "wsl --set-version avr-ubuntu-24.04-amd64 2") {
		t.Errorf("the message does not carry the exact command to run:\n%s", err)
	}
	if !strings.Contains(err.Error(), "will not convert it for you") {
		t.Errorf("the message does not say that avar leaves the conversion to the user:\n%s", err)
	}
}

// wsl.exe writes UTF-16 to a redirected stream, with no byte-order mark to say
// so. Reading it as bytes yields text with a NUL between every character, which
// no parser above this recovers from.
func TestDecodeWSLOutput_ReadsWhatWSLActuallyWrites_REQ_18_2(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{
			// The exact opening bytes of `wsl --version` on WSL 2.7.12.
			name: "UTF-16LE with no byte-order mark",
			in:   []byte{0x57, 0x00, 0x53, 0x00, 0x4C, 0x00, 0x20, 0x00, 0x32, 0x00},
			want: "WSL 2",
		},
		{
			name: "UTF-16LE with a byte-order mark",
			in:   append([]byte{0xFF, 0xFE}, utf16LE("WSL 2")...),
			want: "WSL 2",
		},
		{
			name: "UTF-16BE with a byte-order mark",
			in:   []byte{0xFE, 0xFF, 0x00, 0x57, 0x00, 0x53, 0x00, 0x4C},
			want: "WSL",
		},
		{
			// A build that has moved to UTF-8 must not be mangled by the
			// decoder that exists for the ones that have not.
			name: "UTF-8",
			in:   []byte("WSL version: 2.7.12.0"),
			want: "WSL version: 2.7.12.0",
		},
		{
			name: "non-ASCII UTF-16LE, as a localized Windows writes",
			in:   utf16LE("Distribution par défaut : Ubuntu"),
			want: "Distribution par défaut : Ubuntu",
		},
		{
			// A French name keeps its NUL at the odd offset, because é is
			// U+00E9 and still fits in a byte. A Japanese one does not: 名 is
			// U+540D, so its odd byte is 0x54 and a detector requiring every
			// odd byte to be NUL would read this whole buffer as UTF-8 and
			// return NUL-interleaved garbage. The CRLF ending each line is
			// what keeps the proportion well above the threshold.
			name: "distribution names outside Latin-1",
			in:   utf16LE("名前\r\nUbuntu-24.04\r\n"),
			want: "名前\r\nUbuntu-24.04\r\n",
		},
		{
			// Every code unit outside Latin-1 and no line ending to help: the
			// proportion of odd NULs is zero, so this is not detected as
			// UTF-16 and is returned as bytes. Documented rather than fixed —
			// wsl.exe terminates its lines, so avar never reads such a buffer,
			// and the alternative is guessing between two encodings on no
			// evidence.
			name: "unterminated non-Latin-1 is below the threshold",
			in:   utf16LE("名前"),
			want: string(utf16LE("名前")),
		},
		{
			// Odd length cannot be UTF-16, so it is text whatever it looks
			// like — better a mangled byte than a dropped one.
			name: "odd length is not UTF-16",
			in:   []byte{'a', 0x00, 'b'},
			want: "a\x00b",
		},
		{name: "empty", in: nil, want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := DecodeWSLOutput(tc.in); got != tc.want {
				t.Errorf("DecodeWSLOutput(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The version gate is only as good as the constant behind it.
func TestMinWSLVersion_Parses(t *testing.T) {
	t.Parallel()

	v, err := ParseVersion(MinWSLVersion)
	if err != nil {
		t.Fatalf("MinWSLVersion %q does not parse: %v", MinWSLVersion, err)
	}
	if v != MinWSL() {
		t.Errorf("MinWSL() = %s, want the parsed constant %s", MinWSL(), v)
	}
	// The Store channel is what carries `wsl --version` and every flag the
	// provider drives, and it is numbered from 2.0.0.
	if v.Major < 2 {
		t.Errorf("MinWSLVersion = %s, which admits a WSL with no `--version` to gate on", v)
	}
}

// Windows numbers its releases with four components, so a parser that only
// accepts three would find no version in `wsl --version` at all.
func TestParseVersion_AcceptsAFourthComponent_REQ_18_2(t *testing.T) {
	t.Parallel()

	got, err := ParseVersion("WSL version: 2.7.12.0")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if want := (Version{Major: 2, Minor: 7, Patch: 12}); got != want {
		t.Errorf("ParseVersion = %+v, want %+v", got, want)
	}
	// The fourth component takes no part in precedence, which is the reason it
	// can be discarded rather than stored.
	newer, err := ParseVersion("2.7.13.0")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	if !newer.AtLeast(got) || got.AtLeast(newer) {
		t.Errorf("2.7.13.0 (%s) does not compare as newer than 2.7.12.0 (%s)", newer, got)
	}
}

// REQ-18.3: a refusal should say what avar knows. When the version is the
// problem, "WSL 2 is not usable" is true and useless — the user needs to know
// which version they have, which one avar needs, and that updating is the fix.
//
// This is reachable without a terminal, which is exactly when it matters: a
// script or a CI job cannot answer a prompt, and the message is the only thing
// it leaves behind.
func TestEnsureWSL_ARefusedUpdateNamesTheVersion_REQ_18_3(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		interactive bool
		answer      bool
	}{
		{name: "nobody to ask", interactive: false},
		{name: "the user said no", interactive: true, answer: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := &fakeWSL{versionOut: versionOutput(tooOldWSL), interactive: tc.interactive, confirmAnswer: tc.answer}
			_, err := f.manager(t).EnsureWSL(context.Background())
			if err == nil {
				t.Fatal("EnsureWSL succeeded against a WSL below the minimum")
			}

			var tooOld *WSLVersionTooOldError
			if !errors.As(err, &tooOld) {
				t.Fatalf("error %v is not a WSLVersionTooOldError, so the user is not told what is wrong", err)
			}
			for _, want := range []string{tooOldWSL[:strings.LastIndex(tooOldWSL, ".")], MinWSLVersion, "wsl --update"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the message does not mention %q:\n%s", want, err)
				}
			}
			assertNoSetup(t, f)
		})
	}
}
