//go:build windows

// This backend's tests are Windows tests, for the same reason the Lima
// backend's are Unix tests: they assert on Windows paths, and what counts as an
// absolute path is the host's question, not avar's. `C:\Users\ola\code\app` is
// absolute on Windows and a relative path everywhere else, so path/filepath —
// and with it MapProjectPath, MountSpec.Validate and every mount the two plan —
// answers differently off Windows. Verified by cross-compiling this package's
// tests for linux/amd64 and running them under WSL: fourteen fail, all of them
// on that one difference.
//
// Prefixing a drive letter is not available as a fix here the way it was for
// the host-neutral packages. There the fixture was a stand-in for whatever the
// host calls an absolute path; here the Windows path *is* the subject.
//
// What the macOS build claims for this package is therefore that it compiles —
// which is what keeps `avr` linkable while both backends live in one binary —
// not that a backend which cannot run there passes its behaviour tests.

package wsl2

import (
	"context"
	"errors"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

const testProjectID = "3fa9c2b1d0e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0"

// --- Path mapping ---------------------------------------------------------

// REQ-18.5, PROP-1: a Windows path is not a Linux path and never can be, so the
// project appears at a Linux path avar chose, and the working directory keeps
// its position beneath it.
func TestMapProjectPath_MapsAWindowsProjectOntoALinuxPath_REQ_18_5(t *testing.T) {
	t.Parallel()

	mount, guestCwd, err := (&Provider{}).MapProjectPath(testProjectID, `C:\Users\ola\code\app`, `C:\Users\ola\code\app\services\api`)
	if err != nil {
		t.Fatalf("MapProjectPath: %v", err)
	}

	if mount.HostPath != `C:\Users\ola\code\app` {
		t.Errorf("HostPath = %q, want the Windows project root unchanged", mount.HostPath)
	}
	if !strings.HasPrefix(mount.GuestPath, GuestProjectRoot+"/") {
		t.Errorf("GuestPath = %q, want it beneath %s", mount.GuestPath, GuestProjectRoot)
	}
	if err := mount.Validate(); err != nil {
		t.Errorf("the planned mount is not applicable: %v", err)
	}
	// The relative suffix is preserved, which is the whole of PROP-1: the
	// directory the user is in is the directory the guest starts in.
	if want := path.Join(mount.GuestPath, "services", "api"); guestCwd != want {
		t.Errorf("guestCwd = %q, want %q", guestCwd, want)
	}
	if mount.ProjectID != testProjectID {
		t.Errorf("ProjectID = %q, want the identity the mount serves", mount.ProjectID)
	}
	if !mount.Writable {
		t.Error("a project is shared writable; the point is editing the same files from both sides")
	}
}

// The guest path names the project before it identifies it. It is the user's
// working directory — in their prompt, their editor's title bar and every error
// any tool prints — so it has to be readable as well as unique.
func TestGuestRoot_IsReadableAndUnique_REQ_18_5(t *testing.T) {
	t.Parallel()

	root := GuestRoot(testProjectID, `C:\Users\ola\code\app`)
	if !strings.HasPrefix(root, GuestProjectRoot+"/app-") {
		t.Errorf("GuestRoot = %q, want it to name the project directory", root)
	}
	if !strings.HasSuffix(root, testProjectID[:guestDirHashLen]) {
		t.Errorf("GuestRoot = %q, want it to carry the project identity", root)
	}
}

// PROP-14: two projects with the same directory name — which a developer really
// does have — must not land on the same guest path, or one would hide the other.
func TestGuestRoot_DistinctProjectsGetDistinctPaths_PROP_14(t *testing.T) {
	t.Parallel()

	work := GuestRoot("aaaaaaaaaa11111111", `C:\Users\ola\work\api`)
	personal := GuestRoot("bbbbbbbbbb22222222", `C:\Users\ola\personal\api`)
	if work == personal {
		t.Fatalf("both projects map to %s; one would hide the other", work)
	}
	for _, got := range []string{work, personal} {
		if !strings.HasPrefix(got, GuestProjectRoot+"/") {
			t.Errorf("guest root %q escapes %s", got, GuestProjectRoot)
		}
	}
}

// The mapping is deterministic: the same project is the same guest path across
// invocations, or a project's remembered state would be attached to a directory
// that moves.
func TestMapProjectPath_IsDeterministic_PROP_14(t *testing.T) {
	t.Parallel()

	first, _, err := (&Provider{}).MapProjectPath(testProjectID, `C:\Users\ola\code\app`, `C:\Users\ola\code\app`)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := (&Provider{}).MapProjectPath(testProjectID, `C:\Users\ola\code\app`, `C:\Users\ola\code\app`)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("two calls planned different mounts: %v and %v", first, second)
	}
}

