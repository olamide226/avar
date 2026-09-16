//go:build unix

// See shell_test.go: LimaProvider is a macOS backend, so its behaviour tests
// run where it runs.

package lima

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/olamide226/avar/internal/deps"
	"github.com/olamide226/avar/internal/types"
)

const testLimactl = "/opt/homebrew/bin/limactl"

// agentCommand is a host agent's command line as Lima 2.2.0 starts it, taken
// from `ps -axww -o pid=,command=` on a real host.
func agentCommand(machine string) string {
	return fmt.Sprintf("%s hostagent --pidfile /Users/me/.lima/%[2]s/ha.pid --socket /Users/me/.lima/%[2]s/ha.sock "+
		"--guestagent /opt/homebrew/share/lima/lima-guestagent.Linux-aarch64.gz %[2]s", testLimactl, machine)
}

// processTable stands in for the host's processes behind `ps` and `kill`.
//
// Its answers are the real tools' answers: `ps` output in the column layout
// macOS prints, and for a pid that does not exist the error deps' exec runner
// returns for `/bin/kill`, whose exit status 1 and "No such process" were taken
// from the real command.
type processTable struct {
	mu       sync.Mutex
	commands map[int]string
	// survives lists the signals a process outlives: "-TERM" is a wedged
	// agent, and "-KILL" as well is one avar cannot end at all.
	survives map[int][]string
	// exitsAfterScan makes a process exit straight after the nth `ps` (counted
	// from 1) lists it, which is how a test puts an exit between a listing and
	// the signal that follows it.
	exitsAfterScan map[int]int
	scans          int
	signals        []string
}

func newProcessTable() *processTable {
	return &processTable{commands: map[int]string{}, survives: map[int][]string{}, exitsAfterScan: map[int]int{}}
}

func (t *processTable) add(pid int, command string) *processTable {
	t.commands[pid] = command
	return t
}

func (t *processTable) run(name string, args []string) ([]byte, error) {
	if t == nil {
		t = newProcessTable()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if name == psProgram {
		return t.scan(args), nil
	}
	return nil, t.signal(args)
}

func (t *processTable) scan(args []string) []byte {
	if !reflect.DeepEqual(args, psArgs) {
		return nil
	}
	t.scans++
	pids := make([]int, 0, len(t.commands))
	for pid := range t.commands {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	var out strings.Builder
	for _, pid := range pids {
		fmt.Fprintf(&out, "%5d %s\n", pid, t.commands[pid])
		if t.exitsAfterScan[pid] == t.scans {
			delete(t.commands, pid)
		}
	}
	return []byte(out.String())
}

func (t *processTable) signal(args []string) error {
	signal, pid := args[0], args[1]
	t.signals = append(t.signals, signal+" "+pid)
	n, _ := strconv.Atoi(pid)
	if _, ok := t.commands[n]; !ok {
		return fmt.Errorf("exit status 1: kill: %s: No such process", pid)
	}
	for _, survived := range t.survives[n] {
		if survived == signal {
			if signal == "-KILL" {
				return fmt.Errorf("exit status 1: kill: %s: Operation not permitted", pid)
			}
			return nil
		}
	}
	delete(t.commands, n)
	return nil
}

func (t *processTable) sent() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.signals...)
}

func TestParseOrphanHostAgentPIDs_MatchesOnlyTheExactLimaMachine(t *testing.T) {
	output := `  101 /opt/homebrew/bin/limactl hostagent --pidfile /Users/me/.lima/avr-ubuntu/ha.pid avr-ubuntu
  102 /opt/homebrew/bin/limactl hostagent --pidfile /Users/me/.lima/avr-debian/ha.pid avr-debian
  103 /usr/local/bin/limactl hostagent --pidfile /Users/me/.lima/avr-ubuntu/ha.pid avr-ubuntu
  104 /opt/homebrew/bin/limactl shell avr-ubuntu
bad line`

	if got, want := parseOrphanHostAgentPIDs(output, testLimactl, "avr-ubuntu"), []int{101}; !reflect.DeepEqual(got, want) {
		t.Errorf("orphanHostAgentPIDs() = %v, want %v", got, want)
	}
}

