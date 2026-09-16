package projconfig

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/olamide226/avar/internal/types"
)

// Runtime is a language toolchain avar init knows how to propose packages for.
type Runtime string

const (
	RuntimeNode   Runtime = "Node.js"
	RuntimePython Runtime = "Python"
	RuntimeGo     Runtime = "Go"
	RuntimeRust   Runtime = "Rust"
	RuntimeRuby   Runtime = "Ruby"
)

// Finding is one thing a manifest says about the project.
type Finding struct {
	// Manifest is the file the finding came from, as named in the project
	// directory.
	Manifest string

	// Stack names what was found for a person: a runtime's name, "Docker
	// Compose", or the name of a tool avar init has no packages for.
	Stack string

	// Detail is what the manifest states beyond the stack's presence, such as
	// a pinned version or a base image. Empty when it states nothing more.
	Detail string

	// Runtime is set when the finding is a toolchain avar init can propose
	// packages for.
	Runtime Runtime

	// Pinned reports that the manifest pins a version the distribution's own
	// package may not match.
	Pinned bool

	// Distro and Arch are set only from a Dockerfile base image avar can map
	// onto one of its own environments.
	Distro types.Distro
	Arch   types.Arch
}

// manifests are the files avar init reads (REQ-15.2), in the order findings
// are reported. Each is read from the project directory alone: nothing is
// searched for, and no subdirectory is walked.
var manifests = []struct {
	name   string
	detect func(body []byte) []Finding
}{
	{"package.json", detectPackageJSON},
	{"pyproject.toml", detectPyproject},
	{"go.mod", detectGoMod},
	{"Cargo.toml", detectCargo},
	{".tool-versions", detectToolVersions},
	{"mise.toml", detectMise},
	{"Dockerfile", detectDockerfile},
	{"docker-compose.yml", detectCompose},
	{"docker-compose.yaml", detectCompose},
}

// maxManifestSize bounds what detection reads from any one manifest. Detection
// looks for a handful of declarations near the top of small files; a
// multi-megabyte lockfile mistakenly named like a manifest is not worth reading.
const maxManifestSize = 1 << 20

// Detect reads the manifests avar init understands from projectDir and reports
// what they say. It reads those files and nothing else.
//
// Detection is shallow on purpose. It proposes; the user confirms (REQ-15.3),
// so a manifest it cannot read fully costs a less complete proposal, never a
// wrong environment.
func Detect(projectDir string) ([]Finding, error) {
	var out []Finding
	for _, m := range manifests {
		path := filepath.Join(projectDir, m.name)
		info, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > maxManifestSize {
			out = append(out, Finding{Manifest: m.name, Stack: "unread", Detail: "too large to inspect"})
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		for _, f := range m.detect(body) {
			f.Manifest = m.name
			out = append(out, f)
		}
	}
	return out, nil
}

func detectPackageJSON(body []byte) []Finding {
	f := Finding{Stack: string(RuntimeNode), Runtime: RuntimeNode}
	var manifest struct {
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		f.Detail = "not valid JSON, so no version was read"
		return []Finding{f}
	}
	if node := strings.TrimSpace(manifest.Engines.Node); node != "" {
		f.Detail, f.Pinned = "engines.node "+node, true
	}
	return []Finding{f}
}

func detectPyproject(body []byte) []Finding {
	f := Finding{Stack: string(RuntimePython), Runtime: RuntimePython}
	if v := tomlLineValue(body, "requires-python"); v != "" {
		f.Detail, f.Pinned = "requires-python "+v, true
	}
	return []Finding{f}
}

func detectGoMod(body []byte) []Finding {
	f := Finding{Stack: string(RuntimeGo), Runtime: RuntimeGo}
	for _, line := range lines(body) {
		if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "go" {
			f.Detail, f.Pinned = "go "+fields[1], true
			break
		}
	}
	return []Finding{f}
}

func detectCargo(body []byte) []Finding {
	f := Finding{Stack: string(RuntimeRust), Runtime: RuntimeRust}
	if v := tomlLineValue(body, "rust-version"); v != "" {
		f.Detail, f.Pinned = "rust-version "+v, true
	}
	return []Finding{f}
}

// detectToolVersions reads asdf's .tool-versions: one "tool version..." per
// line, with # comments.
func detectToolVersions(body []byte) []Finding {
	var out []Finding
	for _, line := range lines(body) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		out = append(out, toolFinding(fields[0], strings.Join(fields[1:], " ")))
	}
	return out
}

