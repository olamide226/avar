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
// The reader is strict where avar's global config.toml readers are lenient, and
// deliberately so. A typo in the user's own global file must not stop a shell.
// A project file travels with a repository, and misreading it applies an
// environment its author did not write, so anything it cannot read exactly is
// refused with the line and the reason.
package projconfig

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/olamide226/avar/internal/types"
)

// FileName is the project configuration file avar reads from a project's
// directory.
const FileName = ".avr.toml"

// maxFileSize bounds what Load will read. The schema fits in a few hundred
// bytes; anything this large is not a configuration file avar should parse.
const maxFileSize = 64 << 10

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

	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read the project configuration %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return Config{}, fmt.Errorf("read the project configuration %s: %w", path, err)
	}
	if info.IsDir() {
		return Config{}, fmt.Errorf("read the project configuration %s: it is a directory; avar expects a file there, or nothing", path)
	}

	body, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return Config{}, fmt.Errorf("read the project configuration %s: %w", path, err)
	}
	if len(body) > maxFileSize {
		return Config{}, fmt.Errorf("read the project configuration %s: it is larger than %d KiB, which no .avr.toml needs to be", path, maxFileSize>>10)
	}
	return Parse(path, body)
}

// key is one setting the schema knows.
type key struct {
	name  string
	apply func(c *Config, v value) error
}

// schema is the whole of what .avr.toml may contain, in the order messages list
// it. The schema is closed: a key that is not here is refused, because a
// misspelt key that was quietly ignored would leave a team's environments
// silently different.
var schema = []key{
	{name: "distro", apply: applyDistro},
	{name: "arch", apply: applyArch},
	{name: "cpus", apply: applyCPUs},
	{name: "memory", apply: applyMemory},
	{name: "packages", apply: applyPackages},
	{name: "forward_env", apply: applyForwardEnv},
}

// knownKeys renders the schema's key names for an error message.
func knownKeys() string {
	names := make([]string, 0, len(schema))
	for _, k := range schema {
		names = append(names, k.name)
	}
	return strings.Join(names, ", ")
}

func lookupKey(name string) (key, bool) {
	for _, k := range schema {
		if k.name == name {
			return k, true
		}
	}
	return key{}, false
}

// Parse reads the contents of a .avr.toml. path is used only in messages and
// recorded as Config.Path.
//
// The accepted language is a strict subset of TOML, chosen so that every file
// Parse accepts means the same to any conforming TOML parser: comments, bare
// keys, quoted strings with no escape sequences, unsigned decimal integers, and
// arrays of strings on one line or several. Every other construct TOML allows
// is refused with the line it is on.
func Parse(path string, body []byte) (Config, error) {
	if !utf8.Valid(body) {
		return Config{}, fmt.Errorf("%s: not valid UTF-8, which TOML requires", path)
	}
	cfg := Config{Path: path}
	seen := map[string]int{}
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")

	for i := 0; i < len(lines); i++ {
		line := i + 1
		fail := func(at int, format string, args ...any) error {
			return fmt.Errorf("%s line %d: %s", path, at, fmt.Sprintf(format, args...))
		}

		text := strings.TrimSpace(lines[i])
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if strings.HasPrefix(text, "[") {
			return Config{}, fail(line, "tables are not supported: every setting in %s is a top-level key (%s)", FileName, knownKeys())
		}

		name, rest, ok := strings.Cut(text, "=")
		if !ok {
			return Config{}, fail(line, "expected key = value, got %q", text)
		}
		name = strings.TrimSpace(name)
		if err := checkKeyName(name); err != nil {
			return Config{}, fail(line, "%v", err)
		}
		k, known := lookupKey(name)
		if !known {
			return Config{}, fail(line, "unknown key %q: %s understands %s (a key from a newer avar needs a newer avr)", name, FileName, knownKeys())
		}
		if first, dup := seen[name]; dup {
			return Config{}, fail(line, "%q is already set on line %d; a key may appear only once", name, first)
		}
		seen[name] = line

		v, consumed, err := parseValue(rest, lines[i+1:])
		if err != nil {
			return Config{}, fail(line+consumed, "%s: %v", name, err)
		}
		if err := k.apply(&cfg, v); err != nil {
			return Config{}, fail(line, "%s: %v", name, err)
		}
		switch name {
		case "cpus":
			cfg.CPUsLine = line
		case "memory":
			cfg.MemoryLine = line
		}
		i += consumed
	}

	if len(cfg.Packages) > 0 && cfg.Distro == "" {
		return Config{}, lineError(path, seen["packages"], `packages needs distro: package names belong to one distribution, so name it, for example distro = "ubuntu"`)
	}
	return cfg, nil
}

