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
	"testing"
)

const (
	fakeLimactl = "/opt/homebrew/bin/limactl"
	fakeBrew    = "/opt/homebrew/bin/brew"
)

// call records one subprocess invocation as the argv it was given. Tests assert
// on argv rather than on a command string: if any of this were ever routed
// through a shell, the argv would stop matching.
type call struct {
	kind string // "output" or "stream"
	argv []string
}

func (c call) String() string { return c.kind + " " + strings.Join(c.argv, " ") }

// fakeEnv is a scripted host: which executables exist, what limactl reports,
// whether there is a user to ask, and what `brew install lima` does. Nothing in
// it touches the real PATH, the real filesystem, or a real subprocess.
type fakeEnv struct {
	limactl    string // path LookPath returns for limactl; "" means absent
	brew       string // path LookPath returns for brew; "" means absent
	versionOut string
	versionErr error

	interactive   bool
	confirmAnswer bool
	confirmErr    error
	confirmCalls  int
	// prompt is set to the question the user was asked.
	prompt string

	installed  bool // `brew install lima` was invoked
	installErr error
	// afterInstall models what Homebrew left behind. Nil means "Lima is now
	// present at the standard path reporting versionOut".
	afterInstall func(*fakeEnv)

	calls []call
	out   bytes.Buffer
}

func (e *fakeEnv) record(kind, name string, args []string) {
	e.calls = append(e.calls, call{kind: kind, argv: append([]string{name}, args...)})
}

func (e *fakeEnv) argvs() []string {
	got := make([]string, 0, len(e.calls))
	for _, c := range e.calls {
		got = append(got, c.String())
	}
	return got
}

// fakeRunner adapts fakeEnv to the Runner interface.
type fakeRunner struct{ e *fakeEnv }

func (r fakeRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.e.record("output", name, args)
	if r.e.versionErr != nil {
		return nil, r.e.versionErr
	}
	return []byte(r.e.versionOut), nil
}

func (r fakeRunner) Stream(ctx context.Context, w io.Writer, name string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.e.record("stream", name, args)
	r.e.installed = true
	fmt.Fprintln(w, "==> Fetching lima")
	if r.e.installErr != nil {
		return r.e.installErr
	}
	if r.e.afterInstall != nil {
		r.e.afterInstall(r.e)
	} else {
		r.e.limactl = fakeLimactl
	}
	return nil
}

// manager wires the fake environment into a Manager. Confirm defaults to
// failing the test, so any flow that must not prompt proves it by construction.
func (e *fakeEnv) manager(t *testing.T) *Manager {
	t.Helper()

	return &Manager{
		Runner: fakeRunner{e},
		LookPath: func(file string) (string, error) {
			switch file {
			case limactlBinary:
				if e.limactl == "" {
					return "", fmt.Errorf("exec: %q: %w", file, exec.ErrNotFound)
				}
				return e.limactl, nil
			case "brew":
				if e.brew == "" {
					return "", fmt.Errorf("exec: %q: %w", file, exec.ErrNotFound)
				}
				return e.brew, nil
			default:
				return "", fmt.Errorf("exec: %q: %w", file, exec.ErrNotFound)
			}
		},
		Interactive: func() bool { return e.interactive },
		Confirm: func(_ context.Context, question string) (bool, error) {
			e.confirmCalls++
			e.prompt = question
			return e.confirmAnswer, e.confirmErr
		},
		// Never the real Homebrew directories: the result must not depend on
		// whether the developer running the tests has Lima installed.
		FallbackDirs: []string{filepath.Join(t.TempDir(), "no-such-bin")},
		Out:          &e.out,
	}
}

func assertNoInstall(t *testing.T, e *fakeEnv) {
	t.Helper()
	if e.installed {
		t.Error("brew install lima was invoked; it must not be")
	}
	for _, c := range e.calls {
		if c.kind == "stream" {
			t.Errorf("unexpected install-shaped call: %s", c)
		}
	}
}

