package lima

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/olamide226/avar/internal/types"
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

// HostCapacity reports the Mac's logical CPUs and physical memory
// (provider.MachineSizer).
//
// It reads the same values hostResources sizes default machines from, but it
// never falls back: hostResources may guess low because a small machine is
// recoverable, while a size refused against a guess would refuse a file that
// is fine.
func (p *Provider) HostCapacity(ctx context.Context) (types.HostCapacity, error) {
	capacity := types.HostCapacity{CPUs: p.host.CPUs}
	if capacity.CPUs <= 0 {
		capacity.CPUs = runtime.NumCPU()
	}
	if p.host.MemoryGB > 0 {
		capacity.MemoryBytes = int64(p.host.MemoryGB * bytesPerGiB)
		return capacity, nil
	}
	bytes, err := p.hostMemoryBytes(ctx)
	if err != nil {
		return types.HostCapacity{}, err
	}
	capacity.MemoryBytes = bytes
	return capacity, nil
}

// sysctlPaths are where sysctl lives. The absolute path is tried first because
// a terminal launched from the macOS GUI can inherit a PATH without /usr/sbin.
var sysctlPaths = []string{"/usr/sbin/sysctl", "sysctl"}

// hostMemoryGB reads the host's physical memory.
//
// A failure here is not worth failing provisioning over — the machine only ends
// up conservatively sized — so it falls back rather than returning an error.
func (p *Provider) hostMemoryGB(ctx context.Context) float64 {
	bytes, err := p.hostMemoryBytes(ctx)
	if err != nil {
		return fallbackHostMemoryGB
	}
	return float64(bytes) / bytesPerGiB
}

// hostMemoryBytes reads the host's physical memory with `sysctl hw.memsize`.
func (p *Provider) hostMemoryBytes(ctx context.Context) (int64, error) {
	var errs []error
	for _, sysctl := range sysctlPaths {
		out, err := p.runner.Output(ctx, sysctl, "-n", "hw.memsize")
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", sysctl, err))
			continue
		}
		bytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if err != nil || bytes <= 0 {
			errs = append(errs, fmt.Errorf("%s printed %q, which is not a size in bytes", sysctl, strings.TrimSpace(string(out))))
			continue
		}
		return bytes, nil
	}
	return 0, fmt.Errorf("read this computer's memory with `sysctl -n hw.memsize`: %w", errors.Join(errs...))
}
