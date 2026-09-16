package lima

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/olamide226/avar/internal/types"
)

const (
	// psProgram and killProgram are named by absolute path so that a PATH
	// entry cannot substitute either of them, and they run through the
	// provider's Runner like every other subprocess this package starts.
	//
	// Plain programs rather than syscall.Kill: this package compiles on
	// Windows (where the WSL backend is built), and syscall.Kill does not exist
	// there.
	psProgram   = "/bin/ps"
	killProgram = "/bin/kill"

	// hostAgentTermGrace is how long an agent is given to exit on SIGTERM
	// before it is sent SIGKILL.
	hostAgentTermGrace = 200 * time.Millisecond
	// hostAgentKillSettle bounds how long avar waits for a SIGKILLed agent to
	// leave the process table, polling every hostAgentPoll.
	hostAgentKillSettle = time.Second
	hostAgentPoll       = 50 * time.Millisecond
	// reapTimeout bounds the whole cleanup, so a stuck `ps` cannot hold up
	// a stop that has otherwise finished.
	reapTimeout = 3 * time.Second
)

// psArgs lists every process with its full command line. `ww` lifts the column
// limit: a command line cut short would lose the machine name at its end, and
// the agent would then be missed silently rather than reported.
var psArgs = []string{"-axww", "-o", "pid=,command="}

// reapHostAgents ends the host-agent processes Lima left running for a machine
// it has already stopped, and tells the user when there were any.
//
// The agents are identified by the exact limactl executable and machine name
// rather than a broad process-name match, so an unrelated Lima environment is
// never touched (REQ-5.4).
func (p *Provider) reapHostAgents(ctx context.Context, machine string, progress types.ProgressSink) error {
	ended, err := p.endHostAgents(ctx, machine)
	if len(ended) > 0 {
		progress.Progress(types.ProgressEvent{
			Kind:    types.ProgressWarning,
			Machine: machine,
			Message: reapedMessage(p.environmentLabel(machine), ended),
		})
	}
	if err != nil {
		return fmt.Errorf("ending the Lima host agents left running for machine %s: %w", machine, err)
	}
	return nil
}

// endHostAgents signals every matching agent and reports the ones that are
// gone afterwards.
//
// The exit status of `kill` is not the verdict. An agent can exit between the
// listing that found it and the signal sent to it — the SIGKILL pass targets
// exactly the agents that received SIGTERM a moment earlier, which are the
// likeliest to have just exited — and `kill` then fails with "No such process"
// for a process that is precisely where avar wants it. Whether an agent is gone
// is decided by listing the processes again, and a signal's error is kept only
// to explain an agent that survives.
func (p *Provider) endHostAgents(ctx context.Context, machine string) ([]int, error) {
	ctx, cancel := context.WithTimeout(ctx, reapTimeout)
	defer cancel()

	found, err := p.hostAgentPIDs(ctx, machine)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	p.signalEach(ctx, "-TERM", found)

	// Well-behaved agents exit on TERM. A wedged agent is precisely the leak
	// this exists for, so it gets one bounded escalation.
	if err := pause(ctx, hostAgentTermGrace); err != nil {
		return nil, err
	}
	remaining, err := p.hostAgentPIDs(ctx, machine)
	if err != nil {
		return nil, err
	}
	if len(remaining) == 0 {
		return found, nil
	}
	signalErr := p.signalEach(ctx, "-KILL", remaining)

	remaining, err = p.awaitHostAgentsGone(ctx, machine)
	if err != nil {
		return nil, err
	}
	if len(remaining) > 0 {
		return without(found, remaining), fmt.Errorf("%s still running after SIGKILL (last signal error: %v)",
			processesPhrase(remaining), signalErr)
	}
	return found, nil
}

// hostAgentPIDs lists the agents currently running for a machine.
func (p *Provider) hostAgentPIDs(ctx context.Context, machine string) ([]int, error) {
	out, err := p.runner.Output(ctx, psProgram, psArgs...)
	if err != nil {
		return nil, fmt.Errorf("listing host processes: %w", err)
	}
	return parseOrphanHostAgentPIDs(string(out), p.limactl, machine), nil
}

// signalEach sends a signal to every pid, and returns the last failure so an
// agent that outlives it can be explained. A failure does not stop the others
// being signalled.
func (p *Provider) signalEach(ctx context.Context, signal string, pids []int) error {
	var last error
	for _, pid := range pids {
		if _, err := p.runner.Output(ctx, killProgram, signal, strconv.Itoa(pid)); err != nil {
			last = fmt.Errorf("sending %s to process %d: %w", signal, pid, err)
		}
	}
	return last
}

// awaitHostAgentsGone polls until no agent is listed or hostAgentKillSettle
// passes, and reports what is still there. SIGKILL cannot be caught, but a
// process leaves the table only once it has been reaped, which is not
// instantaneous.
func (p *Provider) awaitHostAgentsGone(ctx context.Context, machine string) ([]int, error) {
	deadline := time.Now().Add(hostAgentKillSettle)
	for {
		remaining, err := p.hostAgentPIDs(ctx, machine)
		if err != nil || len(remaining) == 0 || time.Now().After(deadline) {
			return remaining, err
		}
		if err := pause(ctx, hostAgentPoll); err != nil {
			return nil, err
		}
	}
}

// pause waits for d, or returns early with the context's error.
func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseOrphanHostAgentPIDs(output, limactl, machine string) []int {
	prefix, suffix := limactl+" hostagent ", " "+machine
	var pids []int
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		command := strings.Join(fields[1:], " ")
		if strings.HasPrefix(command, prefix) && strings.HasSuffix(command, suffix) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// environmentLabel names a machine the way the user chose it — distro, version
// and architecture — so that what avar says about it never asks the user to
// know a machine name (REQ-1.5). A machine avar holds no usable record of is
// named as it is, which is the only honest description left.
func (p *Provider) environmentLabel(machine string) string {
	if p.records == nil {
		return machine
	}
	record, ok, err := p.records.Machine(machine)
	if err != nil || !ok || record.Selector.Distro == "" {
		return machine
	}
	return record.Selector.Label()
}

// reapedMessage tells the user that avar ended processes it did not start in
// this invocation, and why.
func reapedMessage(label string, pids []int) string {
	if len(pids) == 1 {
		return fmt.Sprintf("%s had a Lima host agent (%s) still running after it stopped; avar ended it.",
			label, processesPhrase(pids))
	}
	return fmt.Sprintf("%s had %d Lima host agents (%s) still running after it stopped; avar ended them.",
		label, len(pids), processesPhrase(pids))
}

// processesPhrase renders pids as "process 12" or "processes 12, 34".
func processesPhrase(pids []int) string {
	numbers := make([]string, 0, len(pids))
	for _, pid := range pids {
		numbers = append(numbers, strconv.Itoa(pid))
	}
	if len(pids) == 1 {
		return "process " + numbers[0]
	}
	return "processes " + strings.Join(numbers, ", ")
}

// without reports the pids in all that are not in some.
func without(all, some []int) []int {
	excluded := make(map[int]bool, len(some))
	for _, pid := range some {
		excluded[pid] = true
	}
	var out []int
	for _, pid := range all {
		if !excluded[pid] {
			out = append(out, pid)
		}
	}
	return out
}
