package cmd

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/projconfig"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/types"
)

// Flow tests for the parts of .avr.toml that are grants or sizes: packages,
// forward_env, cpus and memory (design §3.11, PROP-23).

// secretName is a host variable a project's file asks to forward. Its value is
// what must never reach the guest without the user's approval.
const secretName = "AVR_TEST_PROJECT_SECRET"

const secretValue = "s3cr3t-value"

// run runs `avr <argv>` once more in the same project, as a fresh invocation
// would: a fresh reply on stdin, and the recorded calls cleared.
func (pt *projectTest) run(t *testing.T, inv cli.Invocation, terminal bool, reply string) error {
	t.Helper()
	pt.f.Reset()
	pt.out.Reset()
	pt.err.Reset()
	pt.Stdin = strings.NewReader(reply)
	pt.terminal = func() bool { return terminal }
	return runGuest(context.Background(), pt.App, inv)
}

// guestShell is the Shell call that ran the user's own command, as opposed to
// one avar ran for itself: avar's own work always redirects its output.
func guestShell(t *testing.T, f *fake.Fake) fake.Call {
	t.Helper()
	var found []fake.Call
	for _, c := range f.CallsFor(fake.OpShell) {
		if c.Shell.Stdout == nil {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one guest command, got %d:\n%s", len(found), f.Transcript())
	}
	return found[0]
}

// installShells are the Shell calls avar made to install packages: its own
// guest work, which redirects its output, and runs through sudo as no other
// avar probe does.
func installShells(f *fake.Fake) [][]string {
	var out [][]string
	for _, c := range f.CallsFor(fake.OpShell) {
		if c.Shell.Stdout != nil && len(c.Shell.Argv) > 0 && c.Shell.Argv[0] == "sudo" {
			out = append(out, c.Shell.Argv)
		}
	}
	return out
}

func (pt *projectTest) project(t *testing.T) types.ProjectRecord {
	t.Helper()
	target, err := pt.Resolve(guestInvocation("true"))
	if err != nil {
		t.Fatal(err)
	}
	return target.Project
}

// PROP-23: without a terminal, a variable the file names does not cross, and
// nothing is approved — not even when stdin happens to say yes.
func TestShell_UnapprovedForwardEnvNeverCrosses_PROP_23(t *testing.T) {
	t.Setenv(secretName, secretValue)
	pt := newProjectTest(t, "forward_env = [\""+secretName+"\"]\n")

	if err := pt.run(t, guestInvocation("env"), false, "y\n"); err != nil {
		t.Fatalf("avr env: %v", err)
	}
	if _, leaked := guestShell(t, pt.f).Shell.Env[secretName]; leaked {
		t.Fatalf("%s crossed into the guest without approval", secretName)
	}
	if !strings.Contains(pt.err.String(), secretName) || !strings.Contains(pt.err.String(), "from a terminal") {
		t.Errorf("the pending grant was not named with how to review it:\n%s", pt.err.String())
	}
	if strings.Contains(pt.err.String(), secretValue) {
		t.Errorf("avr printed the variable's value:\n%s", pt.err.String())
	}
	if got := pt.project(t).ApprovedForwardEnv; got != nil {
		t.Errorf("approval was recorded without a terminal: %v", got)
	}
}

// REQ-15.1 with REQ-15.3: an approved variable crosses, and once approved is
// not asked about again, with or without a terminal.
func TestShell_ApprovedForwardEnvCrosses_REQ_15_1_REQ_15_3(t *testing.T) {
	t.Setenv(secretName, secretValue)
	pt := newProjectTest(t, "forward_env = [\""+secretName+"\"]\n")

	if err := pt.run(t, guestInvocation("env"), true, "y\n"); err != nil {
		t.Fatalf("avr env: %v", err)
	}
	if !strings.Contains(pt.err.String(), "Approve?") || !strings.Contains(pt.err.String(), secretName) {
		t.Errorf("the prompt did not name the variable:\n%s", pt.err.String())
	}
	if got := guestShell(t, pt.f).Shell.Env[secretName]; got != secretValue {
		t.Errorf("approved %s = %q in the guest, want the host's value", secretName, got)
	}

	if err := pt.run(t, guestInvocation("env"), false, ""); err != nil {
		t.Fatalf("avr env (again): %v", err)
	}
	if got := guestShell(t, pt.f).Shell.Env[secretName]; got != secretValue {
		t.Errorf("an approval did not last: %s = %q", secretName, got)
	}
	if pt.err.Len() != 0 {
		t.Errorf("avr said something about an already-approved grant:\n%s", pt.err.String())
	}
}

// Declining applies nothing, records nothing, and is asked again next time.
func TestShell_DecliningApprovesNothing_REQ_15_3(t *testing.T) {
	t.Setenv(secretName, secretValue)
	pt := newProjectTest(t, "distro = \"ubuntu\"\npackages = [\"jq\"]\nforward_env = [\""+secretName+"\"]\n")

	for i, reply := range []string{"n\n", "\n"} {
		if err := pt.run(t, guestInvocation("true"), true, reply); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if !strings.Contains(pt.err.String(), "Approve?") {
			t.Errorf("run %d did not ask:\n%s", i, pt.err.String())
		}
		if _, leaked := guestShell(t, pt.f).Shell.Env[secretName]; leaked {
			t.Errorf("run %d: a declined variable crossed", i)
		}
		if installs := installShells(pt.f); len(installs) != 0 {
			t.Errorf("run %d: a declined package was installed: %q", i, installs)
		}
	}
	rec := pt.project(t)
	if rec.ApprovedForwardEnv != nil || rec.ApprovedPackages != nil {
		t.Errorf("a refusal was recorded as approval: %+v", rec)
	}
}

// REQ-15.1: approved packages are installed with the distribution's package
// manager, before the user's command runs, and only once.
func TestShell_ApprovedPackagesAreInstalledOnce_REQ_15_1(t *testing.T) {
	pt := newProjectTest(t, "distro = \"ubuntu\"\npackages = [\"ripgrep\", \"jq\"]\n")

	if err := pt.run(t, guestInvocation("rg", "--version"), true, "yes\n"); err != nil {
		t.Fatalf("avr rg --version: %v", err)
	}
	if !strings.Contains(pt.err.String(), "ripgrep, jq") || !strings.Contains(pt.err.String(), "shared") {
		t.Errorf("the prompt did not name the packages and that the environment is shared:\n%s", pt.err.String())
	}
	apt := []string{"sudo", "-n", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get"}
	want := [][]string{append(slices.Clone(apt), "update"), append(slices.Clone(apt), "install", "-y", "ripgrep", "jq")}
	if got := installShells(pt.f); !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("installed with %q, want %q", got, want)
	}
	shells := pt.f.CallsFor(fake.OpShell)
	if last := shells[len(shells)-1]; !slices.Equal(last.Shell.Argv, []string{"rg", "--version"}) {
		t.Errorf("the user's command did not run after the install:\n%s", pt.f.Transcript())
	}
	if pt.stdout() != "" {
		t.Errorf("avr's install wrote to stdout, which belongs to the user's command:\n%s", pt.stdout())
	}

	// The warm path: approved and installed, so nothing but the command.
	if err := pt.run(t, guestInvocation("rg", "--version"), false, ""); err != nil {
		t.Fatalf("avr rg --version (again): %v", err)
	}
	pt.f.AssertOps(t, fake.OpEnsureMachine, fake.OpAppliedMounts, fake.OpShell)
	if pt.err.Len() != 0 {
		t.Errorf("the warm path said something:\n%s", pt.err.String())
	}
}

// PROP-23: without approval, not one install command reaches the guest.
func TestShell_UnapprovedPackagesAreNeverInstalled_PROP_23(t *testing.T) {
	pt := newProjectTest(t, "distro = \"ubuntu\"\npackages = [\"jq\"]\n")

	if err := pt.run(t, guestInvocation("true"), false, "y\n"); err != nil {
		t.Fatalf("avr true: %v", err)
	}
	if installs := installShells(pt.f); len(installs) != 0 {
		t.Errorf("installed without approval: %q", installs)
	}
	if !strings.Contains(pt.err.String(), "install jq") {
		t.Errorf("the pending package was not named:\n%s", pt.err.String())
	}
}

// Approval is per environment: packages approved for the shared environment
// are asked about again before they go into the project's own.
func TestShell_PackageApprovalIsPerEnvironment_PROP_23(t *testing.T) {
	pt := newProjectTest(t, "distro = \"ubuntu\"\npackages = [\"jq\"]\n")
	if err := pt.run(t, guestInvocation("true"), true, "y\n"); err != nil {
		t.Fatal(err)
	}

	isolated := guestInvocation("true")
	isolated.Selector.Isolate = true
	if err := pt.run(t, isolated, false, ""); err != nil {
		t.Fatalf("avr --isolate true: %v", err)
	}
	if installs := installShells(pt.f); len(installs) != 0 {
		t.Errorf("an approval for the shared environment installed into the project's own: %q", installs)
	}
	if !strings.Contains(pt.err.String(), "install jq") {
		t.Errorf("the isolated environment's pending package was not named:\n%s", pt.err.String())
	}

	if err := pt.run(t, isolated, true, "y\n"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pt.err.String(), "this project's own") {
		t.Errorf("the prompt did not say the packages go into the project's own environment:\n%s", pt.err.String())
	}
	if installs := installShells(pt.f); len(installs) == 0 {
		t.Error("approved packages were not installed into the isolated environment")
	}
}

// Packages belong to the distribution the file names, and are neither offered
// to nor installed in another.
func TestShell_PackagesForAnotherDistroAreNotInstalled_REQ_15_1(t *testing.T) {
	pt := newProjectTest(t, "distro = \"ubuntu\"\npackages = [\"golang-go\"]\n")

	inv := guestInvocation("true")
	inv.Selector.Distro = types.DistroFedora
	if err := pt.run(t, inv, true, "y\n"); err != nil {
		t.Fatalf("avr --distro fedora true: %v", err)
	}
	if strings.Contains(pt.err.String(), "Approve?") {
		t.Errorf("Ubuntu's packages were offered for Fedora:\n%s", pt.err.String())
	}
	if installs := installShells(pt.f); len(installs) != 0 {
		t.Errorf("Ubuntu's packages were installed in Fedora: %q", installs)
	}
	if !strings.Contains(pt.err.String(), "lists packages for ubuntu") {
		t.Errorf("avr did not say why no packages were installed:\n%s", pt.err.String())
	}
}

// A failed install is reported, does not stop the session, and is retried.
func TestShell_FailedInstallDoesNotStopTheSession_REQ_15_1(t *testing.T) {
	pt := newProjectTest(t, "distro = \"fedora\"\npackages = [\"jq\"]\n")
	if err := pt.run(t, guestInvocation("true"), true, "y\n"); err != nil {
		t.Fatal(err)
	}
	store, _ := pt.Store()
	target, _ := pt.Resolve(guestInvocation("true"))
	if err := store.DeleteMachine(target.MachineName); err != nil {
		t.Fatal(err)
	}
	pt.f.RemoveMachine(target.MachineName)

	pt.f.Reset()
	pt.f.FailNextOn(fake.OpShell, errors.New("mirror unreachable"))
	pt.err.Reset()
	pt.Stdin = strings.NewReader("")
	if err := runGuest(context.Background(), pt.App, guestInvocation("true")); err != nil {
		t.Fatalf("a failed install stopped the session: %v", err)
	}
	if !strings.Contains(pt.err.String(), "mirror unreachable") || !strings.Contains(pt.err.String(), "try again") {
		t.Errorf("the failure was not reported with what happens next:\n%s", pt.err.String())
	}
	guestShell(t, pt.f)
	if rec, _, _ := store.Machine(target.MachineName); len(rec.Packages) != 0 {
		t.Errorf("a failed install was recorded as installed: %v", rec.Packages)
	}

	if err := pt.run(t, guestInvocation("true"), false, ""); err != nil {
		t.Fatal(err)
	}
	if installs := installShells(pt.f); len(installs) != 1 {
		t.Errorf("the install was not retried: %q", installs)
	}
}

// REQ-15.1: cpus and memory size a project's own environment when it is
// created.
func TestShell_ProjectSizeAppliesToAnIsolatedEnvironment_REQ_15_1(t *testing.T) {
	pt := newProjectTest(t, "cpus = 6\nmemory = \"12GiB\"\n")

	inv := guestInvocation("true")
	inv.Selector.Isolate = true
	if err := pt.run(t, inv, false, ""); err != nil {
		t.Fatalf("avr --isolate true: %v", err)
	}
	spec := pt.f.AssertCalled(t, fake.OpEnsureMachine).Spec
	if spec.CPUs != 6 || spec.MemoryGB != 12 {
		t.Errorf("created with %d CPU and %v GB, want 6 and 12", spec.CPUs, spec.MemoryGB)
	}
	if strings.Contains(pt.err.String(), projconfig.FileName) {
		t.Errorf("avr advised about a size it applied:\n%s", pt.err.String())
	}

	// Checked once; the warm path does not ask the backend again.
	if err := pt.run(t, guestInvocation("true"), false, ""); err != nil {
		t.Fatal(err)
	}
	pt.f.AssertNotCalled(t, fake.OpStatus)
}

// The shared environment is never sized by one project's file, and avar says
// so once.
func TestShell_ProjectSizeNeverAppliesToTheSharedEnvironment_REQ_15_1(t *testing.T) {
	pt := newProjectTest(t, "cpus = 6\n")

	if err := pt.run(t, guestInvocation("true"), false, ""); err != nil {
		t.Fatal(err)
	}
	if spec := pt.f.AssertCalled(t, fake.OpEnsureMachine).Spec; spec.CPUs != 0 || spec.MemoryGB != 0 {
		t.Errorf("the shared environment was sized by a project: %d CPU, %v GB", spec.CPUs, spec.MemoryGB)
	}
	if !strings.Contains(pt.err.String(), "avr --isolate") {
		t.Errorf("avr did not say how to get the declared size:\n%s", pt.err.String())
	}

	if err := pt.run(t, guestInvocation("true"), false, ""); err != nil {
		t.Fatal(err)
	}
	if pt.err.Len() != 0 {
		t.Errorf("the advice was repeated:\n%s", pt.err.String())
	}

	// A changed declaration is advised again.
	pt.writeFile(t, "cpus = 8\n")
	if err := pt.run(t, guestInvocation("true"), false, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pt.err.String(), "8 CPU") {
		t.Errorf("a changed declaration was not advised:\n%s", pt.err.String())
	}
}

// An existing isolated environment is never resized; avar says it differs and
// how to recreate it.
func TestShell_ExistingEnvironmentOfAnotherSizeIsNotResized_REQ_15_1(t *testing.T) {
	pt := newProjectTest(t, "")
	inv := guestInvocation("true")
	inv.Selector.Isolate = true
	if err := pt.run(t, inv, false, ""); err != nil {
		t.Fatal(err)
	}

	pt.writeFile(t, "cpus = 8\n")
	if err := pt.run(t, guestInvocation("true"), false, ""); err != nil {
		t.Fatal(err)
	}
	pt.f.AssertNotCalled(t, fake.OpStop)
	if !strings.Contains(pt.err.String(), "avr reset") || !strings.Contains(pt.err.String(), "4 CPU") {
		t.Errorf("avr did not say the environment differs and how to recreate it:\n%s", pt.err.String())
	}
}

// A backend that cannot size one environment is never handed a size, and the
// user is told the declaration cannot apply on this host.
func TestShell_ProjectSizeOnABackendThatCannotSize_REQ_15_1(t *testing.T) {
	pt := newProjectTest(t, "cpus = 6\n")
	unsized := struct{ provider.Provider }{pt.f}
	pt.prov = unsized

	inv := guestInvocation("true")
	inv.Selector.Isolate = true
	if err := pt.run(t, inv, false, ""); err != nil {
		t.Fatal(err)
	}
	if spec := pt.f.AssertCalled(t, fake.OpEnsureMachine).Spec; spec.CPUs != 0 {
		t.Errorf("a backend that cannot size machines was given %d CPU", spec.CPUs)
	}
	if !strings.Contains(pt.err.String(), "cannot be set") {
		t.Errorf("avr did not say the size cannot apply here:\n%s", pt.err.String())
	}
}

// Approval needs a person at a terminal, not merely a character device on
// stdin: /dev/null is one, and nobody is typing into it. A stream that reads
// "y" without a person behind it must never grant anything (PROP-23).
func TestInteractive_NullDeviceIsNotATerminal_PROP_23(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if isTerminal(null) {
		t.Errorf("%s was taken for a terminal, so a script could be treated as a person", os.DevNull)
	}
}

// The editor commands review and apply the file as the shell does.
func TestEditor_ReviewsAndAppliesProjectConfig_REQ_15_1(t *testing.T) {
	pt := newProjectTest(t, "distro = \"ubuntu\"\npackages = [\"jq\"]\n")
	e := newEditorTest(t, "code")
	e.seedWSLTarget(t)
	e.terminal = func() bool { return true }
	e.Stdin = strings.NewReader("y\n")
	_ = pt

	if err := e.run(t, editorInvocation("code")); err != nil {
		t.Fatalf("avr code: %v", err)
	}
	if installs := installShells(e.f); len(installs) != 2 {
		t.Errorf("avr code did not install the approved packages: %q", installs)
	}
	if _, ok := e.launched(t); !ok {
		t.Error("avr code did not open the editor")
	}
}
