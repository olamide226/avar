package lima

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// reapHostAgents removes host-agent processes that Lima left behind after it
// has stopped an instance. These agents are identified by the exact limactl
// executable and machine name, rather than a broad process-name match, so an
// unrelated Lima environment is never touched.
func reapHostAgents(ctx context.Context, limactl, machine string) error {
	cleanupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	pids, err := orphanHostAgentPIDs(cleanupCtx, limactl, machine)
	if err != nil {
		return err
	}
	for _, pid := range pids {
		if err := signalPID(cleanupCtx, "-TERM", pid); err != nil {
			return err
		}
	}
	if len(pids) == 0 {
		return nil
	}

	// Well-behaved agents exit on TERM. A wedged agent is precisely the leak we
	// are fixing, so make one bounded escalation before returning control.
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-cleanupCtx.Done():
		return cleanupCtx.Err()
	case <-timer.C:
	}
	pids, err = orphanHostAgentPIDs(cleanupCtx, limactl, machine)
	if err != nil {
		return err
	}
	for _, pid := range pids {
		if err := signalPID(cleanupCtx, "-KILL", pid); err != nil {
			return err
		}
	}
	return nil
}

func orphanHostAgentPIDs(ctx context.Context, limactl, machine string) ([]int, error) {
	out, err := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("listing host processes: %w", err)
	}
	return parseOrphanHostAgentPIDs(string(out), limactl, machine), nil
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

func signalPID(ctx context.Context, signal string, pid int) error {
	if err := exec.CommandContext(ctx, "/bin/kill", signal, strconv.Itoa(pid)).Run(); err != nil {
		return fmt.Errorf("sending %s to process %d: %w", signal, pid, err)
	}
	return nil
}
