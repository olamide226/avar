package types

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// MountSpec is one host directory shared into a guest: which host directory,
// where it appears inside Linux, and whether the guest may write to it.
//
// The two paths are separate because they are only equal by accident of the
// backend. Lima shares a directory at the identical absolute path, so a Mac
// user's `/Users/dev/code/app` is `/Users/dev/code/app` in the guest and
// avar can talk about "the path" without qualification. WSL cannot do that:
// `C:\Users\ola\code\app` is not a Linux path and never can be, so the project
// is exposed at a deterministic Linux path instead (REQ-18.5, design §3.4). A
// single host path would have forced every caller to know which world it was
// in; a pair lets avar plan the mapping once, in the provider, and hand the
// answer to everything else (PROP-1, PROP-14).
//
// Keeping both halves in one value is also what makes mount confinement
// checkable rather than merely intended: the complete set of specs applied to a
// machine says exactly which host directories the guest can reach and exactly
// where, which is the whole of what PROP-5 asserts (REQ-6.3, REQ-9.3, REQ-9.4).
type MountSpec struct {
	// ProjectID is the Project_Identity this mount exists to serve, when it
	// serves one. It is metadata, not part of the mapping: it records why a
	// directory is shared, and a backend that needs a project-derived guest
	// path (WSL) builds one from it. It is empty for a mount avar learned about
	// from a backend listing rather than from a project registration, because
	// no backend can know which project a directory belongs to.
	ProjectID string `json:"project_id,omitempty"`

	// HostPath is the directory on the host, absolute in the host's own path
	// vocabulary: POSIX on macOS, drive-letter or UNC on Windows.
	HostPath string `json:"host_path"`

	// GuestPath is where that directory appears inside Linux. It is always an
	// absolute, normalized Linux path, whatever the host's path syntax is, and
	// it is chosen by the provider through MapProjectPath — never by a caller.
	GuestPath string `json:"guest_path"`

	// Writable reports whether the guest may modify the shared directory. avar
	// shares project directories writable, because the point is editing the
	// same files from both sides (REQ-6.1, REQ-6.2).
	Writable bool `json:"writable"`
}

// String renders the mapping for diagnostics and test failures.
func (m MountSpec) String() string {
	access := "ro"
	if m.Writable {
		access = "rw"
	}
	return fmt.Sprintf("%s -> %s (%s)", m.HostPath, m.GuestPath, access)
}

// Validate reports whether the spec describes a mount avar can apply.
//
// HostPath must be absolute in the host's vocabulary and GuestPath absolute and
// normalized in Linux's (design §4). The guest check deliberately uses path
// rather than filepath: a guest path is a Linux path on every host, and
// filepath on Windows would reject `/mnt/avr/projects/x` as relative and accept
// `C:\...` as absolute, which is precisely backwards.
//
// ProjectID is not required. A mount read back from a backend listing has no
// project attached to it, and refusing to describe what the backend actually
// has would make reconciliation unable to say what it found.
func (m MountSpec) Validate() error {
	if strings.TrimSpace(m.HostPath) == "" {
		return fmt.Errorf("a mount needs a host directory")
	}
	if !filepath.IsAbs(m.HostPath) {
		return fmt.Errorf("host path %q must be absolute", m.HostPath)
	}
	if filepath.Clean(m.HostPath) != m.HostPath {
		return fmt.Errorf("host path %q is not normalized (want %q)", m.HostPath, filepath.Clean(m.HostPath))
	}
	if strings.TrimSpace(m.GuestPath) == "" {
		return fmt.Errorf("mount of %s needs a guest directory", m.HostPath)
	}
	if !path.IsAbs(m.GuestPath) {
		return fmt.Errorf("guest path %q for %s must be an absolute Linux path", m.GuestPath, m.HostPath)
	}
	if path.Clean(m.GuestPath) != m.GuestPath {
		return fmt.Errorf("guest path %q for %s is not normalized (want %q)", m.GuestPath, m.HostPath, path.Clean(m.GuestPath))
	}
	return nil
}