func assertMessageOffersManualInstall(t *testing.T, err error) {
	t.Helper()
	for _, want := range []string{"brew install lima", "https://lima-vm.io", MinLimaVersion} {
		if !contains(err.Error(), want) {
			t.Errorf("error message does not mention %q:\n%v", want, err)
		}
	}
}

// REQ-8.1: a compatible Lima is used with no further prompts and no output.
func TestEnsureLima_UsesCompatibleInstallWithoutPrompting_REQ_8_1(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		versionOut  string
		wantVersion string
	}{
		{name: "current release", versionOut: "limactl version 1.0.4\n", wantVersion: "1.0.4"},
		{name: "exactly the minimum", versionOut: "limactl version " + MinLimaVersion, wantVersion: MinLimaVersion},
		{name: "much newer", versionOut: "limactl version 1.10.0", wantVersion: "1.10.0"},
		{name: "next major", versionOut: "limactl version v2.1.0", wantVersion: "2.1.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := &fakeEnv{limactl: fakeLimactl, brew: fakeBrew, versionOut: tc.versionOut, interactive: true}
			m := e.manager(t)
			m.Confirm = func(context.Context, string) (bool, error) {
				t.Error("the user was prompted even though a compatible Lima is installed")
				return false, nil
			}

			lima, err := m.EnsureLima(context.Background())
			if err != nil {
				t.Fatalf("EnsureLima returned an unexpected error: %v", err)
			}
			if lima.Path != fakeLimactl {
				t.Errorf("Path = %q, want %q", lima.Path, fakeLimactl)
			}
			if got := lima.Version.String(); got != tc.wantVersion {
				t.Errorf("Version = %q, want %q", got, tc.wantVersion)
			}

			// One argv, no shell, no install.
			want := []string{"output " + fakeLimactl + " --version"}
			if got := e.argvs(); !equalStrings(got, want) {
				t.Errorf("subprocess calls = %v, want %v", got, want)
			}
			assertNoInstall(t, e)
			if e.out.Len() != 0 {
				t.Errorf("a compatible Lima must produce no output, got %q", e.out.String())
			}
		})
	}
}

// REQ-8.1: a GUI-launched process can have a PATH without Homebrew's bin
// directory. Lima that is installed must still be found.
func TestEnsureLima_FindsLimactlInHomebrewDirWhenPathIsThin_REQ_8_1(t *testing.T) {
	t.Parallel()

	brewBin := t.TempDir()
	installed := filepath.Join(brewBin, limactlBinary)
	if err := os.WriteFile(installed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing the fake limactl: %v", err)
	}
	// A non-executable file of the same name elsewhere must be ignored.
	notExecutable := t.TempDir()
	if err := os.WriteFile(filepath.Join(notExecutable, limactlBinary), []byte("data"), 0o644); err != nil {
		t.Fatalf("writing the non-executable file: %v", err)
	}

	e := &fakeEnv{brew: fakeBrew, versionOut: "limactl version 1.0.4", interactive: true}
	m := e.manager(t)
	m.FallbackDirs = []string{notExecutable, brewBin}
	m.Confirm = func(context.Context, string) (bool, error) {
		t.Error("the user was prompted even though Lima is installed")
		return false, nil
	}

	lima, err := m.EnsureLima(context.Background())
	if err != nil {
		t.Fatalf("EnsureLima returned an unexpected error: %v", err)
	}
	if lima.Path != installed {
		t.Errorf("Path = %q, want the Homebrew-directory binary %q", lima.Path, installed)
	}
	assertNoInstall(t, e)
}

