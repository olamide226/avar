package state

import (
	"fmt"
	"os"
	"strings"
)

// ConfigList reads a list-valued key from the user's config.toml, such as
//
//	forward_env = ["AWS_PROFILE", "GITHUB_TOKEN"]
//
// A missing file, a missing key, or a value that is not a list all return no
// entries and no error: config.toml is optional and hand-edited, and a typo in
// it must not stop the user from getting a shell. The cost of that choice is
// that a malformed entry is silently ignored, which is why the caller is
// expected to be able to show what it did read — `avr status` does.
//
// The parser is deliberately small rather than a TOML dependency: avar owns two
// keys, both flat, and the standard-library-first rule in CLAUDE.md makes a
// parser for the whole format hard to justify. It understands a single-line
// list and a list spread over several lines, with # comments, which is the
// whole of what these keys can be.
func (s *Store) ConfigList(key string) ([]string, error) {
	data, err := os.ReadFile(s.ConfigPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.ConfigPath(), err)
	}
	return parseConfigList(string(data), key), nil
}

// parseConfigList extracts key's list value from TOML text. It is separate from
// ConfigList so that it can be tested without a state directory.
func parseConfigList(body, key string) []string {
	var (
		collecting bool
		raw        strings.Builder
	)
	for _, line := range strings.Split(body, "\n") {
		line = stripComment(line)
		if !collecting {
			name, value, ok := strings.Cut(line, "=")
			if !ok || strings.TrimSpace(name) != key {
				continue
			}
			value = strings.TrimSpace(value)
			if !strings.HasPrefix(value, "[") {
				// A scalar under a list-valued key: not something this
				// function can answer, and not worth failing a shell over.
				return nil
			}
			raw.WriteString(strings.TrimPrefix(value, "["))
			if strings.Contains(value, "]") {
				break
			}
			collecting = true
			continue
		}

		if idx := strings.Index(line, "]"); idx >= 0 {
			raw.WriteString(line[:idx])
			break
		}
		raw.WriteString(line)
	}

	var out []string
	for _, field := range strings.Split(strings.TrimSuffix(raw.String(), "]"), ",") {
		if item := strings.Trim(strings.TrimSpace(field), `"'`); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// stripComment removes a trailing # comment, respecting quotes so that a "#"
// inside a value is kept.
func stripComment(line string) string {
	inQuote := byte(0)
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case inQuote != 0 && c == inQuote:
			inQuote = 0
		case inQuote == 0 && (c == '"' || c == '\''):
			inQuote = c
		case inQuote == 0 && c == '#':
			return line[:i]
		}
	}
	return line
}