// detectMise reads the [tools] table of mise.toml, whose entries are
// `tool = "version"` or `tool = ["version", ...]`.
func detectMise(body []byte) []Finding {
	var out []Finding
	inTools := false
	for _, line := range lines(body) {
		if strings.HasPrefix(line, "[") {
			inTools = strings.TrimSpace(strings.Trim(line, "[]")) == "tools"
			continue
		}
		if !inTools {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		name = strings.Trim(strings.TrimSpace(name), `"'`)
		if name == "" {
			continue
		}
		var versions []string
		for _, v := range strings.Split(strings.Trim(strings.TrimSpace(value), "[]"), ",") {
			if v = strings.Trim(strings.TrimSpace(v), `"'`); v != "" {
				versions = append(versions, v)
			}
		}
		out = append(out, toolFinding(name, strings.Join(versions, " ")))
	}
	return out
}

// toolNames maps the names version managers use for a tool onto the runtimes
// avar init can propose packages for.
var toolNames = map[string]Runtime{
	"node":   RuntimeNode,
	"nodejs": RuntimeNode,
	"python": RuntimePython,
	"go":     RuntimeGo,
	"golang": RuntimeGo,
	"rust":   RuntimeRust,
	"ruby":   RuntimeRuby,
}

func toolFinding(tool, version string) Finding {
	f := Finding{Detail: strings.TrimSpace(tool + " " + version)}
	if runtime, ok := toolNames[strings.ToLower(tool)]; ok {
		f.Stack, f.Runtime, f.Pinned = string(runtime), runtime, version != ""
		return f
	}
	f.Stack = tool
	return f
}

// detectDockerfile reads the final stage's base image, which is what the
// project's software actually runs on, and maps it onto one of avar's
// environments where it can.
func detectDockerfile(body []byte) []Finding {
	stages := map[string]string{} // alias → image, so FROM builder follows the chain
	var platform, image string
	for _, line := range lines(body) {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "FROM") {
			continue
		}
		fields = fields[1:]
		platform = ""
		for len(fields) > 0 && strings.HasPrefix(fields[0], "--") {
			if value, ok := strings.CutPrefix(fields[0], "--platform="); ok {
				platform = value
			}
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		image = fields[0]
		if resolved, ok := stages[strings.ToLower(image)]; ok {
			image = resolved
		}
		if len(fields) >= 3 && strings.EqualFold(fields[1], "AS") {
			stages[strings.ToLower(fields[2])] = image
		}
	}
	if image == "" {
		return nil
	}

	f := Finding{Stack: "Dockerfile", Detail: "FROM " + image}
	repo, tag := splitImage(image)
	switch {
	case strings.Contains(image, "$"):
		f.Detail += " (a build argument, so no base could be read)"
	case repo == "ubuntu" || repo == "debian" || repo == "fedora":
		f.Distro = types.Distro(repo)
	case slices.Contains([]string{"node", "python", "golang", "rust", "ruby"}, repo):
		f.Runtime = toolNames[repo]
		f.Stack = string(f.Runtime)
		// The official language images are built on Debian unless their tag
		// names another base.
		if !strings.Contains(tag, "alpine") && !strings.Contains(tag, "windowsservercore") {
			f.Distro = types.DistroDebian
			f.Detail += " (Debian-based)"
		}
	}
	switch platform {
	case "linux/amd64":
		f.Arch = types.ArchAMD64
	case "linux/arm64", "linux/arm64/v8":
		f.Arch = types.ArchARM64
	}
	return []Finding{f}
}

// splitImage returns an image reference's repository name, without registry,
// namespace or library prefix, and its tag.
func splitImage(image string) (repo, tag string) {
	image, _, _ = strings.Cut(image, "@")
	name := image
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		name, tag = image[:i], image[i+1:]
	}
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return strings.ToLower(name), strings.ToLower(tag)
}

func detectCompose([]byte) []Finding {
	return []Finding{{Stack: "Docker Compose"}}
}

