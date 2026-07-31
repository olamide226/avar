package lima

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/olamide226/avar/internal/provider"
)

// Lima forwards guest ports without avar asking and releases them the same way
// (REQ-7.1, REQ-7.3), so there is no forwarding operation to implement — only
// something to report when a port could not be published (REQ-7.2).
var _ provider.PortDiagnoser = (*Provider)(nil)

// hostAgentLog is the file Lima's host agent writes its own log to, inside the
// instance directory. The host agent is the component that opens host listeners
// for guest ports, so it is the only place a refused listener is recorded.
//
// Lima recreates the file on every start, so what it holds describes the
// machine's current run rather than an old one.
const hostAgentLog = "ha.stderr.log"

// maxLogTail bounds how much of the host agent log is read. A long-lived
// machine's log grows without limit, and `avr status` has a latency budget to
// keep (REQ-17.1); the forwarding lines that matter are the most recent ones,
// because each supersedes the last for its port.
const maxLogTail = 1 << 20 // 1 MiB

// sshGuestPort is the guest's SSH port, which Lima never publishes because it is
// how Lima itself reaches the guest.
//
// It is excluded from diagnostics deliberately: the transport avar uses to run
// commands is avar's business, not the user's (REQ-1.5), and reporting it as a
// port that "could not be forwarded" would be reporting avar's own plumbing as a
// user-facing problem.
const sshGuestPort = 22

// PortDiagnostics reports what Lima's host agent knows about the machine's
// forwarded ports, ordered by guest port (REQ-7.2).
//
// It is read-only and never fails because forwarding is broken: a port the host
// could not publish is an entry in the result, not an error. A machine that is
// not running has nothing forwarded and reports nothing, and so does one whose
// host agent has not written a log yet.
func (p *Provider) PortDiagnostics(ctx context.Context, machine string) ([]provider.PortDiagnostic, error) {
	// The prefix alone, as in Status: this is a query, and a machine avar
	// created but has not finished recording still has ports worth reporting
	// (design §3.5, PROP-6).
	if err := p.gate(ctx, machine, ownershipPrefix); err != nil {
		return nil, err
	}
	inst, err := p.newView().require(ctx, machine)
	if err != nil {
		return nil, err
	}
	if !inst.running() || inst.Dir == "" {
		return nil, nil
	}

	log, err := readLogTail(filepath.Join(inst.Dir, hostAgentLog), maxLogTail)
	if err != nil {
		return nil, fmt.Errorf("reading Lima's port-forwarding log for machine %s: %w", machine, err)
	}
	return parsePortDiagnostics(log), nil
}

// readLogTail reads at most limit bytes from the end of a file, discarding a
// leading partial line so the caller only ever sees whole records. A file that
// is not there is not an error: a machine can be running before its host agent
// has written anything.
func readLogTail(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}

	size := info.Size()
	truncated := false
	if size > limit {
		if _, err := file.Seek(size-limit, io.SeekStart); err != nil {
			return nil, err
		}
		truncated = true
	}

	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return nil, err
	}
	if truncated {
		// The first line of a tail is almost certainly half a record.
		if nl := bytes.IndexByte(data, '\n'); nl >= 0 {
			data = data[nl+1:]
		} else {
			data = nil
		}
	}
	return data, nil
}

// logEntry is the part of one host-agent log line avar reads.
//
// Lima's host agent logs JSON — `logrus.SetFormatter(new(logrus.JSONFormatter))`
// in its own entry point — with "msg", "level" and "time" plus whatever fields
// the call site attached. Only "msg" and the two fields below carry anything
// avar needs, and a line missing any of them is skipped rather than fatal:
// this is diagnostics, and one unreadable line must not cost the user the rest.
type logEntry struct {
	Level string `json:"level"`
	Msg   string `json:"msg"`
	// Error is what logrus.WithError attaches, e.g. "listen tcp 127.0.0.1:3000:
	// bind: address already in use" — the sentence that says why a host port
	// could not be opened.
	Error string `json:"error"`
	// NegligibleReason is attached when Lima itself considers a failed listener
	// unremarkable, such as a privileged port or the system resolver holding 53.
	NegligibleReason string `json:"negligible-reason"`
}

