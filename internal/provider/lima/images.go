package lima

import (
	"fmt"
	"sort"
	"strings"

	"github.com/olamide226/avar/internal/types"
)

// Every image avar can provision is pinned to an immutable, dated publisher
// URL and to that image's published digest. Nothing here is a floating "latest"
// pointer, and nothing is downloaded without a digest for Lima to check it
// against: an image is the entire trusted computing base of the environment the
// user is about to run their source code in.
//
// The cost of pinning is that publishers eventually prune dated directories, at
// which point provisioning fails loudly with the download error rather than
// silently fetching something unverified. Refreshing a pin is a single-point
// change in this file:
//
//  1. read the publisher's checksum manifest for the new build —
//     cloud-images.ubuntu.com/releases/<codename>/release-<date>/SHA256SUMS,
//     cloud.debian.org/images/cloud/<codename>/<serial>/SHA512SUMS, or
//     Fedora's per-image CHECKSUM file;
//  2. update location and digest together, never one without the other;
//  3. run `go test ./internal/provider/lima/...` — the golden files record the
//     change as a reviewable diff.
//
// The digest algorithm is whatever the publisher signs: Ubuntu and Fedora
// publish SHA-256, Debian publishes SHA-512. Lima accepts either, so avar
// carries the publisher's own value rather than re-hashing and inviting a
// transcription error.

// imageKey identifies one pinned image by the environment it provides.
type imageKey struct {
	distro  types.Distro
	version string
	arch    types.Arch
}

// image is a pinned cloud image: an immutable location and the digest Lima
// verifies the download against.
type image struct {
	// location is the URL the image is downloaded from.
	location string
	// digest is the publisher's digest, in Lima's "<algo>:<hex>" form.
	digest string
}

