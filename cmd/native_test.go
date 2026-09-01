package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/types"
)

// Flow tests for Linux-native workspace mode: the real command code against the
// in-process FakeProvider, asserting the provider calls it made and the text it
// wrote. The Fake models both copies for real, so a flow that asks for the wrong
// direction — or applies a plan it should have refused — fails here rather than
// on a user's machine.

// syncInvocation is `avr sync [...]` as the grammar hands it over.
func syncInvocation(args ...string) cli.Invocation {
	return cli.Invocation{Mode: cli.ModeSubcommand, Subcommand: "sync", SubcommandArgs: args}
}

// nativeInvocation is `avr --native-fs [command...]`.
func nativeInvocation(guest ...string) cli.Invocation {
	inv := cli.Invocation{NativeFS: true}
	if len(guest) > 0 {
		inv.Mode, inv.Guest = cli.ModeGuestCommand, guest
	}
	return inv
}

func entry(hash string) types.WorkspaceEntry {
	for len(hash) < 64 {
		hash += "0"
	}
	return types.WorkspaceEntry{Hash: hash}
}

// nativeTarget resolves this invocation the way the command does and returns the
// machine name and the workspace the provider plans for it.
func nativeTarget(t *testing.T, app *testApp, f *fake.Fake) (string, types.NativeWorkspace) {
	t.Helper()
	target, err := app.Resolve(nativeInvocation())
	if err != nil {
		t.Fatalf("resolving the target environment: %v", err)
	}
	ws, _, err := f.MapNativeWorkspace(target.Project.ID, target.Project.Path, target.HostCwd)
	if err != nil {
		t.Fatalf("planning the native workspace: %v", err)
	}
	return target.MachineName, ws
}

// REQ-14.1: `avr --native-fs` on a project that has never had a native copy
// creates one, copies the project into it, and runs the session there — not in
// the share it was copied from.
func TestNativeFS_CopiesTheProjectInAndRunsThere_REQ_14_1(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	machine, ws := nativeTarget(t, app, f)
	seedMachine(t, f, machine, ubuntu(), types.KindShared)

	f.SetWorkspaceHost(ws.Path, types.WorkspaceManifest{"main.go": entry("a1"), "src/api.go": entry("a2")})
	f.SetWorkspaceGuest(ws.Path, nil)

	if err := runGuest(context.Background(), app.App, nativeInvocation("true")); err != nil {
		t.Fatalf("avr --native-fs true: %v", err)
	}

	apply := f.AssertCalled(t, fake.OpApplyNativeWorkspace)
	if apply.Sync.Direction != types.ToGuest {
		t.Errorf("first copy went %s, want %s", apply.Sync.Direction, types.ToGuest)
	}
	if len(apply.Sync.Copy) != 2 {
		t.Errorf("copied %v, want both of the project's files", apply.Sync.Copy)
	}

	// The session runs in the native copy. If it ran in the share, native mode
	// would be a no-op that still paid for the copy.
	shell := f.AssertCalled(t, fake.OpShell)
	if shell.Shell.Workdir != ws.Path {
		t.Errorf("the session started in %q, want the native copy %q", shell.Shell.Workdir, ws.Path)
	}
	if shell.Shell.Workdir == ws.MountPath {
		t.Errorf("the session started in the share %q rather than on the Linux filesystem", ws.MountPath)
	}

	// And the copy really landed, rather than only being requested.
	if got := f.WorkspaceGuest(ws.Path); len(got) != 2 {
		t.Errorf("the native copy holds %v, want both files", got)
	}
}

// The advisory that recommends --native-fs is not shown to somebody who has
// just used it. Advice a user has already acted on is noise, and noise is how
// the advice that matters stops being read (REQ-18.11).
func TestNativeFS_DoesNotAdviseWhatTheUserJustDid_REQ_18_11(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	machine, ws := nativeTarget(t, app, f)
	seedMachine(t, f, machine, ubuntu(), types.KindShared)
	f.SetWorkspaceGuest(ws.Path, types.WorkspaceManifest{})

	if err := runGuest(context.Background(), app.App, nativeInvocation("true")); err != nil {
		t.Fatalf("avr --native-fs true: %v", err)
	}
	if strings.Contains(app.err.String(), "once per project") {
		t.Errorf("the native-workspace advisory was shown to a user already in native mode:\n%s", app.err.String())
	}
}

