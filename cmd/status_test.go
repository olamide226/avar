package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/state"
	"github.com/olamide226/avar/internal/types"
)

// These are flow tests: they drive the real command code against the in-process
// FakeProvider and assert on the provider calls it made and the text it wrote.
// No virtual machine is involved, and neither is the developer's own ~/.avr —
// the App is given a temporary state directory and the Fake directly (design
// §7).

// testApp wires an App to a fake backend and a temporary state directory, and
// captures everything the command writes.
type testApp struct {
	*App
	out   *bytes.Buffer
	err   *bytes.Buffer
	store *state.Store
}

func newTestApp(t *testing.T, p provider.Provider) *testApp {
	t.Helper()

	// The command layer picks its backend from the host and refuses a host it
	// has no backend for, so on such a host these flow tests cannot run at all:
	// every command fails in Resolve before it reaches the Fake. The skip clears
	// itself — the moment Windows routes to the WSL2Provider these run there too
	// (REQ-18.1).
	if !provider.SupportedHost(runtime.GOOS) {
		t.Skipf("avar has no backend for %s yet, so the command layer refuses before reaching the fake", runtime.GOOS)
	}

	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening a temporary state directory: %v", err)
	}

	app := &App{Version: "test", Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	app.prov, app.store = p, store
	// Mark the lazy constructors done so that neither Lima nor the real state
	// directory is ever reached from a test.
	app.once.provider.Do(func() {})
	app.once.store.Do(func() {})

	return &testApp{App: app, out: app.Out.(*bytes.Buffer), err: app.Err.(*bytes.Buffer), store: store}
}

// stdout is what the user would have seen.
func (a *testApp) stdout() string { return a.out.String() }

// ubuntu is the environment most of these tests are about.
func ubuntu() types.EnvironmentSelector {
	return types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.HostArch()}
}

const ubuntuMachine = "avr-ubuntu-24.04-test"

// seedMachine gives the Fake a running machine sharing the given project
// directories, and clears the recording so that only the command under test's
// own calls are asserted on.
func seedMachine(t *testing.T, f *fake.Fake, name string, selector types.EnvironmentSelector, kind types.MachineKind, projects ...string) {
	t.Helper()
	mounts := make([]types.MountSpec, 0, len(projects))
	for i, dir := range projects {
		mount, _, err := f.MapProjectPath(strings.Repeat("a", 10)+string(rune('0'+i)), dir, dir)
		if err != nil {
			t.Fatalf("planning the mount for %s: %v", dir, err)
		}
		mounts = append(mounts, mount)
	}
	spec := provider.MachineSpec{Name: name, Selector: selector, Kind: kind, Mounts: mounts}
	if err := f.EnsureMachine(context.Background(), spec, types.DiscardProgress); err != nil {
		t.Fatalf("seeding machine %s: %v", name, err)
	}
	f.Reset()
}

func mustRunStatus(t *testing.T, app *testApp) {
	t.Helper()
	if err := runStatus(context.Background(), app.App, cli.Invocation{Mode: cli.ModeSubcommand, Subcommand: "status"}); err != nil {
		t.Fatalf("avr status: %v", err)
	}
}

