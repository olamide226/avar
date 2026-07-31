//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// freePort returns a host TCP port that nothing is listening on right now.
// The returned port is free only at the instant of return — the caller must
// occupy it before something else does.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// detach starts argv in the guest so that it survives the avr invocation.
// It uses nohup and backgrounding through a shell, which is the simplest way
// to make a one-shot command leave a process behind.
func detach(t *testing.T, dir string, argv ...string) {
	t.Helper()

	// Assemble a shell command that background-detaches argv.
	var b strings.Builder
	b.WriteString("nohup")
	for _, a := range argv {
		b.WriteString(" ")
		b.WriteString(a)
	}
	b.WriteString(" > /dev/null 2>&1 &")
	_, stderr, code := avr(t, dir, nil, "sh", "-c", b.String())
	if code != 0 {
		t.Fatalf("start detached guest process %v: exit %d\nstderr:\n%s", argv, code, stderr)
	}
}

// killGuest kills every guest process whose full command line matches the
// pattern, through pkill -f. A mismatch is harmless (pkill exits 0 or 1), so
// this is safe to call on cleanup even when the process may already be dead.
func killGuest(t *testing.T, dir string, pattern string) {
	t.Helper()
	avr(t, dir, nil, "sh", "-c", "pkill -f "+pattern+" || true")
}

// ---------------------------------------------------------------------------
// REQ-7.1 — a guest TCP listener is reachable from the host at the same port

func TestAvr_GuestPortReachableFromHost_REQ_7_1(t *testing.T) {
	dir := project(t, "ports-forward")
	port := freePort(t)

	// Ensure the machine is running — a no-op when already warm.
	if _, _, code := avr(t, dir, nil, "true"); code != 0 {
		t.Fatalf("warm-up failed")
	}

	// Start a Python HTTP server in the guest, detached.
	detach(t, dir, "python3", "-m", "http.server", fmt.Sprint(port), "--bind", "127.0.0.1")
	t.Cleanup(func() { killGuest(t, dir, fmt.Sprintf("'http.server %d'", port)) })

	// Lima's host agent needs a moment to notice the guest listener and open a
	// host-side port.
	waitForPort(t, port, 15*time.Second)

	// Connect from the host — the proof that forwarding worked.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("host could not reach guest server at 127.0.0.1:%d: %v", port, err)
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		t.Errorf("unexpected HTTP status %d from guest server on port %d", resp.StatusCode, port)
	}
}

// ---------------------------------------------------------------------------
// REQ-7.3 — when the guest process stops, the host listener is released

func TestAvr_HostPortReleasedAfterGuestStop_REQ_7_3(t *testing.T) {
	dir := project(t, "ports-release")
	port := freePort(t)

	if _, _, code := avr(t, dir, nil, "true"); code != 0 {
		t.Fatalf("warm-up failed")
	}

	// Start a guest server and confirm the host can reach it.
	detach(t, dir, "python3", "-m", "http.server", fmt.Sprint(port), "--bind", "127.0.0.1")
	waitForPort(t, port, 15*time.Second)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("host could not reach guest server at 127.0.0.1:%d: %v", port, err)
	}
	resp.Body.Close()

	// Kill the guest server.
	killGuest(t, dir, fmt.Sprintf("'http.server %d'", port))

	// Wait for Lima's host agent to release the host-side listener and prove
	// the port is genuinely free: if avar can't bind, something still holds it.
	waitForPortFree(t, port, 15*time.Second)
}

// ---------------------------------------------------------------------------
// REQ-7.2 — when the host port is already occupied, the session continues and
// the conflict is discoverable through avr status

func TestAvr_ConflictReportedWhenHostPortTaken_REQ_7_2(t *testing.T) {
	dir := project(t, "ports-conflict")
	port := freePort(t)

	if _, _, code := avr(t, dir, nil, "true"); code != 0 {
		t.Fatalf("warm-up failed")
	}

	// Occupy the host port first.
	occupy, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("start host listener on 127.0.0.1:%d: %v", port, err)
	}
	defer occupy.Close()

	// Start a guest server on the same port. The guest binds inside the VM
	// where 127.0.0.1:PORT is available, so the server starts fine, but Lima's
	// host agent cannot open the host-side listener. The invocation must
	// succeed — a forwarding conflict must not break the session.
	detach(t, dir, "python3", "-m", "http.server", fmt.Sprint(port), "--bind", "127.0.0.1")
	t.Cleanup(func() { killGuest(t, dir, fmt.Sprintf("'http.server %d'", port)) })

	// Give the host agent time to attempt forwarding and write to its log.
	time.Sleep(5 * time.Second)

	// The conflict must be discoverable through avr status.
	stdout, stderr, code := avr(t, dir, nil, "status")
	if code != 0 {
		t.Fatalf("avr status exited %d\nstderr:\n%s", code, stderr)
	}

	portStr := fmt.Sprint(port)
	combined := stdout + stderr
	if !strings.Contains(combined, portStr) {
		t.Errorf("avr status output does not mention port %d:\n%s", port, combined)
	}
	if !strings.Contains(combined, "already in use") {
		t.Errorf("avr status does not explain port %d as already in use:\n%s", port, combined)
	}

	// Log the relevant host-agent lines verbatim so the report can confirm
	// whether the parser matched reality.
	t.Logf("--- raw avr status output for port %d ---\n%s", port, combined)
}

// ---------------------------------------------------------------------------
// Helpers

// waitForPort polls 127.0.0.1:port until a TCP connection succeeds or the
// deadline passes.
func waitForPort(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("port 127.0.0.1:%d did not become reachable within %v", port, timeout)
}

// waitForPortFree polls until a net.Listen on the port succeeds, proving the
// port is no longer occupied, or the deadline passes.
func waitForPortFree(t *testing.T, port int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			l.Close()
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("port 127.0.0.1:%d was still occupied after the guest server stopped", port)
}
