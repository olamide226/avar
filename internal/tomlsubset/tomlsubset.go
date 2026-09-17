// Package tomlsubset reads the strict subset of TOML that avar's configuration
// files are written in: a project's .avr.toml and the user's global config.toml
// (design §3.3, §3.11).
//
// It is the lexical layer only. It knows what a comment, a key, a string, an
// integer and a list of strings look like, and that a file's keys form a closed
// set. What a key means, and which values it accepts, belongs to the package
// that owns the file, which receives each setting through a callback and
// refuses it with its own example. The package does no I/O beyond reading the
// one file it is asked to read, prints nothing, and imports no avar package.
//
// The subset is chosen so that every file it accepts means the same to any
// conforming TOML parser: full-line and trailing # comments; bare keys; basic
// strings without escape sequences, and literal strings; decimal integers
// without sign, underscores or leading zeros; and arrays of strings on one line
// or several, with comments and a trailing comma allowed. Every other construct
// TOML allows is refused with the line it is on.
package tomlsubset

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
)

// MaxFileSize bounds what ReadFile will read. Neither schema needs more than a
// few hundred bytes; anything this large is not a configuration file avar
// should parse.
const MaxFileSize = 64 << 10

// ReadFile reads the configuration file at path. A file that does not exist is
// not an error: found is false, because every configuration file avar reads is
// optional. The errors it returns do not name the path, so the caller can say
// which file it was reading and why.
func ReadFile(path string) (body []byte, found bool, err error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if info.IsDir() {
		return nil, false, errors.New("it is a directory; avar expects a file there, or nothing")
	}

	body, err = io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, false, err
	}
	if len(body) > MaxFileSize {
		return nil, false, fmt.Errorf("it is larger than %d KiB, which no %s needs to be", MaxFileSize>>10, filepath.Base(path))
	}
	return body, true, nil
}

// Kind is the TOML type of a value.
type Kind int

const (
	// String is a basic ("...") or literal ('...') string.
	String Kind = iota
	// Integer is an unsigned decimal integer.
	Integer
	// StringList is an array whose every element is a string.
	StringList
)

// Key is one top-level setting a file may contain.
type Key struct {
	Name string

	// Kind is the type the key takes. The reader does not enforce it: a value
	// of another kind is the owning schema's to refuse, with its own example.
	// It is used only when the value is not TOML at all, to say what it
	// should have been.
	Kind Kind
}

// Schema is the closed set of keys one file may contain.
type Schema struct {
	// File is the file's name as messages say it, such as ".avr.toml".
	File string

	// Keys lists every key the file may contain, in the order messages list
	// them. A key that is not here is refused: a misspelt key quietly ignored
	// would leave the user believing a setting applied that never did.
	Keys []Key

	// Unsupported explains keys that are refused for a reason more specific
	// than being unknown, such as a setting that belongs in the other file. The
	// explanation replaces the unknown-key message and starts with the key.
	Unsupported map[string]string
}

// KnownKeys renders the schema's key names for a message.
func (s Schema) KnownKeys() string {
	names := make([]string, 0, len(s.Keys))
	for _, k := range s.Keys {
		names = append(names, k.Name)
	}
	return strings.Join(names, ", ")
}

// Value is one parsed right-hand side. Only the field for its Kind is set.
type Value struct {
	Kind Kind
	Str  string
	Int  int
	Strs []string
}

// Expect refuses a value that is not of kind, saying what the key takes, such
// as `a whole number, such as cpus = 4`.
func (v Value) Expect(kind Kind, takes string) error {
	if v.Kind == kind {
		return nil
	}
	return fmt.Errorf("takes %s", takes)
}

// Setting is one key and its value, with the line the key is on.
type Setting struct {
	Key   string
	Line  int
	Value Value
}

// LineError is an error at one line of a file, in the form every message about
// avar's configuration files takes: the full path, the line, then the problem.
func LineError(path string, line int, format string, args ...any) error {
	return fmt.Errorf("%s line %d: %s", path, line, fmt.Sprintf(format, args...))
}

// Parse reads body as a file of schema, calling apply with each setting in the
// order the file gives them. path is used only in messages.
//
// Parsing stops at the first problem, whether it is the reader's or apply's,
// and the error names path, the line, and the key. An error from apply is
// reported on the key's line, prefixed with the key, so apply need only say
// what is wrong with the value and what to write instead.
func Parse(path string, body []byte, schema Schema, apply func(Setting) error) error {
	if !utf8.Valid(body) {
		return fmt.Errorf("%s: not valid UTF-8, which TOML requires", path)
	}
	seen := map[string]int{}
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")

	for i := 0; i < len(lines); i++ {
		line := i + 1

		text := strings.TrimSpace(lines[i])
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if strings.HasPrefix(text, "[") {
			return LineError(path, line, "tables are not supported: every setting in %s is a top-level key (%s)", schema.File, schema.KnownKeys())
		}

		name, rest, ok := strings.Cut(text, "=")
		if !ok {
			return LineError(path, line, "expected key = value, got %q", text)
		}
		name = strings.TrimSpace(name)
		if err := schema.checkKeyName(name); err != nil {
			return LineError(path, line, "%v", err)
		}
		k, err := schema.lookup(name)
		if err != nil {
			return LineError(path, line, "%v", err)
		}
		if first, dup := seen[name]; dup {
			return LineError(path, line, "%q is already set on line %d; a key may appear only once", name, first)
		}
		seen[name] = line

		v, consumed, err := parseValue(schema, k, rest, lines[i+1:])
		if err != nil {
			return LineError(path, line+consumed, "%s: %v", name, err)
		}
		if err := apply(Setting{Key: name, Line: line, Value: v}); err != nil {
			return LineError(path, line, "%s: %v", name, err)
		}
		i += consumed
	}
	return nil
}

