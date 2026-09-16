package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/types"
)

// These are flow tests against the in-process FakeProvider: they assert what
// `avr ports` and `avr open` asked the backend, what they wrote, and — for
// `open` — what they would have handed to the browser, through a recording
// double rather than a real one.

func portsInvocation(args ...string) cli.Invocation {
	return cli.Invocation{Mode: cli.ModeSubcommand, Subcommand: "ports", SubcommandArgs: args}
}

func openInvocation(args ...string) cli.Invocation {
	return cli.Invocation{Mode: cli.ModeSubcommand, Subcommand: "open", SubcommandArgs: args}
}

// recordingBrowser is the Opener a flow test hands the App, so that `avr open`
// is proven to open the right address without opening anything.
type recordingBrowser struct {
	opened []string
	err    error
}

func (b *recordingBrowser) Open(_ context.Context, address string) error {
	b.opened = append(b.opened, address)
	return b.err
}

// unexpectedBrowser is every test App's browser until withBrowser replaces it,
// so a test that reaches the host's browser fails rather than opening a window
// on the developer's screen.
type unexpectedBrowser struct{ t *testing.T }

func (b unexpectedBrowser) Open(_ context.Context, address string) error {
	b.t.Errorf("a test asked the host's real browser to open %s; use withBrowser", address)
	return errors.New("no real browser in tests")
}

// withBrowser gives the test App a recording browser.
func withBrowser(app *testApp) *recordingBrowser {
	b := &recordingBrowser{}
	app.browser = b
	return b
}

// fedora is a second environment for the tests that need another one running.
func fedora() types.EnvironmentSelector {
	return types.EnvironmentSelector{Distro: types.DistroFedora, Version: "43", Arch: types.HostArch()}
}

const fedoraMachine = "avr-fedora-43-test"

// devServer is a forwarded port with its guest process known, and dbConflict
// one listening in Linux whose host port was taken.
var (
	devServer  = provider.PortDiagnostic{GuestPort: 3000, HostPort: 3000, Forwarded: true, PID: 812, Process: "node server.js"}
	dbConflict = provider.PortDiagnostic{GuestPort: 5432, Reason: "host port 5432 is already in use by another program on the host"}
)

