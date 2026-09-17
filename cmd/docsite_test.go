package cmd

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"html"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/resolve"
)

// The documentation site under site/ carries things that already have a
// source of truth in the code: each command's help text, the list of commands,
// the reserved names, and the environment matrix. Copying any of them by hand
// would give the site a second answer that drifts from the binary, so each
// lives between markers that these tests own. A page whose generated section
// differs from what the code renders today fails here; `make docs` rewrites
// the sections.
//
// The prose around those sections is written by hand. What can be checked
// about it is checked too: examples on a command page must be command lines
// avar's grammar accepts, and links into the repository must name files that
// exist.
var updateDocs = flag.Bool("update", false, "rewrite the generated sections of the documentation site under site/")

// siteDir is the documentation site's source, relative to this package.
const siteDir = "../site"

// rootPage is the command page for `avr` itself: the shell and one-shot
// commands, documented by the root help.
const rootPage = "avr"

// documentedCommands lists every command that has a page: `avr` itself, then
// each subcommand except internal, which is avar's own scheduled idle check
// and deliberately absent from help.
func documentedCommands() []string {
	names := []string{rootPage}
	for _, name := range cli.Subcommands() {
		if name != "internal" {
			names = append(names, name)
		}
	}
	return names
}

func commandPagePath(name string) string {
	return filepath.Join(siteDir, "commands", name+".md")
}

func TestDocsite_EveryCommandPageCarriesItsHelp_REQ_17_2(t *testing.T) {
	for _, name := range documentedCommands() {
		checkGeneratedSection(t, commandPagePath(name), "help", func() (string, error) {
			return renderHelpSection(name)
		})
	}
}

func TestDocsite_CommandIndexListsEveryCommand_REQ_17_2(t *testing.T) {
	checkGeneratedSection(t, filepath.Join(siteDir, "commands", "index.md"), "command-index", renderCommandIndex)
}

func TestDocsite_ReservedNamesMatchTheGrammar_REQ_2_6(t *testing.T) {
	checkGeneratedSection(t, filepath.Join(siteDir, "syntax", "command-line.md"), "reserved-names", renderReservedNames)
}

func TestDocsite_EnvironmentMatrixMatchesResolver_REQ_17_2(t *testing.T) {
	checkGeneratedSection(t, filepath.Join(siteDir, "environments.md"), "matrix", renderEnvironmentMatrix)
}

// A page for a command that no longer exists would keep documenting it.
func TestDocsite_NoPageForACommandAvarDoesNotHave_REQ_17_2(t *testing.T) {
	pages, err := filepath.Glob(filepath.Join(siteDir, "commands", "*.md"))
	if err != nil {
		t.Fatalf("listing the command pages: %v", err)
	}
	known := map[string]bool{"index": true}
	for _, name := range documentedCommands() {
		known[name] = true
	}
	for _, page := range pages {
		if name := strings.TrimSuffix(filepath.Base(page), ".md"); !known[name] {
			t.Errorf("%s documents %q, which is not an avar command; remove the page or rename it", page, name)
		}
	}
}

// Every example on a command page is a command line avar's grammar accepts,
// and a subcommand's page shows at least one example that runs that
// subcommand. An example that avar would refuse, or would read as a command
// for Linux, teaches the wrong thing.
func TestDocsite_CommandExamplesAreValidCommandLines_REQ_2_5(t *testing.T) {
	for _, name := range documentedCommands() {
		path := commandPagePath(name)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("reading %s: %v", path, err)
			continue
		}
		examples := shellExamples(string(body))
		if len(examples) == 0 {
			t.Errorf("%s has no `avr` examples in a sh code block", path)
			continue
		}

		showsCommand := false
		for _, example := range examples {
			inv, err := cli.Parse(strings.Fields(example)[1:])
			if err != nil {
				t.Errorf("%s: the example %q is not a command line avar accepts: %v", path, example, err)
				continue
			}
			switch {
			case name == rootPage && inv.Mode != cli.ModeSubcommand:
				showsCommand = true
			case inv.Mode == cli.ModeSubcommand && inv.Subcommand == name:
				showsCommand = true
			}
		}
		if !showsCommand {
			t.Errorf("%s has no example that runs %s", path, name)
		}
	}
}

// shellExamples returns the `avr ...` lines of a page's sh code blocks, with
// any trailing comment removed.
func shellExamples(page string) []string {
	var (
		out     []string
		inShell bool
	)
	scanner := bufio.NewScanner(strings.NewReader(page))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "```"):
			inShell = !inShell && line == "```sh"
		case inShell && strings.HasPrefix(line, "avr"):
			if i := strings.Index(line, " #"); i >= 0 {
				line = strings.TrimSpace(line[:i])
			}
			if line == "avr" || strings.HasPrefix(line, "avr ") {
				out = append(out, line)
			}
		}
	}
	return out
}

