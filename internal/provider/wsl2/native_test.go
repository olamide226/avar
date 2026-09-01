//go:build windows

package wsl2

import (
	"context"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/types"
)

// Windows-only, like every test in this package: these assert on Windows paths,
// and `C:\Users\ola\code\app` is not an absolute path to path/filepath compiled
// for any other host (see docs/lessons.md, "A boundary is provider-neutral when
// a second host compiles it").

// nativeProvider is a Provider wired onto the package's fake wsl.exe, with the
// guest account pinned so a generated script is the same on every machine.
func nativeProvider(t *testing.T) *Provider {
	t.Helper()
	return newProvider(t, newFakeWSL(), recorded(testMachine))
}

// REQ-14.1: the native copy is a second directory, on the guest's own
// filesystem, beneath the account's home — and it is emphatically not the mount.
// If the two were one directory the session would still be running on DrvFS and
// the whole feature would be a no-op nobody could see.
func TestMapNativeWorkspace_IsASecondDirectoryOnTheLinuxFilesystem_REQ_14_1(t *testing.T) {
	p := nativeProvider(t)

	ws, guestCwd, err := p.MapNativeWorkspace(strings.Repeat("a", 64), `C:\Users\ola\code\app`, `C:\Users\ola\code\app`)
	if err != nil {
		t.Fatalf("mapping a native workspace: %v", err)
	}

	if !strings.HasPrefix(ws.Path, "/home/"+testUser+"/workspaces/") {
		t.Errorf("workspace path = %q, want it under the guest account's own home (REQ-14.1)", ws.Path)
	}
	if strings.HasPrefix(ws.Path, GuestProjectRoot) {
		t.Errorf("workspace path %q is under the DrvFS project root, so it is not on the Linux filesystem at all", ws.Path)
	}
	if ws.MountPath != GuestRoot(strings.Repeat("a", 64), `C:\Users\ola\code\app`) {
		t.Errorf("MountPath = %q, want the share the copy is made through", ws.MountPath)
	}
	if guestCwd != ws.Path {
		t.Errorf("guest cwd = %q, want the workspace root %q", guestCwd, ws.Path)
	}

	// Named for the project and then for its identity, exactly as the share is,
	// so that the two are visibly one project (PROP-14).
	if !strings.HasSuffix(ws.Path, "/app-"+strings.Repeat("a", 10)) {
		t.Errorf("workspace path = %q, want the project's readable label and its identity", ws.Path)
	}
}

// A session started in a subdirectory lands in that subdirectory of the copy.
func TestMapNativeWorkspace_KeepsTheWorkingSubdirectory_REQ_14_1(t *testing.T) {
	p := nativeProvider(t)

	ws, guestCwd, err := p.MapNativeWorkspace(strings.Repeat("b", 64), `C:\Users\ola\code\app`, `C:\Users\ola\code\app\src\api`)
	if err != nil {
		t.Fatalf("mapping a native workspace: %v", err)
	}
	if want := ws.Path + "/src/api"; guestCwd != want {
		t.Errorf("guest cwd = %q, want %q", guestCwd, want)
	}
}

// A working directory outside the project is refused rather than mapped, for
// the reason MapProjectPath refuses it: a guest path escaping its own project is
// what mount confinement forbids (PROP-5).
func TestMapNativeWorkspace_RefusesADirectoryOutsideTheProject_PROP_5(t *testing.T) {
	p := nativeProvider(t)

	if _, _, err := p.MapNativeWorkspace(strings.Repeat("c", 64), `C:\Users\ola\code\app`, `C:\Users\ola\secrets`); err == nil {
		t.Fatal("a working directory outside the project was mapped instead of refused")
	}
}

// The scan output the parser is fed here is what the real tools write, not what
// avar hoped to read. Twenty-nine unit tests once agreed that a broken
// /proc/mounts filter worked because the fake spoke avar's vocabulary rather
// than the tool's (docs/lessons.md); these lines are the shapes `sha256sum` and
// `find -printf` actually produce.
const realScanOutput = `AVR:baseline
` + baseHashA + ` false bWFpbi5nbw==
AVR:mount
AVR:present
` + hashA + `  ./main.go
` + hashB + `  ./scripts/run.sh
AVR:meta
x ./scripts/run.sh
s ./link
AVR:workspace
AVR:present
` + hashA + `  ./main.go
AVR:meta
AVR:end
`