// REQ-8.2: missing Lima is offered as an install, which runs only after the
// user confirms, and is then re-verified.
func TestEnsureLima_InstallsViaHomebrewAfterConfirmation_REQ_8_2(t *testing.T) {
	t.Parallel()

	e := &fakeEnv{
		limactl:       "", // missing
		brew:          fakeBrew,
		versionOut:    "limactl version 1.0.4",
		interactive:   true,
		confirmAnswer: true,
	}

	lima, err := e.manager(t).EnsureLima(context.Background())
	if err != nil {
		t.Fatalf("EnsureLima returned an unexpected error: %v", err)
	}

	if e.confirmCalls != 1 {
		t.Errorf("the user was asked %d times, want exactly 1", e.confirmCalls)
	}
	if !contains(e.prompt, "brew install lima") {
		t.Errorf("the prompt should name the command being run, got %q", e.prompt)
	}
	if !e.installed {
		t.Error("brew install lima was not invoked after the user confirmed")
	}

	// The install argv, then the re-verification. Asserting the argv proves
	// avar passes ["install", "lima"] to the resolved brew path rather than
	// handing a command string to a shell.
	want := []string{
		"stream " + fakeBrew + " install lima",
		"output " + fakeLimactl + " --version",
	}
	if got := e.argvs(); !equalStrings(got, want) {
		t.Fatalf("subprocess calls = %v, want %v", got, want)
	}
	if lima.Version.String() != "1.0.4" || lima.Path != fakeLimactl {
		t.Errorf("re-verification returned %+v, want the freshly installed 1.0.4 at %s", lima, fakeLimactl)
	}
	if !contains(e.out.String(), "Fetching lima") {
		t.Errorf("installer output must be streamed to the user, got %q", e.out.String())
	}
}

// REQ-8.2: Homebrew exiting zero is not proof of a usable Lima. Each way the
// install can fall short must produce a clear, non-nil error.
func TestEnsureLima_ReverifiesAfterInstallAndFailsWhenLimaIsStillUnusable_REQ_8_2(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		installErr   error
		afterInstall func(*fakeEnv)
		wantReason   NotInstalledReason
		wantTooOld   bool
		wantInError  []string
	}{
		{
			name:        "brew install fails",
			installErr:  errors.New("exit status 1: Error: No available formula"),
			wantReason:  ReasonInstallFailed,
			wantInError: []string{"No available formula"},
		},
		{
			name:         "brew succeeds but limactl is still not there",
			afterInstall: func(e *fakeEnv) { e.limactl = "" },
			wantReason:   ReasonInstallIncomplete,
			wantInError:  []string{"limactl not found"},
		},
		{
			name: "brew succeeds but limactl cannot be run",
			afterInstall: func(e *fakeEnv) {
				e.limactl = fakeLimactl
				e.versionErr = errors.New("fork/exec: permission denied")
			},
			wantReason:  ReasonInstallIncomplete,
			wantInError: []string{"permission denied", "--version"},
		},
		{
			name: "brew installs a version below the minimum",
			afterInstall: func(e *fakeEnv) {
				e.limactl = fakeLimactl
				e.versionOut = "limactl version 0.23.2"
			},
			wantTooOld:  true,
			wantInError: []string{"0.23.2", MinLimaVersion},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := &fakeEnv{
				brew:          fakeBrew,
				versionOut:    "limactl version 1.0.4",
				interactive:   true,
				confirmAnswer: true,
				installErr:    tc.installErr,
				afterInstall:  tc.afterInstall,
			}

			lima, err := e.manager(t).EnsureLima(context.Background())
			if err == nil {
				t.Fatalf("EnsureLima = %+v, want an error", lima)
			}
			if lima != (Lima{}) {
				t.Errorf("EnsureLima returned %+v alongside an error, want the zero Lima", lima)
			}
			if !e.installed {
				t.Error("the install should have been attempted")
			}

			if tc.wantTooOld {
				var tooOld *VersionTooOldError
				if !errors.As(err, &tooOld) {
					t.Fatalf("error is %T (%v), want *VersionTooOldError", err, err)
				}
			} else {
				var notInstalled *NotInstalledError
				if !errors.As(err, &notInstalled) {
					t.Fatalf("error is %T (%v), want *NotInstalledError", err, err)
				}
				if notInstalled.Reason != tc.wantReason {
					t.Errorf("Reason = %q, want %q", notInstalled.Reason, tc.wantReason)
				}
				// The cause has to survive wrapping, or a caller cannot tell
				// an install failure from a missing formula.
				if tc.installErr != nil && !errors.Is(err, tc.installErr) {
					t.Errorf("error does not wrap the install failure %v:\n%v", tc.installErr, err)
				}
				if notInstalled.Unwrap() == nil {
					t.Error("Unwrap() = nil, want the underlying cause")
				}
				assertMessageOffersManualInstall(t, err)
			}
			for _, want := range tc.wantInError {
				if !contains(err.Error(), want) {
					t.Errorf("error message does not mention %q:\n%v", want, err)
				}
			}
		})
	}
}

