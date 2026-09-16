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
}

// Present reports whether the configuration came from a file.
func (c Config) Present() bool { return c.Path != "" }

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
// keys, and quoted strings with no escape sequences. Every other construct
// TOML allows is refused with the line it is on.
func Parse(path string, body []byte) (Config, error) {
	if !utf8.Valid(body) {
		return Config{}, fmt.Errorf("%s: not valid UTF-8, which TOML requires", path)
	}
	cfg := Config{Path: path}
	seen := map[string]int{}

	for i, raw := range strings.Split(string(body), "\n") {
		line := i + 1
		fail := func(format string, args ...any) error {
			return fmt.Errorf("%s line %d: %s", path, line, fmt.Sprintf(format, args...))
		}

		text := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if strings.HasPrefix(text, "[") {
			return Config{}, fail("tables are not supported: every setting in %s is a top-level key (%s)", FileName, knownKeys())
		}

		name, rest, ok := strings.Cut(text, "=")
		if !ok {
			return Config{}, fail("expected key = value, got %q", text)
		}
		name = strings.TrimSpace(name)
		if err := checkKeyName(name); err != nil {
			return Config{}, fail("%v", err)
		}
		k, known := lookupKey(name)
		if !known {
			return Config{}, fail("unknown key %q: %s understands %s (a key from a newer avar needs a newer avr)", name, FileName, knownKeys())
		}
		if first, dup := seen[name]; dup {
			return Config{}, fail("%q is already set on line %d; a key may appear only once", name, first)
		}
		seen[name] = line

		v, err := parseValue(rest)
		if err != nil {
			return Config{}, fail("%s: %v", name, err)
		}
		if err := k.apply(&cfg, v); err != nil {
			return Config{}, fail("%s: %v", name, err)
		}
	}
	return cfg, nil
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

// value is one parsed right-hand side.
type value struct {
	str string
}

// parseValue reads the text after "=": one value, then nothing but an optional
// comment.
func parseValue(text string) (value, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return value{}, errors.New("no value after =")
	}

	var (
		v    value
		rest string
		err  error
	)
	switch text[0] {
	case '"', '\'':
		v.str, rest, err = parseString(text)
	default:
		return value{}, fmt.Errorf("%s is not a value this file accepts: write a quoted string, such as \"ubuntu\"", describe(text))
	}
	if err != nil {
		return value{}, err
	}

	rest = strings.TrimSpace(rest)
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return value{}, fmt.Errorf("unexpected %q after the value", rest)
	}
	return v, nil
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

// describe names the TOML construct a value looks like, so a refusal says what
// was written rather than only what was expected.
func describe(text string) string {
	switch {
	case text[0] == '{':
		return "an inline table"
	case text[0] == '[':
		return "an array"
	case strings.HasPrefix(text, "true") || strings.HasPrefix(text, "false"):
		return "a boolean"
	case strings.ContainsAny(text[:1], "+-0123456789") || strings.HasPrefix(text, "inf") || strings.HasPrefix(text, "nan"):
		return "a number or date"
	default:
		return fmt.Sprintf("%q", text)
	}
}

// applyDistro reads distro = "name" or "name:version", the --distro syntax.
// Whether avar supports the name and version is the resolver's question, where
// it is answered the same way for the file as for the flag.
func applyDistro(c *Config, v value) error {
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
	arch := strings.TrimSpace(v.str)
	if arch == "" {
		return errors.New(`names no architecture: write "arm64" or "amd64"`)
	}
	c.Arch = types.Arch(arch)
	return nil
}
