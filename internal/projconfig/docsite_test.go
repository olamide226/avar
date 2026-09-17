package projconfig

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The documentation site describes .avr.toml in site/syntax/avr-toml.md and
// avr init's proposals in site/commands/init.md. These tests hold both to this
// package: the key table must list exactly the keys the reader accepts, every
// example the page shows is fed to Parse and must be accepted or refused as the
// page says, and the tables of what avr init reads and proposes are generated
// from the tables detection uses. `make docs` rewrites the generated sections.
var updateDocs = flag.Bool("update", false, "rewrite the generated sections of the documentation site under site/")

const siteDir = "../../site"

var avrTOMLPage = filepath.Join(siteDir, "syntax", "avr-toml.md")

func TestDocsite_KeyTableListsEveryKeyInSchemaOrder_REQ_15_1(t *testing.T) {
	body := readPage(t, avrTOMLPage)
	section, err := between(body, "<!-- keys:begin", "<!-- keys:end -->")
	if err != nil {
		t.Fatalf("%s: %v", avrTOMLPage, err)
	}

	// The first cell of each table row, skipping the header and the rule.
	var documented []string
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|").FindAllStringSubmatch(section, -1) {
		documented = append(documented, m[1])
	}
	var accepted []string
	for _, k := range schema {
		accepted = append(accepted, k.name)
	}
	if !slices.Equal(documented, accepted) {
		t.Errorf("%s documents the keys %q; the reader accepts %q. Update the table so a key is neither missing nor invented",
			avrTOMLPage, documented, accepted)
	}
}

// exampleBlock is a toml code block the page marks as an example, and the
// text block that follows it, if any, which shows the error avar prints.
type exampleBlock struct {
	valid    bool
	body     string
	message  string
	location string
}

var exampleMarker = regexp.MustCompile("<!-- example:(valid|invalid)[^\\n]*-->\\n```toml\\n((?s:.*?))```\\n(?:\\n```text\\n((?s:.*?))```\\n)?")

func TestDocsite_ExamplesAreAcceptedOrRefusedAsThePageSays_REQ_15_1(t *testing.T) {
	body := readPage(t, avrTOMLPage)
	var examples []exampleBlock
	for _, m := range exampleMarker.FindAllStringSubmatchIndex(body, -1) {
		examples = append(examples, exampleBlock{
			valid:    body[m[2]:m[3]] == "valid",
			body:     body[m[4]:m[5]],
			message:  textAt(body, m[6], m[7]),
			location: fmt.Sprintf("%s byte %d", avrTOMLPage, m[0]),
		})
	}
	if len(examples) == 0 {
		t.Fatalf("%s has no marked examples, so nothing would be checked", avrTOMLPage)
	}

	const shownPath = "<project>/.avr.toml"
	sawValid, sawInvalid := false, false
	for _, ex := range examples {
		_, err := Parse(shownPath, []byte(ex.body))
		switch {
		case ex.valid:
			sawValid = true
			if err != nil {
				t.Errorf("%s: the page shows this file as valid, and the reader refuses it: %v", ex.location, err)
			}
		default:
			sawInvalid = true
			if err == nil {
				t.Errorf("%s: the page shows this file as refused, and the reader accepts it", ex.location)
				continue
			}
			if want := strings.TrimSpace(ex.message); want != "" && want != "avr: "+err.Error() {
				t.Errorf("%s: the page shows the error\n  %s\nand avar prints\n  avr: %v", ex.location, want, err)
			}
		}
	}
	if !sawValid || !sawInvalid {
		t.Errorf("%s should show at least one valid and one refused example (valid: %t, refused: %t)", avrTOMLPage, sawValid, sawInvalid)
	}
}

func TestDocsite_InitPageListsTheManifestsDetectionReads_REQ_15_2(t *testing.T) {
	checkGenerated(t, filepath.Join(siteDir, "commands", "init.md"), "init-manifests", func() string {
		names := make([]string, 0, len(manifests))
		for _, m := range manifests {
			names = append(names, "`"+m.name+"`")
		}
		return "\n   " + strings.Join(names, ", ") + "\n\n   "
	})
}

func TestDocsite_InitPageListsTheRuntimePackages_REQ_15_2(t *testing.T) {
	checkGenerated(t, filepath.Join(siteDir, "commands", "init.md"), "init-packages", func() string {
		var b strings.Builder
		b.WriteString("\n| Runtime | Ubuntu, Debian | Fedora |\n| --- | --- | --- |\n")
		for _, r := range []Runtime{RuntimeNode, RuntimePython, RuntimeGo, RuntimeRust, RuntimeRuby} {
			pkgs, ok := runtimePackages[r]
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", r, codeList(pkgs.apt), codeList(pkgs.dnf))
		}
		b.WriteString("\n")
		return b.String()
	})
	if len(runtimePackages) != 5 {
		t.Errorf("runtimePackages has %d runtimes and the page lists 5; add the new runtime to this test's list", len(runtimePackages))
	}
}

func codeList(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, "`"+n+"`")
	}
	return strings.Join(quoted, ", ")
}

func checkGenerated(t *testing.T, path, section string, render func() string) {
	t.Helper()
	body := readPage(t, path)
	begin := "<!-- generated:" + section + ":begin"
	end := "<!-- generated:" + section + ":end -->"

	start := strings.Index(body, begin)
	if start < 0 {
		t.Fatalf("%s has no %q marker, so there is nothing to check", path, begin)
	}
	lineEnd := strings.Index(body[start:], "\n")
	if lineEnd < 0 {
		t.Fatalf("%s: the %q marker is the last line", path, begin)
	}
	contentStart := start + lineEnd + 1
	stop := strings.Index(body[contentStart:], end)
	if stop < 0 {
		t.Fatalf("%s: %q has no closing %q", path, begin, end)
	}

	updated := body[:contentStart] + render() + body[contentStart+stop:]
	if updated == body {
		return
	}
	if *updateDocs {
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			t.Fatalf("rewriting %s: %v", path, err)
		}
		return
	}
	t.Errorf("%s: the generated %q section no longer matches the code it documents; run `make docs` and commit the result", path, section)
}

func readPage(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(body)
}

func between(body, begin, end string) (string, error) {
	start := strings.Index(body, begin)
	if start < 0 {
		return "", fmt.Errorf("no %q marker", begin)
	}
	stop := strings.Index(body[start:], end)
	if stop < 0 {
		return "", fmt.Errorf("%q has no closing %q", begin, end)
	}
	return body[start : start+stop], nil
}

func textAt(body string, from, to int) string {
	if from < 0 {
		return ""
	}
	return body[from:to]
}
