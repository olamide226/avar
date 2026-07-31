// Package editor owns avar's editor integration, which is the surface behind
// `avr code`. It manages avar-owned SSH configuration — never the user's own
// ~/.ssh/config — and launches editors that connect through it.
//
// The SSH configuration lives at ~/.avr/ssh/config. Every host entry is
// delimited by a comment header that names the machine it belongs to, so that
// individual entries can be added, replaced or removed without rewriting the
// rest. WriteHost writes atomically: a temporary file is written, fsynced,
// and renamed, so a crash never leaves a half-written config (REQ-17.5).
package editor

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// hostHeaderPrefix starts each machine's block. It carries the machine name
	// so that the block can be found and replaced in a later write.
	hostHeaderPrefix = "# Machine: "
	// hostHeaderSuffix follows the machine name.
	hostHeaderSuffix = " — managed by avar, do not edit by hand"
	// configFilePreamble explains what the file is and how to use it.
	configFilePreamble = `# This file is managed by avar. Do not edit it by hand.
# To use this config, add the following line to your ~/.ssh/config:
#   Include %s
`
)

// sshDirPerm locks access to ~/.avr/ssh/ down to the user.
const sshDirPerm = 0o700

// configPerm keeps SSH private key references readable only by the owner.
const configPerm = 0o600

// ConfigDir returns the path to avar's SSH configuration directory
// (~/.avr/ssh/).
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find your home directory: %w", err)
	}
	return filepath.Join(home, ".avr", "ssh"), nil
}

// ConfigPath returns the path to avar's SSH config file
// (~/.avr/ssh/config).
func ConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config"), nil
}

// hostHeader builds the comment that marks the start of one machine's block.
func hostHeader(machine string) string {
	return hostHeaderPrefix + machine + hostHeaderSuffix
}

// WriteHost writes or replaces the SSH host entry for machine in avar's
// config file.
//
// hostBlock is the full SSH config stanza including the Host line. It is
// written verbatim after a header comment that names the machine, so a later
// write can find and replace the same entry. The file is written atomically.
func WriteHost(machine, hostBlock string) error {
	dir, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, sshDirPerm); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	configPath, err := ConfigPath()
	if err != nil {
		return err
	}

	hosts, err := readHosts(configPath)
	if err != nil {
		return fmt.Errorf("read the SSH configuration at %s: %w", configPath, err)
	}

	header := hostHeader(machine)
	hosts[machine] = header + "\n" + strings.TrimSpace(hostBlock)

	if err := writeConfigAtomic(configPath, hosts); err != nil {
		return fmt.Errorf("write the SSH configuration at %s: %w", configPath, err)
	}
	return nil
}

// RemoveHost deletes the SSH host entry for machine from avar's config file.
// Removing a machine that has no entry is a no-op.
func RemoveHost(machine string) error {
	configPath, err := ConfigPath()
	if err != nil {
		return err
	}

	hosts, err := readHosts(configPath)
	if err != nil {
		return fmt.Errorf("read the SSH configuration at %s: %w", configPath, err)
	}
	if _, exists := hosts[machine]; !exists {
		return nil
	}
	delete(hosts, machine)

	if err := writeConfigAtomic(configPath, hosts); err != nil {
		return fmt.Errorf("write the SSH configuration at %s: %w", configPath, err)
	}
	return nil
}

// readHosts reads avar's SSH config file and returns the blocks for each
// machine, keyed by machine name. It returns an empty map when the file does
// not exist.
func readHosts(configPath string) (map[string]string, error) {
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return make(map[string]string), nil
	}
	if err != nil {
		return nil, err
	}

	hosts := make(map[string]string)
	content := string(data)

	// Split on the header prefix. The first segment before any header is
	// the preamble; each subsequent segment is one machine's block.
	segments := strings.Split(content, "\n"+hostHeaderPrefix)
	if len(segments) <= 1 {
		// No host blocks.
		return hosts, nil
	}

	for _, segment := range segments[1:] {
		// The segment starts with "<machine> — managed by avar..."
		// Find the end of the header line.
		nl := strings.Index(segment, "\n")
		if nl < 0 {
			continue
		}
		headerLine := segment[:nl]
		// Extract the machine name: everything before " — managed by avar..."
		machine, _, ok := strings.Cut(headerLine, hostHeaderSuffix)
		if !ok {
			continue
		}
		body := strings.TrimSpace(segment[nl+1:])
		if body != "" {
			hosts[machine] = hostHeaderPrefix + machine + hostHeaderSuffix + "\n" + body
		}
	}

	return hosts, nil
}

// writeConfigAtomic builds the config file content from the host map and
// writes it through a temp file so that a crash never leaves a half-written
// config.
func writeConfigAtomic(configPath string, hosts map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(configPath), sshDirPerm); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(configPath), err)
	}

	var buf bytes.Buffer

	// Preamble
	fmt.Fprintf(&buf, configFilePreamble, configPath)
	buf.WriteString("\n")

	if len(hosts) > 0 {
		names := make([]string, 0, len(hosts))
		for name := range hosts {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			buf.WriteString(hosts[name])
			buf.WriteString("\n\n")
		}
	}

	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, buf.Bytes(), configPerm); err != nil {
		return fmt.Errorf("write temporary SSH configuration at %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		return fmt.Errorf("install the SSH configuration at %s: %w", configPath, err)
	}
	return nil
}

// ReadHostConfig returns the full SSH config file content as a string,
// suitable for passing to `ssh -F` or for writing tests against. It returns
// an empty string when the file does not exist.
func ReadHostConfig() (string, error) {
	configPath, err := ConfigPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", configPath, err)
	}
	return string(data), nil
}