func TestPorts_ListsForwardedWithProcess_REQ_16_1(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	target, label := resolvedTarget(t, app)
	seedMachine(t, f, target, ubuntu(), types.KindShared)
	f.SetPortDiagnostics(target, []provider.PortDiagnostic{devServer, dbConflict})

	if err := runPorts(context.Background(), app.App, portsInvocation()); err != nil {
		t.Fatalf("avr ports: %v", err)
	}

	// One listing to find the environment, then its ports — nothing started,
	// nothing created.
	f.AssertOps(t, fake.OpStatus, fake.OpPortDiagnostics)
	if call := f.AssertCalled(t, fake.OpPortDiagnostics); call.Machine != target {
		t.Errorf("asked for the ports of %s, want the resolved target %s", call.Machine, target)
	}

	out := app.stdout()
	for _, want := range []string{
		label,
		"http://localhost:3000",
		"node server.js (pid 812)",
		// A port listening in Linux that the user cannot reach is exactly what
		// they may be looking for, so it is shown with the reason (REQ-7.2).
		"5432",
		"already in use",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("`avr ports` output does not contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "http://localhost:5432") {
		t.Errorf("an unreachable port was offered as an address to open:\n%s", out)
	}
	if strings.Contains(out, target) {
		t.Errorf("`avr ports` showed the user a machine name (REQ-1.5):\n%s", out)
	}
}

// The command is read-only: an environment that is not running forwards
// nothing, and finding that out must not start it.
func TestPorts_NeverStartsOrCreatesAnEnvironment_REQ_16_1(t *testing.T) {
	t.Run("stopped", func(t *testing.T) {
		f := fake.New()
		app := newTestApp(t, f)
		target, label := resolvedTarget(t, app)
		seedMachine(t, f, target, ubuntu(), types.KindShared)
		f.SetMachineState(target, types.StateStopped)

		if err := runPorts(context.Background(), app.App, portsInvocation()); err != nil {
			t.Fatalf("avr ports: %v", err)
		}
		f.AssertOps(t, fake.OpStatus)
		if out := app.stdout(); !strings.Contains(out, label+" is not running") || !strings.Contains(out, "Run `avr`") {
			t.Errorf("`avr ports` did not explain the stopped environment:\n%s", out)
		}
	})

	t.Run("absent", func(t *testing.T) {
		f := fake.New()
		app := newTestApp(t, f)

		if err := runPorts(context.Background(), app.App, portsInvocation()); err != nil {
			t.Fatalf("avr ports: %v", err)
		}
		f.AssertOps(t, fake.OpStatus)
		if out := app.stdout(); !strings.Contains(out, "no ") || !strings.Contains(out, "environment yet") {
			t.Errorf("`avr ports` did not explain that there is no environment:\n%s", out)
		}
	})
}

func TestPorts_SaysSoWhenNothingIsListening_REQ_16_1(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	target, label := resolvedTarget(t, app)
	seedMachine(t, f, target, ubuntu(), types.KindShared)

	if err := runPorts(context.Background(), app.App, portsInvocation()); err != nil {
		t.Fatalf("avr ports: %v", err)
	}
	out := app.stdout()
	if !strings.Contains(out, label+" is running, but nothing inside it is listening") {
		t.Errorf("an empty listing does not say what it means:\n%s", out)
	}
	if strings.Contains(out, "PORT") {
		t.Errorf("an empty table was printed:\n%s", out)
	}
}

// --all covers every running environment and skips the stopped ones, the way
// `avr stop --all` covers every environment.
func TestPorts_AllListsEveryRunningEnvironment_REQ_16_1(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	seedMachine(t, f, ubuntuMachine, ubuntu(), types.KindShared)
	seedMachine(t, f, fedoraMachine, fedora(), types.KindShared)
	seedMachine(t, f, "avr-debian-13-test", types.EnvironmentSelector{Distro: types.DistroDebian, Version: "13", Arch: types.HostArch()}, types.KindShared)
	f.SetMachineState("avr-debian-13-test", types.StateStopped)
	f.SetPortDiagnostics(ubuntuMachine, []provider.PortDiagnostic{devServer})
	f.SetPortDiagnostics(fedoraMachine, []provider.PortDiagnostic{{GuestPort: 8080, HostPort: 8080, Forwarded: true}})

	if err := runPorts(context.Background(), app.App, portsInvocation("--all")); err != nil {
		t.Fatalf("avr ports --all: %v", err)
	}

	f.AssertCallCount(t, fake.OpPortDiagnostics, 2)
	for _, call := range f.CallsFor(fake.OpPortDiagnostics) {
		if call.Machine == "avr-debian-13-test" {
			t.Errorf("asked a stopped environment for its ports")
		}
	}
	out := app.stdout()
	for _, want := range []string{ubuntu().Label(), "http://localhost:3000", fedora().Label(), "http://localhost:8080"} {
		if !strings.Contains(out, want) {
			t.Errorf("`avr ports --all` output does not contain %q:\n%s", want, out)
		}
	}
}

// One environment whose ports cannot be read does not hide another's, and the
// command does not report success.
func TestPorts_AllReportsAFailureAndListsTheRest_REQ_16_1(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	seedMachine(t, f, fedoraMachine, fedora(), types.KindShared)
	seedMachine(t, f, ubuntuMachine, ubuntu(), types.KindShared)
	f.SetPortDiagnostics(ubuntuMachine, []provider.PortDiagnostic{devServer})
	// Status lists by name, so the Fedora machine is asked first.
	f.FailNextOn(fake.OpPortDiagnostics, errors.New("reading the listening ports inside machine: exit status 255"))

	err := runPorts(context.Background(), app.App, portsInvocation("--all"))
	if err == nil || !strings.Contains(err.Error(), "exit status 255") {
		t.Fatalf("avr ports --all = %v, want the underlying failure reported", err)
	}
	if out := app.stdout(); !strings.Contains(out, "http://localhost:3000") {
		t.Errorf("the environment that could be read was not listed:\n%s", out)
	}
}

func TestPorts_RejectsUnknownArguments_REQ_16_1(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)

	err := runPorts(context.Background(), app.App, portsInvocation("3000"))
	assertExitCode(t, err, exitUsage)
	if len(f.Calls()) != 0 {
		t.Errorf("a command line avar could not read still reached the backend: %v", f.Ops())
	}
}

