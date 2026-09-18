//go:build windows

// See portdiag_test.go: this backend's behaviour tests run on Windows.

package wsl2

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/editors"
)

// constructedWSLGuest is the editors package's constructed WSL fixture: VS Code
// installed by its WSL integration, and Zed as wsl.exe starts it. It is
// constructed, not captured — see that package's tests for what it rests on.
func constructedWSLGuest(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "editors", "testdata", "constructed-wsl-and-cursor.txt"))
	if err != nil {
		t.Fatalf("reading the guest process list: %v", err)
	}
	return string(data)
}

// REQ-5.10: the shared probe is run inside the distribution, and what it finds
// is reported in the provider's vocabulary.
func TestConnectedEditors_RunsTheSharedProbeInTheDistribution_REQ_5_10(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	f.processes = constructedWSLGuest(t)
	p := newProvider(t, f, recorded(testMachine))

	got, err := p.ConnectedEditors(context.Background(), testMachine)
	if err != nil {
		t.Fatalf("ConnectedEditors: %v", err)
	}
	want := []provider.EditorConnection{
		{Editor: "VS Code", PID: 120},
		{Editor: "Cursor", PID: 164},
		{Editor: "Zed", PID: 171},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ConnectedEditors = %+v, want %+v", got, want)
	}
	if script := f.lastGuestScript(t, "@processes"); script != editors.Script {
		t.Errorf("the distribution ran a different script:\n%s", script)
	}
}

// A stopped distribution has no editor connected, and it is not entered:
// wsl.exe would start it in order to run the probe.
func TestConnectedEditors_AStoppedDistributionIsNotStarted_REQ_5_10(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	p := newProvider(t, f, recorded(testMachine))

	got, err := p.ConnectedEditors(context.Background(), testMachine)
	if err != nil || len(got) != 0 {
		t.Fatalf("ConnectedEditors = %+v, %v; want nothing and no error", got, err)
	}
	for _, call := range f.calls {
		if has(call, "--exec") {
			t.Fatalf("entered a stopped distribution, which starts it: %q", call)
		}
	}
	if f.running[testMachine] {
		t.Error("asking about a stopped distribution started it")
	}
}

// PROP-11: a distribution avar cannot reach is an error, never "no editor".
func TestConnectedEditors_AnUnreachableDistributionIsAnError_PROP_11(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	f.failOn["--exec"] = errors.New("exit status 0xffffffff: the remote procedure call failed")
	p := newProvider(t, f, recorded(testMachine))

	got, err := p.ConnectedEditors(context.Background(), testMachine)
	if err == nil {
		t.Fatalf("ConnectedEditors = %+v with no error, from a distribution it could not reach", got)
	}
	if !strings.Contains(err.Error(), testMachine) || !strings.Contains(err.Error(), "remote procedure call") {
		t.Errorf("error %q does not say what was attempted and why it failed", err)
	}
}