// Message prefixes Lima's host agent uses. They are matched as prefixes rather
// than parsed strictly, so a wording change in Lima costs avar a missing
// diagnostic rather than a broken status command.
const (
	msgForwarding     = "Forwarding "
	msgStopping       = "Stopping forwarding "
	msgNotForwarding  = "Not forwarding "
	msgFailedToListen = "failed to listen "
	msgFailedToSetUp  = "failed to set up forwarding "
)

// parsePortDiagnostics folds a host-agent log into one entry per guest port.
//
// Later lines supersede earlier ones for the same port, because the log is a
// history and only the latest word about a port is its current state: a port
// that was forwarded and then released has no host listener, and a port that
// failed to publish and was retried successfully does.
//
// Only TCP is reported. provider.PortDiagnostic describes a port rather than a
// protocol, and REQ-7.1 is about TCP listeners; folding UDP into the same
// numbering would make an entry mean two different things.
func parsePortDiagnostics(log []byte) []provider.PortDiagnostic {
	if len(log) == 0 {
		return nil
	}

	found := map[int]provider.PortDiagnostic{}
	scanner := bufio.NewScanner(bytes.NewReader(log))
	scanner.Buffer(make([]byte, 0, 64*1024), maxLogTail)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var entry logEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		diag, port, drop, ok := readPortLine(entry)
		switch {
		case !ok:
			continue
		case drop:
			delete(found, port)
		default:
			found[port] = diag
		}
	}

	if len(found) == 0 {
		return nil
	}
	out := make([]provider.PortDiagnostic, 0, len(found))
	for _, diag := range found {
		out = append(out, diag)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GuestPort < out[j].GuestPort })
	return out
}

// readPortLine turns one log entry into what it says about one guest port.
//
// drop reports a port that stopped being forwarded, which removes it from the
// picture rather than recording it as unforwardable — a closed listener is
// REQ-7.3 working, not a problem to report.
func readPortLine(entry logEntry) (diag provider.PortDiagnostic, port int, drop, ok bool) {
	msg := entry.Msg
	switch {
	case strings.HasPrefix(msg, msgStopping):
		// "Stopping forwarding TCP from 127.0.0.1:3000 to 127.0.0.1:3000"
		guest, _, ok := forwardingAddresses(strings.TrimPrefix(msg, msgStopping))
		if !ok {
			return provider.PortDiagnostic{}, 0, false, false
		}
		return provider.PortDiagnostic{}, guest, true, true

	case strings.HasPrefix(msg, msgForwarding):
		// "Forwarding TCP from 127.0.0.1:3000 to 127.0.0.1:3000"
		guest, host, ok := forwardingAddresses(strings.TrimPrefix(msg, msgForwarding))
		if !ok || guest == sshGuestPort {
			return provider.PortDiagnostic{}, 0, false, false
		}
		return provider.PortDiagnostic{GuestPort: guest, HostPort: host, Forwarded: true}, guest, false, true

	case strings.HasPrefix(msg, msgNotForwarding):
		// "Not forwarding TCP 127.0.0.1:22" — a port Lima's own rules exclude,
		// or one whose host listener failed for a reason Lima considers
		// unremarkable.
		proto, addr, found := strings.Cut(strings.TrimPrefix(msg, msgNotForwarding), " ")
		if !found || !isTCP(proto) {
			return provider.PortDiagnostic{}, 0, false, false
		}
		guest, ok := portOf(addr)
		if !ok || guest == sshGuestPort {
			return provider.PortDiagnostic{}, 0, false, false
		}
		return provider.PortDiagnostic{
			GuestPort: guest,
			Forwarded: false,
			Reason:    notForwardedReason(entry, guest),
		}, guest, false, true

	case strings.HasPrefix(msg, msgFailedToListen):
		// "failed to listen tcp: 127.0.0.1:3000", with the cause in "error".
		proto, addr, found := strings.Cut(strings.TrimPrefix(msg, msgFailedToListen), ":")
		if !found || !isTCP(proto) {
			return provider.PortDiagnostic{}, 0, false, false
		}
		guest, ok := portOf(strings.TrimSpace(addr))
		if !ok || guest == sshGuestPort {
			return provider.PortDiagnostic{}, 0, false, false
		}
		return provider.PortDiagnostic{
			GuestPort: guest,
			Forwarded: false,
			Reason:    listenFailureReason(entry, guest),
		}, guest, false, true

	case strings.HasPrefix(msg, msgFailedToSetUp):
		// "failed to set up forwarding tcp port 3000 (negligible if already
		// forwarded)" — the SSH-based forwarder's version of the same failure.
		rest := strings.TrimPrefix(msg, msgFailedToSetUp)
		proto, rest, found := strings.Cut(rest, " ")
		if !found || !isTCP(proto) {
			return provider.PortDiagnostic{}, 0, false, false
		}
		guest, ok := trailingPort(rest)
		if !ok || guest == sshGuestPort {
			return provider.PortDiagnostic{}, 0, false, false
		}
		return provider.PortDiagnostic{
			GuestPort: guest,
			Forwarded: false,
			Reason:    listenFailureReason(entry, guest),
		}, guest, false, true
	}
	return provider.PortDiagnostic{}, 0, false, false
}

