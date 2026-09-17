// Package projconfig reads a project's optional .avr.toml (design §3.11).
//
// The file lets a team write down the environment a project wants. It is never
// required: a project without one behaves exactly as it did before the file
// existed, and Load reports its absence as a zero Config rather than an error.
//
// The package does no I/O beyond reading the one file it is asked to read. It
// prints nothing and decides nothing about machines; the resolver consumes the
// selection a Config carries, and the command layer everything else.
//
// The file is read by internal/tomlsubset, the strict reader avar's global
// config.toml shares, and anything it cannot read exactly is refused with the
// line and the reason: a project file travels with a repository, and misreading
// it applies an environment its author did not write. This package owns only
// what each key means.
package projconfig

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/olamide226/avar/internal/tomlsubset"
	"github.com/olamide226/avar/internal/types"
)

// FileName is the project configuration file avar reads from a project's
// directory.
const FileName = ".avr.toml"

// Config is what a project's .avr.toml asks for. A zero field means the file
// has no opinion, and the zero Config is a project with no file at all.
type Config struct {
	// Path is the file the configuration was read from, for messages that
	// must say where a setting came from. Empty when there is no file.
	Path string

	// Distro and Version select the distribution, exactly as --distro does:
	// Version is empty unless the file named one after a colon.
	Distro  types.Distro
	Version string

	// Arch selects the guest architecture, exactly as --arch does.
	Arch types.Arch

	// CPUs and MemoryMiB size the project's isolated environment when it is
	// created, and nothing else (design §3.11). Zero means not set.
	CPUs      int
	MemoryMiB int

	// CPUsLine and MemoryLine are the lines of Path that set CPUs and
	// MemoryMiB, zero when unset, so that a size refused after parsing can
	// still say where the file asked for it.
	CPUsLine   int
	MemoryLine int

	// Packages are names in Distro's own repositories, installed only after
	// the user approves them. A file that lists packages always names Distro.
	Packages []string

	// ForwardEnv names host variables the project asks to forward into its
	// sessions, each only after the user approves it.
	ForwardEnv []string
}

// Present reports whether the configuration came from a file.
func (c Config) Present() bool { return c.Path != "" }

// PackagesApplyTo reports whether the file's packages are meant for an
// environment of distribution d. Package names belong to one distribution, so
// packages listed for Ubuntu are never offered to, or installed in, Fedora.
func (c Config) PackagesApplyTo(d types.Distro) bool {
	return len(c.Packages) > 0 && strings.EqualFold(strings.TrimSpace(string(c.Distro)), string(d))
}

// MemoryGB is MemoryMiB in the gibibytes provider.MachineSpec takes.
func (c Config) MemoryGB() float64 { return float64(c.MemoryMiB) / 1024 }

// A HostExcess is one size the file asks for that is larger than the computer
// it would be created on.
type HostExcess struct {
	// Key is the setting, "cpus" or "memory".
	Key string
	// Line is the line of the file that sets it.
	Line int
	// Setting is the setting as the file expresses it, e.g. memory = "64GiB".
	Setting string
}

// ExceedsHost reports each size the file asks for that host does not have: cpus
// above its logical CPUs, or memory above its physical memory. A size equal to
// the host's is not an excess, and neither is a setting the file leaves out.
// The result lists cpus before memory, as the schema does, and is empty when
// everything fits.
func (c Config) ExceedsHost(host types.HostCapacity) []HostExcess {
	var out []HostExcess
	if c.CPUs > host.CPUs {
		out = append(out, HostExcess{Key: "cpus", Line: c.CPUsLine, Setting: fmt.Sprintf("cpus = %d", c.CPUs)})
	}
	if int64(c.MemoryMiB)<<20 > host.MemoryBytes {
		out = append(out, HostExcess{Key: "memory", Line: c.MemoryLine, Setting: fmt.Sprintf("memory = %q", formatMemory(c.MemoryMiB))})
	}
	return out
}