// REQ-8.3: declining the offer prints manual instructions, installs nothing,
// and fails so avar exits non-zero.
func TestEnsureLima_DeclinedInstallExitsWithInstructions_REQ_8_3(t *testing.T) {
	t.Parallel()

	e := &fakeEnv{brew: fakeBrew, interactive: true, confirmAnswer: false}

	_, err := e.manager(t).EnsureLima(context.Background())
	if err == nil {
		t.Fatal("EnsureLima succeeded, want an error so avr exits non-zero")
	}

	var notInstalled *NotInstalledError
	if !errors.As(err, &notInstalled) {
		t.Fatalf("error is %T (%v), want *NotInstalledError", err, err)
	}
	if notInstalled.Reason != ReasonDeclined {
		t.Errorf("Reason = %q, want %q", notInstalled.Reason, ReasonDeclined)
	}
	if e.confirmCalls != 1 {
		t.Errorf("the user was asked %d times, want exactly 1", e.confirmCalls)
	}
	assertMessageOffersManualInstall(t, err)
	assertNoInstall(t, e)
	if len(e.calls) != 0 {
		t.Errorf("no subprocess should run at all, got %v", e.argvs())
	}
}

// REQ-8.3: with no Homebrew there is nothing to offer, so avar goes straight to
// manual instructions without asking or installing.
func TestEnsureLima_MissingHomebrewExitsWithInstructions_REQ_8_3(t *testing.T) {
	t.Parallel()

	e := &fakeEnv{brew: "", interactive: true, confirmAnswer: true}
	m := e.manager(t)
	m.Confirm = func(context.Context, string) (bool, error) {
		t.Error("the user was offered an install that avar cannot perform")
		return true, nil
	}

	_, err := m.EnsureLima(context.Background())
	if err == nil {
		t.Fatal("EnsureLima succeeded, want an error so avr exits non-zero")
	}

	var notInstalled *NotInstalledError
	if !errors.As(err, &notInstalled) {
		t.Fatalf("error is %T (%v), want *NotInstalledError", err, err)
	}
	if notInstalled.Reason != ReasonNoHomebrew {
		t.Errorf("Reason = %q, want %q", notInstalled.Reason, ReasonNoHomebrew)
	}
	if !contains(err.Error(), "https://brew.sh") {
		t.Errorf("a user without Homebrew should be told where to get it:\n%v", err)
	}
	assertMessageOffersManualInstall(t, err)
	assertNoInstall(t, e)
}

// REQ-8.3: a script or CI job has nobody to answer. avar declines by default —
// it never blocks on input that will not arrive, and never installs software
// without consent.
func TestEnsureLima_NonInteractiveDeclinesByDefault_REQ_8_3(t *testing.T) {
	t.Parallel()

	e := &fakeEnv{brew: fakeBrew, interactive: false}
	m := e.manager(t)
	m.Confirm = func(context.Context, string) (bool, error) {
		t.Error("avar prompted with no terminal attached")
		return false, nil
	}

	_, err := m.EnsureLima(context.Background())
	if err == nil {
		t.Fatal("EnsureLima succeeded, want an error so avr exits non-zero")
	}

	var notInstalled *NotInstalledError
	if !errors.As(err, &notInstalled) {
		t.Fatalf("error is %T (%v), want *NotInstalledError", err, err)
	}
	if notInstalled.Reason != ReasonNonInteractive {
		t.Errorf("Reason = %q, want %q", notInstalled.Reason, ReasonNonInteractive)
	}
	if e.confirmCalls != 0 {
		t.Errorf("the user was asked %d times with no terminal, want 0", e.confirmCalls)
	}
	// The message has to say what to run, since nobody can answer a prompt.
	assertMessageOffersManualInstall(t, err)
	assertNoInstall(t, e)
	if len(e.calls) != 0 {
		t.Errorf("no subprocess should run at all, got %v", e.argvs())
	}
}

