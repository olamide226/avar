package cmd

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/projconfig"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/resolve"
	"github.com/olamide226/avar/internal/types"
)

// Tests for REQ-15.5: a .avr.toml that asks for more CPUs or memory than this
// computer has is refused where the size would apply, before any machine work,
// and nowhere else (design §3.11, PROP-25).

// sixteenGiBMac is the computer these tests size against.
var sixteenGiBMac = types.HostCapacity{CPUs: 10, MemoryBytes: 16 << 30}

// isolatedInvocation is `avr --isolate true`.
func isolatedInvocation() cli.Invocation {
	inv := guestInvocation("true")
	inv.Selector.Isolate = true
	return inv
}

// newSizeTest is a project with the given file on a 10-core, 16 GiB computer.
func newSizeTest(t *testing.T, avrToml string) *projectTest {
	t.Helper()
	pt := newProjectTest(t, avrToml)
	pt.f.SetHostCapacity(sixteenGiBMac)
	return pt
}

// assertRefusedBeforeMachineWork requires that the only thing the backend was
// asked is what this computer has: nothing was provisioned, started, cloned,
// listed or deleted.
func assertRefusedBeforeMachineWork(t *testing.T, f *fake.Fake) {
	t.Helper()
	for _, c := range f.Calls() {
		if c.Op != fake.OpHostCapacity {
			t.Errorf("the backend was asked to %s before the size was refused:\n%s", c.Op, f.Transcript())
			return
		}
	}
}

// assertConfigurationExit requires the exit status every other .avr.toml
// error has: Execute returns 1 for an error that is neither an ExitCodeError
// nor an unsupported environment, which is exactly what a malformed file
// produces (design §6).
func assertConfigurationExit(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("avr ran with a size this computer does not have")
	}
	var exit *ExitCodeError
	if errors.As(err, &exit) || errors.Is(err, resolve.ErrUnsupportedEnvironment) {
		t.Errorf("the refusal would not exit 1 like other configuration errors: %#v", err)
	}
}

func TestProjectConfig_RejectsMoreCPUsThanTheHost_REQ_15_5(t *testing.T) {
	pt := newSizeTest(t, "cpus = 11\n")

	err := pt.run(t, isolatedInvocation(), false, "")
	assertConfigurationExit(t, err)
	assertRefusedBeforeMachineWork(t, pt.f)
	path := filepath.Join(pt.dir, projconfig.FileName)
	for _, want := range []string{path + " line 1: cpus = 11, but this computer has 10 CPUs", "at most 10", "remove that line"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
}

func TestProjectConfig_RejectsMoreMemoryThanTheHost_REQ_15_5(t *testing.T) {
	pt := newSizeTest(t, "distro = \"ubuntu\"\nmemory = \"64GiB\"\n")

	err := pt.run(t, isolatedInvocation(), false, "")
	assertConfigurationExit(t, err)
	assertRefusedBeforeMachineWork(t, pt.f)
	if want := `line 2: memory = "64GiB", but this computer has 16 GiB of memory`; !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not say %q:\n%v", want, err)
	}
}

// Both sizes over the host are named in one refusal, so fixing the file takes
// one edit rather than two runs.
func TestProjectConfig_RejectsBothSizesInOneMessage_REQ_15_5(t *testing.T) {
	pt := newSizeTest(t, "cpus = 32\nmemory = \"64GiB\"\n")

	err := pt.run(t, isolatedInvocation(), false, "")
	assertConfigurationExit(t, err)
	assertRefusedBeforeMachineWork(t, pt.f)
	for _, want := range []string{"line 1: cpus = 32", `line 2: memory = "64GiB"`, "cpus to at most 10 and memory to at most 16 GiB"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}
}

// Exactly what the computer has is not oversized.
func TestProjectConfig_AllowsExactlyTheHost_REQ_15_5(t *testing.T) {
	pt := newSizeTest(t, "cpus = 10\nmemory = \"16GiB\"\n")

	if err := pt.run(t, isolatedInvocation(), false, ""); err != nil {
		t.Fatalf("avr --isolate refused a size equal to this computer: %v", err)
	}
	if spec := pt.f.AssertCalled(t, fake.OpEnsureMachine).Spec; spec.CPUs != 10 || spec.MemoryGB != 16 {
		t.Errorf("created with %d CPU and %v GB, want 10 and 16", spec.CPUs, spec.MemoryGB)
	}
}

