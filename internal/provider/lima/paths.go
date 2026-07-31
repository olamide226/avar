package lima

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/olamide226/avar/internal/types"
)

// Lima's answer to "where does this project appear inside Linux", and the
// host-side checks that decide whether sharing it can work at all.
//
// Both the configuration generated for a new machine and the mount list applied
// to an existing one go through normalizeMounts, so "the same set of shares"
// means the same thing in both places — which is what makes SetMounts'
// no-change case decidable, and with it the promise that returning to a known
// project is instant (REQ-17.1).

// ID reports that this is the Lima backend (REQ-18.14).
func (p *Provider) ID() types.ProviderID { return types.ProviderLima }

// MapProjectPath is the identity mapping: on Lima a project is shared at the
// identical absolute path, so the guest sees `/Users/dev/code/app` at
// `/Users/dev/code/app` and a path printed inside Linux means the same thing in
// macOS (REQ-6.1, REQ-6.6, PROP-1).
//
// That this is the identity mapping is exactly what keeps the abstraction
// honest rather than ceremonial. The interface does not exist because Lima
// needed it — it exists because WSL cannot have it: `C:\Users\ola\code\app` is
// not a Linux path, so the Windows backend maps a project root to
// `/mnt/avr/projects/<Project_Identity>` and appends the relative suffix
// (REQ-18.5, design §3.6). Writing Lima's answer out as a mapping rather than
// letting callers assume it is what makes that second backend a new
// implementation of an existing contract instead of a new branch in every
// caller.
//
// It is a pure function: no filesystem access, no subprocess, no mutation. The
// containment check is not host-specific either — a working directory outside
// the project root would be shared by nothing and is refused here rather than
// mapped to a path the guest cannot reach (REQ-6.5, REQ-9.3, PROP-5).
func (p *Provider) MapProjectPath(projectID, hostRoot, hostCwd string) (types.MountSpec, string, error) {
	if strings.TrimSpace(projectID) == "" {
		return types.MountSpec{}, "", fmt.Errorf("mapping %s into a Linux environment: no project identity was given", hostRoot)
	}
	root, err := absoluteDir(hostRoot, "project directory")
	if err != nil {
		return types.MountSpec{}, "", err
	}
	cwd, err := absoluteDir(hostCwd, "current directory")
	if err != nil {
		return types.MountSpec{}, "", err
	}
	if !covers(root, cwd) {
		return types.MountSpec{}, "", fmt.Errorf("mapping %s into a Linux environment: it is not inside the project directory %s, and avar shares only registered project directories", cwd, root)
	}

	mount := types.MountSpec{
		ProjectID: projectID,
		HostPath:  root,
		GuestPath: root,
		Writable:  true,
	}
	if err := mount.Validate(); err != nil {
		return types.MountSpec{}, "", fmt.Errorf("mapping %s into a Linux environment: %w", root, err)
	}
	return mount, cwd, nil
}

// absoluteDir checks one of MapProjectPath's path arguments.
//
// A relative path is refused rather than resolved against avar's own working
// directory, because which directory is meant is the caller's decision and
// guessing at it could share the wrong tree.
func absoluteDir(dir, what string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", fmt.Errorf("mapping a project into a Linux environment: no %s was given", what)
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("mapping a project into a Linux environment: the %s %q must be an absolute path", what, dir)
	}
	return filepath.Clean(dir), nil
}

// covers reports whether dir is root itself or a directory beneath it. Both
// arguments are cleaned absolute paths, so /a/b covers /a/b and /a/b/c but not
// /a/bc.
func covers(root, dir string) bool {
	if root == dir {
		return true
	}
	sep := string(filepath.Separator)
	if root == sep {
		return strings.HasPrefix(dir, sep)
	}
	return strings.HasPrefix(dir, root+sep)
}

// normalizeMounts turns a caller's desired set into the canonical form avar
// compares and writes: cleaned, validated, deduplicated and ordered.
//
// The rules live in types.NormalizeMounts, so that the store and every backend
// agree on what one set of shares is; this wrapper adds the sentence that
// explains a rejection in Lima's terms.
func normalizeMounts(mounts []types.MountSpec) ([]types.MountSpec, error) {
	out, err := types.NormalizeMounts(mounts)
	if err != nil {
		return nil, fmt.Errorf("%w; avar shares a project at the identical absolute path in the guest, so both paths have to be absolute and the set has to describe one directory per path", err)
	}
	return out, nil
}

// checkHostDirs is the pre-flight that stops avar from configuring a share for
// something that is not a directory the user can reach.
//
// It runs before anything is provisioned or restarted, so that a mistyped or
// deleted project directory costs an immediate error rather than a ten-second
// machine restart that ends in one (REQ-6.5, design §6).
func checkHostDirs(mounts []types.MountSpec) error {
	for _, m := range mounts {
		info, err := os.Stat(m.HostPath)
		if err != nil {
			return fmt.Errorf("checking the project directory %s before sharing it: %w", m.HostPath, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("checking the project directory %s before sharing it: it is not a directory", m.HostPath)
		}
	}
	return nil
}
