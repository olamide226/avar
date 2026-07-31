package lima

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/provider"
)

// Lima's host agent logs JSON lines — its entry point installs
// logrus.JSONFormatter — into ha.stderr.log inside the instance directory, and
// recreates the file on every start. The lines below are the shapes avar reads,
// taken from Lima 2.2.0's own call sites:
//
//	pkg/hostagent/port.go     "Forwarding TCP from %s to %s"
//	                          "Stopping forwarding TCP from %s to %s"
//	                          "Not forwarding TCP %s"
//	                          "failed to set up forwarding tcp port %d (negligible if already forwarded)"
//	pkg/portfwd/forward.go    "Forwarding %s from %s to %s"
//	pkg/portfwd/listener.go   "failed to listen %s: %s"    (+ "error" field)
//	                          "Not forwarding %s %s"       (+ "negligible-reason" field)
//
// They were not captured from a running machine: provisioning a VM is not a
// unit test, and the host these tests run on has no avar instance.

// logLine renders one host-agent log line.
func logLine(level, msg string, fields ...string) string {
	line := fmt.Sprintf(`{"level":%q,"msg":%q,"time":"2026-07-31T10:15:00+01:00"`, level, msg)
	for i := 0; i+1 < len(fields); i += 2 {
		line += fmt.Sprintf(`,%q:%q`, fields[i], fields[i+1])
	}
	return line + "}\n"
}

// runningInstance renders a `limactl list --json` line for a running avar
// machine whose instance directory is dir, so a test can put a host-agent log
// where the provider will look for it.
func runningInstance(name, dir string) []byte {
	return []byte(fmt.Sprintf(
		`{"name":%q,"status":"Running","dir":%q,"vmType":"vz","arch":"aarch64","cpus":4,"memory":8589934592,"disk":107374182400,"config":{"mounts":[]}}`+"\n",
		name, dir))
}

// withHostAgentLog gives the provider a running machine whose host-agent log
// holds the given lines, and returns it.
func withHostAgentLog(t *testing.T, lines ...string) *Provider {
	t.Helper()
	dir := t.TempDir()
	if len(lines) > 0 {
		path := filepath.Join(dir, hostAgentLog)
		if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o600); err != nil {
			t.Fatalf("writing the host agent log: %v", err)
		}
	}
	runner := newFakeRunner().listing(runningInstance(testMachine, dir))
	return newTestProvider(t, runner, newFakeRecords(ownedRecord(testMachine)))
}

func TestPortDiagnostics_ReportsAHostPortAlreadyInUse_REQ_7_2(t *testing.T) {
	p := withHostAgentLog(t,
		logLine("warning", "failed to listen tcp: 127.0.0.1:3000",
			"error", "listen tcp 127.0.0.1:3000: bind: address already in use"),
	)

	got, err := p.PortDiagnostics(context.Background(), testMachine)
	if err != nil {
		t.Fatalf("PortDiagnostics: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want one diagnostic, got %+v", got)
	}
	if got[0].GuestPort != 3000 {
		t.Errorf("guest port = %d, want 3000", got[0].GuestPort)
	}
	if got[0].Forwarded {
		t.Error("a port whose host listener failed was reported as forwarded")
	}
	// The user can act on "something else has the port"; they cannot act on a
	// syscall name.
	if !strings.Contains(got[0].Reason, "already in use") {
		t.Errorf("reason %q does not say the host port is taken", got[0].Reason)
	}
}

