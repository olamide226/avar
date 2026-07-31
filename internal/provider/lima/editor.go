package lima

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/olamide226/avar/internal/provider"
)

// EditorTarget describes how an editor reaches a Lima machine's filesystem
// (REQ-13.1, REQ-18.10).
//
// It reads Lima's per-instance SSH configuration at ~/.lima/<machine>/ssh.config,
// replaces the Lima host name with avar's remote authority so that VS Code
// resolves it correctly, and returns a self-contained SSH stanza. Nothing is
// written to the user's SSH configuration — the caller owns where it lands
// (REQ-13.3).
func (p *Provider) EditorTarget(ctx context.Context, machine, guestPath string) (provider.EditorTarget, error) {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return provider.EditorTarget{}, err
	}

	v := p.newView()
	inst, err := v.require(ctx, machine)
	if err != nil {
		return provider.EditorTarget{}, err
	}
	if !inst.running() {
		return provider.EditorTarget{}, fmt.Errorf("%w: %s is %s", provider.ErrMachineNotRunning, machine, inst.state())
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return provider.EditorTarget{}, fmt.Errorf("find your home directory while preparing the SSH configuration for %s: %w", machine, err)
	}
	configPath := filepath.Join(home, ".lima", machine, "ssh.config")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return provider.EditorTarget{}, fmt.Errorf("read Lima's SSH configuration for %s at %s: the file does not exist — the environment may have been created with an older version of Lima",
				machine, configPath)
		}
		return provider.EditorTarget{}, fmt.Errorf("read Lima's SSH configuration for %s at %s: %w", machine, configPath, err)
	}

	// The machine name is the SSH host alias, and it already carries avar's
	// prefix. The editor authority wraps that alias rather than replacing it:
	// VS Code strips "ssh-remote+" and looks the remainder up as an SSH host,
	// so the Host directive below must be the bare alias or the lookup fails.
	authority := "ssh-remote+" + machine

	// Replace the Lima host name with avar's alias, leaving every other
	// directive — Hostname, Port, IdentityFile, User — intact so that the
	// resulting stanza is self-contained and needs no Lima knowledge to use.
	lines := strings.Split(string(data), "\n")
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Host ") && !strings.HasPrefix(trimmed, "Host *") {
			lines[i] = "Host " + machine
			replaced = true
			break
		}
	}
	if !replaced {
		return provider.EditorTarget{}, fmt.Errorf("read Lima's SSH configuration for %s at %s: the file contains no Host directive", machine, configPath)
	}
	sshConfig := strings.Join(lines, "\n")

	return provider.EditorTarget{
		Authority: authority,
		GuestPath: guestPath,
		SSHConfig: sshConfig,
	}, nil
}
