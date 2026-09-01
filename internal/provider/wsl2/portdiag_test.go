//go:build windows

// This backend's tests are Windows tests, for the same reason the Lima
// backend's are Unix tests: they assert on Windows paths, and what counts as an
// absolute path is the host's question, not avar's. `C:\Users\ola\code\app` is
// absolute on Windows and a relative path everywhere else, so path/filepath —
// and with it MapProjectPath, MountSpec.Validate and every mount the two plan —
// answers differently off Windows. Verified by cross-compiling this package's
// tests for linux/amd64 and running them under WSL: fourteen fail, all of them
// on that one difference.
//
// Prefixing a drive letter is not available as a fix here the way it was for
// the host-neutral packages. There the fixture was a stand-in for whatever the
// host calls an absolute path; here the Windows path *is* the subject.
//
// What the macOS build claims for this package is therefore that it compiles —
// which is what keeps `avr` linkable while both backends live in one binary —
// not that a backend which cannot run there passes its behaviour tests.

package wsl2

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/provider"
)

// --- Editor target --------------------------------------------------------

// REQ-18.10, PROP-17: VS Code reaches a WSL distribution through its own
// integration. There is no endpoint to configure, no host entry to write and no
// key to manage, so avar must generate none of it — and the empty SSHConfig is
// how cmd/code.go knows there is nothing to write without having to ask which
// backend answered.
func TestEditorTarget_UsesTheWSLAuthorityAndNoSSH_REQ_18_10(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	guestPath := GuestRoot(testProjectID, `C:\Users\ola\code\app`)
	target, err := p.EditorTarget(context.Background(), testMachine, guestPath)
	if err != nil {
		t.Fatalf("EditorTarget: %v", err)
	}

	if target.Authority != "wsl+"+testMachine {
		t.Errorf("Authority = %q, want wsl+%s", target.Authority, testMachine)
	}
	if target.GuestPath != guestPath {
		t.Errorf("GuestPath = %q, want the directory asked for", target.GuestPath)
	}
	if target.SSHConfig != "" {
		t.Errorf("the WSL backend produced SSH configuration:\n%s", target.SSHConfig)
	}
}

// The target describes a live endpoint, so a stopped environment is refused
// rather than started as a side effect of opening an editor.
func TestEditorTarget_RefusesAStoppedEnvironment_REQ_18_10(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	p := newProvider(t, f, recorded(testMachine))

	if _, err := p.EditorTarget(context.Background(), testMachine, "/work"); !errors.Is(err, provider.ErrMachineNotRunning) {
		t.Fatalf("error = %v, want ErrMachineNotRunning", err)
	}
}

// PROP-6: an editor is not a way around the ownership rule.
func TestEditorTarget_RefusesADistributionAvarDoesNotOwn_PROP_6(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register("Ubuntu", 2, true)
	p := newProvider(t, f, recorded())

	if _, err := p.EditorTarget(context.Background(), "Ubuntu", "/home/ola"); !errors.Is(err, provider.ErrNotOwned) {
		t.Fatalf("error = %v, want ErrNotOwned", err)
	}
}

// --- Port diagnostics -----------------------------------------------------

// /proc/net/tcp writes the local address as hexadecimal, little-endian, with a
// different layout in each address family. Only the port is read, because it is
// the part that is the same in both — and a listener bound to loopback inside
// Linux is deliberately not exposed, so reporting it as unforwarded would report
// the user's own choice as a problem.
func TestParseListeningPorts_ReadsWhatTheKernelWrites_REQ_18_9(t *testing.T) {
	t.Parallel()

	// Ports 3000 (0BB8) and 8080 (1F90) on 0.0.0.0 and ::, and 5432 (1538) on
	// 127.0.0.1 and ::1, which are not published.
	const procNetTCP = "00000000:0BB8\n" +
		"0100007F:1538\n" +
		"00000000000000000000000000000000:1F90\n" +
		"00000000000000000000000001000000:1538\n" +
		// A duplicate: the same port in both address families is one port.
		"00000000:0BB8\n" +
		// Not an address at all.
		"\n"

	got := parseListeningPorts(procNetTCP)
	want := []int{3000, 8080}
	if len(got) != len(want) {
		t.Fatalf("parseListeningPorts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseListeningPorts = %v, want %v (ordered)", got, want)
		}
	}
}

// REQ-18.9: a guest process listening on a port that nothing answers on the
// Windows side is the case worth reporting, and the report has to say what the
// user can do about it.
func TestPortDiagnostics_ReportsAPortThatIsNotPublished_REQ_18_9(t *testing.T) {
	t.Parallel()

	// A port nothing is listening on. Taking one and releasing it is how the
	// test gets a number the operating system has just confirmed is free.
	port := freePort(t)

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	f.listeners = []int{port}
	p := newProvider(t, f, recorded(testMachine))

	got, err := p.PortDiagnostics(context.Background(), testMachine)
	if err != nil {
		t.Fatalf("PortDiagnostics: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("PortDiagnostics = %v, want the one listening port", got)
	}
	if got[0].GuestPort != port {
		t.Errorf("GuestPort = %d, want %d", got[0].GuestPort, port)
	}
	if got[0].Forwarded {
		t.Error("a port nothing answers on was reported as forwarded")
	}
	if !strings.Contains(got[0].Reason, fmt.Sprint(port)) {
		t.Errorf("the reason does not name the port:\n%s", got[0].Reason)
	}
	// A diagnostic the user cannot act on is not a diagnostic.
	if !strings.Contains(got[0].Reason, ".wslconfig") {
		t.Errorf("the reason does not say what to look at:\n%s", got[0].Reason)
	}
}

// A port something does answer on is reported as forwarded, at the same number:
// WSL publishes a guest port at the port it was opened on, so there is no
// mapping to discover.
func TestPortDiagnostics_ReportsAPublishedPort_REQ_18_9(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("taking a port to answer on: %v", err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	f.listeners = []int{port}
	p := newProvider(t, f, recorded(testMachine))

	got, err := p.PortDiagnostics(context.Background(), testMachine)
	if err != nil {
		t.Fatalf("PortDiagnostics: %v", err)
	}
	if len(got) != 1 || !got[0].Forwarded {
		t.Fatalf("PortDiagnostics = %v, want the port reported as forwarded", got)
	}
	if got[0].HostPort != port {
		t.Errorf("HostPort = %d, want the same number as the guest port", got[0].HostPort)
	}
	if got[0].Reason != "" {
		t.Errorf("a forwarded port carries a reason: %q", got[0].Reason)
	}
}

// REQ-7.2: this is a diagnostic, so broken forwarding is data in the result and
// never an error — and a stopped environment has no listeners, which is an empty
// answer rather than a failure that would stop `avr status` printing the rest.
func TestPortDiagnostics_NeverFailsBecauseForwardingIsBroken_REQ_7_2(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	p := newProvider(t, f, recorded(testMachine))

	got, err := p.PortDiagnostics(context.Background(), testMachine)
	if err != nil {
		t.Fatalf("PortDiagnostics on a stopped environment: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("PortDiagnostics = %v, want nothing from a stopped environment", got)
	}

	// An environment avar has no record of is not something to report on.
	if _, err := p.PortDiagnostics(context.Background(), "Ubuntu"); !errors.Is(err, provider.ErrNotOwned) {
		t.Errorf("error = %v, want ErrNotOwned", err)
	}
}

// freePort returns a port number nothing is listening on.
func freePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing the port: %v", err)
	}
	return port
}
