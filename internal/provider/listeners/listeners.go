// Package listeners finds the TCP ports a Linux guest is listening on, and which
// guest process holds each one.
//
// It is shared by every backend because the question is asked of Linux, not of
// a virtualization product: a Lima virtual machine and a WSL distribution answer
// it from the same kernel files in the same format. A backend runs Script inside
// the guest however it runs anything there, hands the output to Parse, and joins
// the result with what it knows about forwarding from its own side (REQ-7.2,
// REQ-16.1). Nothing here runs a command or knows how a guest is reached.
//
// The script reads /proc rather than running `ss` or `netstat`, because those
// are packages a minimal image may not have — Fedora's container base has
// neither — while /proc/net/tcp is a kernel-defined format that is identical on
// every distribution and in every locale. Attribution uses only what the
// supported images all carry: a POSIX shell, awk (mawk on Debian and Ubuntu,
// gawk on Fedora), ls, cat and tr.
package listeners

import (
	"sort"
	"strconv"
	"strings"
)

// Script reports the guest's TCP sockets and the processes that hold the
// listening ones, in three sections:
//
//	@tcp       /proc/net/tcp and /proc/net/tcp6 verbatim
//	@owners    "<pid> <inode>" for each process holding a listening socket
//	@commands  "<pid> <command line>" for each of those processes
//
// The kernel tables are passed through untouched so that the parsing — the
// part worth testing — happens in Go against what the kernel really writes,
// rather than in awk against the author's idea of it.
//
// Ownership is found by listing every process's file descriptors in one `ls`
// and matching the socket inodes, which costs one process however many the guest
// runs. A process's descriptors are readable only by its own user or by a
// sufficiently privileged one, so a script run as an ordinary user attributes
// that user's servers — the ones a developer started — and leaves a system
// daemon's port unattributed rather than failing. That is what "where
// determinable" means in REQ-16.1.
//
// It interpolates nothing: a backend may pass it through a shell as a constant.
const Script = `inodes=$(awk '$4 == "0A" { printf "%s ", $10 }' /proc/net/tcp /proc/net/tcp6 2>/dev/null)
echo '@tcp'
cat /proc/net/tcp /proc/net/tcp6 2>/dev/null
echo '@owners'
owners=$(ls -l /proc/[0-9]*/fd 2>/dev/null | awk -v inodes="$inodes" '
BEGIN { n = split(inodes, list, " "); for (i = 1; i <= n; i++) listening["socket:[" list[i] "]"] = 1 }
/^\/proc\/[0-9]+\/fd:$/ { split($0, part, "/"); pid = part[3]; next }
($NF in listening) { inode = $NF; gsub(/[^0-9]/, "", inode); print pid, inode }
')
printf '%s\n' "$owners"
echo '@commands'
for pid in $(printf '%s\n' "$owners" | awk 'NF && !seen[$1]++ { print $1 }'); do
	printf '%s ' "$pid"
	{ tr '\000\n\t' '   ' < "/proc/$pid/cmdline"; } 2>/dev/null
	echo
done
`

// tcpStateListen is the value /proc/net/tcp writes in its state column for a
// listening socket.
const tcpStateListen = "0A"

// Listener is one guest TCP port with at least one listening socket.
type Listener struct {
	// Port is the port number.
	Port int

	// LoopbackOnly reports that every socket on the port is bound to the
	// guest's loopback address, so nothing outside the guest can reach it
	// except through a forwarder that listens on loopback itself.
	LoopbackOnly bool

	// PID and Command identify the guest process holding the port, where it
	// could be determined; zero and empty where it could not. When several
	// processes share a listening socket — a server that forks its workers
	// after binding — the lowest process id is reported, which is the parent
	// that bound it.
	PID     int
	Command string
}

// Parse reads Script's output into one Listener per port, ordered by port.
//
// It never fails. A line it cannot read is skipped, because this output feeds
// diagnostics, and one unexpected line must not cost the user every other port.
func Parse(out string) []Listener {
	sockets, owners, commands := sections(out)

	byPort := map[int]*Listener{}
	for _, s := range sockets {
		l, ok := byPort[s.port]
		if !ok {
			l = &Listener{Port: s.port, LoopbackOnly: true}
			byPort[s.port] = l
		}
		if !s.loopback {
			l.LoopbackOnly = false
		}
		for _, pid := range owners[s.inode] {
			if l.PID == 0 || pid < l.PID {
				l.PID = pid
			}
		}
	}

	result := make([]Listener, 0, len(byPort))
	for _, l := range byPort {
		if l.PID != 0 {
			l.Command = commands[l.PID]
		}
		result = append(result, *l)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Port < result[j].Port })
	return result
}

// socket is one listening row of /proc/net/tcp or /proc/net/tcp6.
type socket struct {
	port     int
	inode    string
	loopback bool
}

// sections splits Script's output and reads each part.
func sections(out string) (sockets []socket, owners map[string][]int, commands map[int]string) {
	owners = map[string][]int{}
	commands = map[int]string{}

	section := ""
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		switch strings.TrimSpace(line) {
		case "@tcp", "@owners", "@commands":
			section = strings.TrimSpace(line)
			continue
		}
		switch section {
		case "@tcp":
			if s, ok := parseSocket(line); ok {
				sockets = append(sockets, s)
			}
		case "@owners":
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			if pid, ok := parsePID(fields[0]); ok {
				owners[fields[1]] = append(owners[fields[1]], pid)
			}
		case "@commands":
			pidText, command, found := strings.Cut(strings.TrimSpace(line), " ")
			if !found {
				continue
			}
			if pid, ok := parsePID(pidText); ok {
				commands[pid] = strings.TrimSpace(command)
			}
		}
	}
	return sockets, owners, commands
}

// parseSocket reads one /proc/net/tcp row, reporting only listening sockets.
//
// The columns are the same in both files — index, local address, remote
// address, state, queues, timer, retransmits, uid, timeout, inode — and only
// the address layout differs: eight hexadecimal digits for IPv4, thirty-two for
// IPv6, each followed by a colon and the port in hexadecimal. The header row
// fails the state check and is skipped with every other line that is not a
// listener.
func parseSocket(line string) (socket, bool) {
	fields := strings.Fields(line)
	if len(fields) < 10 || fields[3] != tcpStateListen {
		return socket{}, false
	}
	host, portHex, ok := strings.Cut(fields[1], ":")
	if !ok {
		return socket{}, false
	}
	port, err := strconv.ParseUint(portHex, 16, 32)
	if err != nil || port == 0 || port > 65535 {
		return socket{}, false
	}
	return socket{port: int(port), inode: fields[9], loopback: isLoopbackHex(host)}, true
}

// isLoopbackHex reports whether a /proc/net/tcp local address is a loopback
// one.
//
// The bytes are written in host order, which is little-endian on every
// architecture avar runs, so 127.0.0.1 is 0100007F — and any 127.x.y.z ends in
// 7F, which matters because systemd-resolved listens on 127.0.0.53 and
// 127.0.0.54. IPv6's ::1 is fifteen zero bytes and a one; an IPv4-mapped
// loopback address (::ffff:127.0.0.1) is loopback too.
func isLoopbackHex(host string) bool {
	host = strings.ToUpper(host)
	switch len(host) {
	case 8:
		return strings.HasSuffix(host, "7F")
	case 32:
		return host == "00000000000000000000000001000000" ||
			(strings.HasPrefix(host, "0000000000000000FFFF0000") && strings.HasSuffix(host, "7F"))
	}
	return false
}

// parsePID accepts only a positive process id.
func parsePID(text string) (int, bool) {
	pid, err := strconv.Atoi(text)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