func TestPortDiagnostics_ReportsForwardedPorts_REQ_7_1(t *testing.T) {
	p := withHostAgentLog(t,
		logLine("info", "Forwarding TCP from 127.0.0.1:3000 to 127.0.0.1:3000"),
		logLine("info", "Forwarding TCP from 127.0.0.1:8080 to 127.0.0.1:8080"),
	)

	got, err := p.PortDiagnostics(context.Background(), testMachine)
	if err != nil {
		t.Fatalf("PortDiagnostics: %v", err)
	}
	want := []provider.PortDiagnostic{
		{GuestPort: 3000, HostPort: 3000, Forwarded: true},
		{GuestPort: 8080, HostPort: 8080, Forwarded: true},
	}
	if len(got) != len(want) {
		t.Fatalf("diagnostics = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("diagnostic %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A guest listener that closes releases its host listener (REQ-7.3), so the
// port stops being anything avar has to say something about — it is not a
// failure to report.
func TestPortDiagnostics_ForgetsAPortThatStoppedBeingForwarded_REQ_7_3(t *testing.T) {
	p := withHostAgentLog(t,
		logLine("info", "Forwarding TCP from 127.0.0.1:3000 to 127.0.0.1:3000"),
		logLine("info", "Stopping forwarding TCP from 127.0.0.1:3000 to 127.0.0.1:3000"),
	)

	got, err := p.PortDiagnostics(context.Background(), testMachine)
	if err != nil {
		t.Fatalf("PortDiagnostics: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a released port is still reported: %+v", got)
	}
}

// The latest word about a port is its current state: a conflict that was
// resolved by a retry must not stay on the user's screen for the life of the
// machine.
func TestPortDiagnostics_LaterLinesSupersedeEarlierOnes(t *testing.T) {
	p := withHostAgentLog(t,
		logLine("warning", "failed to listen tcp: 127.0.0.1:3000",
			"error", "listen tcp 127.0.0.1:3000: bind: address already in use"),
		logLine("info", "Forwarding TCP from 127.0.0.1:3000 to 127.0.0.1:3000"),
	)

	got, err := p.PortDiagnostics(context.Background(), testMachine)
	if err != nil {
		t.Fatalf("PortDiagnostics: %v", err)
	}
	if len(got) != 1 || !got[0].Forwarded {
		t.Fatalf("diagnostics = %+v, want port 3000 reported as forwarded", got)
	}
}

// avar's own transport reaches the guest over SSH, which Lima never publishes.
// Reporting it would show the user avar's plumbing as a problem (REQ-1.5).
func TestPortDiagnostics_NeverReportsTheTransportPort(t *testing.T) {
	p := withHostAgentLog(t,
		logLine("info", "Not forwarding TCP 127.0.0.1:22"),
		logLine("info", "Not forwarding TCP 127.0.0.1:60022"),
	)

	got, err := p.PortDiagnostics(context.Background(), testMachine)
	if err != nil {
		t.Fatalf("PortDiagnostics: %v", err)
	}
	for _, diag := range got {
		if diag.GuestPort == 22 {
			t.Fatalf("the guest SSH port was reported to the user: %+v", got)
		}
	}
}

func TestPortDiagnostics_ReadsBothForwardersAndIgnoresTheRest(t *testing.T) {
	p := withHostAgentLog(t,
		// The gRPC forwarder's wording, with the protocol in the verb position.
		logLine("info", "Forwarding TCP from 127.0.0.1:5173 to 127.0.0.1:5173"),
		// A UDP line: provider.PortDiagnostic describes a port, not a protocol,
		// so folding UDP in would make one entry mean two things.
		logLine("info", "Forwarding UDP from 127.0.0.1:5173 to 127.0.0.1:5173"),
		// An IPv6 listener, which Lima writes in bracketed form.
		logLine("info", "Forwarding TCP from [::1]:9229 to [::1]:9229"),
		// A privileged port Lima declines, with its own explanation attached.
		logLine("info", "Not forwarding TCP 127.0.0.1:80",
			"error", "listen tcp 127.0.0.1:80: bind: permission denied",
			"negligible-reason", "privileged port"),
		// Noise avar must survive rather than fail on.
		"this line is not JSON at all\n",
		"{\"level\":\"info\"\n",
		logLine("info", "Waiting for the essential requirement 1 of 2: \"ssh\""),
	)

	got, err := p.PortDiagnostics(context.Background(), testMachine)
	if err != nil {
		t.Fatalf("PortDiagnostics: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("diagnostics = %+v, want ports 80, 5173 and 9229", got)
	}
	if got[0].GuestPort != 80 || got[0].Forwarded {
		t.Errorf("first diagnostic = %+v, want port 80 unforwarded", got[0])
	}
	if !strings.Contains(got[0].Reason, "privileged port") {
		t.Errorf("reason %q does not carry Lima's own explanation", got[0].Reason)
	}
	if got[1].GuestPort != 5173 || !got[1].Forwarded {
		t.Errorf("second diagnostic = %+v, want port 5173 forwarded", got[1])
	}
	if got[2].GuestPort != 9229 || !got[2].Forwarded {
		t.Errorf("third diagnostic = %+v, want port 9229 forwarded", got[2])
	}
}

// The ssh-based forwarder reports the same failure in its own words.
func TestPortDiagnostics_ReadsTheSSHForwarderFailure(t *testing.T) {
	p := withHostAgentLog(t,
		logLine("warning", "failed to set up forwarding tcp port 3000 (negligible if already forwarded)",
			"error", "listen tcp 127.0.0.1:3000: bind: address already in use"),
	)

	got, err := p.PortDiagnostics(context.Background(), testMachine)
	if err != nil {
		t.Fatalf("PortDiagnostics: %v", err)
	}
	if len(got) != 1 || got[0].GuestPort != 3000 || got[0].Forwarded {
		t.Fatalf("diagnostics = %+v, want port 3000 unforwarded", got)
	}
	if !strings.Contains(got[0].Reason, "already in use") {
		t.Errorf("reason %q does not say the host port is taken", got[0].Reason)
	}
}

// A machine that has just come up may have no log yet, and one that is stopped
// forwards nothing. Neither is a failure of the query.
func TestPortDiagnostics_QuietWhenThereIsNothingToReport(t *testing.T) {
	t.Run("no log yet", func(t *testing.T) {
		p := withHostAgentLog(t)
		got, err := p.PortDiagnostics(context.Background(), testMachine)
		if err != nil {
			t.Fatalf("PortDiagnostics: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("diagnostics = %+v, want none", got)
		}
	})

	t.Run("machine stopped", func(t *testing.T) {
		runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
		p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-fedora-42-amd64")))
		got, err := p.PortDiagnostics(context.Background(), "avr-fedora-42-amd64")
		if err != nil {
			t.Fatalf("PortDiagnostics: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("a stopped machine reported forwarded ports: %+v", got)
		}
	})
}

func TestPortDiagnostics_RefusesMachinesAvarDoesNotOwn_PROP_6(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	p := newTestProvider(t, runner, newFakeRecords())

	_, err := p.PortDiagnostics(context.Background(), "default")
	if !errors.Is(err, provider.ErrNotOwned) {
		t.Fatalf("PortDiagnostics on a machine avar does not own returned %v, want ErrNotOwned", err)
	}
	if calls := runner.calls(); len(calls) != 0 {
		t.Errorf("a refused machine still caused %d limactl invocation(s): %v", len(calls), runner.argvs())
	}
}

func TestPortDiagnostics_ReportsAMachineThatDoesNotExist(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-empty.json"))
	p := newTestProvider(t, runner, newFakeRecords())

	_, err := p.PortDiagnostics(context.Background(), testMachine)
	if !errors.Is(err, provider.ErrMachineNotFound) {
		t.Fatalf("PortDiagnostics on a missing machine returned %v, want ErrMachineNotFound", err)
	}
}

// A long-lived machine's log grows without limit and `avr status` has a latency
// budget, so only the tail is read — and the partial line it starts on must not
// be mistaken for a record.
func TestReadLogTail_ReadsWholeLinesFromTheEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, hostAgentLog)
	body := logLine("info", "Forwarding TCP from 127.0.0.1:3000 to 127.0.0.1:3000")
	filler := strings.Repeat(logLine("info", "some earlier line"), 20)
	if err := os.WriteFile(path, []byte(filler+body), 0o600); err != nil {
		t.Fatalf("writing the log: %v", err)
	}

	got, err := readLogTail(path, int64(len(body)+10))
	if err != nil {
		t.Fatalf("readLogTail: %v", err)
	}
	if string(got) != body {
		t.Errorf("tail = %q, want %q", got, body)
	}

	diags := parsePortDiagnostics(got)
	if len(diags) != 1 || diags[0].GuestPort != 3000 {
		t.Errorf("the tail did not parse into the forwarded port: %+v", diags)
	}
}