const (
	hashA     = "1111111111111111111111111111111111111111111111111111111111111111"
	hashB     = "2222222222222222222222222222222222222222222222222222222222222222"
	baseHashA = "1111111111111111111111111111111111111111111111111111111111111111"
)

// REQ-14.1/14.2: reading both copies is the whole basis of every decision that
// follows, so the parse has to produce exactly what each tree holds — including
// the executable bit, which `find` reports in a separate pass.
func TestParseNativeScan_ReadsWhatTheToolsWrite_REQ_14_2(t *testing.T) {
	scan, err := parseNativeScan(realScanOutput)
	if err != nil {
		t.Fatalf("parsing a real-shaped scan: %v", err)
	}

	if !scan.Exists {
		t.Error("the native copy was reported as absent although the scan said it is present")
	}
	if got, want := len(scan.Mount), 2; got != want {
		t.Errorf("the shared copy holds %d files, want %d: %v", got, want, scan.Mount)
	}
	if entry := scan.Mount["scripts/run.sh"]; entry.Hash != hashB || !entry.Exec {
		t.Errorf("scripts/run.sh = %+v, want hash %s and the executable bit", entry, hashB)
	}
	if entry := scan.Mount["main.go"]; entry.Hash != hashA || entry.Exec {
		t.Errorf("main.go = %+v, want hash %s and no executable bit", entry, hashA)
	}
	if got, want := len(scan.Guest), 1; got != want {
		t.Errorf("the native copy holds %d files, want %d: %v", got, want, scan.Guest)
	}
	if entry, ok := scan.Baseline["main.go"]; !ok || entry.Hash != baseHashA {
		t.Errorf("baseline for main.go = %+v (present=%t), want the recorded agreement", entry, ok)
	}

	// A symbolic link is neither carried nor silently dropped: it is reported,
	// because a file the user believes is synchronized and is not is this
	// feature's worst failure.
	if want := []string{"link"}; len(scan.Skipped) != 1 || scan.Skipped[0] != want[0] {
		t.Errorf("Skipped = %v, want %v", scan.Skipped, want)
	}
}

// A workspace that has never been created is not an error — it is the first
// thing `avr --native-fs` fixes — but a share that is not there is, because
// avar copies through the share and cannot proceed without it.
func TestParseNativeScan_DistinguishesAMissingCopyFromAMissingShare_REQ_14_1(t *testing.T) {
	absent := "AVR:baseline\nAVR:mount\nAVR:present\nAVR:meta\nAVR:workspace\nAVR:absent\nAVR:end\n"
	scan, err := parseNativeScan(absent)
	if err != nil {
		t.Fatalf("parsing a scan with no native copy: %v", err)
	}
	if scan.Exists {
		t.Error("a native copy that does not exist was reported as existing")
	}

	noShare := "AVR:baseline\nAVR:mount\nAVR:absent\nAVR:workspace\nAVR:absent\nAVR:end\n"
	if _, err := parseNativeScan(noShare); err == nil {
		t.Error("a scan with no shared copy was accepted; avar copies through the share")
	}
}

// A scan that ended early is refused outright. Treating a truncated listing as
// an empty tree would make avar propose deleting every file in the other copy —
// the one mistake in this feature that destroys work.
func TestParseNativeScan_RefusesATruncatedListing_REQ_17_5(t *testing.T) {
	truncated := "AVR:baseline\nAVR:mount\nAVR:present\n" + hashA + "  ./main.go\n"
	if _, err := parseNativeScan(truncated); err == nil {
		t.Fatal("a truncated scan was accepted as a complete picture of both copies")
	}
}

// A tool the environment does not have produces an error naming it, not an
// empty manifest. An empty manifest looks exactly like an empty project.
func TestParseNativeScan_NamesAMissingTool_REQ_14_1(t *testing.T) {
	_, err := parseNativeScan("AVR:missing-tool find\n")
	if err == nil {
		t.Fatal("a scan from an environment with no find was accepted")
	}
	if !strings.Contains(err.Error(), "find") {
		t.Errorf("the error does not name the missing tool: %v", err)
	}
}