// A user who has just installed avar and typed `avr status` has done nothing
// wrong. They get the sentence that gets them a Linux shell, not an error and
// not an empty table.
func TestStatus_EmptyStateExplainsHowToStart_REQ_5_3(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)

	mustRunStatus(t, app)

	out := app.stdout()
	for _, want := range []string{"not managing any Linux environments", "Run `avr`"} {
		if !strings.Contains(out, want) {
			t.Errorf("the empty-state message does not contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ENVIRONMENT") {
		t.Errorf("an empty table was printed instead of onboarding text:\n%s", out)
	}
	if app.err.Len() != 0 {
		t.Errorf("the first-run case wrote to stderr, which reads like a failure: %q", app.err.String())
	}
	f.AssertOps(t, fake.OpStatus)
}

func TestStatus_ShowsEnvironmentStateResourcesModeAndMounts_REQ_5_1(t *testing.T) {
	project := t.TempDir()
	f := fake.New()
	seedMachine(t, f, ubuntuMachine, ubuntu(), types.KindShared, project)
	app := newTestApp(t, f)

	mustRunStatus(t, app)

	out := app.stdout()
	want := []string{
		"Ubuntu 24.04",             // environment label (REQ-5.1)
		string(types.StateRunning), // state
		"shared",                   // mode
		"4",                        // CPU allocation
		"8 GB",                     // memory allocation
		"100 GB",                   // disk
		project,                    // registered mount
		"SESSIONS",                 // live session count column
	}
	for _, fragment := range want {
		if !strings.Contains(out, fragment) {
			t.Errorf("`avr status` does not report %q:\n%s", fragment, out)
		}
	}
	// The guest path differs from the host path on this backend, so status has
	// to say where the directory lands inside Linux (REQ-18.5).
	if !strings.Contains(out, fake.GuestProjectRoot) {
		t.Errorf("a mount whose guest path differs was shown as if the paths were the same:\n%s", out)
	}
}

// PROP-6, in the command layer: what the backend hands over is filtered again
// before anything is shown, so a machine the user created themselves cannot
// appear under avar's name even if a backend listed it.
func TestStatus_NeverShowsAMachineAvarDoesNotOwn_PROP_6(t *testing.T) {
	t.Run("a backend that listed one anyway", func(t *testing.T) {
		got := avarOwned([]types.MachineStatus{
			{Name: "colima", State: types.StateRunning},
			{Name: ubuntuMachine, State: types.StateRunning},
			{Name: "default", State: types.StateStopped},
		})
		if len(got) != 1 || got[0].Name != ubuntuMachine {
			t.Fatalf("machines outside avar's namespace survived the filter: %+v", got)
		}
	})

	t.Run("end to end", func(t *testing.T) {
		f := fake.New()
		f.AddForeignMachine("colima")
		seedMachine(t, f, ubuntuMachine, ubuntu(), types.KindShared)
		app := newTestApp(t, f)

		mustRunStatus(t, app)

		if strings.Contains(app.stdout(), "colima") {
			t.Errorf("a machine the user created themselves was listed:\n%s", app.stdout())
		}
		for _, call := range f.Calls() {
			if call.Machine == "colima" {
				t.Errorf("`avr status` reached a machine avar does not own: %s", call)
			}
		}
	})
}

// A machine avar created but never finished recording — what a crash mid-create
// leaves behind — must stay visible, because seeing it is how the user (and
// reconciliation) learns it is there (design §3.5, PROP-6).
func TestStatus_ShowsAnOrphanItCannotDescribe_PROP_6(t *testing.T) {
	entries := []statusEntry{{
		Machine: types.MachineStatus{
			Name:     "avr-prj-3fa9c2b1d0-ubuntu-24.04-arm64",
			Provider: types.ProviderLima,
			Selector: types.EnvironmentSelector{Isolated: true, Arch: types.ArchARM64},
			State:    types.StateRunning,
		},
	}}

	var buf bytes.Buffer
	writeStatus(&buf, entries)
	out := buf.String()

	if !strings.Contains(out, "avr-prj-3fa9c2b1d0-ubuntu-24.04-arm64") {
		t.Errorf("an orphan avar cannot describe was not named at all:\n%s", out)
	}
	if strings.Contains(out, " · arm64") {
		t.Errorf("a half-empty selector was rendered as if it were an environment label:\n%s", out)
	}
}

// A field the backend could not report must read as "not known", never as a
// zero that looks like a real allocation.
func TestStatus_RendersUnknownFieldsSanely_REQ_5_1(t *testing.T) {
	var buf bytes.Buffer
	writeStatus(&buf, []statusEntry{{
		Machine: types.MachineStatus{Name: "avr-ubuntu-24.04-arm64", Selector: ubuntu()},
	}})
	out := buf.String()

	if strings.Contains(out, "0 GB") || strings.Contains(out, "\t0\t") {
		t.Errorf("an unknown allocation was rendered as zero:\n%s", out)
	}
	if strings.Count(out, unknownValue) < 3 {
		t.Errorf("unknown CPU, memory and disk were not all reported as unknown:\n%s", out)
	}
	if !strings.Contains(out, unknownValue+"  "+unknownValue) && !strings.Contains(out, unknownValue) {
		t.Errorf("the table did not render at all:\n%s", out)
	}
}

func TestStatus_CountsLiveSessions_REQ_5_1(t *testing.T) {
	f := fake.New()
	seedMachine(t, f, ubuntuMachine, ubuntu(), types.KindShared)
	app := newTestApp(t, f)

	if err := app.store.PutMachine(types.MachineRecord{
		Name:     ubuntuMachine,
		Provider: fake.ProviderID,
		Selector: ubuntu(),
		Kind:     types.KindShared,
	}); err != nil {
		t.Fatalf("recording the machine: %v", err)
	}
	// This process is the live session: a pid that is not running is pruned on
	// read, which is exactly what makes the count trustworthy.
	if err := app.store.AddSession(types.SessionRecord{Machine: ubuntuMachine, PID: os.Getpid(), StartedAt: time.Now()}); err != nil {
		t.Fatalf("recording the session: %v", err)
	}

	mustRunStatus(t, app)

	line := machineLine(t, app.stdout(), "Ubuntu 24.04")
	if !strings.HasSuffix(strings.TrimSpace(line), "1") {
		t.Errorf("the live session was not counted on the machine's line: %q", line)
	}
}

// REQ-7.2: a guest port the host could not publish is not allowed to break the
// session, and has to be discoverable here.
func TestStatus_ReportsAPortTheHostCouldNotPublish_REQ_7_2(t *testing.T) {
	f := fake.New()
	seedMachine(t, f, ubuntuMachine, ubuntu(), types.KindShared)
	f.SetPortDiagnostics(ubuntuMachine, []provider.PortDiagnostic{
		{GuestPort: 3000, HostPort: 3000, Forwarded: true},
		{GuestPort: 8080, Forwarded: false, Reason: "host port 8080 is already in use by another program on the host"},
	})
	app := newTestApp(t, f)

	mustRunStatus(t, app)

	out := app.stdout()
	if !strings.Contains(out, "3000 → localhost:3000") {
		t.Errorf("a forwarded port was not shown:\n%s", out)
	}
	if !strings.Contains(out, "already in use") {
		t.Errorf("the port conflict is not discoverable in `avr status`:\n%s", out)
	}
	f.AssertOpsInOrder(t, fake.OpStatus, fake.OpPortDiagnostics)
}

// A machine that is not running is forwarding nothing, so asking it about ports
// is a subprocess spent on an answer that cannot be interesting (REQ-17.1).
func TestStatus_DoesNotAskAStoppedMachineAboutPorts(t *testing.T) {
	f := fake.New()
	seedMachine(t, f, ubuntuMachine, ubuntu(), types.KindShared)
	f.SetMachineState(ubuntuMachine, types.StateStopped)
	app := newTestApp(t, f)

	mustRunStatus(t, app)

	f.AssertNotCalled(t, fake.OpPortDiagnostics)
}

// Port diagnostics are an optional capability. A backend that cannot explain
// forwarding must cost the user the ports line and nothing else.
func TestStatus_WorksWithABackendThatCannotDiagnosePorts_REQ_7_2(t *testing.T) {
	f := fake.New()
	seedMachine(t, f, ubuntuMachine, ubuntu(), types.KindShared)
	app := newTestApp(t, f)
	app.prov = coreOnly{f}

	mustRunStatus(t, app)

	if !strings.Contains(app.stdout(), "Ubuntu 24.04") {
		t.Errorf("a backend without port diagnostics lost the whole listing:\n%s", app.stdout())
	}
	f.AssertNotCalled(t, fake.OpPortDiagnostics)
}

// A diagnostics query that fails is shown on that machine's line rather than
// failing the command: a listing that dies over one unreadable port log is
// worse than one that says so.
func TestStatus_ReportsAFailedPortQueryWithoutLosingTheListing(t *testing.T) {
	f := fake.New()
	seedMachine(t, f, ubuntuMachine, ubuntu(), types.KindShared)
	f.FailOn(fake.OpPortDiagnostics, errPortLogUnreadable)
	app := newTestApp(t, f)

	mustRunStatus(t, app)

	out := app.stdout()
	if !strings.Contains(out, "Ubuntu 24.04") {
		t.Errorf("the listing was lost because one port query failed:\n%s", out)
	}
	if !strings.Contains(out, errPortLogUnreadable.Error()) {
		t.Errorf("the failed port query was swallowed:\n%s", out)
	}
}

func TestStatus_RejectsArgumentsItDoesNotUnderstand(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)

	err := runStatus(context.Background(), app.App, cli.Invocation{
		Mode: cli.ModeSubcommand, Subcommand: "status", SubcommandArgs: []string{"--all"},
	})
	assertExitCode(t, err, exitUsage)
	f.AssertNotCalled(t, fake.OpStatus)
}

