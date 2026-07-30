package lima

import (
	"context"
	"runtime"
	"strconv"
	"strings"
)

// hostResources reports the host avar is running on, probing it only when the
// caller did not pin one.
//
// This runs on the create path only: EnsureMachine's warm path must not pay for
// a subprocess it has no use for (REQ-17.1).
func (p *Provider) hostResources(ctx context.Context) HostResources {
	if p.host.CPUs > 0 && p.host.MemoryGB > 0 {
		return p.host
	}
	host := p.host
	if host.CPUs <= 0 {
		host.CPUs = runtime.NumCPU()
	}
	if host.MemoryGB <= 0 {
		host.MemoryGB = p.hostMemoryGB(ctx)
	}
	return host
}

// sysctlPaths are where sysctl lives. The absolute path is tried first because
// a terminal launched from the macOS GUI can inherit a PATH without /usr/sbin.
var sysctlPaths = []string{"/usr/sbin/sysctl", "sysctl"}

// hostMemoryGB reads the host's physical memory.
//
// A failure here is not worth failing provisioning over — the machine only ends
// up conservatively sized — so it falls back rather than returning an error.
func (p *Provider) hostMemoryGB(ctx context.Context) float64 {
	for _, sysctl := range sysctlPaths {
		out, err := p.runner.Output(ctx, sysctl, "-n", "hw.memsize")
		if err != nil {
			continue
		}
		bytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if err != nil || bytes <= 0 {
			continue
		}
		return float64(bytes) / bytesPerGiB
	}
	return fallbackHostMemoryGB
}