// GNU sha256sum escapes a whole line when the name holds a backslash or a
// newline. Such a name cannot come from Windows at all, so avar reports it
// rather than guessing at the unescaping.
func TestParseNativeScan_SkipsAnEscapedName_REQ_14_2(t *testing.T) {
	out := "AVR:baseline\nAVR:mount\nAVR:present\n\\" + hashA + "  ./odd\\\\name\nAVR:meta\nAVR:workspace\nAVR:absent\nAVR:end\n"
	scan, err := parseNativeScan(out)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if len(scan.Mount) != 0 {
		t.Errorf("an escaped name was carried into the manifest: %v", scan.Mount)
	}
	if len(scan.Skipped) != 1 {
		t.Errorf("Skipped = %v, want the escaped name reported", scan.Skipped)
	}
}

// The walk must not enter a build output tree. Copying node_modules back onto
// the Windows filesystem would undo the entire point of native mode.
func TestNativeScanScript_PrunesBuildOutput_REQ_14_1(t *testing.T) {
	p := nativeProvider(t)
	ws, _, err := p.MapNativeWorkspace(strings.Repeat("d", 64), `C:\Users\ola\code\app`, `C:\Users\ola\code\app`)
	if err != nil {
		t.Fatal(err)
	}
	script := nativeScanScript(ws)

	for _, name := range types.WorkspaceExcludedDirs {
		if !strings.Contains(script, "-name '"+name+"'") {
			t.Errorf("the scan does not prune %q:\n%s", name, script)
		}
	}
	if !strings.Contains(script, "-prune") {
		t.Errorf("the scan has no prune clause at all:\n%s", script)
	}
	// Both copies are read in one invocation: three would be three different
	// moments, and a plan built from that describes a state that never existed.
	if !strings.Contains(script, shellQuote(ws.MountPath)) || !strings.Contains(script, shellQuote(ws.Path)) {
		t.Errorf("the scan does not read both copies:\n%s", script)
	}
}

// REQ-17.5: a file arrives whole or not at all, and the baseline is written
// last. Together those are what make an interrupted synchronization something a
// repeat converges from rather than a state nobody can interpret.
func TestNativeApplyScript_WritesEachFileAsideAndTheBaselineLast_REQ_17_5(t *testing.T) {
	p := nativeProvider(t)
	ws, _, err := p.MapNativeWorkspace(strings.Repeat("e", 64), `C:\Users\ola\code\app`, `C:\Users\ola\code\app`)
	if err != nil {
		t.Fatal(err)
	}

	sync := types.WorkspaceSync{
		Direction: types.ToGuest,
		Copy:      []string{"src/main.go"},
		Delete:    []string{"old.txt"},
		Baseline:  types.WorkspaceManifest{"src/main.go": {Hash: hashA}},
	}
	script := p.nativeApplyScript(ws, sync)

	if !strings.HasPrefix(script, "set -e\n") {
		t.Errorf("the script does not abort on the first failure:\n%s", script)
	}
	if !strings.Contains(script, syncTempSuffix) || !strings.Contains(script, "mv -f --") {
		t.Errorf("a file is copied straight over its destination rather than moved into place:\n%s", script)
	}

	baselineAt := strings.Index(script, baselineDir)
	copyAt := strings.LastIndex(script, "avr_cp ")
	deleteAt := strings.LastIndex(script, "avr_rm ")
	if baselineAt < copyAt || baselineAt < deleteAt {
		t.Errorf("the baseline is written before the work it describes has been done:\n%s", script)
	}

	// Every path travels encoded, so a file named `; rm -rf /` is a file name.
	if strings.Contains(script, "src/main.go") {
		t.Errorf("a project path was interpolated into the script as text:\n%s", script)
	}
	if !strings.Contains(script, "base64 -d") {
		t.Errorf("paths are not decoded in the guest:\n%s", script)
	}
}

