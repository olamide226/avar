package lima

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// errStubbornRecords stands in for a state store that cannot be read.
var errStubbornRecords = errors.New("reading machines.json: permission denied")

func TestAppliedMounts_ReportsWhatLimaHasApplied_REQ_6_4(t *testing.T) {
	// What the backend has applied, not what avar's records expect: comparing
	// the two is how a caller decides whether this project still needs
	// registering.
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-arm64")))

	got, err := p.AppliedMounts(context.Background(), "avr-ubuntu-24.04-arm64")
	if err != nil {
		t.Fatalf("AppliedMounts: %v", err)
	}
	want := []string{"/Users/dev/code/api", "/Users/dev/code/web"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AppliedMounts = %v, want %v", got, want)
	}
	assertArgvs(t, runner.limactlArgvs(), []string{"limactl list --json"})
}

func TestAppliedMounts_WorksOnAStoppedMachine(t *testing.T) {
	// It is a read-only query: Lima records the mounts in the instance's own
	// configuration whatever state the machine is in.
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-fedora-42-amd64")))

	got, err := p.AppliedMounts(context.Background(), "avr-fedora-42-amd64")
	if err != nil {
		t.Fatalf("AppliedMounts: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"/Users/dev/code/api"}) {
		t.Errorf("AppliedMounts = %v", got)
	}
}

func TestSetMounts_UnchangedSetChangesNothing_REQ_17_1(t *testing.T) {
	// Returning to a project avar already knows has to stay instant: no
	// restart, no progress, no subprocess beyond the one question.
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-arm64")))
	sink := &recordingSink{}

	// Deliberately in a different order and with a redundant trailing slash:
	// the same set, expressed differently, is still the same set.
	err := p.SetMounts(context.Background(), "avr-ubuntu-24.04-arm64",
		[]string{"/Users/dev/code/web/", "/Users/dev/code/api"}, sink)
	if err != nil {
		t.Fatalf("SetMounts: %v", err)
	}

	assertArgvs(t, runner.limactlArgvs(), []string{"limactl list --json"})
	if kinds := sink.kinds(); len(kinds) != 0 {
		t.Errorf("an unchanged mount set produced progress the user would see on an ordinary command: %v", kinds)
	}
}

func TestSetMounts_RestartsOnceAndVerifies_REQ_6_4(t *testing.T) {
	project := t.TempDir()
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-arm64")))
	sink := &recordingSink{}

	dirs := []string{"/Users/dev/code/api", "/Users/dev/code/web", project}
	if err := p.SetMounts(context.Background(), "avr-ubuntu-24.04-arm64", dirs, sink); err != nil {
		t.Fatalf("SetMounts: %v", err)
	}

	// Lima refuses to edit a running instance, so a mount change is stop,
	// edit, start — and then a check that the share really landed.
	want := []string{
		"limactl list --json",
		"limactl stop avr-ubuntu-24.04-arm64",
		"limactl edit avr-ubuntu-24.04-arm64 --set " + mountsExpression(sortedUnique(dirs)) + " --tty=false",
		"limactl start --tty=false avr-ubuntu-24.04-arm64",
		"limactl shell --workdir / avr-ubuntu-24.04-arm64 -- test -d /Users/dev/code/api",
		"limactl shell --workdir / avr-ubuntu-24.04-arm64 -- test -w /Users/dev/code/api",
		"limactl shell --workdir / avr-ubuntu-24.04-arm64 -- test -d /Users/dev/code/web",
		"limactl shell --workdir / avr-ubuntu-24.04-arm64 -- test -w /Users/dev/code/web",
		"limactl shell --workdir / avr-ubuntu-24.04-arm64 -- test -d " + project,
		"limactl shell --workdir / avr-ubuntu-24.04-arm64 -- test -w " + project,
	}
	assertArgvs(t, runner.limactlArgvs(), want)

	mounting := sink.of(types.ProgressMounting)
	if len(mounting) != 1 {
		t.Fatalf("want one Mounting event explaining the delay, got %d: %v", len(mounting), sink.kinds())
	}
	// The user has to be told why an otherwise instant command took ten
	// seconds, and told before it happens rather than after.
	for _, want := range []string{project, "restart"} {
		if !strings.Contains(mounting[0].Message, want) {
			t.Errorf("the Mounting message does not mention %q: %q", want, mounting[0].Message)
		}
	}
	if kind := sink.kinds()[0]; kind != types.ProgressMounting {
		t.Errorf("the explanation came after the work: first event was %q", kind)
	}
}

func TestSetMounts_LeavesAStoppedMachineStopped(t *testing.T) {
	// Starting a machine the caller did not ask to start would be a side
	// effect of its own. The new configuration applies at the next start.
	project := t.TempDir()
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-fedora-42-amd64")))

	err := p.SetMounts(context.Background(), "avr-fedora-42-amd64",
		[]string{"/Users/dev/code/api", project}, types.DiscardProgress)
	if err != nil {
		t.Fatalf("SetMounts: %v", err)
	}
	for _, argv := range runner.limactlArgvs() {
		if strings.HasPrefix(argv, "limactl stop") || strings.HasPrefix(argv, "limactl start") {
			t.Fatalf("a stopped machine was cycled: %v", runner.limactlArgvs())
		}
	}
}

func TestSetMounts_MountThatDoesNotAppearInTheGuestIsAnError_REQ_6_5(t *testing.T) {
	// Dropping the user into a shell at an empty path is worse than failing.
	project := t.TempDir()
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json")).
		failOn("shell", errors.New("exit status 1"))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-arm64")))

	err := p.SetMounts(context.Background(), "avr-ubuntu-24.04-arm64",
		[]string{"/Users/dev/code/api", "/Users/dev/code/web", project}, types.DiscardProgress)
	if err == nil {
		t.Fatal("a mount that never appeared in the guest was reported as applied")
	}
	if !strings.Contains(err.Error(), "network volume") {
		t.Errorf("the error does not explain the most likely cause or what to do: %v", err)
	}
}

func TestSetMounts_MissingHostDirectoryFailsBeforeAnyRestart_REQ_6_5(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-there")
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-arm64")))

	err := p.SetMounts(context.Background(), "avr-ubuntu-24.04-arm64",
		[]string{"/Users/dev/code/api", "/Users/dev/code/web", missing}, types.DiscardProgress)
	if err == nil {
		t.Fatal("a directory that does not exist was accepted")
	}
	// Learning this after a ten-second restart would be ten seconds spent to
	// find out nothing.
	assertArgvs(t, runner.limactlArgvs(), []string{"limactl list --json"})
}

func TestSetMounts_ReplacesRatherThanAppends_PROP_5(t *testing.T) {
	// The guest reaches exactly the project roots avar registered. Anything
	// absent from the desired set stops being shared, which is what makes
	// mount confinement checkable.
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-arm64")))

	err := p.SetMounts(context.Background(), "avr-ubuntu-24.04-arm64",
		[]string{"/Users/dev/code/api"}, types.DiscardProgress)
	if err != nil {
		t.Fatalf("SetMounts: %v", err)
	}

	var edit string
	for _, argv := range runner.limactlArgvs() {
		if strings.Contains(argv, " edit ") {
			edit = argv
		}
	}
	if strings.Contains(edit, "/Users/dev/code/web") {
		t.Errorf("a directory dropped from the desired set is still shared: %s", edit)
	}
	if !strings.Contains(edit, ".mounts = [") {
		t.Errorf("the mount list was not replaced wholesale: %s", edit)
	}
}

func TestSetMounts_UnknownMachineIsNotFound(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-empty.json"))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord(testMachine)))

	err := p.SetMounts(context.Background(), testMachine, []string{"/Users/dev/code/api"}, types.DiscardProgress)
	if !errors.Is(err, provider.ErrMachineNotFound) {
		t.Fatalf("want ErrMachineNotFound, got %v", err)
	}
}

func TestMountsExpression_QuotesPathsAsData(t *testing.T) {
	// A directory name is user data and reaches yq as part of an expression.
	// It has to be a quoted scalar there, or a bracket in a directory name
	// could restructure the expression.
	got := mountsExpression([]string{`/Users/dev/we"ird]`})
	want := `.mounts = [{"location": "/Users/dev/we\"ird]", "mountPoint": "/Users/dev/we\"ird]", "writable": true}]`
	if got != want {
		t.Errorf("mountsExpression =\n%s\nwant\n%s", got, want)
	}
}

func TestMountsExpression_SharesEveryDirectoryWritableAtItsOwnPath_REQ_6_1(t *testing.T) {
	got := mountsExpression([]string{"/Users/dev/code/api", "/Users/dev/code/web"})
	for _, want := range []string{
		`{"location": "/Users/dev/code/api", "mountPoint": "/Users/dev/code/api", "writable": true}`,
		`{"location": "/Users/dev/code/web", "mountPoint": "/Users/dev/code/web", "writable": true}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("mountsExpression is missing %s:\n%s", want, got)
		}
	}
}

func TestMountsExpression_EmptySetSharesNothing(t *testing.T) {
	if got := mountsExpression(nil); got != ".mounts = []" {
		t.Errorf("mountsExpression(nil) = %q, want an empty list", got)
	}
}

func TestNormalizeMounts_CanonicalisesTheDesiredSet(t *testing.T) {
	got, err := normalizeMounts([]string{"/b/../b/two/", "/a/one", "/a/one"})
	if err != nil {
		t.Fatalf("normalizeMounts: %v", err)
	}
	want := []string{"/a/one", "/b/two"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalizeMounts = %v, want %v", got, want)
	}
}

func TestNormalizeMounts_RefusesRelativePaths(t *testing.T) {
	// avar shares a project at the identical absolute path in the guest, so a
	// relative path has no meaning — and resolving it against avar's own
	// working directory could share the wrong tree.
	for _, dir := range []string{"code/api", "", "./api", "~/code"} {
		if _, err := normalizeMounts([]string{dir}); err == nil {
			t.Errorf("normalizeMounts(%q) was accepted", dir)
		}
	}
}

func TestMountingMessage_ExplainsTheDelayOnlyWhenThereIsOne(t *testing.T) {
	restarting := mountingMessage([]string{"/b"}, true)
	if !strings.Contains(restarting, "/b") || !strings.Contains(restarting, "restart") {
		t.Errorf("a restart was not explained: %q", restarting)
	}
	stopped := mountingMessage([]string{"/b"}, false)
	if strings.Contains(stopped, "restart") {
		t.Errorf("a delay that is not happening was announced: %q", stopped)
	}
}

func TestSetMounts_DoesNotRecheckDirectoriesItAlreadyShares(t *testing.T) {
	// A project registered months ago may since have been deleted. Refusing to
	// register today's project because of it would be a failure the user
	// cannot act on, and the stale entry is dropped by this very call anyway.
	project := t.TempDir()
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord("avr-ubuntu-24.04-arm64")))

	// /Users/dev/code/api and /Users/dev/code/web do not exist on this
	// machine, and are exactly what the fixture says Lima already shares.
	err := p.SetMounts(context.Background(), "avr-ubuntu-24.04-arm64",
		[]string{"/Users/dev/code/api", "/Users/dev/code/web", project}, types.DiscardProgress)
	if err != nil {
		t.Fatalf("SetMounts: %v", err)
	}
}