func lineError(path string, line int, msg string) error {
	return fmt.Errorf("%s line %d: %s", path, line, msg)
}

// bareKey is TOML's bare-key alphabet.
var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// checkKeyName refuses the key forms TOML has and this reader does not, so that
// they fail as what they are rather than as an unknown key.
func checkKeyName(name string) error {
	switch {
	case name == "":
		return errors.New("a setting needs a key before the =")
	case strings.ContainsAny(name, `"'`):
		return fmt.Errorf("quoted keys are not supported: write the key bare, one of %s", knownKeys())
	case strings.Contains(name, "."):
		return fmt.Errorf("dotted keys are not supported: every setting is a top-level key, one of %s", knownKeys())
	case !bareKey.MatchString(name):
		return fmt.Errorf("%q is not a key: keys are letters, digits, - and _", name)
	}
	return nil
}

// valueKind is the TOML type of a parsed value.
type valueKind int

const (
	kindString valueKind = iota
	kindInteger
	kindStrings
)

// value is one parsed right-hand side.
type value struct {
	kind    valueKind
	str     string
	integer int
	strs    []string
}

// parseValue reads the text after "=": one value, then nothing but an optional
// comment. An array may continue onto the following lines; consumed reports how
// many of them it used, and on an error, which one the error is on.
func parseValue(text string, following []string) (v value, consumed int, err error) {
	text = strings.TrimSpace(text)
	if text == "" || text[0] == '#' {
		return value{}, 0, errors.New("no value after =")
	}

	var rest string
	switch c := text[0]; {
	case c == '"' || c == '\'':
		v.kind = kindString
		v.str, rest, err = parseString(text)
	case c == '[':
		v.kind = kindStrings
		v.strs, rest, consumed, err = parseArray(text, following)
	case c >= '0' && c <= '9':
		v.kind = kindInteger
		v.integer, rest, err = parseInteger(text)
	default:
		return value{}, 0, fmt.Errorf("%s is not a value this file accepts: write a quoted string, a whole number, or a list of quoted strings", describe(text))
	}
	if err != nil {
		return value{}, consumed, err
	}

	rest = strings.TrimSpace(rest)
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return value{}, consumed, fmt.Errorf("unexpected %q after the value", rest)
	}
	return v, consumed, nil
}

