package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
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

// backendExemptions are the files inside providerNeutralDirs that may name a
// concrete backend — by importing its package or by using its ProviderID —
// each with the reason it may. Paths are relative to the module root, with
// forward slashes.
//
// Property 21's own exempt list — the WSL provider, the Windows dependency
// checker, the platform and scheduler adapters, and the Windows-only terminal
// files — all live outside these directories, so the only entry is the one
// place the command layer has to meet a real backend: the point where one is
// built.
var backendExemptions = map[string]string{
	"cmd/app.go": "composition root: App.backend is the single place that " +
		"chooses and constructs a backend for this host, switching on its " +
		"ProviderID, after which every command sees only provider.Provider",
}

// fakeProviderDir is the in-process test double. It is not a backend: command
// flow tests are expected to import it, but production code never may, or the
// double would ship in avr.
const fakeProviderDir = "internal/provider/fake"

// fakeProviderIDValue is the id the test double reports (fake.ProviderID). A
// ProviderID constant in internal/types with this value names the double, not
// a backend, and is not held to the identifier rule.
const fakeProviderIDValue = "fake"

// typesDir is the shared vocabulary package that declares ProviderID.
const typesDir = "internal/types"

func TestProp_ProviderPurity_PROP_21(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("locate the module root: %v", err)
	}

	rules, err := loadPurityRules(root, backendExemptions)
	if err != nil {
		t.Fatal(err)
	}
	// Discovery reading the wrong directory would find no backends and pass
	// every file. A purity check that cannot fail is worse than none, so
	// insist that it found the backends avar actually has.
	for _, known := range []string{"internal/provider/lima", "internal/provider/wsl2", "internal/deps"} {
		if !rules.backends[known] {
			t.Fatalf("backend discovery under %s did not find %s; found %v", root, known, sortedKeys(rules.backends))
		}
	}
	for _, known := range []string{"ProviderLima", "ProviderWSL2"} {
		if !rules.backendIDs[known] {
			t.Fatalf("ProviderID discovery in %s did not find %s; found %v", typesDir, known, sortedKeys(rules.backendIDs))
		}
	}

	violations, scanned, err := rules.violations(root)
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

