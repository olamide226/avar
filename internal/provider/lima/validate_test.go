package lima

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Golden files prove avar's configuration generator is stable. They do not
// prove the result is a configuration Lima will accept — only Lima can say
// that, and `limactl validate` says it without creating a virtual machine,
// downloading an image, or needing virtualization.
//
// So when limactl is on the machine, every golden configuration is put through
// it. That closes the gap between "this is what avar generates" and "this is a
// machine Lima can build", which is otherwise the largest thing a test suite
// without Lima cannot check.
//
// The test skips when limactl is absent, so CI without Lima stays green — but
// it runs by default rather than behind the e2e tag, because it neither
// provisions anything nor takes measurable time.
func TestGeneratedConfigurations_AreAcceptedByLima(t *testing.T) {
	limactl, err := exec.LookPath("limactl")
	if err != nil {
		t.Skip("limactl is not installed: skipping validation of the generated configurations against real Lima")
	}

	goldens, err := filepath.Glob(filepath.Join("testdata", "golden", "*.yaml"))
	if err != nil {
		t.Fatalf("finding the golden configurations: %v", err)
	}
	if len(goldens) == 0 {
		t.Fatal("there are no golden configurations to validate")
	}

	version, err := exec.CommandContext(context.Background(), limactl, "--version").Output()
	if err != nil {
		t.Fatalf("reading the installed Lima version: %v", err)
	}
	t.Logf("validating %d generated configurations against %s", len(goldens), strings.TrimSpace(string(version)))

	for _, golden := range goldens {
		t.Run(filepath.Base(golden), func(t *testing.T) {
			// --tty=false keeps limactl out of any interactive path.
			cmd := exec.CommandContext(context.Background(), limactl, "validate", "--tty=false", golden)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("Lima rejected the configuration avar generates:\n%s\n%v", out, err)
			}
		})
	}
}

// TestGeneratedConfiguration_LimaFillsInTheGuestAccount checks REQ-1.4 against
// Lima itself rather than against a belief about Lima.
//
// The requirement is a non-root guest user matching the host username, with
// passwordless sudo. avar writes no provisioning script for that, because Lima
// already does it — and `limactl validate --fill`, which prints the
// configuration with every default filled in, is where that stops being an
// assumption. If a future Lima changed the default, this test fails and avar
// learns it needs to configure the account itself.
func TestGeneratedConfiguration_LimaFillsInTheGuestAccount_REQ_1_4(t *testing.T) {
	limactl, err := exec.LookPath("limactl")
	if err != nil {
		t.Skip("limactl is not installed: skipping the guest-account check against real Lima")
	}
	golden := filepath.Join("testdata", "golden", "ubuntu-24.04-arm64-vz.yaml")
	if _, err := os.Stat(golden); err != nil {
		t.Fatalf("reading the golden configuration: %v", err)
	}

	out, err := exec.CommandContext(context.Background(), limactl, "validate", "--tty=false", "--fill", golden).Output()
	if err != nil {
		t.Fatalf("filling in the configuration's defaults: %v", err)
	}
	filled := string(out)

	current, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("reading the host user's home directory: %v", err)
	}
	username := filepath.Base(current)

	if !strings.Contains(filled, "name: "+username) {
		t.Errorf("Lima does not create a guest user matching the host username %q, so avar would have to (REQ-1.4):\n%s", username, filled)
	}
	if !strings.Contains(filled, "passwordlessSudo: true") {
		t.Errorf("Lima does not grant the guest user passwordless sudo, so avar would have to (REQ-1.4):\n%s", filled)
	}
}