// Every script this package sends runs as root, so a file copied into the guest
// belongs to root unless avar says otherwise — which would leave the user unable
// to write to their own project. This is the same defect the mount options
// exist to avoid, one layer along.
func TestNativeApplyScript_GivesTheCopyToTheUserNotRoot_REQ_1_4(t *testing.T) {
	p := nativeProvider(t)
	ws, _, err := p.MapNativeWorkspace(strings.Repeat("f", 64), `C:\Users\ola\code\app`, `C:\Users\ola\code\app`)
	if err != nil {
		t.Fatal(err)
	}

	toGuest := p.nativeApplyScript(ws, types.WorkspaceSync{
		Direction: types.ToGuest, Copy: []string{"a.txt"}, Baseline: types.WorkspaceManifest{},
	})
	if !strings.Contains(toGuest, "chown '"+testUser+"':'"+testUser+"'") {
		t.Errorf("a file copied into the guest is not handed to the user's account:\n%s", toGuest)
	}
	if !strings.Contains(toGuest, "-o '"+testUser+"' -g '"+testUser+"'") {
		t.Errorf("a directory created in the guest is not handed to the user's account:\n%s", toGuest)
	}

	// Going the other way there is nothing to set: a DrvFS file's ownership
	// comes from the mount's own uid/gid options, and a chown there could fail
	// and abort a synchronization over something avar does not want anyway.
	toHost := p.nativeApplyScript(ws, types.WorkspaceSync{
		Direction: types.ToHost, Copy: []string{"a.txt"}, Baseline: types.WorkspaceManifest{},
	})
	if strings.Contains(toHost, "chown") {
		t.Errorf("a file copied to the host is chowned, which DrvFS decides for itself:\n%s", toHost)
	}
	if !strings.Contains(toHost, "AVR_SRC="+shellQuote(ws.Path)) {
		t.Errorf("syncing to the host does not read from the native copy:\n%s", toHost)
	}
	if !strings.Contains(toHost, "AVR_DST="+shellQuote(ws.MountPath)) {
		t.Errorf("syncing to the host does not write to the share:\n%s", toHost)
	}
}

// The baseline round-trips: what avar writes is what avar reads back, or the
// next scan reports every file as a conflict.
func TestBaseline_RoundTrips_REQ_14_3(t *testing.T) {
	want := types.WorkspaceManifest{
		"src/main.go":    {Hash: hashA},
		"a file with sp": {Hash: hashB, Exec: true},
	}
	rendered := renderBaseline(want)

	got := types.WorkspaceManifest{}
	for _, line := range strings.Split(rendered, "\n") {
		if line == "" {
			continue
		}
		rel, entry, ok := parseBaselineLine(line)
		if !ok {
			t.Fatalf("avar could not read back a baseline line it wrote: %q", line)
		}
		got[rel] = entry
	}
	if len(got) != len(want) {
		t.Fatalf("read back %d entries, want %d: %v", len(got), len(want), got)
	}
	for rel, entry := range want {
		if got[rel] != entry {
			t.Errorf("%s round-tripped as %+v, want %+v", rel, got[rel], entry)
		}
	}
}

// PROP-6: a distribution avar did not create is not one it will read a project
// out of, whatever it is asked. The refusal comes before any subprocess.
func TestNativeWorkspace_RefusesAMachineAvarDoesNotOwn_PROP_6(t *testing.T) {
	p := nativeProvider(t)
	ws := types.NativeWorkspace{
		ProjectID: strings.Repeat("a", 64),
		HostPath:  `C:\Users\ola\code\app`,
		MountPath: "/mnt/avr/projects/app-aaaaaaaaaa",
		Path:      "/home/ola/workspaces/app-aaaaaaaaaa",
	}

	if _, err := p.ScanNativeWorkspace(context.Background(), "Ubuntu", ws); err == nil {
		t.Error("avar scanned a distribution it does not own")
	}
	if err := p.ApplyNativeWorkspace(context.Background(), "Ubuntu", ws, types.WorkspaceSync{Direction: types.ToHost}, nil); err == nil {
		t.Error("avar synchronized into a distribution it does not own")
	}
}