// checkGeneratedSection compares one page's generated section with what the
// code renders now, or rewrites it when -update is given.
func checkGeneratedSection(t *testing.T, path, section string, render func() (string, error)) {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("reading %s: %v", path, err)
		return
	}
	want, err := render()
	if err != nil {
		t.Errorf("rendering the %s section of %s: %v", section, path, err)
		return
	}

	updated, err := spliceGenerated(string(body), section, want)
	if err != nil {
		t.Errorf("%s: %v", path, err)
		return
	}
	if updated == string(body) {
		return
	}
	if *updateDocs {
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			t.Errorf("rewriting %s: %v", path, err)
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

func TestDocsite_ShellExamplesReadOnlyAvrLinesInShBlocks(t *testing.T) {
	const page = "avr outside\n```sh\navr stop --all   # comment\navrx not avar\n```\n```text\navr in text\n```\n```sh\navr\n```\n"
	got := shellExamples(page)
	want := []string{"avr stop --all", "avr"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("shellExamples = %q, want %q", got, want)
	}
}

// renderHelp returns what `avr help <name>` prints, or `avr help` for the
// root page, through the same functions Execute calls.
func renderHelp(name string) (string, error) {
	var out bytes.Buffer
	if name == rootPage {
		cmd := NewRootCommand("", cli.Invocation{}, nil)
		cmd.SetOut(&out)
		if err := cmd.Help(); err != nil {
			return "", fmt.Errorf("render `avr help`: %w", err)
		}
		return out.String(), nil
	}
	if err := writeCommandHelp(&out, name); err != nil {
		return "", fmt.Errorf("render `avr help %s`: %w; every reserved name but internal must have help", name, err)
	}
	return out.String(), nil
}

// renderHelpSection is a command page's help block.
func renderHelpSection(name string) (string, error) {
	help, err := renderHelp(name)
	if err != nil {
		return "", err
	}
	source := fmt.Sprintf("`avr help %s` or `avr %s --help`", name, name)
	if name == rootPage {
		source = "`avr help` or `avr --help`"
	}
	// Liquid reads the page before Markdown does. Help text is not Liquid, and
	// raw keeps a future `{{` in it from being treated as a template.
	return fmt.Sprintf("\nThis is exactly what %s prints.\n\n{%% raw %%}\n```text\n%s\n```\n{%% endraw %%}\n\n",
		source, strings.TrimRight(help, "\n")), nil
}

// renderCommandIndex is the table of commands on the commands landing page,
// each with its usage line from help.
func renderCommandIndex() (string, error) {
	var b strings.Builder
	b.WriteString("\n| Command | Usage |\n| --- | --- |\n")
	for _, name := range documentedCommands() {
		usage, err := usageLine(name)
		if err != nil {
			return "", err
		}
		title := "avr " + name
		if name == rootPage {
			title = "avr"
		}
		// A pipe separates table cells even inside a code span, and usage
		// lines use it for alternatives, so this cell is HTML.
		usage = strings.ReplaceAll(html.EscapeString(usage), "|", "&#124;")
		fmt.Fprintf(&b, "| [`%s`]({%% link commands/%s.md %%}) | <code>%s</code> |\n", title, name, usage)
	}
	b.WriteString("\n")
	return b.String(), nil
}

// usageLine is the first line under "Usage:" in a command's help.
func usageLine(name string) (string, error) {
	help, err := renderHelp(name)
	if err != nil {
		return "", err
	}
	lines := strings.Split(help, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "Usage:" && i+1 < len(lines) {
			return strings.TrimSpace(lines[i+1]), nil
		}
	}
	return "", fmt.Errorf("the help for %q has no Usage: line", name)
}

// renderReservedNames lists avar's subcommand names, the words that never
// reach Linux as the start of a command.
func renderReservedNames() (string, error) {
	names := cli.Subcommands()
	if len(names) == 0 {
		return "", fmt.Errorf("the grammar reports no subcommands, so the list would be empty")
	}
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, "`"+name+"`")
	}
	return "\n" + strings.Join(quoted, " ") + "\n\n", nil
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
	var pages []string
	err := filepath.WalkDir(siteDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != siteDir && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_") || d.Name() == "vendor") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, ".md") {
			pages = append(pages, path)
		}
		return nil
	})
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