// A directory whose name has nothing usable in it, or a drive root that names
// nothing at all, still gets a valid, unique guest path.
func TestGuestRoot_HandlesNamesThatAreNotNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		hostRoot string
	}{
		{name: "drive root", hostRoot: `C:\`},
		{name: "non-Latin name", hostRoot: `C:\Users\ola\プロジェクト`},
		{name: "spaces and punctuation", hostRoot: `C:\Users\ola\My Code (v2)!`},
		{name: "a name that is only separators", hostRoot: `C:\Users\ola\---`},
		{name: "a very long name", hostRoot: `C:\` + strings.Repeat("a", 200)},
	}

	seen := map[string]string{}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := GuestRoot(testProjectID, tc.hostRoot)
			mount := types.MountSpec{HostPath: tc.hostRoot, GuestPath: root, Writable: true}
			if err := mount.Validate(); err != nil {
				t.Errorf("GuestRoot(%q) = %q, which is not a usable guest path: %v", tc.hostRoot, root, err)
			}
			if !strings.HasPrefix(root, GuestProjectRoot+"/") {
				t.Errorf("GuestRoot(%q) = %q, which escapes %s", tc.hostRoot, root, GuestProjectRoot)
			}
			if strings.ContainsAny(root, ` '"\`) {
				t.Errorf("GuestRoot(%q) = %q, which would need quoting wherever it is used", tc.hostRoot, root)
			}
			seen[tc.hostRoot] = root
		})
	}
}

// REQ-9.3, PROP-5: a working directory outside the project is refused rather
// than mapped. A guest path that escaped its own project mount is exactly what
// mount confinement forbids.
func TestMapProjectPath_RefusesAWorkingDirectoryOutsideTheProject_PROP_5(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		root string
		cwd  string
	}{
		{name: "a sibling", root: `C:\Users\ola\code\app`, cwd: `C:\Users\ola\code\other`},
		{name: "the parent", root: `C:\Users\ola\code\app`, cwd: `C:\Users\ola\code`},
		{name: "a traversal", root: `C:\Users\ola\code\app`, cwd: `C:\Users\ola\code\app\..\..\secrets`},
		{name: "another drive", root: `C:\Users\ola\code\app`, cwd: `D:\Users\ola\code\app\sub`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, guestCwd, err := (&Provider{}).MapProjectPath(testProjectID, tc.root, tc.cwd); err == nil {
				t.Errorf("mapping %s under %s succeeded, giving %s", tc.cwd, tc.root, guestCwd)
			}
		})
	}
}

// A caller with no project identity, or with a path that is not absolute, is
// refused rather than given a guess.
func TestMapProjectPath_RefusesWhatItCannotMap(t *testing.T) {
	t.Parallel()

	if _, _, err := (&Provider{}).MapProjectPath("", `C:\code\app`, `C:\code\app`); err == nil {
		t.Error("a mount was planned with no project identity to name it")
	}
	if _, _, err := (&Provider{}).MapProjectPath(testProjectID, `code\app`, `code\app`); err == nil {
		t.Error("a relative project root was mapped")
	}
}

// --- Mount application ----------------------------------------------------