// ResourceDeclaration renders what the file asks for in cpus and memory as a
// stable string, or "" when it asks for neither. It identifies a declaration,
// so that advice about one is given once and given again when it changes.
func (c Config) ResourceDeclaration() string {
	var parts []string
	if c.CPUs > 0 {
		parts = append(parts, fmt.Sprintf("cpus=%d", c.CPUs))
	}
	if c.MemoryMiB > 0 {
		parts = append(parts, "memory="+formatMemory(c.MemoryMiB))
	}
	return strings.Join(parts, " ")
}

// Load reads the .avr.toml in projectDir. A directory without one yields the
// zero Config and no error, because the absence of the file is the ordinary
// case and must change nothing (REQ-15.4).
func Load(projectDir string) (Config, error) {
	path := filepath.Join(projectDir, FileName)
	body, found, err := tomlsubset.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read the project configuration %s: %w", path, err)
	}
	if !found {
		return Config{}, nil
	}
	return Parse(path, body)
}

// schema is the whole of what .avr.toml may contain, in the order messages list
// it. The schema is closed: a key that is not here is refused, because a
// misspelt key that was quietly ignored would leave a team's environments
// silently different.
var schema = tomlsubset.Schema{
	File: FileName,
	Keys: []tomlsubset.Key{
		{Name: "distro", Kind: tomlsubset.String},
		{Name: "arch", Kind: tomlsubset.String},
		{Name: "cpus", Kind: tomlsubset.Integer},
		{Name: "memory", Kind: tomlsubset.String},
		{Name: "packages", Kind: tomlsubset.StringList},
		{Name: "forward_env", Kind: tomlsubset.StringList},
	},
}

// appliers gives each key in schema its meaning.
var appliers = map[string]func(c *Config, v tomlsubset.Value) error{
	"distro":      applyDistro,
	"arch":        applyArch,
	"cpus":        applyCPUs,
	"memory":      applyMemory,
	"packages":    applyPackages,
	"forward_env": applyForwardEnv,
}

// Parse reads the contents of a .avr.toml. path is used only in messages and
// recorded as Config.Path.
//
// The accepted language is tomlsubset's strict subset of TOML, so every file
// Parse accepts means the same to any conforming TOML parser, and every other
// construct TOML allows is refused with the line it is on.
func Parse(path string, body []byte) (Config, error) {
	cfg := Config{Path: path}
	packagesLine := 0
	err := tomlsubset.Parse(path, body, schema, func(s tomlsubset.Setting) error {
		if err := appliers[s.Key](&cfg, s.Value); err != nil {
			return err
		}
		switch s.Key {
		case "cpus":
			cfg.CPUsLine = s.Line
		case "memory":
			cfg.MemoryLine = s.Line
		case "packages":
			packagesLine = s.Line
		}
		return nil
	})
	if err != nil {
		return Config{}, err
	}

	if len(cfg.Packages) > 0 && cfg.Distro == "" {
		return Config{}, tomlsubset.LineError(path, packagesLine, `packages needs distro: package names belong to one distribution, so name it, for example distro = "ubuntu"`)
	}
	return cfg, nil
}

// applyDistro reads distro = "name" or "name:version", the --distro syntax.
// Whether avar supports the name and version is the resolver's question, where
// it is answered the same way for the file as for the flag.
func applyDistro(c *Config, v tomlsubset.Value) error {
	if err := v.Expect(tomlsubset.String, `a quoted name, such as "ubuntu" or "ubuntu:24.04"`); err != nil {
		return err
	}
	name, version, hasVersion := strings.Cut(strings.TrimSpace(v.Str), ":")
	name, version = strings.TrimSpace(name), strings.TrimSpace(version)
	if name == "" {
		return errors.New(`names no distribution: write one such as "ubuntu", optionally with a version as "ubuntu:24.04"`)
	}
	if hasVersion && version == "" {
		return fmt.Errorf("%q names no version after the colon: drop the colon to use %s's pinned version", v.Str, name)
	}
	c.Distro, c.Version = types.Distro(name), version
	return nil
}