// The signal's exit status is not the verdict on whether an agent is gone: a
// re-listing is. Each case is a way an agent can leave, or fail to, while avar
// is ending it.
func TestEndHostAgents_ALaterListingDecidesWhetherAnAgentIsGone_REQ_5_2(t *testing.T) {
	const machine = "avr-ubuntu-24.04-arm64"
	other := "avr-debian-13-arm64"

	cases := []struct {
		name        string
		table       func() *processTable
		wantSignals []string
		wantEnded   []int
		wantErr     string
	}{
		{
			name:  "nothing to end sends no signal",
			table: func() *processTable { return newProcessTable().add(900, agentCommand(other)) },
		},
		{
			name:        "an agent that exits on TERM needs nothing more",
			table:       func() *processTable { return newProcessTable().add(101, agentCommand(machine)) },
			wantSignals: []string{"-TERM 101"},
			wantEnded:   []int{101},
		},
		{
			name: "a wedged agent is killed",
			table: func() *processTable {
				pt := newProcessTable().add(101, agentCommand(machine))
				pt.survives[101] = []string{"-TERM"}
				return pt
			},
			wantSignals: []string{"-TERM 101", "-KILL 101"},
			wantEnded:   []int{101},
		},
		{
			name: "an agent that exits between the listing and TERM does not stop the others",
			table: func() *processTable {
				pt := newProcessTable().add(101, agentCommand(machine)).add(102, agentCommand(machine))
				pt.exitsAfterScan[101] = 1
				return pt
			},
			wantSignals: []string{"-TERM 101", "-TERM 102"},
			wantEnded:   []int{101, 102},
		},
		{
			// The race the escalation walks into: SIGKILL goes to the agents that
			// received SIGTERM a moment ago, which are the likeliest to have
			// just exited.
			name: "an agent that exits between the re-listing and KILL is not a failure",
			table: func() *processTable {
				pt := newProcessTable().add(101, agentCommand(machine))
				pt.survives[101] = []string{"-TERM"}
				pt.exitsAfterScan[101] = 2
				return pt
			},
			wantSignals: []string{"-TERM 101", "-KILL 101"},
			wantEnded:   []int{101},
		},
		{
			name: "an agent that outlives KILL is reported with the reason",
			table: func() *processTable {
				pt := newProcessTable().add(101, agentCommand(machine)).add(102, agentCommand(machine))
				pt.survives[101] = []string{"-TERM", "-KILL"}
				return pt
			},
			wantSignals: []string{"-TERM 101", "-TERM 102", "-KILL 101"},
			wantEnded:   []int{102},
			wantErr:     "process 101 still running after SIGKILL (last signal error: sending -KILL to process 101: exit status 1: kill: 101: Operation not permitted)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := newFakeRunner()
			runner.processes = tc.table()
			p := newTestProvider(t, runner, nil)

			ended, err := p.endHostAgents(context.Background(), machine)

			if got := runner.processes.sent(); !reflect.DeepEqual(got, tc.wantSignals) {
				t.Errorf("signals = %q, want %q", got, tc.wantSignals)
			}
			if !reflect.DeepEqual(ended, tc.wantEnded) {
				t.Errorf("ended = %v, want %v", ended, tc.wantEnded)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("endHostAgents() = %v, want success", err)
			case tc.wantErr != "" && (err == nil || err.Error() != tc.wantErr):
				t.Errorf("endHostAgents() = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// The failure PR #50's review found: a stop that succeeded, reported as failed
// because an agent exited on its own just before avar's SIGKILL reached it.
func TestStop_AnAgentThatExitsBeforeItsSignalIsNotAFailedStop_REQ_5_2(t *testing.T) {
	const machine = "avr-ubuntu-24.04-arm64"
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	runner.processes = newProcessTable().add(4242, agentCommand(machine))
	runner.processes.survives[4242] = []string{"-TERM"}
	runner.processes.exitsAfterScan[4242] = 2
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord(machine)))

	if err := p.Stop(context.Background(), machine, &recordingSink{}); err != nil {
		t.Fatalf("Stop() = %v, want success: the agent is gone", err)
	}
}

func TestStop_ReapsAnAgentWhenLimaAlreadyReportsStopped_REQ_5_2(t *testing.T) {
	const machine = "avr-fedora-42-amd64"
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	runner.processes = newProcessTable().
		add(4242, agentCommand(machine)).
		add(4343, agentCommand("avr-ubuntu-24.04-arm64"))
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord(machine)))
	sink := &recordingSink{}

	if err := p.Stop(context.Background(), machine, sink); err != nil {
		t.Fatalf("Stop() = %v", err)
	}
	if got := runner.processes.sent(); !reflect.DeepEqual(got, []string{"-TERM 4242"}) {
		t.Errorf("signals = %q, want only the stopped machine's agent ended", got)
	}
	if got := runner.limactlArgvs(); !reflect.DeepEqual(got, []string{"limactl list --json"}) {
		t.Errorf("stopped machine was sent a limactl stop: %v", got)
	}
}

// Ending a process the user may have noticed eating CPU is something avar did
// on their behalf, so it says so — naming the environment the way the user
// chose it, not by machine name (REQ-1.5).
func TestStop_ReportsTheHostAgentsItEnded_REQ_5_2(t *testing.T) {
	const machine = "avr-fedora-42-amd64"
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	runner.processes = newProcessTable().add(4242, agentCommand(machine)).add(4243, agentCommand(machine))
	record := ownedRecord(machine)
	record.Selector = types.EnvironmentSelector{Distro: types.DistroFedora, Version: "42", Arch: types.ArchAMD64}
	p := newTestProvider(t, runner, newFakeRecords(record))
	sink := &recordingSink{}

	if err := p.Stop(context.Background(), machine, sink); err != nil {
		t.Fatalf("Stop() = %v", err)
	}

	warnings := sink.of(types.ProgressWarning)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one report of the ended agents", warnings)
	}
	want := "Fedora 42 · amd64 had 2 Lima host agents (processes 4242, 4243) still running after it stopped; avar ended them."
	if warnings[0].Message != want {
		t.Errorf("report = %q, want %q", warnings[0].Message, want)
	}
}