// REQ-18.5: automount is off, so avar mounts the project itself — one directory
// at a time, through the same DrvFS driver, at the path it planned.
func TestSetMounts_SharesTheProjectThroughDrvFS_REQ_18_5(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	mount, _, err := p.MapProjectPath(testProjectID, `C:\Users\ola\code\app`, `C:\Users\ola\code\app`)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetMounts(context.Background(), testMachine, []types.MountSpec{mount}, types.DiscardProgress); err != nil {
		t.Fatalf("SetMounts: %v", err)
	}

	script := f.lastGuestScript(t, "mount -t drvfs")
	if !strings.Contains(script, "mount -t drvfs 'C:\\Users\\ola\\code\\app' '"+mount.GuestPath+"'") {
		t.Errorf("the project was not shared at the planned path:\n%s", script)
	}
	// metadata is what makes chmod work at all on a Windows filesystem; without
	// it every file is 0777 and `chmod +x` silently does nothing.
	if !strings.Contains(script, "metadata") {
		t.Errorf("the share has no metadata option, so Linux permissions would not work:\n%s", script)
	}
}

// Registering a project a user has never opened costs a mount, not a restart.
// The Lima backend has to restart an instance to change its shares; this one
// does not, and must not pretend otherwise.
func TestSetMounts_NeedsNoRestart_REQ_17_1(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	mount, _, err := p.MapProjectPath(testProjectID, `C:\Users\ola\code\app`, `C:\Users\ola\code\app`)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetMounts(context.Background(), testMachine, []types.MountSpec{mount}, types.DiscardProgress); err != nil {
		t.Fatalf("SetMounts: %v", err)
	}
	if f.ranAny("--terminate") || f.ranAny("--shutdown") {
		t.Errorf("sharing a project restarted the environment: %v", f.argvs())
	}
}

// REQ-17.1: returning to a project the user already had open has to stay
// instant, so an unchanged set changes nothing and says nothing.
func TestSetMounts_IsIdempotent_REQ_17_1(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	mount, _, err := p.MapProjectPath(testProjectID, `C:\Users\ola\code\app`, `C:\Users\ola\code\app`)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetMounts(context.Background(), testMachine, []types.MountSpec{mount}, types.DiscardProgress); err != nil {
		t.Fatal(err)
	}

	before := len(f.calls)
	events := 0
	sink := types.ProgressFunc(func(types.ProgressEvent) { events++ })
	if err := p.SetMounts(context.Background(), testMachine, []types.MountSpec{mount}, sink); err != nil {
		t.Fatalf("SetMounts again: %v", err)
	}

	for _, c := range f.calls[before:] {
		if script, ok := decodeGuestScript(c); ok && strings.Contains(script, "mount -t drvfs") {
			t.Errorf("an unchanged set was re-applied:\n%s", script)
		}
	}
	if events != 0 {
		t.Errorf("an unchanged set emitted %d progress events", events)
	}
}

// PROP-5: the set is replaced, not added to. A project that is no longer
// registered stops being reachable from the guest.
func TestSetMounts_ReplacesRatherThanAdds_PROP_5(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	first, _, err := p.MapProjectPath("aaaaaaaaaa1", `C:\Users\ola\code\a`, `C:\Users\ola\code\a`)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := p.MapProjectPath("bbbbbbbbbb2", `C:\Users\ola\code\b`, `C:\Users\ola\code\b`)
	if err != nil {
		t.Fatal(err)
	}

	if err := p.SetMounts(context.Background(), testMachine, []types.MountSpec{first}, types.DiscardProgress); err != nil {
		t.Fatal(err)
	}
	if err := p.SetMounts(context.Background(), testMachine, []types.MountSpec{second}, types.DiscardProgress); err != nil {
		t.Fatalf("SetMounts: %v", err)
	}

	script := f.lastGuestScript(t, "umount")
	if !strings.Contains(script, "umount '"+first.GuestPath+"'") {
		t.Errorf("the project that is no longer registered was left mounted:\n%s", script)
	}
	if !strings.Contains(script, "mount -t drvfs 'C:\\Users\\ola\\code\\b'") {
		t.Errorf("the newly registered project was not shared:\n%s", script)
	}
	applied, err := p.AppliedMounts(context.Background(), testMachine)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0].HostPath != second.HostPath {
		t.Errorf("applied = %v, want only the registered project", applied)
	}
}

