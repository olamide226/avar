package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The documentation site's command-line page has a table of avar's flags.
// The flag table in this package is the whole definition of what avar parses,
// so every name in it must appear in the page's table, and the page must not
// document a flag avar does not have. The flags' help text comes from cmd and
// is generated onto the command pages; this checks the page written by hand.
func TestDocsite_FlagTableDocumentsEveryFlag_REQ_2_5(t *testing.T) {
	path := filepath.Join("..", "..", "site", "syntax", "command-line.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	const (
		beginMarker = "<!-- flags:begin"
		endMarker   = "<!-- flags:end -->"
	)
	start := strings.Index(string(body), beginMarker)
	if start < 0 {
		t.Fatalf("%s has no %q marker, so there is no flag table to check", path, beginMarker)
	}
	rest := string(body)[start:]
	end := strings.Index(rest, endMarker)
	if end < 0 {
		t.Fatalf("%s has %q but no %q", path, beginMarker, endMarker)
	}

	// Flag names in the first column of each row.
	documented := map[string]bool{}
	for _, row := range regexp.MustCompile(`(?m)^\| ([^|]+) \|`).FindAllStringSubmatch(rest[:end], -1) {
		for _, m := range regexp.MustCompile("`(-{1,2}[a-z-]+)`").FindAllStringSubmatch(row[1], -1) {
			documented[m[1]] = true
		}
	}

	parsed := map[string]bool{}
	for _, spec := range avarFlags {
		for _, name := range spec.names {
			parsed[name] = true
			if !documented[name] {
				t.Errorf("avar parses %s and %s's flag table does not document it", name, path)
			}
		}
	}
	for name := range documented {
		if !parsed[name] {
			t.Errorf("%s documents %s, which avar does not parse", path, name)
		}
	}
}