// REQ-14.3: entering native mode when both copies changed the same file
// differently copies nothing at all, and says so. The shell is still opened,
// because resolving the conflict is something the user needs a shell to do.
func TestNativeFS_RefusesToOverwriteOnAConflict_REQ_14_3(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	machine, ws := nativeTarget(t, app, f)
	seedMachine(t, f, machine, ubuntu(), types.KindShared)

	f.SetWorkspaceBaseline(ws.Path, types.WorkspaceManifest{"a.txt": entry("v1")})
	f.SetWorkspaceHost(ws.Path, types.WorkspaceManifest{"a.txt": entry("v2")})
	f.SetWorkspaceGuest(ws.Path, types.WorkspaceManifest{"a.txt": entry("v3")})

	if err := runGuest(context.Background(), app.App, nativeInvocation("true")); err != nil {
		t.Fatalf("avr --native-fs true: %v", err)
	}

	if f.Count(fake.OpApplyNativeWorkspace) != 0 {
		t.Errorf("avar synchronized despite a conflict: %v", f.CallsFor(fake.OpApplyNativeWorkspace))
	}
	if got := f.WorkspaceGuest(ws.Path)["a.txt"]; got != entry("v3") {
		t.Errorf("the Linux copy of a.txt is %+v, want the guest's own version untouched", got)
	}
	if got := f.WorkspaceHost(ws.Path)["a.txt"]; got != entry("v2") {
		t.Errorf("the host copy of a.txt is %+v, want the host's own version untouched", got)
	}

	out := app.err.String()
	for _, want := range []string{"a.txt", "will not overwrite", "avr sync"} {
		if !strings.Contains(out, want) {
			t.Errorf("the conflict report does not mention %q:\n%s", want, out)
		}
	}
	// The shell still ran: a user cannot fix a conflict without one.
	f.AssertCalled(t, fake.OpShell)
}

// REQ-14.2: `avr sync` with no direction is a review. It shows what each
// direction would do and changes nothing.
func TestSync_ReviewsWithoutApplying_REQ_14_2(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	machine, ws := nativeTarget(t, app, f)
	seedMachine(t, f, machine, ubuntu(), types.KindShared)

	f.SetWorkspaceBaseline(ws.Path, types.WorkspaceManifest{"a.txt": entry("v1"), "b.txt": entry("w1")})
	f.SetWorkspaceHost(ws.Path, types.WorkspaceManifest{"a.txt": entry("v1"), "b.txt": entry("w2")})
	f.SetWorkspaceGuest(ws.Path, types.WorkspaceManifest{"a.txt": entry("v9"), "b.txt": entry("w1")})

	if err := runSync(context.Background(), app.App, syncInvocation()); err != nil {
		t.Fatalf("avr sync: %v", err)
	}

	if f.Count(fake.OpApplyNativeWorkspace) != 0 {
		t.Errorf("a bare `avr sync` applied something: %v", f.CallsFor(fake.OpApplyNativeWorkspace))
	}
	out := app.stdout()
	for _, want := range []string{"a.txt", "b.txt", "modified", "--to-host", "--to-guest", "Nothing has been changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("the review does not mention %q:\n%s", want, out)
		}
	}
}

// REQ-14.2: with a direction and confirmation, the guest's changes reach the
// host — and only the guest's changes. A file the host changed stays as the
// host has it, so nothing the user did on either side is lost.
func TestSync_AppliesOnlyTheChosenDirection_REQ_14_2(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	app.Stdin = strings.NewReader("y\n")
	machine, ws := nativeTarget(t, app, f)
	seedMachine(t, f, machine, ubuntu(), types.KindShared)

	f.SetWorkspaceBaseline(ws.Path, types.WorkspaceManifest{"a.txt": entry("v1"), "b.txt": entry("w1")})
	f.SetWorkspaceHost(ws.Path, types.WorkspaceManifest{"a.txt": entry("v1"), "b.txt": entry("w2")})
	f.SetWorkspaceGuest(ws.Path, types.WorkspaceManifest{"a.txt": entry("v9"), "b.txt": entry("w1")})

	if err := runSync(context.Background(), app.App, syncInvocation("--to-host", "--yes")); err != nil {
		t.Fatalf("avr sync --to-host: %v", err)
	}

	apply := f.AssertCalled(t, fake.OpApplyNativeWorkspace)
	if apply.Sync.Direction != types.ToHost {
		t.Errorf("applied %s, want %s", apply.Sync.Direction, types.ToHost)
	}
	if len(apply.Sync.Copy) != 1 || apply.Sync.Copy[0] != "a.txt" {
		t.Errorf("copied %v, want only the file the guest changed", apply.Sync.Copy)
	}

	host := f.WorkspaceHost(ws.Path)
	if host["a.txt"] != entry("v9") {
		t.Errorf("a.txt on the host is %+v, want the guest's version", host["a.txt"])
	}
	if host["b.txt"] != entry("w2") {
		t.Errorf("b.txt on the host is %+v, want the host's own change untouched", host["b.txt"])
	}
}

