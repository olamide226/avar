package resolve

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/olamide226/avar/internal/types"
)

// ErrUnsupportedEnvironment is wrapped by every error that reports an
// environment avar cannot run. The command layer distinguishes "avar could not
// understand you" from "the operation failed" by matching it, so that an
// unsupported --distro or --arch exits with the usage status and prints the
// supported values (REQ-4.4).
var ErrUnsupportedEnvironment = errors.New("unsupported environment")

// DefaultDistro is the distribution a user who names none gets (REQ-1.2). Its
// version is that distribution's pinned release, so the default environment is
// stated in exactly one place: the matrix below.
const DefaultDistro = types.DistroUbuntu

// release is one supported version of one distribution.
type release struct {
	version string

	// arches are the architectures the release runs on, in the order they are
	// offered to the user. Support is recorded per release rather than per
	// distribution because it genuinely varies: a distribution can drop an
	// architecture between releases, and avar must then reject the pair rather
	// than promise something that cannot boot.
	arches []types.Arch
}

// distroSupport is everything avar knows about one distribution.
type distroSupport struct {
	name types.Distro

	// pinned is the release a bare `--distro <name>` resolves to (REQ-4.2).
	// Bumping a distribution's default is this one field: nothing else in avar
	// spells a version out.
	pinned string

	// releases are the supported versions, newest first.
	releases []release
}

// bothArches is the common case: a release that exists for both architectures
// avar supports. It is shared, so every accessor returns a copy.
var bothArches = []types.Arch{types.ArchARM64, types.ArchAMD64}

// supported is the distro/architecture matrix: which (distro, version, arch)
// combinations avar offers, and which release each distribution defaults to.
// It is the single source of truth for that question, so a version bump or a
// new release is a change here and nowhere else.
//
// What this file deliberately does not contain: image URLs, checksums,
// virtualization modes, or any other backend detail. The matrix says which
// environments *exist*; turning one of them into something bootable is the
// provider's job (design §3.5). Keeping that line clean is what lets a second
// backend map the same matrix onto entirely different artifacts (REQ-17.3).
//
// The pinned releases:
//
//   - ubuntu 24.04 — the current LTS and avar's documented default (REQ-1.2).
//     An LTS is the right default for a shared, long-lived machine: security
//     updates for five years and the widest third-party package coverage.
//   - debian 13 (trixie) — Debian stable. Debian users pick Debian for stable,
//     so tracking stable rather than testing is the expected behaviour.
//   - fedora 43 — the newest Fedora release inside its supported window.
//     Fedora users pick Fedora for currency, so the default is deliberately
//     the recent release rather than the conservative one.
//
// Each distribution currently offers exactly its pinned release. That is not a
// limitation of this structure — a release is one row — but a promise avar can
// keep today: every combination listed here has to be mappable to a real image
// by the provider, so widening the matrix means widening the provider's image
// table in the same change.
var supported = []distroSupport{
	{
		name:     types.DistroUbuntu,
		pinned:   "24.04",
		releases: []release{{version: "24.04", arches: bothArches}},
	},
	{
		name:     types.DistroDebian,
		pinned:   "13",
		releases: []release{{version: "13", arches: bothArches}},
	},
	{
		name:     types.DistroFedora,
		pinned:   "43",
		releases: []release{{version: "43", arches: bothArches}},
	},
}

// isolatedNameToken is the token that marks a per-project machine name, and is
// therefore reserved: no distribution may be called this, or a project's
// isolated machine could collide with a shared one (PROP-2).
const isolatedNameToken = "prj"

// SupportedDistros returns the distributions avar can run, in the order they
// are offered to the user.
func SupportedDistros() []types.Distro {
	out := make([]types.Distro, 0, len(supported))
	for _, ds := range supported {
		out = append(out, ds.name)
	}
	return out
}

// SupportedVersions returns the releases avar supports for a distribution,
// newest first. An unknown distribution has none.
func SupportedVersions(d types.Distro) []string {
	ds, ok := lookupDistro(d)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(ds.releases))
	for _, r := range ds.releases {
		out = append(out, r.version)
	}
	return out
}

// PinnedVersion returns the release a bare `--distro <name>` resolves to
// (REQ-4.2), and reports whether the distribution is supported at all.
func PinnedVersion(d types.Distro) (string, bool) {
	ds, ok := lookupDistro(d)
	if !ok {
		return "", false
	}
	return ds.pinned, true
}

// SupportedArches returns the architectures a given release runs on, in the
// order they are offered. An unsupported combination has none.
func SupportedArches(d types.Distro, version string) []types.Arch {
	ds, ok := lookupDistro(d)
	if !ok {
		return nil
	}
	r, ok := ds.release(version)
	if !ok {
		return nil
	}
	return slices.Clone(r.arches)
}

// SupportedEnvironments enumerates every (distro, version, arch) combination
// avar supports, ordered by distribution, then release, then architecture, so
// that callers and tests see a stable list.
//
// Isolated is false in every entry: isolation is a per-project choice about
// which machine an environment is served by, not a property of the matrix.
func SupportedEnvironments() []types.EnvironmentSelector {
	out := make([]types.EnvironmentSelector, 0, len(supported)*2)
	for _, ds := range supported {
		for _, r := range ds.releases {
			for _, arch := range r.arches {
				out = append(out, types.EnvironmentSelector{
					Distro:  ds.name,
					Version: r.version,
					Arch:    arch,
				})
			}
		}
	}
	return out
}

// checkSupported reports whether a fully specified environment is one avar can
// run, and if it is not, says what is supported instead (REQ-4.4).
//
// This is the fuller check the grammar cannot do: internal/cli already rejects
// a distribution *name* that can never resolve, so what is left — and what only
// the matrix knows — is whether a particular release exists and whether it
// exists for the requested architecture.
func checkSupported(d types.Distro, version string, arch types.Arch) error {
	ds, ok := lookupDistro(d)
	if !ok {
		return fmt.Errorf("avar cannot run %q: %w; supported distributions are %s",
			d, ErrUnsupportedEnvironment, joinDistros(SupportedDistros()))
	}

	r, ok := ds.release(version)
	if !ok {
		return fmt.Errorf("avar cannot run %s %s: %w; supported %s versions are %s (omit the version to use %s)",
			d, version, ErrUnsupportedEnvironment, d, strings.Join(SupportedVersions(d), ", "), ds.pinned)
	}

	if !slices.Contains(r.arches, arch) {
		return fmt.Errorf("avar cannot run %s %s on %s: %w; %s %s is available for %s",
			d, version, arch, ErrUnsupportedEnvironment, d, version, joinArches(r.arches))
	}
	return nil
}

func lookupDistro(d types.Distro) (distroSupport, bool) {
	for _, ds := range supported {
		if ds.name == d {
			return ds, true
		}
	}
	return distroSupport{}, false
}

func (ds distroSupport) release(version string) (release, bool) {
	for _, r := range ds.releases {
		if r.version == version {
			return r, true
		}
	}
	return release{}, false
}

func joinDistros(in []types.Distro) string {
	out := make([]string, 0, len(in))
	for _, d := range in {
		out = append(out, string(d))
	}
	return strings.Join(out, ", ")
}

func joinArches(in []types.Arch) string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		out = append(out, string(a))
	}
	return strings.Join(out, ", ")
}
