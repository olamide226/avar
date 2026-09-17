package projconfig

import (
	"reflect"
	"testing"

	"github.com/olamide226/avar/internal/types"
)

// sixteenGiBMac is a 10-core Mac with 16 GiB, as `sysctl -n hw.memsize`
// reports it: 17179869184 bytes.
var sixteenGiBMac = types.HostCapacity{CPUs: 10, MemoryBytes: 16 << 30}

// The boundary is the host itself: a size equal to what the computer has is
// allowed, and one CPU or one MiB more is refused. Nothing stricter is applied.
func TestExceedsHost_ComparesAgainstTheHost_REQ_15_5(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want []HostExcess
	}{
		{name: "no sizes", body: `distro = "ubuntu"`},
		{name: "cpus equal to the host", body: "cpus = 10"},
		{
			name: "one CPU more than the host",
			body: "cpus = 11",
			want: []HostExcess{{Key: "cpus", Line: 1, Setting: "cpus = 11"}},
		},
		{name: "memory equal to the host in GiB", body: `memory = "16GiB"`},
		{name: "memory equal to the host in MiB", body: `memory = "16384MiB"`},
		{
			name: "one MiB more than the host",
			body: `memory = "16385MiB"`,
			want: []HostExcess{{Key: "memory", Line: 1, Setting: `memory = "16385MiB"`}},
		},
		{
			name: "one GiB more than the host",
			body: "# sized for the build\n\nmemory = \"17GiB\"",
			want: []HostExcess{{Key: "memory", Line: 3, Setting: `memory = "17GiB"`}},
		},
		{name: "both at the host", body: "cpus = 10\nmemory = \"16GiB\""},
		{
			name: "both over, reported cpus first whatever the file's order",
			body: "memory = \"64GiB\"\ncpus = 32",
			want: []HostExcess{
				{Key: "cpus", Line: 2, Setting: "cpus = 32"},
				{Key: "memory", Line: 1, Setting: `memory = "64GiB"`},
			},
		},
		{
			name: "one over and one within",
			body: "cpus = 4\nmemory = \"32GiB\"",
			want: []HostExcess{{Key: "memory", Line: 2, Setting: `memory = "32GiB"`}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse(testPath, []byte(tc.body))
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.body, err)
			}
			if got := cfg.ExceedsHost(sixteenGiBMac); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ExceedsHost = %+v, want %+v", got, tc.want)
			}
		})
	}
}
