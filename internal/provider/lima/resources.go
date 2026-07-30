package lima

import (
	"fmt"
	"math"
	"runtime"
	"strconv"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// Resource ceilings and the disk size a new machine gets (REQ-17.4). A Linux
// environment that is meant to feel like another shell tab must not be able to
// starve the editor and browser the user still has open, so the defaults are
// deliberately a fraction of the host rather than a share of it.
const (
	// maxDefaultCPUs caps the automatic CPU count however large the host is.
	maxDefaultCPUs = 4
	// cpuDivisor gives the machine half the host's cores before the cap.
	cpuDivisor = 2
	// maxDefaultMemoryGB caps the automatic memory allocation.
	maxDefaultMemoryGB = 8.0
	// memoryDivisor gives the machine a quarter of host memory before the cap.
	memoryDivisor = 4
	// minMemoryGB is the least memory a usable environment gets, which matters
	// on an 8 GB host where a quarter share would be too small to install a
	// toolchain in.
	minMemoryGB = 2.0
	// defaultDiskGB is the machine's disk size. Lima images are sparse, so this
	// is a ceiling on growth rather than space taken up front.
	defaultDiskGB = 100.0
	// fallbackHostMemoryGB is assumed when the host's memory cannot be read.
	// It is deliberately low: under-provisioning is recoverable, and
	// over-provisioning on a small host is what REQ-17.4 exists to prevent.
	fallbackHostMemoryGB = 8.0
)

// HostResources is what avar sizes a new machine against.
//
// It is a value rather than a probe so that config generation stays a pure
// function of its inputs: the golden-file tests pin an exact host instead of
// producing whatever the machine running the tests happens to have.
type HostResources struct {
	// CPUs is the number of logical cores the host has.
	CPUs int
	// MemoryGB is the host's total physical memory in gibibytes.
	MemoryGB float64
	// Arch is the host's CPU architecture, which decides whether a machine
	// runs natively or under emulation. An empty value means the architecture
	// avar is running on.
	Arch types.Arch
}

// arch reports the host architecture, defaulting to the real one.
func (h HostResources) arch() types.Arch {
	if h.Arch == "" {
		return types.HostArch()
	}
	return h.Arch
}

// bytesPerGiB converts between Lima's byte counts and avar's gibibytes.
const bytesPerGiB = 1 << 30

// resources decides the size of a machine being created.
//
// A non-zero field in spec is the caller's explicit decision and is passed
// through untouched; a zero field means "the backend's conservative,
// host-proportional default", exactly as MachineSpec documents, which keeps
// REQ-17.4's numbers in this one place instead of in every caller.
func resources(spec provider.MachineSpec, host HostResources) (cpus int, memoryGB, diskGB float64) {
	cpus = spec.CPUs
	if cpus == 0 {
		cpus = defaultCPUs(host.CPUs)
	}
	memoryGB = spec.MemoryGB
	if memoryGB == 0 {
		memoryGB = defaultMemoryGB(host.MemoryGB)
	}
	diskGB = spec.DiskGB
	if diskGB == 0 {
		diskGB = defaultDiskGB
	}
	return cpus, memoryGB, diskGB
}

// defaultCPUs is min(maxDefaultCPUs, hostCPUs/2), and never zero.
func defaultCPUs(hostCPUs int) int {
	if hostCPUs <= 0 {
		hostCPUs = runtime.NumCPU()
	}
	cpus := hostCPUs / cpuDivisor
	if cpus > maxDefaultCPUs {
		cpus = maxDefaultCPUs
	}
	if cpus < 1 {
		cpus = 1
	}
	return cpus
}

// defaultMemoryGB is min(maxDefaultMemoryGB, hostMemory/4), floored at
// minMemoryGB so that a small host still gets a usable environment.
func defaultMemoryGB(hostMemoryGB float64) float64 {
	if hostMemoryGB <= 0 {
		hostMemoryGB = fallbackHostMemoryGB
	}
	memory := hostMemoryGB / memoryDivisor
	if memory > maxDefaultMemoryGB {
		memory = maxDefaultMemoryGB
	}
	if memory < minMemoryGB {
		memory = minMemoryGB
	}
	// Whole or half gibibytes only: a machine sized 1.7333 GiB tells the user
	// nothing and makes `avr status` output look like a rounding accident.
	return math.Round(memory*2) / 2
}

// formatGiB renders a gibibyte count the way Lima's go-units parser reads it.
// Whole gibibytes stay gibibytes; a fraction becomes mebibytes so that no
// decimal point ever reaches the config.
func formatGiB(gb float64) string {
	if gb <= 0 {
		gb = minMemoryGB
	}
	if whole := math.Round(gb); math.Abs(gb-whole) < 1e-9 {
		return strconv.FormatInt(int64(whole), 10) + "GiB"
	}
	return strconv.FormatInt(int64(math.Round(gb*1024)), 10) + "MiB"
}

// gibibytes converts a byte count as Lima reports it into gibibytes, rounded to
// two decimals so that status output does not show floating-point noise.
func gibibytes(bytes int64) float64 {
	if bytes <= 0 {
		return 0
	}
	return math.Round(float64(bytes)/bytesPerGiB*100) / 100
}

// describeResources renders the sizing for a progress message, e.g.
// "4 CPU · 8 GB RAM" (REQ-1.2).
func describeResources(cpus int, memoryGB float64) string {
	return fmt.Sprintf("%d CPU · %s GB RAM", cpus, strconv.FormatFloat(memoryGB, 'f', -1, 64))
}
