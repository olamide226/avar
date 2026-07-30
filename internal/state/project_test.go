package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A project's identity has to survive being spelled differently, because the
// user reaches the same directory through symlinks (/tmp, ~/code -> /Volumes/…)
// and through paths their shell built with ".." or a trailing slash. If any of
// those hashed differently, the isolation choice remembered per project would
// silently stop applying (REQ-11.2).
func TestProjectID_EquivalentPathsShareOneIdentity_REQ_11_2(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	project := filepath.Join(base, "code", "my-project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	link := filepath.Join(base, "project-link")
	if err := os.Symlink(project, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	linkedParent := filepath.Join(base, "code-link")
	if err := os.Symlink(filepath.Join(base, "code"), linkedParent); err != nil {
		t.Fatalf("create parent symlink: %v", err)
	}

	want, err := ProjectID(project)
	if err != nil {
		t.Fatalf("ProjectID(%s): %v", project, err)
	}
	if len(want) != 64 {
		t.Errorf("ProjectID returned %d hex characters, want 64 (sha-256)", len(want))
	}

	spellings := map[string]string{
		"trailing slash":        project + string(os.PathSeparator),
		"single-dot segment":    filepath.Join(base, "code", ".", "my-project"),
		"parent traversal":      filepath.Join(base, "code", "my-project", "..", "my-project"),
		"symlink to the dir":    link,
		"symlink to the parent": filepath.Join(linkedParent, "my-project"),
		"repeated call":         project,
	}
	for name, spelling := range spellings {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := ProjectID(spelling)
			if err != nil {
				t.Fatalf("ProjectID(%s): %v", spelling, err)
			}
			if got != want {
				t.Errorf("ProjectID(%s) = %s, want %s — the same directory must have one identity", spelling, got, want)
			}
		})
	}
}

func TestProjectID_DistinctDirectoriesGetDistinctIdentities_REQ_11_2(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	seen := map[string]string{}
	for _, name := range []string{"a", "b", "a-longer-name", "nested/a"} {
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
		id, err := ProjectID(dir)
		if err != nil {
			t.Fatalf("ProjectID(%s): %v", dir, err)
		}
		if other, clash := seen[id]; clash {
			t.Fatalf("%s and %s hash to the same identity %s", other, dir, id)
		}
		seen[id] = dir
	}
}

func TestProjectID_RequiresAnExistingDirectory_REQ_11_2(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	file := filepath.Join(base, "go.mod")
	if err := os.WriteFile(file, []byte("module x\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	danglingTarget := filepath.Join(base, "gone")
	dangling := filepath.Join(base, "dangling-link")
	if err := os.Symlink(danglingTarget, dangling); err != nil {
		t.Fatalf("create dangling symlink: %v", err)
	}

	cases := map[string]struct {
		path string
		want string
	}{
		"missing directory":  {path: filepath.Join(base, "not-there"), want: "no such directory"},
		"unresolvable link":  {path: dangling, want: "no such directory"},
		"path is a file":     {path: file, want: "not a directory"},
		"empty path":         {path: "", want: "no path given"},
		"whitespace-only":    {path: "   ", want: "no path given"},
		"file subdirectory":  {path: filepath.Join(file, "sub"), want: "resolve project directory"},
		"unreadable nowhere": {path: filepath.Join(base, "a", "b", "c"), want: "resolve project directory"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			id, err := ProjectID(tc.path)
			if err == nil {
				t.Fatalf("ProjectID(%q) = %s, want an error", tc.path, id)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not explain the problem (want it to mention %q)", err, tc.want)
			}
		})
	}
}

func TestResolveProjectPath_ReturnsTheResolvedAbsolutePath_REQ_11_2(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	project := filepath.Join(base, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatalf("create project directory: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(project, link); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	want, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	got, err := ResolveProjectPath(link)
	if err != nil {
		t.Fatalf("ResolveProjectPath(%s): %v", link, err)
	}
	if got != want {
		t.Errorf("ResolveProjectPath(%s) = %s, want %s", link, got, want)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("ResolveProjectPath returned a relative path %q", got)
	}
}