func TestOpen_OpensAForwardedPortInTheBrowser_REQ_16_2(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	browser := withBrowser(app)
	target, _ := resolvedTarget(t, app)
	seedMachine(t, f, target, ubuntu(), types.KindShared)
	f.SetPortDiagnostics(target, []provider.PortDiagnostic{devServer, dbConflict})

	if err := runOpen(context.Background(), app.App, openInvocation("3000")); err != nil {
		t.Fatalf("avr open 3000: %v", err)
	}

	if len(browser.opened) != 1 || browser.opened[0] != "http://localhost:3000" {
		t.Fatalf("opened %q, want exactly http://localhost:3000", browser.opened)
	}
	f.AssertOps(t, fake.OpStatus, fake.OpPortDiagnostics)
	if out := app.stdout(); !strings.Contains(out, "http://localhost:3000") || !strings.Contains(out, "node server.js") {
		t.Errorf("`avr open` did not say what it opened and what serves it:\n%s", out)
	}
}

// REQ-16.2's second half: a port that is not forwarded is reported, and nothing
// is opened — a browser tab showing "connection refused" is the failure this
// command exists to spare the user.
func TestOpen_SaysSoWhenThePortIsNotForwarded_REQ_16_2(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, f *fake.Fake, target string)
		port  string
		want  []string
	}{
		{
			name: "nothing listening",
			setup: func(_ *testing.T, f *fake.Fake, target string) {
				f.SetPortDiagnostics(target, []provider.PortDiagnostic{devServer})
			},
			port: "8080",
			want: []string{"port 8080 is not forwarded", "nothing inside it is listening", "avr ports"},
		},
		{
			name: "nothing listening, another environment running",
			setup: func(_ *testing.T, f *fake.Fake, target string) {
				seedMachine(t, f, fedoraMachine, fedora(), types.KindShared)
			},
			port: "8080",
			want: []string{"port 8080 is not forwarded", "avr ports --all", "1 other running environment"},
		},
		{
			name: "listening but unreachable",
			setup: func(_ *testing.T, f *fake.Fake, target string) {
				f.SetPortDiagnostics(target, []provider.PortDiagnostic{dbConflict})
			},
			port: "5432",
			want: []string{"port 5432 is listening", "not reachable from this computer", "already in use"},
		},
		{
			name:  "environment stopped",
			setup: func(_ *testing.T, f *fake.Fake, target string) { f.SetMachineState(target, types.StateStopped) },
			port:  "3000",
			want:  []string{"port 3000 is not forwarded", "is not running", "avr open 3000"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fake.New()
			app := newTestApp(t, f)
			browser := withBrowser(app)
			target, _ := resolvedTarget(t, app)
			seedMachine(t, f, target, ubuntu(), types.KindShared)
			tc.setup(t, f, target)

			err := runOpen(context.Background(), app.App, openInvocation(tc.port))
			if err == nil {
				t.Fatal("avr open succeeded for a port that is not forwarded")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not contain %q:\n%v", want, err)
				}
			}
			if len(browser.opened) != 0 {
				t.Errorf("opened %q for a port that is not forwarded", browser.opened)
			}
			f.AssertNotCalled(t, fake.OpEnsureMachine)
		})
	}
}