// REQ-8.4: an unsupported version is reported with both versions and how to
// upgrade, and avar refuses to operate against it. It does not auto-upgrade.
func TestEnsureLima_RefusesUnsupportedVersion_REQ_8_4(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		versionOut string
		wantFound  string
	}{
		{name: "0.23.2", versionOut: "limactl version 0.23.2", wantFound: "0.23.2"},
		{name: "0.9.9", versionOut: "limactl version v0.9.9", wantFound: "0.9.9"},
		{name: "pre-release of the minimum", versionOut: "limactl version 1.0.0-beta.1", wantFound: "1.0.0-beta.1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := &fakeEnv{limactl: fakeLimactl, brew: fakeBrew, versionOut: tc.versionOut, interactive: true}
			m := e.manager(t)
			m.Confirm = func(context.Context, string) (bool, error) {
				t.Error("avar offered to install over an existing installation")
				return true, nil
			}

			lima, err := m.EnsureLima(context.Background())
			if err == nil {
				t.Fatalf("EnsureLima = %+v, want an error so avr exits non-zero", lima)
			}
			if lima != (Lima{}) {
				t.Errorf("EnsureLima returned %+v alongside an error: avar must not hand back an unsupported Lima", lima)
			}

			var tooOld *VersionTooOldError
			if !errors.As(err, &tooOld) {
				t.Fatalf("error is %T (%v), want *VersionTooOldError", err, err)
			}
			if got := tooOld.Found.String(); got != tc.wantFound {
				t.Errorf("Found = %q, want %q", got, tc.wantFound)
			}
			if tooOld.Minimum != MinLima() {
				t.Errorf("Minimum = %v, want %v", tooOld.Minimum, MinLima())
			}
			// Both versions and the upgrade command, or the user cannot act.
			for _, want := range []string{tc.wantFound, MinLimaVersion, "brew upgrade lima"} {
				if !contains(err.Error(), want) {
					t.Errorf("error message does not mention %q:\n%v", want, err)
				}
			}

			assertNoInstall(t, e)
			want := []string{"output " + fakeLimactl + " --version"}
			if got := e.argvs(); !equalStrings(got, want) {
				t.Errorf("subprocess calls = %v, want only the version check %v", got, want)
			}
		})
	}
}

// Unreadable version output fails closed: avar will not guess that an
// installation it cannot identify is supported (REQ-8.4).
func TestEnsureLima_UnreadableVersionOutputIsAnError_REQ_8_4(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		versionOut string
		versionErr error
	}{
		{name: "garbage output", versionOut: "who knows what this is"},
		{name: "empty output", versionOut: ""},
		{name: "limactl cannot run", versionErr: errors.New("exit status 127")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			e := &fakeEnv{limactl: fakeLimactl, brew: fakeBrew, versionOut: tc.versionOut, versionErr: tc.versionErr, interactive: true}
			m := e.manager(t)
			m.Confirm = func(context.Context, string) (bool, error) {
				t.Error("avar offered to install over an existing installation")
				return true, nil
			}

			_, err := m.EnsureLima(context.Background())
			if err == nil {
				t.Fatal("EnsureLima succeeded, want an error so avr exits non-zero")
			}
			if !contains(err.Error(), fakeLimactl) {
				t.Errorf("error should name the limactl it could not read:\n%v", err)
			}
			assertNoInstall(t, e)
		})
	}
}