// PROP-5: a guest target outside avar's own root is not a mount avar planned,
// and applying it would put a Windows directory somewhere nothing checks.
func TestSetMounts_RefusesAGuestPathOutsideAvarsRoot_PROP_5(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	for _, guestPath := range []string{"/", "/home/dev", "/mnt/c", "/etc"} {
		err := p.SetMounts(context.Background(), testMachine,
			[]types.MountSpec{{HostPath: `C:\Users\ola`, GuestPath: guestPath, Writable: true}}, types.DiscardProgress)
		if err == nil {
			t.Errorf("a share at %s was accepted", guestPath)
		}
	}
	for _, c := range f.calls {
		if script, ok := decodeGuestScript(c); ok && strings.Contains(script, "mount -t drvfs") {
			t.Errorf("avar mounted something outside its own root:\n%s", script)
		}
	}
}

// REQ-6.5: a share that did not land is a hard error. Dropping the user into a
// shell at a path that looks like their project and is empty is worse than
// failing outright.
func TestSetMounts_VerifiesRatherThanAssumes_REQ_6_5(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	f.mountsSilentlyFail = true
	p := newProvider(t, f, recorded(testMachine))

	mount, _, err := p.MapProjectPath(testProjectID, `C:\Users\ola\code\app`, `C:\Users\ola\code\app`)
	if err != nil {
		t.Fatal(err)
	}
	err = p.SetMounts(context.Background(), testMachine, []types.MountSpec{mount}, types.DiscardProgress)
	if err == nil {
		t.Fatal("SetMounts reported success although nothing was mounted")
	}
	if !strings.Contains(err.Error(), `C:\Users\ola\code\app`) {
		t.Errorf("the error does not name the project that was not shared:\n%v", err)
	}
}

// The mounts avar writes to /etc/fstab are what bring a project back after WSL
// terminates a distribution for idleness.
func TestSetMounts_SurvivesARestart_REQ_18_12(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	mount, _, err := p.MapProjectPath(testProjectID, `C:\Users\ola\code\app`, `C:\Users\ola\code\app`)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SetMounts(context.Background(), testMachine, []types.MountSpec{mount}, types.DiscardProgress); err != nil {
		t.Fatal(err)
	}

	script := f.lastGuestScript(t, fstabPath)
	// Only avar's own block is rewritten: the file belongs to the distribution
	// and a user who added a line to it must not lose it.
	for _, want := range []string{fstabBegin, fstabEnd, `C:\134Users\134ola\134code\134app`, "drvfs"} {
		if !strings.Contains(script, want) {
			t.Errorf("the fstab rewrite does not contain %q:\n%s", want, script)
		}
	}
}

// /proc/mounts escapes the backslash that a Windows path has in every
// component. Reading it back unescaped would make every applied share compare
// unequal to the one avar planned, so avar would tear down and rebuild them all
// on every invocation.
func TestParseProcMounts_UnescapesWindowsPaths_REQ_18_5(t *testing.T) {
	t.Parallel()

	got := parseProcMounts(
		"C:\\134Users\\134ola\\134code\\134app\t/mnt/avr/projects/app-3fa9c2b1d0\n" +
			"C:\\134Users\\134ola\\134My\\040Docs\t/mnt/avr/projects/my-docs-bbbbbbbbbb\n")
	if len(got) != 2 {
		t.Fatalf("parseProcMounts = %v, want both shares", got)
	}

	// The result is normalized, so it is ordered by host path rather than by
	// the order the kernel happened to list the mounts in.
	byHost := map[string]string{}
	for _, m := range got {
		byHost[m.HostPath] = m.GuestPath
	}
	if _, ok := byHost[`C:\Users\ola\code\app`]; !ok {
		t.Errorf("parseProcMounts = %v, want the backslashes unescaped", got)
	}
	if _, ok := byHost[`C:\Users\ola\My Docs`]; !ok {
		t.Errorf("parseProcMounts = %v, want the escaped space restored", got)
	}
}