func TestOpen_AbsentEnvironmentIsNotCreated_REQ_16_2(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	browser := withBrowser(app)

	err := runOpen(context.Background(), app.App, openInvocation("3000"))
	if err == nil || !strings.Contains(err.Error(), "port 3000 is not forwarded") {
		t.Fatalf("avr open = %v, want a not-forwarded error", err)
	}
	f.AssertOps(t, fake.OpStatus)
	if len(browser.opened) != 0 {
		t.Errorf("opened %q", browser.opened)
	}
}

func TestOpen_RejectsAnythingButOnePortNumber_REQ_16_2(t *testing.T) {
	for _, args := range [][]string{nil, {"abc"}, {"0"}, {"65536"}, {"-1"}, {"3000", "8080"}, {"http://localhost:3000"}} {
		f := fake.New()
		app := newTestApp(t, f)
		browser := withBrowser(app)

		err := runOpen(context.Background(), app.App, openInvocation(args...))
		assertExitCode(t, err, exitUsage)
		if len(f.Calls()) != 0 || len(browser.opened) != 0 {
			t.Errorf("avr open %q reached the backend or the browser", args)
		}
	}
}

// A browser that cannot be opened is avar's failure to report, not a success.
func TestOpen_ReportsABrowserThatCannotBeOpened_REQ_16_2(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	browser := withBrowser(app)
	browser.err = errors.New("opening http://localhost:3000 in your browser: exit status 1")
	target, _ := resolvedTarget(t, app)
	seedMachine(t, f, target, ubuntu(), types.KindShared)
	f.SetPortDiagnostics(target, []provider.PortDiagnostic{devServer})

	err := runOpen(context.Background(), app.App, openInvocation("3000"))
	if err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("avr open = %v, want the browser failure", err)
	}
	if strings.Contains(app.stdout(), "Opened") {
		t.Errorf("claimed to have opened the page:\n%s", app.stdout())
	}
}

// The port a user types is the one in the address bar: the host side of the
// forward, which a backend publishing at the same number may leave unset.
func TestFindPort_MatchesTheHostSide_REQ_16_2(t *testing.T) {
	t.Parallel()

	diagnostics := []provider.PortDiagnostic{
		{GuestPort: 80, HostPort: 8080, Forwarded: true},
		{GuestPort: 3000, Forwarded: true},
		{GuestPort: 5432, Reason: "taken"},
	}
	cases := []struct {
		port      int
		found     bool
		guestPort int
	}{
		{8080, true, 80},
		{80, false, 0},
		{3000, true, 3000},
		{5432, true, 5432},
		{9999, false, 0},
	}
	for _, tc := range cases {
		got, found := findPort(diagnostics, tc.port)
		if found != tc.found || (found && got.GuestPort != tc.guestPort) {
			t.Errorf("findPort(%d) = %+v, %t; want guest port %d, %t", tc.port, got, found, tc.guestPort, tc.found)
		}
	}
}

func TestProcessLabel_REQ_16_1(t *testing.T) {
	t.Parallel()

	long := "java -Xmx4g -Dspring.profiles.active=local -jar build/libs/application-0.0.1-SNAPSHOT.jar"
	cases := []struct {
		diag provider.PortDiagnostic
		want string
	}{
		{provider.PortDiagnostic{}, unknownValue},
		{provider.PortDiagnostic{PID: 42}, "pid 42"},
		{provider.PortDiagnostic{Process: "node server.js"}, "node server.js"},
		{provider.PortDiagnostic{PID: 42, Process: "node server.js"}, "node server.js (pid 42)"},
		{provider.PortDiagnostic{PID: 7, Process: long}, "java -Xmx4g -Dspring.profiles.active=local -jar build/libs/… (pid 7)"},
	}
	for _, tc := range cases {
		if got := processLabel(tc.diag); got != tc.want {
			t.Errorf("processLabel(%+v) = %q, want %q", tc.diag, got, tc.want)
		}
	}
}
