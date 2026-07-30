package lima

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"text/template"

	"github.com/olamide226/avar/internal/deps"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// The instance configuration is generated from a text template rather than
// marshalled from a struct, for two reasons. The comments explaining why the
// guest gets no host keys and no proxy variables belong in the file the user can
// read at ~/.lima/<machine>/lima.yaml, and a YAML marshaller would drop them.
// And avar's dependency set is the standard library plus cobra and x/term
// (CLAUDE.md): text/template does this job without adding a YAML library.
//
// Everything interpolated goes through the q function, which emits a JSON string
// literal. YAML 1.2 is a superset of JSON, so a directory name containing a
// quote, a colon, a newline, or a leading @ cannot break out of its scalar and
// become YAML structure.

//go:embed templates/instance.yaml.tmpl
var templateFS embed.FS

// instanceTemplate is parsed once: a malformed template is a programming error,
// and TestInstanceTemplate_Parses catches it before it can ship.
var instanceTemplate = template.Must(
	template.New("instance.yaml.tmpl").
		Funcs(template.FuncMap{"q": q}).
		ParseFS(templateFS, "templates/instance.yaml.tmpl"),
)

// Lima's vocabulary for the two virtualization stacks and the two file-sharing
// mechanisms avar uses.
const (
	vmTypeVZ   = "vz"
	vmTypeQEMU = "qemu"

	mountTypeVirtioFS = "virtiofs"
	// mountTypeReverseSSHFS is Lima's portable mount type. QEMU has no
	// VirtioFS on macOS, so an emulated guest shares files over SSHFS.
	mountTypeReverseSSHFS = "reverse-sshfs"
)

// Lima's architecture names, which are the kernel's rather than Go's.
const (
	limaArchAARCH64 = "aarch64"
	limaArchX8664   = "x86_64"
)

// instanceConfig is the data the template renders. It is the whole of avar's
// decision about how a machine is built, in one inspectable value, so that a
// golden-file test compares a decision rather than a side effect.
type instanceConfig struct {
	MinimumLimaVersion string
	Arch               string
	VMType             string
	// VMComment explains the vmType choice in the generated file, since the
	// user may well open it and wonder why one machine is vz and another qemu.
	VMComment     string
	ImageLocation string
	ImageDigest   string
	CPUs          int
	Memory        string
	Disk          string
	MountType     string
	// Mounts are absolute host directories, shared at the identical guest path.
	Mounts []string
	// Rosetta enables Apple's x86_64 translation inside a native arm64 guest.
	Rosetta bool
}

// limaArch translates avar's architecture vocabulary into Lima's.
func limaArch(arch types.Arch) (string, error) {
	switch arch {
	case types.ArchARM64:
		return limaArchAARCH64, nil
	case types.ArchAMD64:
		return limaArchX8664, nil
	default:
		return "", fmt.Errorf("unsupported architecture %q: supported values are %s, %s", arch, types.ArchARM64, types.ArchAMD64)
	}
}

// newInstanceConfig decides how the machine spec asks for is built.
//
// The one branch that matters is native versus foreign architecture. A guest of
// the host's own architecture runs on Apple's Virtualization framework, which
// brings VirtioFS file sharing and near-native speed, and — on Apple silicon —
// Rosetta, so an x86_64 Linux binary still runs inside the arm64 guest without
// emulating a second machine. A guest of a foreign architecture has to be a
// fully emulated QEMU machine, which is correct but slow, and which is what the
// REQ-4.6 warning is about.
func newInstanceConfig(spec provider.MachineSpec, host HostResources) (instanceConfig, error) {
	arch, err := limaArch(spec.Selector.Arch)
	if err != nil {
		return instanceConfig{}, err
	}
	img, err := imageFor(spec.Selector)
	if err != nil {
		return instanceConfig{}, err
	}
	mounts, err := normalizeMounts(spec.Mounts)
	if err != nil {
		return instanceConfig{}, err
	}

	cpus, memoryGB, diskGB := resources(spec, host)
	cfg := instanceConfig{
		MinimumLimaVersion: deps.MinLimaVersion,
		Arch:               arch,
		ImageLocation:      img.location,
		ImageDigest:        img.digest,
		CPUs:               cpus,
		Memory:             formatGiB(memoryGB),
		Disk:               formatGiB(diskGB),
		Mounts:             mounts,
	}

	if spec.Selector.Arch == host.arch() {
		cfg.VMType = vmTypeVZ
		cfg.MountType = mountTypeVirtioFS
		cfg.VMComment = "Native architecture: Apple's Virtualization framework, with VirtioFS file sharing."
		// Rosetta translates x86_64 into arm64 and exists only on Apple
		// silicon; on an Intel host the guest is already x86_64.
		cfg.Rosetta = spec.Selector.Arch == types.ArchARM64
	} else {
		cfg.VMType = vmTypeQEMU
		cfg.MountType = mountTypeReverseSSHFS
		cfg.VMComment = fmt.Sprintf("Foreign architecture: a fully emulated QEMU machine, since the host is %s. Expect it to be markedly slower than a native environment.", host.arch())
	}
	return cfg, nil
}

// renderInstanceConfig produces the complete Lima instance configuration for a
// machine spec.
func renderInstanceConfig(spec provider.MachineSpec, host HostResources) ([]byte, error) {
	cfg, err := newInstanceConfig(spec, host)
	if err != nil {
		return nil, fmt.Errorf("preparing the configuration for machine %s: %w", spec.Name, err)
	}
	var buf bytes.Buffer
	if err := instanceTemplate.Execute(&buf, cfg); err != nil {
		return nil, fmt.Errorf("generating the configuration for machine %s: %w", spec.Name, err)
	}
	return buf.Bytes(), nil
}

// q renders a string as a JSON literal, which is also a YAML double-quoted
// scalar. It is the only way a value reaches the generated configuration.
func q(s string) (string, error) {
	encoded, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("quoting %q for the Lima configuration: %w", s, err)
	}
	return string(encoded), nil
}