// errPortLogUnreadable stands in for a backend that cannot read its own
// forwarding log — a permission problem, a log that was rotated away.
var errPortLogUnreadable = errors.New("reading Lima's port-forwarding log: permission denied")

// coreOnly wraps a backend so that only provider.Provider is satisfied, which
// is how a test reaches the path where an optional capability is absent. It
// forwards deliberately rather than embedding: embedding would promote the
// capability methods and there would be nothing left to prove.
type coreOnly struct{ p provider.Provider }

func (c coreOnly) ID() types.ProviderID { return c.p.ID() }

func (c coreOnly) MapProjectPath(projectID, hostRoot, hostCwd string) (types.MountSpec, string, error) {
	return c.p.MapProjectPath(projectID, hostRoot, hostCwd)
}

func (c coreOnly) EnsureMachine(ctx context.Context, spec provider.MachineSpec, progress types.ProgressSink) error {
	return c.p.EnsureMachine(ctx, spec, progress)
}

func (c coreOnly) Shell(ctx context.Context, machine string, opts provider.ShellOpts) (int, error) {
	return c.p.Shell(ctx, machine, opts)
}

func (c coreOnly) AppliedMounts(ctx context.Context, machine string) ([]types.MountSpec, error) {
	return c.p.AppliedMounts(ctx, machine)
}

