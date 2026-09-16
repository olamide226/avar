package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// providerNeutralDirs are the directories whose code must never name a
// concrete backend, relative to the module root. Subdirectories are included,
// so a package added beneath either one is held to the same rule.
var providerNeutralDirs = []string{"cmd", "internal/resolve"}

// backendImportExemptions are the files inside providerNeutralDirs that may
// import a backend package, each with the reason it may. Paths are relative to
// the module root, with forward slashes.
//
// Property 21's own exempt list — the WSL provider, the Windows dependency
// checker, the platform and scheduler adapters, and the Windows-only terminal
// files — all live outside these directories, so the only entry is the one
// place the command layer has to meet a real backend: the point where one is
// built.
var backendImportExemptions = map[string]string{
	"cmd/app.go": "composition root: App.backend is the single place that " +
		"chooses and constructs a backend for this host, after which every " +
		"command sees only provider.Provider",
}

// fakeProviderDir is the in-process test double. It is not a backend: command
// flow tests are expected to import it, but production code never may, or the
// double would ship in avr.
const fakeProviderDir = "internal/provider/fake"

func TestProp_ProviderPurity_PROP_21(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("locate the module root: %v", err)
	}

	backends, err := backendPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	// Discovery reading the wrong directory would find no backends and pass
	// every file. A purity check that cannot fail is worse than none, so
	// insist that it found the backends avar actually has.
	for _, known := range []string{"internal/provider/lima", "internal/provider/wsl2", "internal/deps"} {
		if !backends[known] {
			t.Fatalf("backend discovery under %s did not find %s; found %v", root, known, sortedKeys(backends))
		}
	}

	violations, scanned, err := providerPurityViolations(root, backends, backendImportExemptions)
	if err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatalf("scanned no Go files under %v in %s; the check is not looking where it should", providerNeutralDirs, root)
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// TestProp_ProviderPurity_ReportsFileAndImport_PROP_21 keeps the check honest
// over time: it builds a miniature module with one clean file, one exempt file,
// and each kind of violation, and requires exactly the violations to be named.
func TestProp_ProviderPurity_ReportsFileAndImport_PROP_21(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example.test/avar\n\ngo 1.23\n")
	for _, dir := range []string{"internal/provider/lima", "internal/provider/future", fakeProviderDir, "internal/deps"} {
		writeFixture(t, root, dir+"/doc.go", "package x\n")
	}
	writeFixture(t, root, "cmd/app.go", goFile("example.test/avar/internal/provider/lima"))
	writeFixture(t, root, "cmd/clean.go", goFile("example.test/avar/internal/provider"))
	writeFixture(t, root, "cmd/flow_test.go", goFile("example.test/avar/internal/provider/fake"))
	writeFixture(t, root, "cmd/shell.go", goFile("example.test/avar/internal/provider/future"))
	writeFixture(t, root, "cmd/doctor_windows.go", goFile("example.test/avar/internal/deps"))
	writeFixture(t, root, "cmd/ship.go", goFile("example.test/avar/internal/provider/fake"))
	writeFixture(t, root, "internal/resolve/sub/matrix.go", goFile("example.test/avar/internal/provider/lima"))

	backends, err := backendPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	violations, _, err := providerPurityViolations(root, backends, map[string]string{"cmd/app.go": "fixture"})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"cmd/doctor_windows.go:3 imports example.test/avar/internal/deps",
		"cmd/shell.go:3 imports example.test/avar/internal/provider/future",
		"cmd/ship.go:3 imports example.test/avar/internal/provider/fake",
		"internal/resolve/sub/matrix.go:3 imports example.test/avar/internal/provider/lima",
	}
	if len(violations) != len(want) {
		t.Fatalf("got %d violations, want %d:\n%s", len(violations), len(want), strings.Join(violations, "\n"))
	}
	for i := range want {
		if !strings.HasPrefix(violations[i], want[i]) {
			t.Errorf("violation %d = %q, want it to begin %q", i, violations[i], want[i])
		}
	}
}

// backendPackages returns the module-relative directories of every package
// that is specific to one backend: each directory under internal/provider
// except the test double, which picks up a future backend without this test
// being edited, and internal/deps, which is where the Lima and WSL dependency
// checks live.
func backendPackages(root string) (map[string]bool, error) {
	entries, err := os.ReadDir(filepath.Join(root, "internal", "provider"))
	if err != nil {
		return nil, fmt.Errorf("list backend packages under internal/provider: %w", err)
	}
	backends := map[string]bool{"internal/deps": true}
	for _, e := range entries {
		dir := "internal/provider/" + e.Name()
		if e.IsDir() && dir != fakeProviderDir {
			backends[dir] = true
		}
	}
	return backends, nil
}

// providerPurityViolations parses the imports of every Go file under
// providerNeutralDirs and describes each one that names a backend package from
// a file not in exempt, or the test double from a non-test file. It also
// reports how many files it scanned.
//
// Files are parsed directly rather than loaded as packages so that build
// constraints are ignored: a file that only compiles on Windows is checked on
// a Mac, which is exactly where a WSL import would otherwise go unseen.
func providerPurityViolations(root string, backends map[string]bool, exempt map[string]string) ([]string, int, error) {
	module, err := modulePath(root)
	if err != nil {
		return nil, 0, err
	}

	var violations []string
	scanned := 0
	for _, dir := range providerNeutralDirs {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(dir)), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			scanned++

			found, err := fileViolations(path, rel, module, backends, exempt)
			violations = append(violations, found...)
			return err
		})
		if err != nil {
			return nil, 0, fmt.Errorf("scan %s for backend imports: %w", dir, err)
		}
	}
	sort.Strings(violations)
	return violations, scanned, nil
}

// fileViolations checks the imports of one file; rel is its module-relative
// path.
func fileViolations(path, rel, module string, backends map[string]bool, exempt map[string]string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, fmt.Errorf("parse imports of %s: %w", rel, err)
	}

	var violations []string
	for _, spec := range f.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, fmt.Errorf("read an import path in %s: %w", rel, err)
		}
		pkg, inModule := strings.CutPrefix(imported, module+"/")
		if !inModule {
			continue
		}
		at := fmt.Sprintf("%s:%d", rel, fset.Position(spec.Pos()).Line)

		switch {
		case backends[pkg] && exempt[rel] == "":
			violations = append(violations, fmt.Sprintf(
				"%s imports %s, a backend-specific package: command-layer code must "+
					"reach backends only through provider.Provider, and constructing one "+
					"belongs in the composition root, cmd/app.go (REQ-17.3, REQ-18.14)",
				at, imported))
		case pkg == fakeProviderDir && !strings.HasSuffix(rel, "_test.go"):
			violations = append(violations, fmt.Sprintf(
				"%s imports %s, the test double, from a non-test file: it would ship in avr",
				at, imported))
		}
	}
	return violations, nil
}

// modulePath reads the module path from root's go.mod.
func modulePath(root string) (string, error) {
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read the module path: %w", err)
	}
	defer f.Close()

	lines := bufio.NewScanner(f)
	for lines.Scan() {
		if path, ok := strings.CutPrefix(strings.TrimSpace(lines.Text()), "module "); ok {
			return strings.TrimSpace(path), nil
		}
	}
	if err := lines.Err(); err != nil {
		return "", fmt.Errorf("read the module path from %s: %w", f.Name(), err)
	}
	return "", errors.New("read the module path: go.mod has no module directive")
}

func goFile(imported string) string {
	return fmt.Sprintf("package x\n\nimport _ %q\n", imported)
}

func writeFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
