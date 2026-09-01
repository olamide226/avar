package wsl2

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olamide226/avar/internal/provider"
)

// What avar can and cannot say about a forwarded port on Windows.
//
// Forwarding itself is not something avar does. A guest process listens, and WSL
// publishes that port on the Windows loopback address by itself; when the
// process stops listening the publication goes away the same way (REQ-7.1,
// REQ-7.3, REQ-18.9). There is nothing to call — only something to report, and
// the reason to report it is the case where a port a developer is expecting is
// not there.
//
// avar therefore asks two questions and joins the answers. Inside the guest:
// which TCP ports have a listener. From Windows: does anything answer on the
// same port on the loopback address. A port listening in the guest with nothing
// answering on the host is the actionable case, and it is what `avr status`
// shows.
//
// One limit is worth stating plainly rather than papering over. When something
// *does* answer on the host, avar cannot tell from outside whether that is WSL's
// own relay publishing the guest's port or a Windows program that took the port
// first — both look identical to a connection attempt. Distinguishing them means
// enumerating Windows listeners with their owning process, which belongs with
// `avr ports` and its process attribution (task 23) rather than here. Until
// then, avar reports what it can prove and does not claim what it cannot.
//
// The probe is a connection attempt rather than a bind attempt, deliberately.
// Binding to find out whether a port is free would put avar in a race with the
// relay it is trying to observe, and a diagnostic that can break the thing it
// diagnoses is worse than no diagnostic.

// hostProbeTimeout bounds one loopback connection attempt. A loopback connection
// either completes immediately or is refused immediately; a timeout this long is
// generous for a machine under load and short enough that a status listing with
// a dozen ports stays instant.
const hostProbeTimeout = 250 * time.Millisecond

// tcpStateListen is the value /proc/net/tcp uses for a listening socket.
const tcpStateListen = "0A"

// listenersScript reports the local address of every listening TCP socket in the
// guest, as /proc/net/tcp writes it.
//
// It reads /proc directly rather than running `ss` or `netstat`, because those
// are packages a minimal image may not have — Fedora's container base has
// neither — and because /proc/net/tcp is a kernel-defined format that is
// identical on all three distributions and in every locale. Both address
// families are read: a server bound to :: is listening on IPv4 too, and one
// bound only to 127.0.0.1 appears in neither list as a forwardable port.
const listenersScript = `cat /proc/net/tcp /proc/net/tcp6 2>/dev/null | awk '$4 == "` + tcpStateListen + `" {print $2}'
`

// PortDiagnostics reports what avar knows about the environment's forwarded
// ports, ordered by guest port.
//
// It is a read-only query and never fails merely because forwarding is broken —
// an unforwardable port is data in the result, not an error (REQ-7.2, REQ-18.9).
func (p *Provider) PortDiagnostics(ctx context.Context, machine string) ([]provider.PortDiagnostic, error) {
	if err := p.gate(ctx, machine, ownershipPrefix); err != nil {
		return nil, err
	}

	d, ok, err := p.view().lookup(ctx, machine)
	if err != nil {
		return nil, err
	}
	if !ok || !d.Running {
		// A stopped environment has no listeners, which is an empty answer
		// rather than a failure: `avr status` prints a stopped environment
		// alongside a running one and must not stop at the first.
		return nil, nil
	}

	out, err := p.run(ctx, guestShellArgv(machine, listenersScript)...)
	if err != nil {
		return nil, fmt.Errorf("reading the listening ports of environment %s: %w", machine, err)
	}

	return probeAll(ctx, parseListeningPorts(out)), nil
}

// probeAll asks about every port at once and returns the answers in the order
// the ports were listed.
//
// Concurrently, because the probes are independent and each can cost the whole
// timeout: a guest with a dozen listeners and forwarding switched off is three
// seconds of `avr status` waiting in series for twelve refusals that could have
// been waited for together. Run at once, the timeout bounds the set rather than
// each member of it.
//
// The results are written into their own slots rather than appended, so the
// order is the ports' and not the scheduler's — `avr status` prints them, and a
// listing that reorders itself between runs is one nobody can read.
func probeAll(ctx context.Context, ports []int) []provider.PortDiagnostic {
	diagnostics := make([]provider.PortDiagnostic, len(ports))

	var wg sync.WaitGroup
	for i, port := range ports {
		wg.Add(1)
		go func() {
			defer wg.Done()
			diagnostics[i] = probeHostPort(ctx, port)
		}()
	}
	wg.Wait()
	return diagnostics
}

// parseListeningPorts reads the port numbers out of /proc/net/tcp local
// addresses, deduplicated and ordered.
//
// The address is hexadecimal and its layout differs between the two files — four
// bytes for IPv4, sixteen for IPv6 — so only the part after the colon is read,
// which is the port in both. A socket bound to the loopback address alone is
// dropped: WSL publishes what a guest exposes to its own network interface, and
// a server listening only on 127.0.0.1 inside Linux is deliberately not exposed
// at all, so reporting it as unforwarded would be reporting the user's own
// choice as a problem.
func parseListeningPorts(out string) []int {
	seen := map[int]bool{}
	for _, line := range strings.Split(out, "\n") {
		address := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		host, portHex, ok := strings.Cut(address, ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseUint(portHex, 16, 32)
		if err != nil || port == 0 || port > 65535 {
			continue
		}
		if isLoopbackHex(host) {
			continue
		}
		seen[int(port)] = true
	}

	ports := make([]int, 0, len(seen))
	for port := range seen {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports
}

// isLoopbackHex reports whether a /proc/net/tcp local address is the loopback
// one.
//
// The bytes are little-endian, so 127.0.0.1 is written 0100007F, and IPv6's ::1
// is fifteen zero bytes with a one in the last position.
func isLoopbackHex(host string) bool {
	return strings.EqualFold(host, "0100007F") ||
		strings.EqualFold(host, "00000000000000000000000001000000")
}

// probeHostPort reports whether anything answers on the Windows loopback address
// at the guest's port.
//
// WSL publishes a guest port at the same number on the host, so there is no
// mapping to discover — only a reachability question to ask (REQ-18.9).
func probeHostPort(ctx context.Context, guestPort int) provider.PortDiagnostic {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(guestPort))

	dialer := net.Dialer{Timeout: hostProbeTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return provider.PortDiagnostic{
			GuestPort: guestPort,
			Forwarded: false,
			Reason: fmt.Sprintf("a process in your Linux environment is listening on %d, but nothing answers on localhost:%d. "+
				"WSL publishes guest ports automatically, so this usually means localhost forwarding is turned off in .wslconfig, "+
				"or another program is holding the port", guestPort, guestPort),
		}
	}
	_ = conn.Close()

	return provider.PortDiagnostic{GuestPort: guestPort, HostPort: guestPort, Forwarded: true}
}
