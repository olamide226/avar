package wsl2

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/listeners"
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
// which TCP ports have a listener, and which process holds each
// (listeners.Script). From Windows: does anything answer on the same port on the
// loopback address. A port listening in the guest with nothing answering on the
// host is the actionable case, and it is what `avr status` shows; a port that
// answers is what `avr ports` lists and `avr open` opens.
//
// One limit is worth stating plainly rather than papering over. When something
// *does* answer on the host, avar cannot tell from outside whether that is WSL's
// own relay publishing the guest's port or a Windows program that took the port
// first — both look identical to a connection attempt. Distinguishing them means
// enumerating Windows listeners with their owning process, and how WSL's
// publication shows up in that enumeration depends on its networking mode in
// ways nobody has measured for avar on a real host. REQ-16.1 asks for the
// *guest* process, which is attributed; the host side is deliberately not
// guessed at. Until it is measured, avar reports what it can prove and does not
// claim what it cannot.
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

// PortDiagnostics reports what avar knows about the environment's forwarded
// ports, ordered by guest port, with the guest process listening on each where
// it can be determined (REQ-16.1).
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

	// guestShellArgv runs as root, so a listener is attributed whichever
	// account in the guest started it.
	out, err := p.run(ctx, guestShellArgv(machine, listeners.Script)...)
	if err != nil {
		return nil, fmt.Errorf("reading the listening ports of environment %s: %w", machine, err)
	}

	return probeAll(ctx, listeners.Parse(out)), nil
}

// probeAll asks about every listener at once and returns the answers in the
// order the listeners were given.
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
func probeAll(ctx context.Context, guest []listeners.Listener) []provider.PortDiagnostic {
	answers := make([]provider.PortDiagnostic, len(guest))
	keep := make([]bool, len(guest))

	var wg sync.WaitGroup
	for i, l := range guest {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i], keep[i] = diagnose(ctx, l)
		}()
	}
	wg.Wait()

	var diagnostics []provider.PortDiagnostic
	for i := range answers {
		if keep[i] {
			diagnostics = append(diagnostics, answers[i])
		}
	}
	return diagnostics
}

// diagnose turns one guest listener into what avar reports about it, and says
// whether there is anything to report.
//
// A listener bound only to the guest's loopback address can be published too —
// Microsoft documents localhostForwarding as covering ports "bound to wildcard
// or localhost in the WSL 2 VM" — so it is probed like any other and reported
// when it answers: a development server that binds localhost by default is
// exactly the one a user runs `avr ports` to find. When it does not answer it is
// left out rather than reported as broken, which is what avar did with every
// loopback listener before `avr ports` existed: whether a loopback bind is
// published depends on how WSL's networking is configured, and calling a
// deliberate bind a forwarding failure would be noise in `avr status`.
func diagnose(ctx context.Context, l listeners.Listener) (provider.PortDiagnostic, bool) {
	diag := probeHostPort(ctx, l.Port)
	if !diag.Forwarded && l.LoopbackOnly {
		return provider.PortDiagnostic{}, false
	}
	diag.PID, diag.Process = l.PID, l.Command
	return diag, true
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