// A path with a space in it survives the round trip through fstab, which is a
// whitespace-separated format.
func TestFstabEscaping_RoundTrips(t *testing.T) {
	t.Parallel()

	for _, original := range []string{`C:\Users\ola\code\app`, `C:\Users\ola\My Docs\a b`, "C:\\tabbed\there"} {
		if got := unescapeProcMounts(escapeFstabField(original)); got != original {
			t.Errorf("round trip of %q gave %q", original, got)
		}
	}
}

// --- Shell ----------------------------------------------------------------

// REQ-18.5: --cd interprets an argument that does not begin with / as a Windows
// path and translates it, so a host path passed here would start the process
// somewhere else rather than failing. It has to be the guest path.
func TestShell_StartsInTheGuestDirectory_REQ_18_5(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	guestCwd := GuestRoot(testProjectID, `C:\Users\ola\code\app`) + "/services/api"
	cmd := p.shellCommand(context.Background(), testMachine, provider.ShellOpts{
		Workdir: guestCwd,
		Argv:    []string{"npm", "test"},
	})

	if !containsSequence(cmd.Args, []string{"--cd", guestCwd}) {
		t.Errorf("argv = %v, want the guest working directory", cmd.Args)
	}
	if !containsSequence(cmd.Args, []string{"--distribution", testMachine}) {
		t.Errorf("argv = %v, want the environment named", cmd.Args)
	}
	if !containsSequence(cmd.Args, []string{"--user", testUser}) {
		t.Errorf("argv = %v, want the non-root account", cmd.Args)
	}
}

// REQ-2.5: the guest command is passed as separate arguments after --exec and is
// re-split by nothing. Without --exec, wsl.exe hands the remainder to the guest's
// login shell, which would parse it a second time.
func TestShell_PassesTheCommandWithoutReparsingIt_REQ_2_5(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	argv := []string{"git", "commit", "-m", "two words", "--author", "A <a@b.c>"}
	cmd := p.shellCommand(context.Background(), testMachine, provider.ShellOpts{Workdir: "/work", Argv: argv})

	if !containsSequence(cmd.Args, append([]string{"--exec"}, argv...)) {
		t.Errorf("argv = %v, want the command passed through unchanged after --exec", cmd.Args)
	}
}

// REQ-1.1: no command means the user's own login shell, which avar does not
// choose and does not name.
func TestShell_InteractiveRunsTheUsersOwnShell_REQ_1_1(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	cmd := p.shellCommand(context.Background(), testMachine, provider.ShellOpts{Workdir: "/work", TTY: true})
	if has(cmd.Args, "--exec") {
		t.Errorf("argv = %v, want no --exec so the login shell runs", cmd.Args)
	}
	for _, shell := range []string{"/bin/bash", "/bin/sh", "/bin/zsh", "bash"} {
		if has(cmd.Args, shell) {
			t.Errorf("avar named a shell (%s) instead of letting the guest choose: %v", shell, cmd.Args)
		}
	}
}

// PROP-4: the guest gets exactly the variables policy allows and nothing else.
// WSL's own WSLENV is the mechanism, used as an allowlist rather than worked
// around: a variable not named there cannot cross.
func TestShell_OnlyThePolicysVariablesCross_PROP_4(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	hostEnv := []string{
		"AWS_SECRET_ACCESS_KEY=shh",
		"GITHUB_TOKEN=ghp_secret",
		"PATH=C:\\Windows\\System32",
		// A WSLENV the user already had must be replaced, not extended: it is
		// the door, and avar decides what goes through it.
		"WSLENV=AWS_SECRET_ACCESS_KEY/u:GITHUB_TOKEN/u",
	}
	policy := map[string]string{"TERM": "xterm-256color", "LANG": "en_GB.UTF-8"}

	env := transportEnv(hostEnv, policy)

	var wslenv string
	for _, entry := range env {
		if name, value, _ := strings.Cut(entry, "="); name == wslEnvVar {
			wslenv = value
		}
	}
	if wslenv != "LANG/u:TERM/u" {
		t.Errorf("WSLENV = %q, want exactly the policy's variables", wslenv)
	}
	for _, secret := range []string{"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN"} {
		if strings.Contains(wslenv, secret) {
			t.Errorf("WSLENV carries %s into the guest: %q", secret, wslenv)
		}
	}
	// wsl.exe still needs the host environment to run at all; what matters is
	// that none of it is named in WSLENV.
	if !containsEntry(env, "PATH=C:\\Windows\\System32") {
		t.Errorf("wsl.exe was given no PATH to run with: %v", env)
	}
	_ = p
}