func (c coreOnly) SetMounts(ctx context.Context, machine string, mounts []types.MountSpec, progress types.ProgressSink) error {
	return c.p.SetMounts(ctx, machine, mounts, progress)
}

func (c coreOnly) Stop(ctx context.Context, machine string, progress types.ProgressSink) error {
	return c.p.Stop(ctx, machine, progress)
}

func (c coreOnly) Delete(ctx context.Context, machine string) error { return c.p.Delete(ctx, machine) }

func (c coreOnly) Status(ctx context.Context) ([]types.MachineStatus, error) {
	return c.p.Status(ctx)
}

// assertExitCode fails unless err carries the expected process exit code, which
// is how avar distinguishes "could not read your command line" from "the
// operation failed" (design §3.1).
func assertExitCode(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("the command succeeded, want exit code %d", want)
	}
	var exit *ExitCodeError
	if !errors.As(err, &exit) {
		t.Fatalf("the command returned %v, want an error carrying exit code %d", err, want)
	}
	if exit.Code != want {
		t.Fatalf("exit code = %d, want %d", exit.Code, want)
	}
}

// machineLine returns the table row mentioning fragment.
func machineLine(t *testing.T, out, fragment string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, fragment) {
			return line
		}
	}
	t.Fatalf("no line mentioning %q in:\n%s", fragment, out)
	return ""
}

// hostPath renders a POSIX-shaped test path in the host's own vocabulary.
//
// A project directory is absolute in the host's syntax by definition, so a
// fixture that hard-codes "/Users/ola/code/app" is a macOS fixture: on Windows
// that string is relative and the resolver is right to refuse it. Prefixing the
// drive keeps each flow test testing the flow it was written for rather than the
// platform's idea of "absolute" (REQ-18.13).
func hostPath(posix string) string {
	if runtime.GOOS != "windows" {
		return posix
	}
	return `C:` + filepath.FromSlash(posix)
}
