// Package editors finds the editor windows connected to a Linux guest, so that
// a machine somebody is working in through an editor is not stopped for being
// idle.
//
// It is shared by every backend because the question is asked of Linux, not of
// a virtualization product: a Lima virtual machine and a WSL distribution both
// answer it out of /proc, in the same format. A backend runs Script inside the
// guest however it runs anything there and hands the output to Parse. Nothing
// here runs a command or knows how a guest is reached.
//
// # Why the guest, and not the host
//
// `avr code`, `avr cursor` and `avr zed` launch the editor and exit. The editor
// then reaches the guest on its own — over SSH on Lima, through its WSL
// integration on Windows — and runs a remote server there that avar never
// started and never sees. No host process avar could track says whether
// somebody is still editing. The guest does.
//
// # The rule: a connected window, not a running server
//
// A process is a connection when the program it runs lies inside the editor's
// server directory and it is the process that exists once per connected window:
//
//	VS Code  ~/.vscode-server/…/node  … --type=extensionHost …
//	Cursor   ~/.cursor-server/…/node  … --type=extensionHost …
//	Zed      ~/.zed_server/zed-remote-server-<channel>-<version> proxy …
//
// The server alone is deliberately not enough. Both editors leave it running
// after the last window has gone: VS Code for five minutes, and only when it was
// started with --enable-remote-auto-shutdown (the ServerLifetime shutdown timer,
// 300 s in 1.135.0's server-main.js), and Zed for ten (IDLE_TIMEOUT in
// crates/remote_server/src/server.rs, v1.20.2). A server started without its
// auto-shutdown would keep a machine up for good. A window is what somebody is
// working in.
//
// VS Code's extension host was measured against a real 1.135.0 server in an
// Ubuntu 24.04 guest (testdata): one appears when a window attaches; it exits at
// once when the window is closed ("The client has disconnected gracefully"); and
// when the connection drops without closing — a closed laptop lid, a lost
// network — it stays for the reconnection grace time, three hours by default,
// so that reopening the lid picks the session up where it was. That is the
// session this package exists to protect, and a machine that keeps it has kept
// something real. Cursor is a fork of VS Code with its own server directory.
//
// Zed's proxy is what Zed's client runs, over SSH and through wsl.exe alike,
// for each connection (crates/remote/src/transport/{ssh,wsl}.rs in v1.20.2). It
// exits when the connection does. Zed starts it by a path relative to the home
// directory — `cd; env .zed_server/zed-remote-server-stable-1.20.2 proxy …` —
// so its command line begins `.zed_server/`, with no leading slash, while the
// daemon it spawns has an absolute one. Both appear in the capture.
package editors

import (
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/olamide226/avar/internal/provider"
)

// Script reports every process in the guest and the command line it is running,
// one process per line, under a single @processes marker:
//
//	@processes
//	1 /sbin/init
//	5905 .zed_server/zed-remote-server-stable-1.20.2 proxy --identifier …
//
// The command lines are passed through untouched — NUL, newline and tab
// separators collapsed to spaces, which is what makes them one line — so that
// the deciding, the part worth testing, happens in Go against what the kernel
// really wrote rather than in shell against the author's idea of it.
//
// It reads /proc rather than running `ps`, because procps is a package a
// minimal image may not have while /proc is the kernel's own interface, and it
// uses only a POSIX shell and `tr`, which every supported image carries. One
// `tr` per process is affordable: this runs only for a machine the idle check
// is about to stop, never on avar's warm path.
//
// Command lines are world-readable in /proc, so an unprivileged account sees
// every editor process, whichever user started it.
//
// It interpolates nothing: a backend may pass it through a shell as a constant.
const Script = `echo '@processes'
for f in /proc/[0-9]*/cmdline; do
	pid=${f#/proc/}
	pid=${pid%/cmdline}
	printf '%s ' "$pid"
	{ tr '\000\n\t' '   ' < "$f"; } 2>/dev/null
	echo
done
`

// The editors avar opens, named as a user would name them (REQ-13.1, REQ-13.5,
// REQ-13.6).
const (
	VSCode = "VS Code"
	Cursor = "Cursor"
	Zed    = "Zed"
)

// Parse reads Script's output into one connection per connected window,
// ordered by process id. The process is the one that shows the window is there:
// an extension host, or Zed's proxy.
//
// It never fails. A line it cannot read is skipped: one unexpected line must
// not decide whether a machine is stopped.
func Parse(out string) []provider.EditorConnection {
	var found []provider.EditorConnection

	inProcesses := false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "@processes" {
			inProcesses = true
			continue
		}
		if !inProcesses {
			continue
		}
		pidText, command, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(pidText)
		if err != nil || pid <= 0 {
			continue
		}
		if editor, ok := classify(strings.Fields(command)); ok {
			found = append(found, provider.EditorConnection{Editor: editor, PID: pid})
		}
	}

	sort.Slice(found, func(i, j int) bool { return found[i].PID < found[j].PID })
	return found
}

// classify reports which editor's window a command line shows, if any.
//
// The program is the first word as it was executed, and it is never looked
// through: a shell whose script merely mentions a server directory — `sh -c
// "cd; env .zed_server/… proxy …"`, or somebody's `ls ~/.vscode-server` — is
// not itself a window, and the process it starts is counted on its own line.
func classify(fields []string) (string, bool) {
	if len(fields) == 0 {
		return "", false
	}
	program, args := fields[0], fields[1:]

	switch {
	case inDir(program, ".vscode-server") && contains(args, "--type=extensionHost"):
		return VSCode, true
	case inDir(program, ".cursor-server") && contains(args, "--type=extensionHost"):
		return Cursor, true
	case inDir(program, ".zed_server") &&
		strings.HasPrefix(path.Base(program), "zed-remote-server-") &&
		len(args) > 0 && args[0] == "proxy":
		return Zed, true
	}
	return "", false
}

// inDir reports whether a program path passes through a directory of the given
// name, whether the path is absolute or relative to the home directory.
func inDir(program, dir string) bool {
	parts := strings.Split(program, "/")
	for _, part := range parts[:len(parts)-1] {
		if part == dir {
			return true
		}
	}
	return false
}

// contains reports whether an argument is present exactly.
func contains(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