// lines returns a file's lines trimmed, without blank lines or # comments.
func lines(body []byte) []string {
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	scanner.Buffer(make([]byte, 0, 64*1024), maxManifestSize)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// tomlLineValue finds `key = "value"` on a line of its own and returns the
// value unquoted. It is detection, not parsing: a manifest it cannot read this
// way yields no detail rather than an error.
func tomlLineValue(body []byte, key string) string {
	for _, line := range lines(body) {
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		value, _, _ = strings.Cut(strings.TrimSpace(value), "#")
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}

// Proposal is what avar init offers to write, and why.
type Proposal struct {
	// Config is the file avar init proposes. Only distro, arch and packages
	// are ever proposed: no manifest states a size, and a credential grant is
	// exactly what detection must never propose (REQ-15.3).
	Config Config

	// Findings are what the manifests said, in the order they were found.
	Findings []Finding

	// Notes explain what the proposal does not do that a reader of the
	// manifests might expect it to.
	Notes []string
}

// Choice is what the user asked for on avar init's command line, which wins
// over anything a manifest suggests.
type Choice struct {
	Distro  types.Distro
	Version string
	Arch    types.Arch
}

// runtimePackages are each runtime's packages in each distribution family.
// Every name was checked against the distribution's own archive for the
// release avar pins (Ubuntu 24.04, Debian 13, Fedora 43).
var runtimePackages = map[Runtime]struct{ apt, dnf []string }{
	RuntimeNode:   {apt: []string{"nodejs", "npm"}, dnf: []string{"nodejs", "nodejs-npm"}},
	RuntimePython: {apt: []string{"python3", "python3-pip", "python3-venv"}, dnf: []string{"python3", "python3-pip"}},
	RuntimeGo:     {apt: []string{"golang-go"}, dnf: []string{"golang"}},
	RuntimeRust:   {apt: []string{"cargo", "rustc"}, dnf: []string{"cargo", "rust"}},
	RuntimeRuby:   {apt: []string{"ruby-full"}, dnf: []string{"ruby"}},
}

// Propose turns findings into a proposal. choice is what the user typed, and
// fallback is the distribution avar uses when neither the user nor a
// Dockerfile names one.
func Propose(findings []Finding, choice Choice, fallback types.Distro) Proposal {
	p := Proposal{Findings: findings}
	if len(findings) == 0 {
		return p
	}

	distro, version, arch := choice.Distro, choice.Version, choice.Arch
	for _, f := range findings {
		if distro == "" && f.Distro != "" {
			distro = f.Distro
		}
		if arch == "" && f.Arch != "" {
			arch = f.Arch
		}
	}
	if distro == "" {
		distro = fallback
	}
	p.Config = Config{Distro: distro, Version: version, Arch: arch}

	var pinned, unmapped []string
	for _, f := range findings {
		if f.Manifest == "Dockerfile" && f.Distro == "" && choice.Distro == "" {
			p.Notes = append(p.Notes, fmt.Sprintf("The Dockerfile's base image (%s) is not one avar can match to a distribution it runs, so the proposal uses %s.",
				strings.TrimPrefix(strings.SplitN(f.Detail, " (", 2)[0], "FROM "), distro))
		}
		switch {
		case f.Runtime != "":
			p.Config.Packages = appendMissing(p.Config.Packages, packagesFor(f.Runtime, distro))
			if f.Pinned {
				pinned = append(pinned, fmt.Sprintf("%s (%s)", f.Detail, f.Manifest))
			}
		case f.Stack == "Docker Compose":
			p.Notes = append(p.Notes, "Docker Compose was found. avr init does not propose a container engine; install one inside Linux if the project needs it.")
		case f.Stack != "Dockerfile" && f.Stack != "unread":
			unmapped = append(unmapped, fmt.Sprintf("%s (%s)", f.Detail, f.Manifest))
		}
	}
	if len(pinned) > 0 {
		p.Notes = append(p.Notes, fmt.Sprintf("The packages are %s's own versions, which may not match the versions pinned by %s. Use a version manager inside Linux for an exact version.",
			distro, strings.Join(pinned, ", ")))
	}
	if len(unmapped) > 0 {
		p.Notes = append(p.Notes, fmt.Sprintf("avr init has no packages to propose for %s.", strings.Join(unmapped, ", ")))
	}
	return p
}

// packagesFor is a runtime's packages in distribution d.
func packagesFor(r Runtime, d types.Distro) []string {
	if d == types.DistroFedora {
		return runtimePackages[r].dnf
	}
	return runtimePackages[r].apt
}

// appendMissing adds to list the names it does not already hold, in order.
func appendMissing(list, names []string) []string {
	for _, name := range names {
		if !slices.Contains(list, name) {
			list = append(list, name)
		}
	}
	return list
}