// An empty grant still sets WSLENV, to an empty value: leaving it unset would
// let a WSLENV inherited from somewhere avar did not look decide what crosses.
func TestShell_AnEmptyGrantStillClosesTheDoor_PROP_4(t *testing.T) {
	t.Parallel()

	env := transportEnv([]string{"WSLENV=SECRET/u", "SECRET=shh"}, nil)
	if !containsEntry(env, wslEnvVar+"=") {
		t.Errorf("WSLENV was not cleared: %v", env)
	}
}

// PROP-3: a guest that ran and exited non-zero is a successful call reporting
// that code. Turning it into an error would make avar unusable in a script.
func TestShell_ReportsTheGuestsOwnStatus_PROP_3(t *testing.T) {
	t.Parallel()

	if code, err := exitStatus(testMachine, nil); code != 0 || err != nil {
		t.Errorf("exitStatus(nil) = (%d, %v), want (0, nil)", code, err)
	}

	// A failure to run the command at all is the one thing that is an error.
	if _, err := exitStatus(testMachine, errors.New("the pipe broke")); err == nil {
		t.Error("a transport failure was reported as a guest exit status")
	}
}

// Shell creates and starts nothing: a caller that skipped EnsureMachine is told
// so rather than getting an environment started as a side effect.
func TestShell_StartsNothing_REQ_1_3(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	p := newProvider(t, f, recorded(testMachine))

	_, err := p.Shell(context.Background(), testMachine, provider.ShellOpts{Workdir: "/work", Stdout: os.Stdout})
	if !errors.Is(err, provider.ErrMachineNotRunning) {
		t.Fatalf("error = %v, want ErrMachineNotRunning", err)
	}
	if f.ranAny("--exec") {
		t.Errorf("Shell started the environment: %v", f.argvs())
	}
}

// An execution that contradicts itself is refused before anything starts.
func TestShell_RefusesASelfContradictoryExecution(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	tests := []struct {
		name string
		opts provider.ShellOpts
	}{
		{name: "no working directory", opts: provider.ShellOpts{}},
		// A Windows path here would be translated by --cd rather than refused,
		// starting the process somewhere else entirely.
		{name: "a Windows working directory", opts: provider.ShellOpts{Workdir: `C:\Users\ola\code\app`}},
		{name: "a redirected interactive session", opts: provider.ShellOpts{Workdir: "/work", TTY: true, Stdout: os.Stdout}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := p.Shell(context.Background(), testMachine, tc.opts); err == nil {
				t.Error("the execution was accepted")
			}
		})
	}
}

// PROP-15: a WSL 1 registration is never entered.
func TestShell_RefusesWSL1_PROP_15(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 1, true)
	p := newProvider(t, f, recorded(testMachine))

	_, err := p.Shell(context.Background(), testMachine, provider.ShellOpts{Workdir: "/work", Stdout: os.Stdout})
	if err == nil {
		t.Fatal("Shell entered a WSL 1 distribution")
	}
	if !strings.Contains(err.Error(), "wsl --set-version") {
		t.Errorf("the message does not carry the conversion command:\n%v", err)
	}
}

// containsEntry reports whether env contains an exact NAME=value entry.
func containsEntry(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}