// The shared environment is never sized by a project's file, so an oversized
// value does not stop the user there. The one-time notice says it would also
// be refused if they took its advice.
func TestProjectConfig_OversizedDoesNotBlockTheSharedEnvironment_REQ_15_5(t *testing.T) {
	pt := newSizeTest(t, "memory = \"64GiB\"\n")

	if err := pt.run(t, guestInvocation("true"), false, ""); err != nil {
		t.Fatalf("avr in the shared environment: %v", err)
	}
	pt.f.AssertCalled(t, fake.OpShell)
	for _, want := range []string{"avr --isolate", `more than this computer has (memory = "64GiB", but this computer has 16 GiB of memory)`} {
		if !strings.Contains(pt.err.String(), want) {
			t.Errorf("the notice does not say %q:\n%s", want, pt.err.String())
		}
	}
}

// On a backend that cannot size one environment (WSL 2), the file's size does
// nothing, so it blocks nothing. That backend reports no capacity, so the
// notice says only that the size cannot apply.
func TestProjectConfig_OversizedDoesNotBlockABackendThatCannotSize_REQ_15_5(t *testing.T) {
	pt := newSizeTest(t, "cpus = 64\n")
	pt.prov = struct{ provider.Provider }{pt.f}

	if err := pt.run(t, isolatedInvocation(), false, ""); err != nil {
		t.Fatalf("avr --isolate on a backend that cannot size: %v", err)
	}
	pt.f.AssertCalled(t, fake.OpShell)
	if !strings.Contains(pt.err.String(), "cannot be set") {
		t.Errorf("avr did not say the size cannot apply here:\n%s", pt.err.String())
	}
}

// An isolated environment that already exists is never resized, so a file
// changed to an oversized value does not lock the user out of it, and a warm
// invocation does not even ask what the computer has.
func TestProjectConfig_OversizedDoesNotBlockAnExistingEnvironment_REQ_15_5(t *testing.T) {
	pt := newSizeTest(t, "")
	if err := pt.run(t, isolatedInvocation(), false, ""); err != nil {
		t.Fatal(err)
	}

	pt.writeFile(t, "cpus = 12\n")
	if err := pt.run(t, guestInvocation("true"), false, ""); err != nil {
		t.Fatalf("avr in an existing isolated environment: %v", err)
	}
	pt.f.AssertCalled(t, fake.OpShell)
	for _, want := range []string{"avr reset", "cpus = 12, but this computer has 10 CPUs"} {
		if !strings.Contains(pt.err.String(), want) {
			t.Errorf("the notice does not say %q:\n%s", want, pt.err.String())
		}
	}

	// Once advised, the warm path asks the backend nothing about size.
	if err := pt.run(t, guestInvocation("true"), false, ""); err != nil {
		t.Fatal(err)
	}
	pt.f.AssertNotCalled(t, fake.OpHostCapacity)
}

// `avr reset` recreates the environment at the file's size, so it refuses an
// oversized one before asking for confirmation or destroying anything.
func TestReset_RejectsOversizedProjectConfigBeforeDestroying_REQ_15_5(t *testing.T) {
	pt := newSizeTest(t, "")
	if err := pt.run(t, isolatedInvocation(), false, ""); err != nil {
		t.Fatal(err)
	}
	pt.writeFile(t, "memory = \"17GiB\"\n")
	pt.f.Reset()

	err := runReset(context.Background(), pt.App, resetInvocation("--yes"))
	assertConfigurationExit(t, err)
	pt.f.AssertNotCalled(t, fake.OpDelete)
	pt.f.AssertNotCalled(t, fake.OpEnsureMachine)
	if want := `memory = "17GiB", but this computer has 16 GiB of memory`; !strings.Contains(err.Error(), want) {
		t.Errorf("the refusal does not say %q:\n%v", want, err)
	}
}