// A cancelled context aborts immediately rather than hanging, prompting, or
// starting an install.
func TestEnsureLima_CancelledContextAborts(t *testing.T) {
	t.Parallel()

	t.Run("cancelled before the check", func(t *testing.T) {
		t.Parallel()

		e := &fakeEnv{limactl: fakeLimactl, brew: fakeBrew, versionOut: "limactl version 1.0.4", interactive: true}
		m := e.manager(t)
		m.Confirm = func(context.Context, string) (bool, error) {
			t.Error("a cancelled invocation must not prompt")
			return false, nil
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if _, err := m.EnsureLima(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("EnsureLima error = %v, want context.Canceled", err)
		}
		if len(e.calls) != 0 {
			t.Errorf("no subprocess should run, got %v", e.argvs())
		}
		assertNoInstall(t, e)
	})

	t.Run("cancelled while being asked", func(t *testing.T) {
		t.Parallel()

		e := &fakeEnv{brew: fakeBrew, interactive: true}
		ctx, cancel := context.WithCancel(context.Background())
		m := e.manager(t)
		m.Confirm = func(ctx context.Context, _ string) (bool, error) {
			cancel() // Ctrl-C at the prompt.
			return false, ctx.Err()
		}

		if _, err := m.EnsureLima(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("EnsureLima error = %v, want context.Canceled", err)
		}
		assertNoInstall(t, e)
	})
}

// No avar subprocess is ever a shell or a command string: every recorded call
// is an argv whose program is a resolved path and whose arguments are separate
// elements.
func TestEnsureLima_NeverInvokesAShell(t *testing.T) {
	t.Parallel()

	e := &fakeEnv{brew: fakeBrew, versionOut: "limactl version 1.0.4", interactive: true, confirmAnswer: true}
	if _, err := e.manager(t).EnsureLima(context.Background()); err != nil {
		t.Fatalf("EnsureLima returned an unexpected error: %v", err)
	}
	if len(e.calls) == 0 {
		t.Fatal("expected the install flow to run subprocesses")
	}

	for _, c := range e.calls {
		if !filepath.IsAbs(c.argv[0]) {
			t.Errorf("program %q is not an absolute resolved path: %s", c.argv[0], c)
		}
		switch filepath.Base(c.argv[0]) {
		case "sh", "bash", "zsh", "env":
			t.Errorf("subprocess goes through a shell: %s", c)
		}
		for _, arg := range c.argv {
			if strings.ContainsAny(arg, "|&;<>$`\n") || arg == "-c" {
				t.Errorf("argument %q looks like shell syntax: %s", arg, c)
			}
			if strings.TrimSpace(arg) != arg || arg == "" {
				t.Errorf("argument %q is not a clean argv element: %s", arg, c)
			}
		}
	}
}

// Whatever the reason, the message a user is left with always says how to get
// Lima (REQ-8.3).
func TestNotInstalledError_AlwaysCarriesInstructions_REQ_8_3(t *testing.T) {
	t.Parallel()

	reasons := []NotInstalledReason{
		ReasonDeclined,
		ReasonNoHomebrew,
		ReasonNonInteractive,
		ReasonInstallFailed,
		ReasonInstallIncomplete,
		"", // a reason nobody thought of must still be actionable
	}

	for _, reason := range reasons {
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()

			err := &NotInstalledError{Reason: reason, Err: errors.New("underlying cause")}
			assertMessageOffersManualInstall(t, err)
			if !contains(err.Error(), "Lima") {
				t.Errorf("message does not name the dependency:\n%v", err)
			}
		})
	}
}

// The zero Manager must be usable: task 7/8 constructs it with only Out set.
func TestManager_ZeroValueUsesRealCollaborators(t *testing.T) {
	t.Parallel()

	var m Manager
	if m.runner() == nil {
		t.Error("runner() returned nil")
	}
	if m.confirm() == nil {
		t.Error("confirm() returned nil")
	}
	if m.interactive() == nil {
		t.Error("interactive() returned nil")
	}
	if m.lookPath() == nil {
		t.Error("lookPath() returned nil")
	}
	if got := m.fallbackDirs(); !equalStrings(got, homebrewBinDirs) {
		t.Errorf("fallbackDirs() = %v, want %v", got, homebrewBinDirs)
	}
	if m.out() != io.Discard {
		t.Errorf("out() = %v, want io.Discard", m.out())
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
