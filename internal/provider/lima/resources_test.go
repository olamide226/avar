package lima

import (
	"testing"

	"github.com/olamide226/avar/internal/provider"
)

func TestResources_AreConservativeAndHostProportional_REQ_17_4(t *testing.T) {
	// Roughly min(4, host/2) CPUs and min(8 GB, host/4) memory, so that
	// provisioning a Linux environment never destabilises the Mac still
	// running the user's editor and browser.
	cases := []struct {
		name       string
		host       HostResources
		wantCPUs   int
		wantMemory float64
	}{
		{"16-core 64 GB workstation", HostResources{CPUs: 16, MemoryGB: 64}, 4, 8},
		{"10-core 32 GB laptop", HostResources{CPUs: 10, MemoryGB: 32}, 4, 8},
		{"8-core 16 GB laptop", HostResources{CPUs: 8, MemoryGB: 16}, 4, 4},
		{"4-core 8 GB laptop", HostResources{CPUs: 4, MemoryGB: 8}, 2, 2},
		{"2-core 4 GB machine", HostResources{CPUs: 2, MemoryGB: 4}, 1, 2},
		{"1-core host still gets a usable machine", HostResources{CPUs: 1, MemoryGB: 2}, 1, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cpus, memory, disk := resources(provider.MachineSpec{}, tc.host)
			if cpus != tc.wantCPUs {
				t.Errorf("cpus = %d, want %d", cpus, tc.wantCPUs)
			}
			if memory != tc.wantMemory {
				t.Errorf("memory = %v GB, want %v", memory, tc.wantMemory)
			}
			if disk != defaultDiskGB {
				t.Errorf("disk = %v GB, want the sparse default %v", disk, defaultDiskGB)
			}
			if cpus < 1 || memory < minMemoryGB {
				t.Errorf("a machine was sized below usability: %d CPU, %v GB", cpus, memory)
			}
		})
	}
}

func TestResources_HonourTheCallersOwnSizing(t *testing.T) {
	// MachineSpec says a zero value means "the backend's default" and a
	// non-zero value is the caller's decision. The resolver may compute these
	// itself, and when it does, avar must not second-guess it.
	spec := provider.MachineSpec{CPUs: 12, MemoryGB: 24, DiskGB: 250}
	cpus, memory, disk := resources(spec, HostResources{CPUs: 4, MemoryGB: 8})
	if cpus != 12 || memory != 24 || disk != 250 {
		t.Errorf("resources = %d CPU, %v GB, %v GB disk; want the caller's 12/24/250", cpus, memory, disk)
	}
}

func TestFormatGiB_NeverEmitsADecimalPoint(t *testing.T) {
	// Lima parses these with go-units, and a decimal point in the generated
	// configuration is a needless way to find out whether it copes.
	cases := map[float64]string{
		8:   "8GiB",
		2:   "2GiB",
		2.5: "2560MiB",
		100: "100GiB",
	}
	for input, want := range cases {
		if got := formatGiB(input); got != want {
			t.Errorf("formatGiB(%v) = %q, want %q", input, got, want)
		}
	}
}

func TestGibibytes_ConvertsLimasByteCounts(t *testing.T) {
	if got := gibibytes(8589934592); got != 8 {
		t.Errorf("gibibytes(8 GiB) = %v, want 8", got)
	}
	if got := gibibytes(0); got != 0 {
		t.Errorf("gibibytes(0) = %v, want 0", got)
	}
}

func TestDescribeResources_ReadsLikeREQ_1_2sExample(t *testing.T) {
	if got := describeResources(4, 8); got != "4 CPU · 8 GB RAM" {
		t.Errorf("describeResources(4, 8) = %q", got)
	}
}
