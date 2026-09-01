package wsl2

import (
	"fmt"
	"strings"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// Where a WSL environment's root filesystem comes from.
//
// The Lima backend pins an image URL and a checksum per (distro, version, arch)
// and downloads it itself. This backend does not, and the difference is not
// laziness: WSL ships a distribution registry, `wsl --install <name>` fetches
// and verifies from it, and `--name`/`--location`/`--no-launch` make that
// mechanism produce a distribution avar owns outright — avar's name, in avar's
// directory, never launched into a first-run setup wizard. Pinning URLs beside
// that would mean avar hosting a second, worse copy of a supply chain Microsoft
// already runs, and being the one to get its checksums wrong.
//
// What avar gives up is the ability to pin an exact release, because the
// registry names some entries by version ("Ubuntu-24.04", "FedoraLinux-43") and
// some by track ("Debian", which is whatever Debian stable is today). That is
// bought back after provisioning rather than before: the guest's own
// /etc/os-release is read and checked against the selector, so a registry entry
// that has moved on produces an honest refusal naming both versions instead of
// an environment that quietly is not what the user asked for. See
// verifyOSRelease.

// registryEntry is how one avar selector maps onto WSL's distribution registry.
type registryEntry struct {
	// Distro is the name `wsl --install` takes, as `wsl --list --online`
	// reports it.
	Distro string
	// OSReleaseID is what the guest's /etc/os-release must report as ID.
	OSReleaseID string
	// OSReleaseVersion is what it must report as VERSION_ID. It is checked by
	// prefix, because a distribution may report "24.04.1" where avar's matrix
	// says "24.04" — a point release of the release avar asked for is the
	// release avar asked for.
	OSReleaseVersion string
}

// registry is the mapping from avar's environment matrix onto WSL's.
//
// It is keyed by (distro, version) and not by architecture, because WSL has no
// architecture to choose: `wsl --install` fetches the build matching the
// Windows host, and there is no emulation to ask for. Architecture is refused
// earlier, in checkSupported, rather than represented here as an option that
// does not exist.
var registry = map[types.Distro]map[string]registryEntry{
	types.DistroUbuntu: {
		"24.04": {Distro: "Ubuntu-24.04", OSReleaseID: "ubuntu", OSReleaseVersion: "24.04"},
	},
	types.DistroDebian: {
		// The registry has no versioned Debian entry: "Debian" is Debian
		// stable, which is 13 (trixie) today and will be 14 one day without
		// the name changing. verifyOSRelease is what keeps that honest.
		"13": {Distro: "Debian", OSReleaseID: "debian", OSReleaseVersion: "13"},
	},
	types.DistroFedora: {
		"43": {Distro: "FedoraLinux-43", OSReleaseID: "fedora", OSReleaseVersion: "43"},
	},
}

// lookupRegistry reports how a selector is installed, or an error naming what
// this backend can offer.
//
// An environment in avar's matrix that this backend cannot serve is
// ErrUnsupportedCapability rather than a plain error: it is a real difference
// between backends — the same `avr --distro fedora` works on macOS — and the
// command layer says so rather than reporting a failure (design §3.6).
func lookupRegistry(sel types.EnvironmentSelector) (registryEntry, error) {
	versions, ok := registry[sel.Distro]
	if !ok {
		return registryEntry{}, fmt.Errorf("%w: WSL cannot run %s; on Windows avar offers %s",
			provider.ErrUnsupportedCapability, sel.Distro, strings.Join(supportedDistroNames(), ", "))
	}
	entry, ok := versions[sel.Version]
	if !ok {
		return registryEntry{}, fmt.Errorf("%w: WSL cannot run %s %s; on Windows avar offers %s %s",
			provider.ErrUnsupportedCapability, sel.Distro, sel.Version, sel.Distro, strings.Join(supportedVersions(sel.Distro), ", "))
	}
	return entry, nil
}

// supportedDistroNames lists the distributions this backend serves, in avar's
// own matrix order so that two error messages never disagree about it.
func supportedDistroNames() []string {
	out := make([]string, 0, len(registry))
	for _, distro := range []types.Distro{types.DistroUbuntu, types.DistroDebian, types.DistroFedora} {
		if _, ok := registry[distro]; ok {
			out = append(out, string(distro))
		}
	}
	return out
}

// supportedVersions lists the releases this backend serves for a distribution.
func supportedVersions(d types.Distro) []string {
	out := make([]string, 0, 2)
	for version := range registry[d] {
		out = append(out, version)
	}
	return out
}

// checkSupported refuses an environment this backend cannot create, before
// anything is downloaded or imported (REQ-18.6).
//
// The architecture rule is the whole of what makes this backend narrower than
// the Lima one. Lima runs a foreign architecture under emulation, slowly but
// really; WSL 2 has no equivalent — a distribution runs on the host's own
// processor and there is nothing to emulate it with. `avr --arch amd64` on an
// Arm64 Windows machine is therefore not a slow request, it is an impossible
// one, and saying so before a gigabyte is downloaded is the difference between
// an error and a wasted ten minutes.
func (p *Provider) checkSupported(sel types.EnvironmentSelector) error {
	if sel.Arch != "" && sel.Arch != p.hostArch {
		return fmt.Errorf("%w: WSL runs Linux on the Windows machine's own processor and cannot emulate another, so avar cannot give you a %s environment on a %s host; the supported architecture here is %s",
			provider.ErrUnsupportedCapability, sel.Arch, p.hostArch, p.hostArch)
	}
	_, err := lookupRegistry(sel)
	return err
}
