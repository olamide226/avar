package cmd

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/resolve"
)

// The documentation site under site/ carries two things that already have a
// source of truth in the code: the command reference, which is avar's help
// text, and the environment matrix, which is the resolver's. Copying either by
// hand would give the site a second answer that drifts from the binary, so
// both live between markers that these tests own. A page whose generated
// section differs from what the code renders today fails here; running
// `make docs` rewrites the sections.
var updateDocs = flag.Bool("update", false, "rewrite the generated sections of the documentation site under site/")

// siteDir is the documentation site's source, relative to this package.
const siteDir = "../site"

func TestDocsite_CommandReferenceMatchesHelp_REQ_17_2(t *testing.T) {
	checkGeneratedSection(t, "commands.md", "commands", renderCommandReference)
}

func TestDocsite_EnvironmentMatrixMatchesResolver_REQ_17_2(t *testing.T) {
	checkGeneratedSection(t, "environments.md", "matrix", renderEnvironmentMatrix)
}

// checkGeneratedSection compares one page's generated section with what the
// code renders now, or rewrites it when -update is given.
func checkGeneratedSection(t *testing.T, page, section string, render func() (string, error)) {
	t.Helper()

	path := filepath.Join(siteDir, page)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	want, err := render()
	if err != nil {
		t.Fatalf("rendering the %s section: %v", section, err)
	}

	updated, err := spliceGenerated(string(body), section, want)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if updated == string(body) {
		return
	}
	if *updateDocs {
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			t.Fatalf("rewriting %s: %v", path, err)
		}
		return
	}
	t.Errorf("%s: the generated %q section no longer matches the code it documents; "+
		"run `make docs` and commit the result", path, section)
}

// spliceGenerated replaces the content between a section's markers with
// content, and fails when either marker is missing.
//
// A missing marker is an error rather than a page to skip: a drift check that
// finds nothing to compare passes vacuously, which is the failure recorded in
// docs/lessons.md under "A check that finds nothing to check passes".
func spliceGenerated(doc, section, content string) (string, error) {
	begin := "<!-- generated:" + section + ":begin"
	end := "<!-- generated:" + section + ":end -->"

	start := strings.Index(doc, begin)
	if start < 0 {
		return "", fmt.Errorf("no %q marker, so there is no generated section to check", begin)
	}
	lineEnd := strings.Index(doc[start:], "\n")
	if lineEnd < 0 {
		return "", fmt.Errorf("the %q marker is the last line, with no %q after it", begin, end)
	}
	contentStart := start + lineEnd + 1

	stop := strings.Index(doc[contentStart:], end)
	if stop < 0 {
		return "", fmt.Errorf("%q has no closing %q; without it the section would run to the end of the page", begin, end)
	}
	return doc[:contentStart] + content + doc[contentStart+stop:], nil
}

func TestDocsite_SpliceRefusesMissingMarkersAndDetectsDrift(t *testing.T) {
	const page = "intro\n<!-- generated:x:begin — note -->\nold\n<!-- generated:x:end -->\noutro\n"

	got, err := spliceGenerated(page, "x", "new\n")
	if err != nil {
		t.Fatalf("splicing a well-formed page: %v", err)
	}
	if want := "intro\n<!-- generated:x:begin — note -->\nnew\n<!-- generated:x:end -->\noutro\n"; got != want {
		t.Errorf("spliced page = %q, want %q", got, want)
	}
	if same, _ := spliceGenerated(page, "x", "old\n"); same != page {
		t.Errorf("splicing the content already there changed the page, so an unchanged page would read as drift")
	}

	for name, broken := range map[string]string{
		"no begin marker": "intro\nold\n<!-- generated:x:end -->\n",
		"no end marker":   "<!-- generated:x:begin -->\nold\n",
		"other section":   "<!-- generated:y:begin -->\nold\n<!-- generated:y:end -->\n",
	} {
		if _, err := spliceGenerated(broken, "x", "new\n"); err == nil {
			t.Errorf("%s: spliced without error, so a page missing its section would pass the drift check", name)
		}
	}
}