// bareKey is TOML's bare-key alphabet.
var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// checkKeyName refuses the key forms TOML has and this reader does not, so that
// they fail as what they are rather than as an unknown key.
func (s Schema) checkKeyName(name string) error {
	switch {
	case name == "":
		return errors.New("a setting needs a key before the =")
	case strings.ContainsAny(name, `"'`):
		return fmt.Errorf("quoted keys are not supported: write the key bare, one of %s", s.KnownKeys())
	case strings.Contains(name, "."):
		return fmt.Errorf("dotted keys are not supported: every setting is a top-level key, one of %s", s.KnownKeys())
	case !bareKey.MatchString(name):
		return fmt.Errorf("%q is not a key: keys are letters, digits, - and _", name)
	}
	return nil
}

// lookup finds name in the schema, or explains why it is not there.
func (s Schema) lookup(name string) (Key, error) {
	for _, k := range s.Keys {
		if k.Name == name {
			return k, nil
		}
	}
	if why, ok := s.Unsupported[name]; ok {
		return Key{}, errors.New(why)
	}
	if near := s.nearest(name); near != "" {
		return Key{}, fmt.Errorf("unknown key %q: did you mean %s? %s understands %s", name, near, s.File, s.KnownKeys())
	}
	return Key{}, fmt.Errorf("unknown key %q: %s understands %s (a key from a newer avar needs a newer avr)", name, s.File, s.KnownKeys())
}

// nearest returns the known key name is most likely a misspelling of, or "".
//
// A key is a candidate when it is within a small edit distance of name, ignoring
// case: one edit for a short key, two for a longer one. That catches a dropped,
// doubled or swapped letter and a hyphen for an underscore, without suggesting
// "arch" for "cpus".
func (s Schema) nearest(name string) string {
	best, bestDistance := "", -1
	for _, k := range s.Keys {
		limit := 1
		if len(k.Name) >= 8 {
			limit = 2
		}
		d := editDistance(strings.ToLower(name), strings.ToLower(k.Name))
		if d <= limit && (bestDistance < 0 || d < bestDistance) {
			best, bestDistance = k.Name, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b, counted in bytes:
// key names are ASCII.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// parseValue reads the text after "=": one value, then nothing but an optional
// comment. An array may continue onto the following lines; consumed reports how
// many of them it used, and on an error, which one the error is on.
func parseValue(schema Schema, k Key, text string, following []string) (v Value, consumed int, err error) {
	text = strings.TrimSpace(text)
	if text == "" || text[0] == '#' {
		return Value{}, 0, errors.New("no value after =")
	}

	var rest string
	switch c := text[0]; {
	case c == '"' || c == '\'':
		v.Kind = String
		v.Str, rest, err = parseString(text)
	case c == '[':
		v.Kind = StringList
		v.Strs, rest, consumed, err = parseArray(schema.File, text, following)
	case c >= '0' && c <= '9':
		v.Kind = Integer
		v.Int, rest, err = parseInteger(text)
		if err != nil && k.Kind == String {
			if unquoted := needsQuotes(k.Name, text); unquoted != nil {
				err = unquoted
			}
		}
	default:
		if k.Kind == String {
			if unquoted := needsQuotes(k.Name, text); unquoted != nil {
				return Value{}, 0, unquoted
			}
		}
		return Value{}, 0, fmt.Errorf("%s is not a value this file accepts: write a quoted string, a whole number, or a list of quoted strings", describe(text))
	}
	if err != nil {
		return Value{}, consumed, err
	}

	rest = strings.TrimSpace(rest)
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return Value{}, consumed, fmt.Errorf("unexpected %q after the value", rest)
	}
	return v, consumed, nil
}

// needsQuotes explains an unquoted word written where a string belongs, such as
// idle_timeout = 30m, by showing the line quoted. It returns nil when the text
// is some other TOML construct, which describe names instead.
func needsQuotes(key, text string) error {
	token := text
	if end := strings.IndexAny(text, " \t#"); end >= 0 {
		token = text[:end]
	}
	if token == "true" || token == "false" || strings.ContainsAny(token, "\"'[]{}\\") {
		return nil
	}
	return fmt.Errorf("%s needs quotes: write a quoted string, as in %s = %q", token, key, token)
}

// parseString reads a basic ("...") or literal ('...') string at the start of
// text and returns it with whatever follows.
//
// Escape sequences are refused rather than interpreted: nothing in either
// schema needs one, and refusing them keeps this reader's meaning identical to
// TOML's without implementing TOML's escape rules. A literal string has no
// escapes in TOML either, so a backslash in one is refused only in a basic
// string.
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
func parseArray(file, text string, following []string) (items []string, rest string, consumed int, err error) {
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
			return nil, "", consumed, fmt.Errorf("lists in %s hold only quoted strings, and this one has %s", file, describe(cur))
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