// TestProp_ProviderPurity_ReportsEachViolation_PROP_21 keeps the check honest
// over time: it builds a miniature module with clean files, an exempt file, and
// each kind of violation, and requires exactly the violations to be named.
func TestProp_ProviderPurity_ReportsEachViolation_PROP_21(t *testing.T) {
	root := t.TempDir()
	const typesImport = `"example.test/avar/internal/types"`

	writeFixture(t, root, "go.mod", "module example.test/avar\n\ngo 1.23\n")
	for _, dir := range []string{"internal/provider/lima", "internal/provider/future", fakeProviderDir, "internal/deps"} {
		writeFixture(t, root, dir+"/doc.go", "package x\n")
	}
	writeFixture(t, root, "internal/types/provider.go", `package types

type ProviderID string

const ProviderLima ProviderID = "lima"

const (
	ProviderFuture    ProviderID = "future"
	ProviderConverted            = ProviderID("converted")
	ProviderFake                 = ProviderID("fake")
)

const NotAProviderID = "lima"
`)

	// Imports.
	writeFixture(t, root, "cmd/clean.go", goFile(`"example.test/avar/internal/provider"`, ""))
	writeFixture(t, root, "cmd/flow_test.go", goFile(`"example.test/avar/internal/provider/fake"`, ""))
	writeFixture(t, root, "cmd/shell.go", goFile(`"example.test/avar/internal/provider/future"`, ""))
	writeFixture(t, root, "cmd/doctor_windows.go", goFile(`"example.test/avar/internal/deps"`, ""))
	writeFixture(t, root, "cmd/ship.go", goFile(`"example.test/avar/internal/provider/fake"`, ""))
	writeFixture(t, root, "internal/resolve/sub/matrix.go", goFile(`"example.test/avar/internal/provider/lima"`, ""))

	// Identifiers. The composition root may switch on a backend's id, and a
	// test may seed records with one; nothing else may.
	writeFixture(t, root, "cmd/app.go", goFile(typesImport,
		"var _ = types.ProviderLima\n"))
	writeFixture(t, root, "cmd/records_test.go", goFile(typesImport,
		"var _ = types.ProviderLima\n"))
	writeFixture(t, root, "cmd/advise.go", goFile(typesImport,
		"func translates(id types.ProviderID) bool { return id == types.ProviderFuture }\n"))
	writeFixture(t, root, "cmd/aliased.go", goFile(`avtypes `+typesImport,
		"var _ = avtypes.ProviderLima\n"))
	writeFixture(t, root, "cmd/neutral.go", goFile(typesImport, `
// Asking p.ID() == types.ProviderLima would spell a capability as a backend's
// name; types.ProviderFuture is mentioned here only in prose.
func record(id types.ProviderID) types.ProviderID { return id }

var _ = types.ProviderFake
var _ = types.NotAProviderID
`))

	rules, err := loadPurityRules(root, map[string]string{"cmd/app.go": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(sortedKeys(rules.backendIDs), ","), "ProviderConverted,ProviderFuture,ProviderLima"; got != want {
		t.Errorf("backend ProviderIDs = %s, want %s", got, want)
	}
	violations, _, err := rules.violations(root)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"cmd/advise.go:5 references types.ProviderFuture",
		"cmd/aliased.go:5 references avtypes.ProviderLima",
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

// purityRules is what every file under providerNeutralDirs is checked against.
type purityRules struct {
	module string
	// backends are the module-relative directories of backend-specific
	// packages.
	backends map[string]bool
	// backendIDs are the names of the internal/types ProviderID constants that
	// identify a real backend.
	backendIDs map[string]bool
	exempt     map[string]string
}

func loadPurityRules(root string, exempt map[string]string) (purityRules, error) {
	module, err := modulePath(root)
	if err != nil {
		return purityRules{}, err
	}
	backends, err := backendPackages(root)
	if err != nil {
		return purityRules{}, err
	}
	ids, err := backendProviderIDs(root)
	if err != nil {
		return purityRules{}, err
	}
	return purityRules{module: module, backends: backends, backendIDs: ids, exempt: exempt}, nil
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

// backendProviderIDs returns the names of the constants of type ProviderID
// declared in internal/types, except one naming the test double. They are
// read from the source rather than listed here, so a constant added for a
// future backend is covered the moment it exists.
//
// A constant is of type ProviderID when its declaration says so, when its
// value is a ProviderID conversion, or when it repeats such a declaration
// implicitly inside a const block.
func backendProviderIDs(root string) (map[string]bool, error) {
	dir := filepath.Join(root, filepath.FromSlash(typesDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list the files declaring ProviderID: %w", err)
	}

	ids := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s/%s for ProviderID constants: %w", typesDir, name, err)
		}
		for _, decl := range f.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
				collectProviderIDs(gen, ids)
			}
		}
	}
	return ids, nil
}

// collectProviderIDs adds the backend ProviderID constants of one const
// declaration to ids.
func collectProviderIDs(gen *ast.GenDecl, ids map[string]bool) {
	var typ ast.Expr
	var values []ast.Expr
	for _, spec := range gen.Specs {
		vs := spec.(*ast.ValueSpec)
		// A spec with neither type nor values repeats the one before it.
		if vs.Type != nil || len(vs.Values) > 0 {
			typ, values = vs.Type, vs.Values
		}
		for i, name := range vs.Names {
			var value ast.Expr
			if i < len(values) {
				value = values[i]
			}
			if name.Name == "_" || !isProviderIDConst(typ, value) {
				continue
			}
			if lit, ok := unwrapConversion(value).(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil && s == fakeProviderIDValue {
					continue
				}
			}
			ids[name.Name] = true
		}
	}
}

func isProviderIDConst(typ, value ast.Expr) bool {
	if isIdentNamed(typ, "ProviderID") {
		return true
	}
	call, ok := value.(*ast.CallExpr)
	return typ == nil && ok && isIdentNamed(call.Fun, "ProviderID")
}

// unwrapConversion returns the operand of a ProviderID(...) conversion, or
// the expression itself.
func unwrapConversion(e ast.Expr) ast.Expr {
	if call, ok := e.(*ast.CallExpr); ok && isIdentNamed(call.Fun, "ProviderID") && len(call.Args) == 1 {
		return call.Args[0]
	}
	return e
}

func isIdentNamed(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// violations parses every Go file under providerNeutralDirs and describes each
// way one names a concrete backend. It also reports how many files it
// scanned.
//
// Files are parsed directly rather than loaded as packages so that build
// constraints are ignored: a file that only compiles on Windows is checked on
// a Mac, which is exactly where a WSL import would otherwise go unseen.
func (r purityRules) violations(root string) ([]string, int, error) {
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

			found, err := r.fileViolations(path, rel)
			violations = append(violations, found...)
			return err
		})
		if err != nil {
			return nil, 0, fmt.Errorf("scan %s for references to a concrete backend: %w", dir, err)
		}
	}
	sort.Strings(violations)
	return violations, scanned, nil
}