// images maps each supported environment onto its pinned image.
//
// Verified 2026-07-30: every location returned HTTP 200, and every digest
// matches the publisher's own checksum manifest.
var images = map[imageKey]image{
	// Ubuntu 24.04 LTS (noble) — avar's default environment (REQ-1.2).
	{types.DistroUbuntu, "24.04", types.ArchARM64}: {
		location: "https://cloud-images.ubuntu.com/releases/noble/release-20260705/ubuntu-24.04-server-cloudimg-arm64.img",
		digest:   "sha256:7df0201546f75b8bcc1044594c806c35749421ad3c9bc1be2a3ab806cfae39cc",
	},
	{types.DistroUbuntu, "24.04", types.ArchAMD64}: {
		location: "https://cloud-images.ubuntu.com/releases/noble/release-20260705/ubuntu-24.04-server-cloudimg-amd64.img",
		digest:   "sha256:ffe6203da54deeb6db5d2a98a83f9ec8e55f149d3f7ba622e1abe5fa966ee3d6",
	},

	// Ubuntu 22.04 LTS (jammy).
	{types.DistroUbuntu, "22.04", types.ArchARM64}: {
		location: "https://cloud-images.ubuntu.com/releases/jammy/release-20260705/ubuntu-22.04-server-cloudimg-arm64.img",
		digest:   "sha256:9c2ddc65079f9a285ca7c9efabdee6d58ff7dd2b08a130114426d2020506db7a",
	},
	{types.DistroUbuntu, "22.04", types.ArchAMD64}: {
		location: "https://cloud-images.ubuntu.com/releases/jammy/release-20260705/ubuntu-22.04-server-cloudimg-amd64.img",
		digest:   "sha256:ec3cdc1bf496078f645ccc8ac823e17609658753477ebc4e5fb730729ac5b434",
	},

	// Debian 12 (bookworm). The genericcloud variant is the one built for
	// virtual machines rather than bare metal.
	{types.DistroDebian, "12", types.ArchARM64}: {
		location: "https://cloud.debian.org/images/cloud/bookworm/20260712-2537/debian-12-genericcloud-arm64-20260712-2537.qcow2",
		digest:   "sha512:52eb678130e85a9bf9f3cf4f7181e060fa60a5db45373717474c5ff9305645c5e6cab8b05ca042bcfa7921d2ca29cf9fd2e815f75abe2d168557ad7207af127b",
	},
	{types.DistroDebian, "12", types.ArchAMD64}: {
		location: "https://cloud.debian.org/images/cloud/bookworm/20260712-2537/debian-12-genericcloud-amd64-20260712-2537.qcow2",
		digest:   "sha512:6c2607f1846ee86040830c87d0b723f0967da3e884ea4673d9db4aa8eee13a4b7c663524bfa42082c16fc6919f3aa1bf425c004d07ff06c53a319ad0c42647bb",
	},

	// Debian 13 (trixie).
	{types.DistroDebian, "13", types.ArchARM64}: {
		location: "https://cloud.debian.org/images/cloud/trixie/20260712-2537/debian-13-genericcloud-arm64-20260712-2537.qcow2",
		digest:   "sha512:8543d795f2fde630eb66c492f245a8c1da19dedc636e0a8e7b3d0f95920e1a05aa911ef2d82d177d41cc53ced5fccbd2a3945d07fa5e15018914c4d864bb07ed",
	},
	{types.DistroDebian, "13", types.ArchAMD64}: {
		location: "https://cloud.debian.org/images/cloud/trixie/20260712-2537/debian-13-genericcloud-amd64-20260712-2537.qcow2",
		digest:   "sha512:7ae53e9dbee282bfc16f289dec483dde3a8598769c38a267948310f7a2a52c662620198603bc52c142627efba379863d16079698a10b34102d55bcedd40e8d32",
	},

	// Fedora 42.
	{types.DistroFedora, "42", types.ArchARM64}: {
		location: "https://download.fedoraproject.org/pub/fedora/linux/releases/42/Cloud/aarch64/images/Fedora-Cloud-Base-Generic-42-1.1.aarch64.qcow2",
		digest:   "sha256:e10658419a8d50231037dc781c3155aa94180a8c7a74e5cac2a6b09eaa9342b7",
	},
	{types.DistroFedora, "42", types.ArchAMD64}: {
		location: "https://download.fedoraproject.org/pub/fedora/linux/releases/42/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-42-1.1.x86_64.qcow2",
		digest:   "sha256:e401a4db2e5e04d1967b6729774faa96da629bcf3ba90b67d8d9cce9906bec0f",
	},

	// Fedora 43.
	{types.DistroFedora, "43", types.ArchARM64}: {
		location: "https://download.fedoraproject.org/pub/fedora/linux/releases/43/Cloud/aarch64/images/Fedora-Cloud-Base-Generic-43-1.6.aarch64.qcow2",
		digest:   "sha256:66031aea9ec61e6d0d5bba12b9454e80ca94e8a79c913d37ded4c60311705b8b",
	},
	{types.DistroFedora, "43", types.ArchAMD64}: {
		location: "https://download.fedoraproject.org/pub/fedora/linux/releases/43/Cloud/x86_64/images/Fedora-Cloud-Base-Generic-43-1.6.x86_64.qcow2",
		digest:   "sha256:846574c8a97cd2d8dc1f231062d73107cc85cbbbda56335e264a46e3a6c8ab2f",
	},
}

// imageFor resolves the pinned image for a selector.
//
// An unsupported (distro, version) pair is an error naming what is supported,
// because the alternative — falling back to some other version — would hand the
// user a different operating system than the one they asked for (REQ-4.4).
func imageFor(selector types.EnvironmentSelector) (image, error) {
	img, ok := images[imageKey{selector.Distro, selector.Version, selector.Arch}]
	if !ok {
		return image{}, fmt.Errorf("no image is pinned for %s %s on %s: avar can provision %s",
			selector.Distro, selector.Version, selector.Arch, strings.Join(SupportedEnvironments(), ", "))
	}
	return img, nil
}

// SupportedEnvironments lists the distro:version pairs avar has pinned images
// for, sorted, so the resolver's supported matrix and the command layer's error
// messages can be checked against what the backend can actually provision.
func SupportedEnvironments() []string {
	seen := make(map[string]struct{}, len(images))
	out := make([]string, 0, len(images))
	for key := range images {
		label := fmt.Sprintf("%s:%s", key.distro, key.version)
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}