// parseString reads a basic ("...") or literal ('...') string at the start of
// text and returns it with whatever follows.
//
// Escape sequences are refused rather than interpreted: nothing in the schema
// needs one, and refusing them keeps this reader's meaning identical to TOML's
// without implementing TOML's escape rules. A literal string has no escapes in
// TOML either, so a backslash in one is refused only in a basic string.
func parseString(text string) (string, string, error) {
	quote := text[0]
	if strings.HasPrefix(text, strings.Repeat(string(quote), 3)) {
		return "", "", errors.New("multi-line strings are not supported")
	}
	end := strings.IndexByte(text[1:], quote)
	if end < 0 {
		return "", "", fmt.Errorf("string %s is not closed", text)
	}
	s := text[1 : 1+end]
	if quote == '"' && strings.Contains(s, `\`) {
		return "", "", errors.New(`escape sequences are not supported; use a literal string ('...') if a value really needs a backslash`)
	}
	for _, r := range s {
		if (r < 0x20 && r != '\t') || r == 0x7f {
			return "", "", errors.New("control characters are not allowed in a string")
		}
	}
	return s, text[2+end:], nil
}

// parseArray reads an array of strings starting at text, continuing onto the
// following lines until it closes. Comments and a trailing comma are allowed,
// as TOML allows them; any element that is not a quoted string is refused.
func parseArray(text string, following []string) (items []string, rest string, consumed int, err error) {
	cur := text[1:]
	wantComma := false
	for {
		cur = strings.TrimLeft(cur, " \t")
		switch {
		case cur == "" || cur[0] == '#':
			if consumed == len(following) {
				return nil, "", consumed, errors.New("the list is not closed with ]")
			}
			cur = following[consumed]
			consumed++
		case cur[0] == ']':
			return items, cur[1:], consumed, nil
		case wantComma && cur[0] == ',':
			cur = cur[1:]
			wantComma = false
		case wantComma:
			return nil, "", consumed, fmt.Errorf("expected , or ] in the list, found %q", cur)
		case cur[0] == '"' || cur[0] == '\'':
			var s string
			s, cur, err = parseString(cur)
			if err != nil {
				return nil, "", consumed, err
			}
			items = append(items, s)
			wantComma = true
		default:
			return nil, "", consumed, fmt.Errorf("lists in %s hold only quoted strings, and this one has %s", FileName, describe(cur))
		}
	}
}

// decimal is an unsigned TOML integer with no underscores or leading zeros.
var decimal = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// parseInteger reads an unsigned decimal integer at the start of text.
func parseInteger(text string) (int, string, error) {
	end := strings.IndexAny(text, " \t#")
	if end < 0 {
		end = len(text)
	}
	token := text[:end]
	if !decimal.MatchString(token) {
		return 0, "", fmt.Errorf("%q is not a number this file accepts: write digits only, such as 4, with no underscores, leading zeros, decimals or dates", token)
	}
	n, err := strconv.Atoi(token)
	if err != nil {
		return 0, "", fmt.Errorf("%s is too large", token)
	}
	return n, text[end:], nil
}

// describe names the TOML construct a value looks like, so a refusal says what
// was written rather than only what was expected.
func describe(text string) string {
	switch {
	case text[0] == '{':
		return "an inline table"
	case text[0] == '[':
		return "a nested list"
	case strings.HasPrefix(text, "true") || strings.HasPrefix(text, "false"):
		return "a boolean"
	case strings.ContainsAny(text[:1], "+-0123456789") || strings.HasPrefix(text, "inf") || strings.HasPrefix(text, "nan"):
		return "a signed number"
	default:
		return fmt.Sprintf("%q", text)
	}
}

// wantKind refuses a value of the wrong type for a key, saying what it takes.
func wantKind(v value, kind valueKind, example string) error {
	if v.kind == kind {
		return nil
	}
	return fmt.Errorf("takes %s", example)
}

// applyDistro reads distro = "name" or "name:version", the --distro syntax.
// Whether avar supports the name and version is the resolver's question, where
// it is answered the same way for the file as for the flag.
func applyDistro(c *Config, v value) error {
	if err := wantKind(v, kindString, `a quoted name, such as "ubuntu" or "ubuntu:24.04"`); err != nil {
		return err
	}
	name, version, hasVersion := strings.Cut(strings.TrimSpace(v.str), ":")
	name, version = strings.TrimSpace(name), strings.TrimSpace(version)
	if name == "" {
		return errors.New(`names no distribution: write one such as "ubuntu", optionally with a version as "ubuntu:24.04"`)
	}
	if hasVersion && version == "" {
		return fmt.Errorf("%q names no version after the colon: drop the colon to use %s's pinned version", v.str, name)
	}
	c.Distro, c.Version = types.Distro(name), version
	return nil
}

// applyArch reads arch = "arm64" or "amd64". As with distro, support is checked
// where the flag's is.
func applyArch(c *Config, v value) error {
	if err := wantKind(v, kindString, `a quoted architecture, "arm64" or "amd64"`); err != nil {
		return err
	}
	arch := strings.TrimSpace(v.str)
	if arch == "" {
		return errors.New(`names no architecture: write "arm64" or "amd64"`)
	}
	c.Arch = types.Arch(arch)
	return nil
}

// applyCPUs reads cpus = N.
func applyCPUs(c *Config, v value) error {
	if err := wantKind(v, kindInteger, "a whole number, such as cpus = 4"); err != nil {
		return err
	}
	if v.integer < 1 {
		return errors.New("must be at least 1")
	}
	c.CPUs = v.integer
	return nil
}

// memorySize is a whole number of gibibytes or mebibytes. Only the binary units
// are accepted: "8GB" is ambiguous between two sizes, and a configuration file
// is the wrong place to guess which one was meant.
var memorySize = regexp.MustCompile(`^([1-9][0-9]*)(GiB|MiB)$`)

// applyMemory reads memory = "8GiB" or "512MiB".
func applyMemory(c *Config, v value) error {
	const example = `a quoted size in GiB or MiB, such as memory = "8GiB"`
	if err := wantKind(v, kindString, example); err != nil {
		return err
	}
	m := memorySize.FindStringSubmatch(strings.TrimSpace(v.str))
	if m == nil {
		return fmt.Errorf("%q is not a size avar reads: write %s", v.str, example)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n > 1<<20 {
		return fmt.Errorf("%q is too large", v.str)
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
func applyPackages(c *Config, v value) error {
	if err := wantKind(v, kindStrings, `a list of quoted package names, such as packages = ["ripgrep", "jq"]`); err != nil {
		return err
	}
	names, err := distinct(v.strs, "package", func(name string) error {
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

// variableName is a portable environment variable name.
var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// applyForwardEnv reads forward_env = ["NAME", ...].
func applyForwardEnv(c *Config, v value) error {
	if err := wantKind(v, kindStrings, `a list of quoted variable names, such as forward_env = ["GITHUB_TOKEN"]`); err != nil {
		return err
	}
	names, err := distinct(v.strs, "variable", func(name string) error {
		if !variableName.MatchString(name) {
			return fmt.Errorf("%q is not a variable name: names are letters, digits and _, and do not start with a digit", name)
		}
		return nil
	})
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
