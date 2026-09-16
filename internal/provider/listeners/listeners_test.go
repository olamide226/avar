package listeners

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The fixtures are Script's real output, not a rendering of what the parser
// hopes to read (docs/lessons.md, "A test double that shares the code's
// assumption confirms the assumption, not the behaviour"). They were captured
// by running Script in an ubuntu:24.04 container holding three listeners:
//
//	pid 16, root:  127.0.0.1:5432  (IPv4, loopback)
//	pid 17, root:  [::]:8080       (IPv6, wildcard)
//	pid 20, dev:   0.0.0.0:3000    (IPv4, wildcard)
//
// once as root with the ptrace capability a virtual machine's root has, and
// once as the unprivileged user dev.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return string(body)
}

func TestParse_AttributesEveryListenerWhenPrivileged_REQ_16_1(t *testing.T) {
	t.Parallel()

	got := Parse(readFixture(t, "ubuntu-24.04-root.txt"))

	want := []struct {
		port     int
		loopback bool
		pid      int
		command  string
	}{
		{3000, false, 20, "perl -MIO::Socket::INET -e my $s = IO::Socket::INET->new(LocalAddr=>q(0.0.0.0:3000)"},
		{5432, true, 16, "perl -MIO::Socket::INET -e my $s = IO::Socket::INET->new(LocalAddr=>q(127.0.0.1:5432)"},
		{8080, false, 17, "perl -MSocket=:all -e socket(my $s, AF_INET6, SOCK_STREAM, 0)"},
	}
	if len(got) != len(want) {
		t.Fatalf("Parse = %+v, want %d listeners", got, len(want))
	}
	for i, w := range want {
		g := got[i]
		if g.Port != w.port || g.LoopbackOnly != w.loopback || g.PID != w.pid {
			t.Errorf("listener %d = {port %d, loopback %t, pid %d}, want {%d, %t, %d}", i, g.Port, g.LoopbackOnly, g.PID, w.port, w.loopback, w.pid)
		}
		if !strings.HasPrefix(g.Command, w.command) {
			t.Errorf("port %d command = %q, want it to start %q", g.Port, g.Command, w.command)
		}
		if strings.HasSuffix(g.Command, " ") {
			t.Errorf("port %d command keeps the trailing separator the NUL became: %q", g.Port, g.Command)
		}
	}
}

// An unprivileged user cannot read another user's file descriptors, so only
// their own server is attributed. The others are still listed — the port is
// known even when its owner is not — and nothing fails.
func TestParse_ListsUnattributablePortsWithoutAProcess_REQ_16_1(t *testing.T) {
	t.Parallel()

	got := Parse(readFixture(t, "ubuntu-24.04-user.txt"))
	if len(got) != 3 {
		t.Fatalf("Parse = %+v, want all three ports", got)
	}
	for _, l := range got {
		switch l.Port {
		case 3000:
			if l.PID != 20 || l.Command == "" {
				t.Errorf("the user's own server is not attributed: %+v", l)
			}
		default:
			if l.PID != 0 || l.Command != "" {
				t.Errorf("port %d is attributed to a process the user cannot see: %+v", l.Port, l)
			}
		}
	}
}

func TestParse_JoinsBothAddressFamiliesAndPrefersTheParent_REQ_16_1(t *testing.T) {
	t.Parallel()

	// Port 3000 (0BB8) on 0.0.0.0 and on ::1, one socket held by a parent and
	// a forked worker. Port 53 (0035) on systemd-resolved's 127.0.0.53, and a
	// connected socket on 3000 that is not a listener at all.
	out := "@tcp\n" +
		"  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111 1 0 100 0 0 10 0\n" +
		"   1: 3500007F:0035 00000000:0000 0A 00000000:00000000 00:00000000 00000000   991        0 222 1 0 100 0 0 10 0\n" +
		"   2: 0100007F:0BB8 0100007F:D431 01 00000000:00000000 00:00000000 00000000  1000        0 333 1 0 100 0 0 10 0\n" +
		"  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000000000000000000001000000:0BB8 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 444 1 0 100 0 0 10 0\n" +
		"@owners\n" +
		"912 111\n" +
		"904 111\n" +
		"not an owner line\n" +
		"@commands\n" +
		"904 node server.js \n" +
		"912 node server.js --worker \n"

	got := Parse(out)
	if len(got) != 2 {
		t.Fatalf("Parse = %+v, want ports 53 and 3000", got)
	}
	if got[0].Port != 53 || !got[0].LoopbackOnly || got[0].PID != 0 {
		t.Errorf("port 53 = %+v, want loopback-only and unattributed", got[0])
	}
	if got[1].Port != 3000 || got[1].LoopbackOnly {
		t.Errorf("port 3000 = %+v, want a wildcard listener: one of its sockets is not on loopback", got[1])
	}
	if got[1].PID != 904 || got[1].Command != "node server.js" {
		t.Errorf("port 3000 is attributed to pid %d %q, want the parent 904 \"node server.js\"", got[1].PID, got[1].Command)
	}
}

func TestParse_EmptyOutputIsNoListeners_REQ_16_1(t *testing.T) {
	t.Parallel()

	for _, out := range []string{"", "@tcp\n@owners\n\n@commands\n", "garbage\r\n"} {
		if got := Parse(out); len(got) != 0 {
			t.Errorf("Parse(%q) = %+v, want nothing", out, got)
		}
	}
}

func TestIsLoopbackHex(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"0100007F":                         true,  // 127.0.0.1
		"3500007F":                         true,  // 127.0.0.53
		"00000000":                         false, // 0.0.0.0
		"0F02000A":                         false, // 10.0.2.15
		"00000000000000000000000001000000": true,  // ::1
		"0000000000000000FFFF00000100007F": true,  // ::ffff:127.0.0.1
		"00000000000000000000000000000000": false, // ::
		"0000000000000000FFFF00000F02000A": false, // ::ffff:10.0.2.15
		"7F":                               false,
	}
	for host, want := range cases {
		if got := isLoopbackHex(host); got != want {
			t.Errorf("isLoopbackHex(%q) = %t, want %t", host, got, want)
		}
	}
}

// The script runs through /bin/sh in the guest, where a syntax error would
// surface as a port listing that is silently empty. The host's sh cannot run it
// — there is no /proc/net/tcp on macOS — but it can parse it.
func TestScript_IsValidShell(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("no POSIX shell on a Windows host")
	}
	if out, err := exec.Command("sh", "-n", "-c", Script).CombinedOutput(); err != nil {
		t.Fatalf("sh -n rejected the script: %v\n%s", err, out)
	}
}