// fileViolations checks one file; rel is its module-relative path.
func (r purityRules) fileViolations(path, rel string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", rel, err)
	}

	violations, typesNames, err := r.importViolations(fset, f, rel)
	if err != nil {
		return nil, err
	}
	// Test files may name a backend's id: flow tests seed records made by a
	// particular backend, and a test never ships in avr.
	if r.exempt[rel] == "" && !strings.HasSuffix(rel, "_test.go") {
		violations = append(violations, r.identifierViolations(fset, f, rel, typesNames)...)
	}
	return violations, nil
}

// importViolations describes each import in f of a backend package from a
// file not exempt, or of the test double from a non-test file. It also returns
// the names under which f imports internal/types.
func (r purityRules) importViolations(fset *token.FileSet, f *ast.File, rel string) ([]string, map[string]bool, error) {
	var violations []string
	typesNames := map[string]bool{}
	for _, spec := range f.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, nil, fmt.Errorf("read an import path in %s: %w", rel, err)
		}
		pkg, inModule := strings.CutPrefix(imported, r.module+"/")
		if !inModule {
			continue
		}
		at := fmt.Sprintf("%s:%d", rel, fset.Position(spec.Pos()).Line)

		switch {
		case pkg == typesDir:
			name := path.Base(typesDir)
			if spec.Name != nil {
				name = spec.Name.Name
			}
			typesNames[name] = true
		case r.backends[pkg] && r.exempt[rel] == "":
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
	return violations, typesNames, nil
}

// identifierViolations describes each selector expression in f that names a
// backend's ProviderID through one of typesNames. Walking the syntax tree
// rather than the text is what keeps a comment explaining why not to compare
// against a backend's id from being reported as doing it.
func (r purityRules) identifierViolations(fset *token.FileSet, f *ast.File, rel string, typesNames map[string]bool) []string {
	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && typesNames[pkg.Name] && r.backendIDs[sel.Sel.Name] {
			violations = append(violations, fmt.Sprintf(
				"%s:%d references %s.%s, a concrete backend's ProviderID: command-layer "+
					"code must not branch on which backend it has; ask provider.Provider "+
					"for the behaviour instead, and leave the choice of backend to "+
					"cmd/app.go (REQ-17.3, REQ-18.14)",
				rel, fset.Position(sel.Pos()).Line, pkg.Name, sel.Sel.Name))
		}
		return true
	})
	return violations
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

// goFile renders a fixture source file: the import is on line 3 and body
// starts on line 5.
func goFile(importSpec, body string) string {
	return fmt.Sprintf("package x\n\nimport %s\n\n%s", importSpec, body)
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
