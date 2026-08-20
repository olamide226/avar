//go:build unix

// See shell_test.go: LimaProvider is a macOS backend, so its behaviour tests
// run where it runs.

package lima

import (
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/types"
)

func TestImages_EveryPinnedImageIsImmutableAndVerified(t *testing.T) {
	// An image is the entire trusted computing base of the environment the
	// user is about to run their source code in. A floating "latest" pointer,
	// or a location with no digest, would mean avar boots whatever a mirror
	// happens to be serving.
	for key, img := range images {
		name := string(key.distro) + ":" + key.version + "/" + string(key.arch)
		t.Run(name, func(t *testing.T) {
			if !strings.HasPrefix(img.location, "https://") {
				t.Errorf("image is fetched over %q, which is not authenticated transport", img.location)
			}
			if img.digest == "" {
				t.Fatal("image has no digest: Lima would download it without verifying anything")
			}
			algo, hex, ok := strings.Cut(img.digest, ":")
			if !ok {
				t.Fatalf("digest %q is not in Lima's <algorithm>:<hex> form", img.digest)
			}
			switch algo {
			case "sha256":
				if len(hex) != 64 {
					t.Errorf("sha256 digest has %d hex characters, want 64", len(hex))
				}
			case "sha512":
				if len(hex) != 128 {
					t.Errorf("sha512 digest has %d hex characters, want 128", len(hex))
				}
			default:
				t.Errorf("digest algorithm %q is not one avar pins with", algo)
			}
			// A dated or serial-numbered directory is what makes the location
			// immutable; "latest" and the undated "release" directory move.
			for _, floating := range []string{"/release/", "/latest/", "/daily/"} {
				if strings.Contains(img.location, floating) {
					t.Errorf("location %q is a moving pointer, so the digest cannot stay true", img.location)
				}
			}
		})
	}
}

func TestImages_EverySupportedEnvironmentCoversBothArchitectures(t *testing.T) {
	// A selector avar accepts on one architecture but not the other would fail
	// only for the user who happens to ask for the other one.
	for key := range images {
		for _, arch := range []types.Arch{types.ArchARM64, types.ArchAMD64} {
			if _, ok := images[imageKey{key.distro, key.version, arch}]; !ok {
				t.Errorf("%s %s has no pinned image for %s", key.distro, key.version, arch)
			}
		}
	}
}

func TestImageFor_UnsupportedEnvironmentListsWhatIsSupported_REQ_4_4(t *testing.T) {
	_, err := imageFor(types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "18.04", Arch: types.ArchARM64})
	if err == nil {
		t.Fatal("an environment avar has no pinned image for was accepted")
	}
	for _, want := range SupportedEnvironments() {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not offer %q as an alternative: %v", want, err)
		}
	}
}

func TestSupportedEnvironments_IsSortedAndDeduplicated(t *testing.T) {
	got := SupportedEnvironments()
	if len(got) == 0 {
		t.Fatal("avar can provision nothing")
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Errorf("SupportedEnvironments is not sorted and unique: %v", got)
			break
		}
	}
	// The default environment REQ-1.2 names has to be one of them.
	var hasDefault bool
	for _, env := range got {
		if env == "ubuntu:24.04" {
			hasDefault = true
		}
	}
	if !hasDefault {
		t.Errorf("the default environment ubuntu:24.04 is not provisionable: %v", got)
	}
}

func TestLimaArch_TranslatesAvarsVocabularyIntoLimas(t *testing.T) {
	cases := map[types.Arch]string{types.ArchARM64: "aarch64", types.ArchAMD64: "x86_64"}
	for arch, want := range cases {
		got, err := limaArch(arch)
		if err != nil {
			t.Fatalf("limaArch(%q): %v", arch, err)
		}
		if got != want {
			t.Errorf("limaArch(%q) = %q, want %q", arch, got, want)
		}
	}
	if _, err := limaArch(types.Arch("riscv64")); err == nil {
		t.Error("an architecture avar does not support was translated anyway")
	}
}