// REQ-14.2: applying without --yes puts the listing in front of the user and
// waits. A "no" changes nothing.
func TestSync_AsksBeforeApplying_REQ_14_2(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	app.Stdin = strings.NewReader("")
	machine, ws := nativeTarget(t, app, f)
	seedMachine(t, f, machine, ubuntu(), types.KindShared)

	f.SetWorkspaceBaseline(ws.Path, types.WorkspaceManifest{"a.txt": entry("v1")})
	f.SetWorkspaceHost(ws.Path, types.WorkspaceManifest{"a.txt": entry("v1")})
	f.SetWorkspaceGuest(ws.Path, types.WorkspaceManifest{"a.txt": entry("v9")})

	err := runSync(context.Background(), app.App, syncInvocation("--to-host"))
	if err == nil {
		t.Fatal("`avr sync --to-host` applied without asking on a non-interactive stdin")
	}
	if f.Count(fake.OpApplyNativeWorkspace) != 0 {
		t.Errorf("something was applied despite no confirmation: %v", f.CallsFor(fake.OpApplyNativeWorkspace))
	}
	// The user still sees what would have happened, so a second run with --yes
	// is a decision rather than a guess.
	if !strings.Contains(app.stdout(), "a.txt") {
		t.Errorf("the listing was not shown before the refusal:\n%s", app.stdout())
	}
}

// REQ-14.3: `avr sync` with a conflict outstanding reports it and exits
// non-zero without applying anything, in either direction.
func TestSync_StopsAtAConflict_REQ_14_3(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	machine, ws := nativeTarget(t, app, f)
	seedMachine(t, f, machine, ubuntu(), types.KindShared)

	f.SetWorkspaceBaseline(ws.Path, types.WorkspaceManifest{"a.txt": entry("v1")})
	f.SetWorkspaceHost(ws.Path, types.WorkspaceManifest{"a.txt": entry("v2")})
	f.SetWorkspaceGuest(ws.Path, types.WorkspaceManifest{"a.txt": entry("v3")})

	err := runSync(context.Background(), app.App, syncInvocation("--to-host", "--yes"))
	var exit *ExitCodeError
	if err == nil || !errors.As(err, &exit) || exit.Code == 0 {
		t.Fatalf("`avr sync --to-host --yes` did not fail on a conflict: %v", err)
	}
	if f.Count(fake.OpApplyNativeWorkspace) != 0 {
		t.Errorf("a conflicted sync applied something: %v", f.CallsFor(fake.OpApplyNativeWorkspace))
	}
	if !strings.Contains(app.stdout(), "will not overwrite") {
		t.Errorf("the conflict was not explained:\n%s", app.stdout())
	}
}

// `avr sync` on a project with no native copy explains what to run rather than
// creating one behind the user's back: making a copy is a decision, and the
// command that makes it is `avr --native-fs`.
func TestSync_SaysWhenThereIsNoWorkspaceYet_REQ_14_1(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	machine, ws := nativeTarget(t, app, f)
	seedMachine(t, f, machine, ubuntu(), types.KindShared)
	f.SetWorkspaceGuest(ws.Path, nil)

	err := runSync(context.Background(), app.App, syncInvocation())
	if err == nil {
		t.Fatal("`avr sync` on a project with no native copy succeeded silently")
	}
	if !strings.Contains(err.Error(), "--native-fs") {
		t.Errorf("the error does not say how to create one: %v", err)
	}
	if f.Count(fake.OpApplyNativeWorkspace) != 0 {
		t.Error("`avr sync` created a native copy the user did not ask for")
	}
}

// The two directions are opposites, so asking for both at once is a command
// line avar cannot honour rather than one it silently picks a half of.
func TestParseSyncArgs_RefusesContradictions_REQ_14_2(t *testing.T) {
	if _, err := parseSyncArgs([]string{"--to-host", "--to-guest"}); err == nil {
		t.Error("both directions at once were accepted")
	}
	if _, err := parseSyncArgs([]string{"--everything"}); err == nil {
		t.Error("an unknown flag was accepted")
	}
	args, err := parseSyncArgs([]string{"--to-guest", "--yes"})
	if err != nil {
		t.Fatalf("parsing a valid command line: %v", err)
	}
	if args.direction != types.ToGuest || !args.yes {
		t.Errorf("parsed %+v, want to-guest with --yes", args)
	}
}
