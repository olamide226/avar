//go:build unix

// See shell_test.go: LimaProvider is a macOS backend, so its behaviour tests
// run where it runs.

package lima

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/editors"
)

// realGuestProcesses is the editors probe's output from a real Lima guest with
// a VS Code window and a Zed connection attached (see the editors package's
// testdata for how it was captured).
func realGuestProcesses(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "editors", "testdata", "lima-ubuntu-24.04-vscode-1.135.0-zed-1.20.2-attached.txt"))
	if err != nil {
		t.Fatalf("reading the captured guest process list: %v", err)
	}
	return data
}

func stoppedInstance(name string) []byte {
	return []byte(fmt.Sprintf(
		`{"name":%q,"status":"Stopped","dir":"/Users/dev/.lima/%s","vmType":"vz","arch":"aarch64","cpus":4,"memory":8589934592,"disk":107374182400,"config":{"mounts":[]}}`+"\n",
		name, name))
}

// REQ-5.10: the shared probe is run inside the guest, as the guest user, and
// what it finds is reported in the provider's vocabulary.
func TestConnectedEditors_RunsTheSharedProbeInTheGuest_REQ_5_10(t *testing.T) {
	runner := newFakeRunner().listing(runningInstance(testMachine, t.TempDir()))
	runner.shellOutput = realGuestProcesses(t)
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord(testMachine)))

	got, err := p.ConnectedEditors(context.Background(), testMachine)
	if err != nil {
		t.Fatalf("ConnectedEditors: %v", err)
	}
	want := []provider.EditorConnection{{Editor: "Zed", PID: 5905}, {Editor: "VS Code", PID: 5944}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ConnectedEditors = %+v, want %+v", got, want)
	}

	var shells []invocation
	for _, c := range runner.calls() {
		if len(c.Args) > 0 && c.Args[0] == "shell" {
			shells = append(shells, c)
		}
	}
	if len(shells) != 1 {
		t.Fatalf("want one guest command, got %v", runner.argvs())
	}
	args := shells[0].Args
	wantPrefix := []string{"shell", "--workdir", guestProbeWorkdir, testMachine, "--", "/bin/sh", "-c"}
	if len(args) != len(wantPrefix)+1 || !reflect.DeepEqual(args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("guest command = %q, want %q followed by the script", args, wantPrefix)
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(args[len(args)-1], "echo "), " | base64 -d | /bin/sh")
	script, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || string(script) != editors.Script {
		t.Errorf("the guest was not given the shared editor probe:\n%s", args[len(args)-1])
	}
	for _, arg := range args {
		if arg == "sudo" {
			t.Errorf("the probe escalated: %q", args)
		}
	}
}

// A stopped machine has no editor connected, and asking must not start it.
func TestConnectedEditors_AStoppedMachineIsNotEntered_REQ_5_10(t *testing.T) {
	runner := newFakeRunner().listing(stoppedInstance(testMachine))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord(testMachine)))

	got, err := p.ConnectedEditors(context.Background(), testMachine)
	if err != nil || len(got) != 0 {
		t.Fatalf("ConnectedEditors = %+v, %v; want nothing and no error", got, err)
	}
	for _, c := range runner.calls() {
		if len(c.Args) > 0 && (c.Args[0] == "shell" || c.Args[0] == "start") {
			t.Fatalf("a stopped machine was entered or started: %v", runner.argvs())
		}
	}
}

// PROP-11: a guest avar cannot reach is an error, never "no editor" — the
// caller would stop the machine on that answer.
func TestConnectedEditors_AnUnreachableGuestIsAnError_PROP_11(t *testing.T) {
	runner := newFakeRunner().listing(runningInstance(testMachine, t.TempDir()))
	runner.failOn("shell", errors.New("exit status 255: ssh: connect to host 127.0.0.1 port 60022: Connection refused"))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord(testMachine)))

	got, err := p.ConnectedEditors(context.Background(), testMachine)
	if err == nil {
		t.Fatalf("ConnectedEditors = %+v with no error, from a guest it could not reach", got)
	}
	if !strings.Contains(err.Error(), testMachine) || !strings.Contains(err.Error(), "Connection refused") {
		t.Errorf("error %q does not say what was attempted and why it failed", err)
	}
}

// PROP-6: a machine that is not avar's is refused before anything runs.
func TestConnectedEditors_RefusesAMachineAvarDoesNotOwn_PROP_6(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-foreign-only.json"))
	p := newTestProvider(t, runner, newFakeRecords())

	if _, err := p.ConnectedEditors(context.Background(), "default"); !errors.Is(err, provider.ErrNotOwned) {
		t.Fatalf("error = %v, want ErrNotOwned", err)
	}
	if calls := runner.calls(); len(calls) != 0 {
		t.Errorf("ran something before refusing: %v", runner.argvs())
	}
}
