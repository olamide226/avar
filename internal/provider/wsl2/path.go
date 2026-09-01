package wsl2

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/olamide226/avar/internal/types"
)

// GuestProjectRoot is the directory every shared project appears under inside a
// WSL guest.
//
// It is a fixed, avar-owned root and not a mirror of the Windows path, because
// there is no Windows path a Linux filesystem can have. `C:\Users\ola\code\app`
// is not a Linux path and never will be, so this backend maps a project to a
// deterministic Linux directory and tells the caller where that is — which is
// the difference MapProjectPath exists to hide from everything above it
// (REQ-18.5, PROP-1, PROP-14).
//
// Everything avar shares lives beneath this one directory, which is also what
// makes mount confinement checkable by inspection: a guest path outside it is a
// mount avar did not plan (PROP-5).
const GuestProjectRoot = "/mnt/avr/projects"

// guestDirHashLen is how much of the Project_Identity goes into a guest
// directory name. It matches the truncation the resolver already uses for a
// per-project machine name, so the same project is recognisable by the same ten
// characters wherever avar shows it.
const guestDirHashLen = 10

// guestLabelMaxLen bounds the readable half of a guest directory name.
const guestLabelMaxLen = 32

// unsafeLabelChars is everything that does not belong in the readable half of a
// guest directory name. Keeping it to this set means the path never needs
// quoting in a shell, a mount unit, or an fstab line.
var unsafeLabelChars = regexp.MustCompile(`[^a-z0-9._-]`)

// MapProjectPath converts a canonical Windows project root and a working
// directory beneath it into the mount to apply and the guest directory to start
// in.
//
// The guest directory is named for the project and then for its identity —
// `code-app-3fa9c2b1d0` — rather than being the identity alone. The identity is
// what makes it unique; the name is what makes it usable, and this path is not
// an implementation detail the user never sees: it is their working directory,
// it is in their shell prompt, in their editor's title bar, and in every error
// message any tool prints. Sixty-four hexadecimal characters there is a real
// cost for no correctness gained, and the ten-character truncation is the same
// one avar already uses to name a per-project machine.
//
// It is a pure function: deterministic, no filesystem access, no subprocess. It
// plans a mapping; applying one is SetMounts.
func (p *Provider) MapProjectPath(projectID, hostRoot, hostCwd string) (types.MountSpec, string, error) {
	return mapProjectPath(projectID, hostRoot, hostCwd)
}

func mapProjectPath(projectID, hostRoot, hostCwd string) (types.MountSpec, string, error) {
	if strings.TrimSpace(projectID) == "" {
		return types.MountSpec{}, "", fmt.Errorf("mapping %s into the guest: no project identity was given", hostRoot)
	}
	if !filepath.IsAbs(hostRoot) || !filepath.IsAbs(hostCwd) {
		return types.MountSpec{}, "", fmt.Errorf("mapping a project into the guest: the project root and the current directory must both be absolute Windows paths (got %q and %q)", hostRoot, hostCwd)
	}
	root, cwd := filepath.Clean(hostRoot), filepath.Clean(hostCwd)

	// A working directory outside the project root is rejected rather than
	// mapped: a guest path that escaped its own project mount is exactly what
	// mount confinement forbids (REQ-9.3, PROP-5).
	rel, err := relativeWithin(root, cwd)
	if err != nil {
		return types.MountSpec{}, "", fmt.Errorf("mapping %s into the guest: it is not inside the project directory %s", cwd, root)
	}

	guestRoot := GuestRoot(projectID, root)
	guestCwd := guestRoot
	if rel != "." {
		guestCwd = path.Join(guestRoot, filepath.ToSlash(rel))
	}

	mount := types.MountSpec{ProjectID: projectID, HostPath: root, GuestPath: guestRoot, Writable: true}
	if err := mount.Validate(); err != nil {
		return types.MountSpec{}, "", fmt.Errorf("mapping %s into the guest: %w", root, err)
	}
	return mount, guestCwd, nil
}

// GuestRoot is the Linux directory a project appears at.
//
// It is exported because the parts of this backend that read mounts back need to
// recognise avar's own guest paths, and because a test that recomputed the rule
// would be asserting its own arithmetic rather than the provider's.
func GuestRoot(projectID, hostRoot string) string {
	return path.Join(GuestProjectRoot, guestDirName(projectID, hostRoot))
}

// guestDirName builds the readable-then-unique directory name.
//
// The hash half is never omitted, whatever the label turns out to be: it is what
// makes two projects with the same directory name — `~/work/api` and
// `~/personal/api`, which a developer really does have — distinct inside the
// guest, and PROP-14 is precisely the claim that distinct canonical host paths
// get distinct guest targets.
func guestDirName(projectID, hostRoot string) string {
	hash := projectID
	if len(hash) > guestDirHashLen {
		hash = hash[:guestDirHashLen]
	}
	label := guestLabel(hostRoot)
	if label == "" {
		return hash
	}
	return label + "-" + hash
}

// guestLabel reduces a Windows path's last component to something that reads
// like a project name and needs no quoting anywhere.
//
// A drive root has no last component to use — `C:\` is not named after
// anything — and yields an empty label, leaving the name as the hash alone.
func guestLabel(hostRoot string) string {
	base := filepath.Base(filepath.Clean(hostRoot))
	// filepath.Base of a drive root is the drive itself ("C:\"), which names
	// no project.
	if base == "" || base == "." || strings.ContainsAny(base, `:\/`) {
		return ""
	}
	label := unsafeLabelChars.ReplaceAllString(strings.ToLower(base), "-")
	label = strings.Trim(label, "-._")
	if len(label) > guestLabelMaxLen {
		label = strings.Trim(label[:guestLabelMaxLen], "-._")
	}
	return label
}

// relativeWithin reports cwd's position beneath root, or an error if it is not
// beneath it at all.
//
// The comparison is the host's, which on Windows means case-insensitive:
// `C:\Code\App` and `c:\code\app` are one directory, and a caller that reached
// here having spelled the two halves differently is describing a working
// directory that really is inside the project (REQ-18.13, PROP-14).
func relativeWithin(root, cwd string) (string, error) {
	rel, err := filepath.Rel(root, cwd)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is not inside %s", cwd, root)
	}
	return rel, nil
}
