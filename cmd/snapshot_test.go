package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/provider/fake"
	"github.com/olamide226/avar/internal/types"
)

// snapshotInvocation is `avr snapshot [name]` as the grammar hands it over.
func snapshotInvocation(args ...string) cli.Invocation {
	return cli.Invocation{Mode: cli.ModeSubcommand, Subcommand: "snapshot", SubcommandArgs: args}
}

// REQ-18.1, REQ-18.4: when a backend refuses a snapshot as unsupported, the
// reason the user reads is the backend's. The command layer used to replace
// every such refusal with the macOS one ("Apple's virtualization framework …
// use --arch amd64"), which on Windows told a user with a WSL 1 environment
// something false and hid the one command that would fix it.
func TestSnapshot_UnsupportedCarriesTheBackendsReason_REQ_18_4(t *testing.T) {
	f := fake.New()
	app := newTestApp(t, f)
	target, label := resolvedTarget(t, app)
	seedMachine(t, f, target, ubuntu(), types.KindShared, hostPath("/Users/ola/code/app"))

	const remedy = "convert it with the command above, then run avr again"
	f.FailOn(fake.OpListSnapshots, fmt.Errorf("%w: %s", provider.ErrUnsupportedCapability, remedy))

	err := runSnapshot(context.Background(), app.App, snapshotInvocation())
	if err == nil {
		t.Fatal("`avr snapshot` succeeded on a backend that refused it")
	}
	msg := err.Error()
	if strings.Contains(msg, "Apple") || strings.Contains(msg, "--arch amd64") {
		t.Errorf("the refusal gives the macOS reason on a backend that did not say it:\n%s", msg)
	}
	if !strings.Contains(msg, remedy) {
		t.Errorf("the refusal dropped the backend's own reason:\n%s", msg)
	}
	if !strings.Contains(msg, label) {
		t.Errorf("the refusal does not name the environment %q:\n%s", label, msg)
	}
	if !errors.Is(err, provider.ErrUnsupportedCapability) {
		t.Errorf("the refusal no longer identifies as ErrUnsupportedCapability: %v", err)
	}
}