// renderCommandReference renders avar's root help and each public command's
// help, through the same functions Execute calls for `avr help` and
// `avr help <command>`.
func renderCommandReference() (string, error) {
	var root bytes.Buffer
	cmd := NewRootCommand("", cli.Invocation{}, nil)
	cmd.SetOut(&root)
	if err := cmd.Help(); err != nil {
		return "", fmt.Errorf("render `avr help`: %w", err)
	}

	var b strings.Builder
	// Liquid reads the page before Markdown does. Help text is not Liquid, and
	// raw keeps a future `{{` in it from being treated as a template.
	b.WriteString("{% raw %}\n")
	writeHelpSection(&b, "avr", "`avr help`, `avr --help`", root.String())

	documented := 0
	for _, name := range cli.Subcommands() {
		if name == "internal" {
			// avar's own scheduled idle check: reserved so that it wins over a
			// guest command, and deliberately absent from help.
			continue
		}
		var help bytes.Buffer
		if err := writeCommandHelp(&help, name); err != nil {
			return "", fmt.Errorf("render `avr help %s`: %w; every reserved name but internal must have help", name, err)
		}
		writeHelpSection(&b, "avr "+name, fmt.Sprintf("`avr help %s`", name), help.String())
		documented++
	}
	if documented == 0 {
		return "", fmt.Errorf("no subcommands to document, so the reference would be empty")
	}
	b.WriteString("{% endraw %}\n")
	return b.String(), nil
}

func writeHelpSection(b *strings.Builder, heading, source, help string) {
	fmt.Fprintf(b, "\n## `%s`\n\nPrinted by %s.\n\n```text\n%s\n```\n", heading, source, strings.TrimRight(help, "\n"))
}

// renderEnvironmentMatrix renders the resolver's supported environments as a
// table, one row per distribution release.
func renderEnvironmentMatrix() (string, error) {
	distros := resolve.SupportedDistros()
	if len(distros) == 0 {
		return "", fmt.Errorf("the resolver reports no supported distributions, so the table would be empty")
	}

	var b strings.Builder
	b.WriteString("\n| Distribution | Release | Selected with | Architectures |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, d := range distros {
		pinned, _ := resolve.PinnedVersion(d)
		display := strings.ToUpper(string(d)[:1]) + string(d)[1:]
		for _, version := range resolve.SupportedVersions(d) {
			release := version
			selectedWith := fmt.Sprintf("`--distro %s:%s`", d, version)
			if version == pinned {
				release += " (default)"
				selectedWith = fmt.Sprintf("`--distro %s` or %s", d, selectedWith)
			}

			var arches []string
			for _, a := range resolve.SupportedArches(d, version) {
				arches = append(arches, "`"+string(a)+"`")
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", display, release, selectedWith, strings.Join(arches, ", "))
		}
	}

	pinned, _ := resolve.PinnedVersion(resolve.DefaultDistro)
	fmt.Fprintf(&b, "\nWith no distribution chosen anywhere, avar uses `%s:%s`.\n\n", resolve.DefaultDistro, pinned)
	return b.String(), nil
}

// repoLink matches a link from the site to a file or directory on the
// repository's main branch.
var repoLink = regexp.MustCompile(`https://github\.com/olamide226/avar/(?:blob|tree)/main/([^)\s"#>]+)`)

// Links from the site into the repository are absolute URLs, which Jekyll
// cannot check and a pull request's build cannot follow. This checks that each
// names a path that exists in the checkout being tested.
func TestDocsite_RepositoryLinksNameFilesThatExist_REQ_17_2(t *testing.T) {
	pages, err := filepath.Glob(filepath.Join(siteDir, "*.md"))
	if err != nil {
		t.Fatalf("listing the site's pages: %v", err)
	}
	if len(pages) == 0 {
		t.Fatalf("found no pages under %s, so no link would be checked", siteDir)
	}

	checked := 0
	for _, page := range pages {
		body, err := os.ReadFile(page)
		if err != nil {
			t.Fatalf("reading %s: %v", page, err)
		}
		for _, m := range repoLink.FindAllStringSubmatch(string(body), -1) {
			checked++
			target := filepath.Join("..", filepath.FromSlash(strings.TrimSuffix(m[1], "/")))
			if _, err := os.Stat(target); err != nil {
				t.Errorf("%s links to %s, which does not exist in this repository", page, m[0])
			}
		}
	}
	if checked == 0 {
		t.Errorf("no repository links found in %s; the pattern is broken or the pages no longer link to the code they describe", siteDir)
	}
}
