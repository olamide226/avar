// Package editor owns avar's editor integration, which is the surface behind
// `avr code`. It manages avar-owned SSH configuration — never the user's own
// ~/.ssh/config — and launches editors that connect through it.
//
// The SSH configuration lives in the state store's ssh directory, which the
// caller supplies — this package derives no paths of its own, so avar has a
// single answer to where its state lives. Every host entry is delimited by a
// comment header that names the machine it belongs to, so that individual
// entries can be added, replaced or removed without rewriting the rest.
// Writes go through state.WriteFileAtomic, so a crash never leaves a
// half-written config (REQ-17.5).
package editor

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/olamide226/avar/internal/state"
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

// ConfigPath returns the path to avar's SSH config file inside sshDir, which
// the caller takes from the state store (state.Store.SSHDir).
//
// The directory is a parameter rather than derived here so that avar has one
// answer to "where is the state directory": the store's, which honours
// $AVR_HOME and is platform-aware. Deriving ~/.avr independently would put
// this file somewhere the rest of avar is not looking.
func ConfigPath(sshDir string) string {
	return filepath.Join(sshDir, "config")
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
func WriteHost(sshDir, machine, hostBlock string) error {
	if err := os.MkdirAll(sshDir, sshDirPerm); err != nil {
		return fmt.Errorf("create %s: %w", sshDir, err)
	}

	configPath := ConfigPath(sshDir)

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
func RemoveHost(sshDir, machine string) error {
	configPath := ConfigPath(sshDir)

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

	// The state package owns avar's atomic-write rule, so this uses it rather
	// than repeating it: the copy that lived here wrote to a fixed ".tmp" path
	// two concurrent `avr code` runs would collide on, and never fsynced,
	// which made the guarantee in this package's doc comment untrue.
	return state.WriteFileAtomic(configPath, buf.Bytes())
}

// ReadHostConfig returns the full SSH config file content as a string,
// suitable for passing to `ssh -F` or for writing tests against. It returns
// an empty string when the file does not exist.
func ReadHostConfig(sshDir string) (string, error) {
	configPath := ConfigPath(sshDir)
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", configPath, err)
	}
	return string(data), nil
}