// A machine that shuts down cleanly, with no agent left behind, says nothing
// beyond stopping.
func TestStop_SaysNothingExtraWhenNoAgentWasLeft_REQ_5_2(t *testing.T) {
	const machine = "avr-ubuntu-24.04-arm64"
	runner := newFakeRunner().listing(fixture(t, "list-mixed.json"))
	runner.processes = newProcessTable()
	p := newTestProvider(t, runner, newFakeRecords(ownedRecord(machine)))
	sink := &recordingSink{}

	if err := p.Stop(context.Background(), machine, sink); err != nil {
		t.Fatalf("Stop() = %v", err)
	}
	if got := sink.kinds(); !reflect.DeepEqual(got, []types.ProgressKind{types.ProgressStopping}) {
		t.Errorf("progress = %v, want only the stop itself", got)
	}
}

// fakeAgentEnv turns this test binary into a stand-in host agent; see TestMain.
const fakeAgentEnv = "AVR_TEST_FAKE_HOST_AGENT"

// TestMain lets the test binary play a wedged host agent for
// TestEndHostAgents_EndsARealProcessWithTheRealTools. The switch is checked
// before flags are parsed, because the command line has to be exactly an
// agent's for the detector to match it, with no test flags in it.
func TestMain(m *testing.M) {
	if ready := os.Getenv(fakeAgentEnv); ready != "" {
		signal.Ignore(syscall.SIGTERM)
		// The file says SIGTERM is now ignored, so the test does not race the
		// reaper against this process's own start-up.
		if err := os.WriteFile(ready, nil, 0o600); err != nil {
			os.Exit(1)
		}
		time.Sleep(time.Minute)
		return
	}
	os.Exit(m.Run())
}

// The fake process table above can only confirm avar understands its author's
// idea of `ps` and `kill`. This puts the same code in front of the real tools
// with a real process that ignores SIGTERM, so the whole escalation runs.
func TestEndHostAgents_EndsARealProcessWithTheRealTools(t *testing.T) {
	for _, tool := range []string{psProgram, killProgram} {
		if _, err := os.Stat(tool); err != nil {
			t.Skipf("%s is not available here: %v", tool, err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	// A limactl path and machine name no real agent can have.
	limactl := filepath.Join(t.TempDir(), "limactl")
	machine := fmt.Sprintf("avr-reap-test-%d", os.Getpid())
	ready := filepath.Join(t.TempDir(), "ready")
	agent := &exec.Cmd{
		Path: self,
		Args: []string{limactl, "hostagent", "--pidfile", filepath.Join(t.TempDir(), "ha.pid"), machine},
		Env:  append(os.Environ(), fakeAgentEnv+"="+ready),
	}
	if err := agent.Start(); err != nil {
		t.Fatalf("starting a stand-in host agent: %v", err)
	}
	// Reap it the way launchd reaps a real orphan, so it leaves the process
	// table once it is killed.
	exited := make(chan struct{})
	go func() { _ = agent.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = agent.Process.Kill()
		<-exited
	})

	p, err := New(Options{Lima: deps.Lima{Path: limactl}, Runner: deps.NewRunner(), LogsDir: t.TempDir(), Host: testHost})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		_, err := os.Stat(ready)
		return err == nil
	})

	ended, err := p.endHostAgents(context.Background(), machine)
	if err != nil {
		t.Fatalf("endHostAgents() = %v", err)
	}
	if want := []int{agent.Process.Pid}; !reflect.DeepEqual(ended, want) {
		t.Errorf("ended = %v, want %v", ended, want)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the stand-in host agent is still running")
	}
}

func waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("the stand-in host agent never became ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
