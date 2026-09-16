package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/projconfig"
	"github.com/olamide226/avar/internal/types"
)

// Flow tests for `avr init` (REQ-15.2), run from a real project directory.

func initInvocation(args ...string) cli.Invocation {
	return cli.Invocation{Mode: cli.ModeSubcommand, Subcommand: "init", SubcommandArgs: args}
}

// newInitTest is a project directory holding the given manifests and no
// .avr.toml.
func newInitTest(t *testing.T, manifests map[string]string) *projectTest {
	t.Helper()
	pt := newProjectTest(t, "")
	for name, body := range manifests {
		if err := os.WriteFile(filepath.Join(pt.dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return pt
}

func (pt *projectTest) runInit(t *testing.T, inv cli.Invocation, terminal bool, reply string) error {
	t.Helper()
	pt.out.Reset()
	pt.err.Reset()
	pt.Stdin = strings.NewReader(reply)
	pt.terminal = func() bool { return terminal }
	return runInit(context.Background(), pt.App, inv)
}

func (pt *projectTest) configPath() string { return filepath.Join(pt.dir, projconfig.FileName) }

func (pt *projectTest) assertNoFile(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(pt.configPath()); !os.IsNotExist(err) {
		t.Errorf("%s exists (stat: %v); nothing should have been written", pt.configPath(), err)
	}
}

var nodeAndGo = map[string]string{
	"package.json": `{"engines": {"node": ">=20"}}`,
	"go.mod":       "module example.com/app\n\ngo 1.22\n",
}

// REQ-15.2: the proposal shows the detected stack and the exact file, and the
// file is written only after a yes. `avr init` starts no environment.
func TestInit_ProposesThenWritesAfterConfirmation_REQ_15_2(t *testing.T) {
	pt := newInitTest(t, nodeAndGo)

	if err := pt.runInit(t, initInvocation(), true, "y\n"); err != nil {
		t.Fatalf("avr init: %v", err)
	}

	out := pt.stdout()
	for _, want := range []string{"package.json", "Node.js: engines.node >=20", "go.mod", "Go: go 1.22", `distro = "ubuntu"`, `packages = ["nodejs", "npm", "golang-go"]`, "not match", "Wrote"} {
		if !strings.Contains(out, want) {
			t.Errorf("the proposal does not show %q:\n%s", want, out)
		}
	}
	if !strings.Contains(pt.err.String(), "Write "+pt.configPath()) {
		t.Errorf("the question did not name the file:\n%s", pt.err.String())
	}

	body, err := os.ReadFile(pt.configPath())
	if err != nil {
		t.Fatalf("the confirmed file was not written: %v", err)
	}
	if !strings.Contains(out, strings.Split(string(body), "\n")[len(strings.Split(string(body), "\n"))-2]) {
		t.Errorf("the written file is not the one shown:\nshown:\n%s\nwritten:\n%s", out, body)
	}
	got, err := projconfig.Parse(pt.configPath(), body)
	if err != nil {
		t.Fatalf("avr init wrote a file avr cannot read: %v\n%s", err, body)
	}
	want := projconfig.Config{Path: pt.configPath(), Distro: types.DistroUbuntu, Packages: []string{"nodejs", "npm", "golang-go"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wrote %+v, want %+v", got, want)
	}

	if calls := pt.f.Calls(); len(calls) != 0 {
		t.Errorf("avr init touched an environment: %v", calls)
	}
}

// PROP-24, second clause: anything but an explicit yes writes nothing.
func TestInit_DecliningWritesNothing_PROP_24(t *testing.T) {
	for _, reply := range []string{"n\n", "\n", "", "sure\n"} {
		t.Run(strings.TrimSpace(reply), func(t *testing.T) {
			pt := newInitTest(t, nodeAndGo)
			if err := pt.runInit(t, initInvocation(), true, reply); err != nil {
				t.Fatalf("avr init: %v", err)
			}
			pt.assertNoFile(t)
			if !strings.Contains(pt.stdout(), "Nothing was written") {
				t.Errorf("avr init did not say nothing was written:\n%s", pt.stdout())
			}
		})
	}
}

// PROP-24, second clause: without a terminal the proposal is shown, nothing is
// written even if stdin says yes, and the command fails saying what to run.
func TestInit_NoTerminalWritesNothing_PROP_24(t *testing.T) {
	pt := newInitTest(t, nodeAndGo)

	err := pt.runInit(t, initInvocation(), false, "y\n")
	if err == nil || !strings.Contains(err.Error(), "from a terminal") {
		t.Fatalf("avr init = %v, want a failure saying to run it from a terminal", err)
	}
	pt.assertNoFile(t)
	if !strings.Contains(pt.stdout(), `packages = ["nodejs", "npm", "golang-go"]`) {
		t.Errorf("the proposal was not shown:\n%s", pt.stdout())
	}
}

// An existing file is never replaced, even one avr cannot read, and even when
// the user would have said yes.
func TestInit_NeverReplacesAnExistingFile_REQ_15_2(t *testing.T) {
	for name, existing := range map[string]string{
		"readable":   "distro = \"fedora\"\n",
		"unreadable": "this is not toml\n",
	} {
		t.Run(name, func(t *testing.T) {
			pt := newInitTest(t, nodeAndGo)
			pt.writeFile(t, existing)

			err := pt.runInit(t, initInvocation(), true, "y\n")
			if err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("avr init = %v, want a refusal saying the file exists", err)
			}
			if body, _ := os.ReadFile(pt.configPath()); string(body) != existing {
				t.Errorf("the existing file changed:\n%s", body)
			}
		})
	}
}

// REQ-15.4: a project with nothing to detect gets a sentence, not a file, and
// is reminded that avar needs no configuration.
func TestInit_NothingDetectedWritesNothing_REQ_15_4(t *testing.T) {
	pt := newInitTest(t, map[string]string{"README.md": "# app"})

	if err := pt.runInit(t, initInvocation(), true, "y\n"); err != nil {
		t.Fatalf("avr init: %v", err)
	}
	pt.assertNoFile(t)
	if !strings.Contains(pt.stdout(), "needs no configuration") {
		t.Errorf("avr init did not say avar needs no configuration:\n%s", pt.stdout())
	}
	if pt.err.Len() != 0 {
		t.Errorf("avr init asked a question with nothing to propose:\n%s", pt.err.String())
	}
}

// Selector flags choose what the proposal is for, and package names follow.
func TestInit_SelectorFlagsChooseTheProposal_REQ_15_2(t *testing.T) {
	pt := newInitTest(t, map[string]string{"package.json": "{}"})

	inv := initInvocation()
	inv.Selector.Distro, inv.Selector.Arch = types.DistroFedora, types.ArchAMD64
	if err := pt.runInit(t, inv, true, "y\n"); err != nil {
		t.Fatalf("avr --distro fedora --arch amd64 init: %v", err)
	}
	cfg, err := projconfig.Load(pt.dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Distro != types.DistroFedora || cfg.Arch != types.ArchAMD64 || !reflect.DeepEqual(cfg.Packages, []string{"nodejs", "nodejs-npm"}) {
		t.Errorf("wrote %+v, want Fedora's Node packages for amd64", cfg)
	}
}

func TestInit_RejectsArguments(t *testing.T) {
	pt := newInitTest(t, nodeAndGo)
	err := pt.runInit(t, initInvocation("--yes"), true, "y\n")
	if err == nil || !strings.Contains(err.Error(), "takes no arguments") {
		t.Fatalf("avr init --yes = %v, want a usage error", err)
	}
	pt.assertNoFile(t)
}

// REQ-15.3 across the two commands: a file avr init wrote is not an approval.
// The next `avr` without a terminal installs nothing and says what is waiting.
func TestInit_WritingTheFileApprovesNothing_REQ_15_3(t *testing.T) {
	pt := newInitTest(t, nodeAndGo)
	if err := pt.runInit(t, initInvocation(), true, "y\n"); err != nil {
		t.Fatal(err)
	}

	if err := pt.run(t, guestInvocation("true"), false, "y\n"); err != nil {
		t.Fatalf("avr true: %v", err)
	}
	if installs := installShells(pt.f); len(installs) != 0 {
		t.Errorf("packages from the file avr init wrote were installed without approval: %q", installs)
	}
	if !strings.Contains(pt.err.String(), "install nodejs, npm, golang-go") {
		t.Errorf("avr did not name the packages waiting for approval:\n%s", pt.err.String())
	}
}

// `init` is reserved, and `--` reaches a guest program of that name (REQ-2.6).
func TestInit_IsReservedWithAnEscapeHatch_REQ_2_6(t *testing.T) {
	inv, err := cli.Parse([]string{"--distro", "fedora", "init"})
	if err != nil || inv.Mode != cli.ModeSubcommand || inv.Subcommand != "init" {
		t.Errorf("avr --distro fedora init = %+v, %v; want the init subcommand", inv, err)
	}
	inv, err = cli.Parse([]string{"--", "init"})
	if err != nil || inv.Mode != cli.ModeGuestCommand || !reflect.DeepEqual(inv.Guest, []string{"init"}) {
		t.Errorf("avr -- init = %+v, %v; want the guest's init", inv, err)
	}

	var help bytes.Buffer
	if err := writeCommandHelp(&help, "init"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(help.String(), "avr -- init") {
		t.Errorf("init's help does not name the escape hatch:\n%s", help.String())
	}
}
