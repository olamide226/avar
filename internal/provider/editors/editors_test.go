package editors

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/olamide226/avar/internal/provider"
)

// The lima-* fixtures are real. Each is this package's own Script, run through
// `limactl shell` exactly as the Lima backend runs it, in a throwaway Lima 2.2.0
// machine cloned from avar's Ubuntu 24.04 base. Inside it were:
//
//   - the real VS Code 1.135.0 CLI and server for the commit of the VS Code
//     that captured them (08d4889f…), installed where Remote-SSH 0.128.0's
//     Linux installer puts them — ~/.vscode-server/code-<commit> and
//     ~/.vscode-server/cli/servers/Stable-<commit>/server — with the CLI
//     running `command-shell` under a shell kept alive the way that installer
//     keeps it;
//   - a real window: headless Chrome loading the server's workbench, which is
//     what made the server start its extension host;
//   - the real Zed 1.20.2 remote server, its proxy started over `limactl
//     shell` with the command Zed's client sends over SSH, `cd; env
//     .zed_server/zed-remote-server-stable-1.20.2 proxy --identifier …`.
//
// "attached" has a VS Code window and a Zed connection. "closed" is the same
// guest after the page was closed and Zed disconnected: the servers are still
// running, which is the case the rule must not count. "dropped" is a window
// whose connection was cut without closing (Chrome killed): VS Code keeps its
// extension host for the reconnection grace time, and the rule counts it.
//
// The one edit is the guest account's name, replaced with "dev".
const (
	attached = "lima-ubuntu-24.04-vscode-1.135.0-zed-1.20.2-attached.txt"
	closed   = "lima-ubuntu-24.04-vscode-1.135.0-zed-1.20.2-closed.txt"
	dropped  = "lima-ubuntu-24.04-vscode-1.135.0-zed-1.20.2-dropped.txt"
)

// constructed is NOT captured, and is the weaker part of this package's
// evidence. It carries what the capture host could not produce: VS Code's WSL
// layout, ~/.vscode-server/bin/<commit>, with its extension host given the real
// 1.135.0 argv from the lima fixtures; Cursor, which is not installed on the
// capture host and is assumed to differ from VS Code only in its directory; and
// Zed as wsl.exe starts it (`--cd ~ --exec env .zed_server/… proxy …`,
// crates/remote/src/transport/wsl.rs in v1.20.2).
const constructed = "constructed-wsl-and-cursor.txt"

func load(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}

// REQ-5.10: a connected window of each editor is found in a real guest, and
// nothing else in that guest is — not the servers, not the CLI Remote-SSH
// keeps running, not Zed's daemon or its crash handlers, and not the probe's
// own shell.
func TestParse_FindsTheWindowsConnectedToARealGuest_REQ_5_10(t *testing.T) {
	got := Parse(load(t, attached))
	want := []provider.EditorConnection{
		{Editor: Zed, PID: 5905},    // .zed_server/zed-remote-server-stable-1.20.2 proxy
		{Editor: VSCode, PID: 5944}, // …/server/node … --type=extensionHost
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse = %+v, want %+v", got, want)
	}
}

// REQ-5.10: once every window has closed, the servers are still running, and
// they do not count.
func TestParse_ServersWithNoWindowAreNotAConnection_REQ_5_10(t *testing.T) {
	if got := Parse(load(t, closed)); len(got) != 0 {
		t.Errorf("Parse = %+v, want nothing: every window had closed", got)
	}
}

// PROP-11: a window whose connection dropped keeps its extension host for the
// reconnection grace time, and is still somebody's session.
func TestParse_ADroppedWindowIsStillAConnection_PROP_11(t *testing.T) {
	got := Parse(load(t, dropped))
	want := []provider.EditorConnection{{Editor: VSCode, PID: 6780}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse = %+v, want %+v", got, want)
	}
}

// REQ-5.10: each editor avar opens is recognised in the layouts the capture could
// not show (see constructed), and a program that merely names a server
// directory, or lives in one without being a window, is not.
func TestParse_RecognisesEveryEditorAvarOpens_REQ_5_10(t *testing.T) {
	got := Parse(load(t, constructed))
	want := []provider.EditorConnection{
		{Editor: VSCode, PID: 120},
		{Editor: Cursor, PID: 164},
		{Editor: Zed, PID: 171},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse = %+v, want %+v", got, want)
	}
}

// Output the probe could not produce is skipped rather than fatal.
func TestParse_SkipsWhatItCannotRead(t *testing.T) {
	out := "@processes\n" +
		"not-a-pid .zed_server/zed-remote-server-stable-1.20.2 proxy\n" +
		"-3 .zed_server/zed-remote-server-stable-1.20.2 proxy\n" +
		"\n" +
		"321\n" +
		"322 .zed_server/zed-remote-server-stable-1.20.2 proxy --identifier x\r\n"

	want := []provider.EditorConnection{{Editor: Zed, PID: 322}}
	if got := Parse(out); !reflect.DeepEqual(got, want) {
		t.Errorf("Parse = %+v, want %+v", got, want)
	}
}

// Lines before the marker are not process lines: a transport may print a banner
// of its own before the script's output begins.
func TestParse_IgnoresAnythingBeforeTheMarker(t *testing.T) {
	out := "410 .zed_server/zed-remote-server-stable-1.20.2 proxy\n" +
		"@processes\n" +
		"411 .zed_server/zed-remote-server-stable-1.20.2 proxy\n"

	want := []provider.EditorConnection{{Editor: Zed, PID: 411}}
	if got := Parse(out); !reflect.DeepEqual(got, want) {
		t.Errorf("Parse = %+v, want %+v", got, want)
	}
}