// The editor commands create an environment as `avr` does, and refuse first
// as `avr` does.
func TestEditor_RejectsOversizedProjectConfigBeforeMachineWork_REQ_15_5(t *testing.T) {
	newSizeTest(t, "cpus = 11\n")
	e := newEditorTest(t, "code")
	e.f.SetHostCapacity(sixteenGiBMac)

	inv := editorInvocation("code")
	inv.Selector.Isolate = true
	err := e.run(t, inv)
	assertConfigurationExit(t, err)
	assertRefusedBeforeMachineWork(t, e.f)
	if _, ok := e.launched(t); ok {
		t.Error("the editor was opened")
	}
}

// A computer whose capacity cannot be read is not guessed at: the invocation
// stops before machine work and says how to proceed without the sizes.
func TestProjectConfig_UnreadableHostCapacityFailsBeforeMachineWork_REQ_15_5(t *testing.T) {
	pt := newSizeTest(t, "memory = \"8GiB\"\n")
	pt.f.FailOn(fake.OpHostCapacity, errors.New("sysctl: unknown oid"))

	err := runGuest(context.Background(), pt.App, isolatedInvocation())
	assertConfigurationExit(t, err)
	assertRefusedBeforeMachineWork(t, pt.f)
	if !strings.Contains(err.Error(), "sysctl: unknown oid") || !strings.Contains(err.Error(), "Remove cpus and memory") {
		t.Errorf("the failure does not name the cause and the way forward:\n%v", err)
	}
}

// The message is the interface, so its rendering is pinned exactly.
func TestProjectConfig_OversizedMessage_REQ_15_5(t *testing.T) {
	const path = "/Users/dev/app/.avr.toml"
	for _, tc := range []struct {
		name string
		body string
		host types.HostCapacity
		want string
	}{
		{
			name: "cpus",
			body: "cpus = 16",
			host: sixteenGiBMac,
			want: path + " line 1: cpus = 16, but this computer has 10 CPUs\n" +
				"     Lower cpus to at most 10, or remove that line to let avar choose the size, then try again. Nothing was changed.",
		},
		{
			name: "memory in GiB",
			body: "distro = \"fedora\"\nmemory = \"64GiB\"",
			host: sixteenGiBMac,
			want: path + " line 2: memory = \"64GiB\", but this computer has 16 GiB of memory\n" +
				"     Lower memory to at most 16 GiB, or remove that line to let avar choose the size, then try again. Nothing was changed.",
		},
		{
			name: "memory in MiB is compared in MiB",
			body: `memory = "16385MiB"`,
			host: sixteenGiBMac,
			want: path + " line 1: memory = \"16385MiB\", but this computer has 16384 MiB of memory\n" +
				"     Lower memory to at most 16384 MiB, or remove that line to let avar choose the size, then try again. Nothing was changed.",
		},
		{
			name: "a computer short of a whole GiB never reads as equal to the request",
			body: `memory = "16GiB"`,
			host: types.HostCapacity{CPUs: 10, MemoryBytes: 16<<30 - 1<<20},
			want: path + " line 1: memory = \"16GiB\", but this computer has 15.9 GiB of memory\n" +
				"     Lower memory to at most 15 GiB, or remove that line to let avar choose the size, then try again. Nothing was changed.",
		},
		{
			name: "one CPU",
			body: "cpus = 2",
			host: types.HostCapacity{CPUs: 1, MemoryBytes: 8 << 30},
			want: path + " line 1: cpus = 2, but this computer has 1 CPU\n" +
				"     Lower cpus to at most 1, or remove that line to let avar choose the size, then try again. Nothing was changed.",
		},
		{
			name: "both",
			body: "cpus = 32\nmemory = \"64GiB\"",
			host: sixteenGiBMac,
			want: path + " asks for more than this computer has:\n" +
				"       line 1: cpus = 32, but this computer has 10 CPUs\n" +
				"       line 2: memory = \"64GiB\", but this computer has 16 GiB of memory\n" +
				"     Lower cpus to at most 10 and memory to at most 16 GiB, or remove those lines to let avar choose the size, then try again. Nothing was changed.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := projconfig.Parse(path, []byte(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			excess := cfg.ExceedsHost(tc.host)
			if len(excess) == 0 {
				t.Fatalf("%q fits %+v, so there is no message to render", tc.body, tc.host)
			}
			if got := oversizedProjectSizeError(cfg, tc.host, excess).Error(); got != tc.want {
				t.Errorf("message:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}