// notForwardedReason explains a port the backend declined to publish, in the
// user's terms rather than the backend's.
func notForwardedReason(entry logEntry, port int) string {
	if reason := strings.TrimSpace(entry.NegligibleReason); reason != "" {
		return fmt.Sprintf("port %d was not published on the host: %s", port, reason)
	}
	if cause := hostPortCause(entry, port); cause != "" {
		return cause
	}
	return fmt.Sprintf("port %d was not published on the host", port)
}

// listenFailureReason explains a host listener that could not be opened. The
// case REQ-7.2 is about — the host port is already taken — is named plainly,
// because it is the one the user can act on.
func listenFailureReason(entry logEntry, port int) string {
	if cause := hostPortCause(entry, port); cause != "" {
		return cause
	}
	return fmt.Sprintf("port %d could not be published on the host", port)
}

// hostPortCause renders the backend's own error, recognising the conflict
// REQ-7.2 names so that avar says what happened rather than echoing a syscall.
func hostPortCause(entry logEntry, port int) string {
	cause := strings.TrimSpace(entry.Error)
	if cause == "" {
		return ""
	}
	if strings.Contains(cause, "address already in use") {
		return fmt.Sprintf("host port %d is already in use by another program on the host", port)
	}
	if strings.Contains(cause, "permission denied") {
		return fmt.Sprintf("host port %d could not be opened: %s", port, cause)
	}
	return fmt.Sprintf("port %d could not be published on the host: %s", port, cause)
}

// forwardingAddresses reads the "<PROTO> from <guest> to <host>" tail both
// forwarders log, and reports the two port numbers.
func forwardingAddresses(rest string) (guest, host int, ok bool) {
	proto, rest, found := strings.Cut(rest, " ")
	if !found || !isTCP(proto) {
		return 0, 0, false
	}
	rest, found = strings.CutPrefix(rest, "from ")
	if !found {
		return 0, 0, false
	}
	guestAddr, hostAddr, found := strings.Cut(rest, " to ")
	if !found {
		return 0, 0, false
	}
	guest, ok = portOf(strings.TrimSpace(guestAddr))
	if !ok {
		return 0, 0, false
	}
	host, ok = portOf(strings.TrimSpace(hostAddr))
	if !ok {
		// A guest port whose host side avar cannot read is still worth
		// reporting; the host port is simply unknown.
		return guest, 0, true
	}
	return guest, host, true
}

// isTCP reports whether a protocol token names TCP, in either case Lima writes
// it (the two forwarders differ).
func isTCP(proto string) bool {
	return strings.EqualFold(strings.TrimSpace(proto), "tcp")
}

// portOf reads the port from a "host:port" address, including the bracketed
// IPv6 form Lima writes for ::1.
func portOf(addr string) (int, bool) {
	addr = strings.Trim(strings.TrimSpace(addr), `"`)
	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, false
	}
	return parsePort(portText)
}

// trailingPort reads the port number a message ends with, ignoring any
// parenthesised commentary after it.
func trailingPort(rest string) (int, bool) {
	fields := strings.Fields(rest)
	for i := len(fields) - 1; i >= 0; i-- {
		if port, ok := parsePort(fields[i]); ok {
			return port, true
		}
	}
	return 0, false
}

// parsePort accepts only a real TCP port number.
func parsePort(text string) (int, bool) {
	port, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}