// applyArch reads arch = "arm64" or "amd64". As with distro, support is checked
// where the flag's is.
func applyArch(c *Config, v tomlsubset.Value) error {
	if err := v.Expect(tomlsubset.String, `a quoted architecture, "arm64" or "amd64"`); err != nil {
		return err
	}
	arch := strings.TrimSpace(v.Str)
	if arch == "" {
		return errors.New(`names no architecture: write "arm64" or "amd64"`)
	}
	c.Arch = types.Arch(arch)
	return nil
}

// applyCPUs reads cpus = N.
func applyCPUs(c *Config, v tomlsubset.Value) error {
	if err := v.Expect(tomlsubset.Integer, "a whole number, such as cpus = 4"); err != nil {
		return err
	}
	if v.Int < 1 {
		return errors.New("must be at least 1")
	}
	c.CPUs = v.Int
	return nil
}

// memorySize is a whole number of gibibytes or mebibytes. Only the binary units
// are accepted: "8GB" is ambiguous between two sizes, and a configuration file
// is the wrong place to guess which one was meant.
var memorySize = regexp.MustCompile(`^([1-9][0-9]*)(GiB|MiB)$`)

// applyMemory reads memory = "8GiB" or "512MiB".
func applyMemory(c *Config, v tomlsubset.Value) error {
	const example = `a quoted size in GiB or MiB, such as memory = "8GiB"`
	if err := v.Expect(tomlsubset.String, example); err != nil {
		return err
	}
	m := memorySize.FindStringSubmatch(strings.TrimSpace(v.Str))
	if m == nil {
		return fmt.Errorf("%q is not a size avar reads: write %s", v.Str, example)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n > 1<<20 {
		return fmt.Errorf("%q is too large", v.Str)
	}
	if m[2] == "GiB" {
		n *= 1024
	}
	c.MemoryMiB = n
	return nil
}

// formatMemory renders MiB in the unit a person would have written.
func formatMemory(mib int) string {
	if mib%1024 == 0 {
		return strconv.Itoa(mib/1024) + "GiB"
	}
	return strconv.Itoa(mib) + "MiB"
}

// packageName is what a package name may look like. It is narrower than any one
// package manager's rules on purpose, because the name reaches a package
// manager running as root: no leading "-", so it can never be read as an
// option; no "/", so it can never name a file in the project or a URL; no "=",
// ":" or "*", so it can never pin a version, an architecture, or a pattern.
// Debian, Ubuntu and Fedora package names all fit.
var packageName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._-]*$`)

// ValidPackageName reports whether name is a package name avar will pass to a
// package manager.
func ValidPackageName(name string) bool { return packageName.MatchString(name) }

// applyPackages reads packages = ["name", ...].
func applyPackages(c *Config, v tomlsubset.Value) error {
	if err := v.Expect(tomlsubset.StringList, `a list of quoted package names, such as packages = ["ripgrep", "jq"]`); err != nil {
		return err
	}
	names, err := distinct(v.Strs, "package", func(name string) error {
		if !ValidPackageName(name) {
			return fmt.Errorf("%q is not a package name avar will install: names start with a letter or digit and hold only letters, digits and + . _ -", name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	c.Packages = names
	return nil
}

// applyForwardEnv reads forward_env = ["NAME", ...].
func applyForwardEnv(c *Config, v tomlsubset.Value) error {
	if err := v.Expect(tomlsubset.StringList, `a list of quoted variable names, such as forward_env = ["GITHUB_TOKEN"]`); err != nil {
		return err
	}
	names, err := distinct(v.Strs, "variable", types.CheckVariableName)
	if err != nil {
		return err
	}
	c.ForwardEnv = names
	return nil
}

// distinct validates each name and refuses a name listed twice.
func distinct(names []string, what string, check func(string) error) ([]string, error) {
	seen := make(map[string]bool, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		if err := check(name); err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, fmt.Errorf("%s %q is listed twice", what, name)
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
