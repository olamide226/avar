package lima

import (
	"context"
	"reflect"
	"testing"

	"github.com/olamide226/avar/internal/deps"
)

func TestParseOrphanHostAgentPIDs_MatchesOnlyTheExactLimaMachine(t *testing.T) {
	const limactl = "/opt/homebrew/bin/limactl"
	output := `  101 /opt/homebrew/bin/limactl hostagent --pidfile /Users/me/.lima/avr-ubuntu/ha.pid avr-ubuntu
  102 /opt/homebrew/bin/limactl hostagent --pidfile /Users/me/.lima/avr-debian/ha.pid avr-debian
  103 /usr/local/bin/limactl hostagent --pidfile /Users/me/.lima/avr-ubuntu/ha.pid avr-ubuntu
  104 /opt/homebrew/bin/limactl shell avr-ubuntu
bad line`

	if got, want := parseOrphanHostAgentPIDs(output, limactl, "avr-ubuntu"), []int{101}; !reflect.DeepEqual(got, want) {
		t.Errorf("orphanHostAgentPIDs() = %v, want %v", got, want)
	}
}

func TestStop_ReapsAnAgentWhenLimaAlreadyReportsStopped_REQ_5_2(t *testing.T) {
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	var gotMachine string
	p, err := New(Options{
		Lima:    deps.Lima{Path: "/opt/homebrew/bin/limactl"},
		Runner:  runner,
		Records: newFakeRecords(ownedRecord("avr-fedora-42-amd64")),
		LogsDir: t.TempDir(),
		Host:    testHost,
	})
	if err != nil {
		t.Fatal(err)
	}
	p.reapHostAgents = func(_ context.Context, limactl, machine string) error {
		if limactl != "/opt/homebrew/bin/limactl" {
			t.Errorf("limactl = %q", limactl)
		}
		gotMachine = machine
		return nil
	}

	if err := p.Stop(context.Background(), "avr-fedora-42-amd64", &recordingSink{}); err != nil {
		t.Fatalf("Stop() = %v", err)
	}
	if gotMachine != "avr-fedora-42-amd64" {
		t.Errorf("reaped machine = %q", gotMachine)
	}
	if got := runner.limactlArgvs(); !reflect.DeepEqual(got, []string{"limactl list --json"}) {
		t.Errorf("stopped machine was sent a limactl stop: %v", got)
	}
}