// CleanMount returns the spec with both paths in canonical form, so that two
// spellings of one mapping become one value.
func CleanMount(m MountSpec) MountSpec {
	if m.HostPath != "" {
		m.HostPath = filepath.Clean(m.HostPath)
	}
	if m.GuestPath != "" {
		m.GuestPath = path.Clean(m.GuestPath)
	}
	return m
}

// NormalizeMounts returns the canonical form of a desired mount set: every spec
// cleaned and validated, exact duplicates collapsed, ordered by host path and
// then guest path. An empty input returns nil, so that "shares nothing" is one
// value rather than two.
//
// It also refuses the two ways a set can be self-contradictory, and both
// refusals are confinement rules rather than tidiness:
//
//   - One host directory mapped to two different guest paths would make "where
//     is my project inside Linux" have two answers, and PROP-1 have none.
//   - Two host directories mapped onto one guest path would hide one of them
//     behind the other, so the set of paths avar configured would no longer
//     describe what the guest can actually reach (PROP-5, PROP-14).
//
// Callers therefore compare and store normalized sets, which is what makes
// "the applied set already equals the desired set" a decidable question and
// with it the promise that returning to a known project is instant (REQ-17.1).
func NormalizeMounts(in []MountSpec) ([]MountSpec, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]MountSpec, 0, len(in))
	byHost := make(map[string]MountSpec, len(in))
	byGuest := make(map[string]MountSpec, len(in))

	for _, m := range in {
		m = CleanMount(m)
		if err := m.Validate(); err != nil {
			return nil, err
		}
		if seen, ok := byHost[m.HostPath]; ok {
			if seen != m {
				return nil, fmt.Errorf("%s is shared twice with different mappings (%s and %s)", m.HostPath, seen, m)
			}
			continue
		}
		if seen, ok := byGuest[m.GuestPath]; ok {
			return nil, fmt.Errorf("%s and %s both claim the guest path %s", seen.HostPath, m.HostPath, m.GuestPath)
		}
		byHost[m.HostPath] = m
		byGuest[m.GuestPath] = m
		out = append(out, m)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].HostPath != out[j].HostPath {
			return out[i].HostPath < out[j].HostPath
		}
		return out[i].GuestPath < out[j].GuestPath
	})
	return out, nil
}

// EqualMounts reports whether two mount sets describe the same sharing. Both
// are expected to be normalized, which is what makes this a plain comparison
// rather than a set operation.
func EqualMounts(a, b []MountSpec) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// MountHostPaths reports just the host directories of a mount set, in order.
// It exists for the places that have to talk about "which projects are shared"
// without caring where they land in the guest — a status line, an error naming
// the directory the user recognises.
func MountHostPaths(in []MountSpec) []string {
	out := make([]string, 0, len(in))
	for _, m := range in {
		out = append(out, m.HostPath)
	}
	return out
}

// EqualMappings reports whether two mount sets describe the same sharing,
// ignoring which project each share serves.
//
// It exists because the two sets being compared can never agree on that field.
// A desired set is built from project registrations and carries a ProjectID on
// every entry; an applied set is read back from the backend, which knows which
// directories it shares and never which project a directory belongs to, so its
// entries carry none. Comparing them with EqualMounts therefore reports
// "different" for two sets that describe the identical sharing — which, for a
// caller checking whether its change landed, means a correct change looks like
// a failed one.
//
// What is compared is the mapping: which host directory appears where, and
// whether the guest may write to it. That is the whole of what a backend applies
// and the whole of what PROP-5 asserts. Both sets are expected to be normalized.
func EqualMappings(a, b []MountSpec) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].HostPath != b[i].HostPath || a[i].GuestPath != b[i].GuestPath || a[i].Writable != b[i].Writable {
			return false
		}
	}
	return true
}
