package state

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// REQ-18.13, PROP-14: Windows filesystems are case-insensitive and accept both
// separators, so a user who reaches one project by different routes — a
// shortcut, a shell that lower-cases, a script that joins with forward slashes —
// must get one environment and one set of remembered choices, not a second
// project record with a second machine behind it.
func TestPathKey_EquivalentWindowsSpellingsShareOneIdentity_REQ_18_13(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("path keys fold case only where the filesystem does")
	}

	canonical := `C:\Users\ola\code\app`
	equivalent := []string{
		`C:\Users\ola\code\app`,
		`c:\users\ola\code\app`,
		`C:\USERS\OLA\CODE\APP`,
		`C:/Users/ola/code/app`,
		`C:\Users\ola\code\app\`,
		`C:\Users\ola\code\.\app`,
		`C:\Users\ola\other\..\code\app`,
	}

	want := PathKey(canonical)
	for _, spelling := range equivalent {
		if got := PathKey(spelling); got != want {
			t.Errorf("PathKey(%q) = %q, want %q — the same directory would become two projects", spelling, got, want)
		}
	}
}

// The other half of PROP-14: distinct directories must stay distinct, or two
// projects would share one environment and one set of remembered choices.
func TestPathKey_DistinctWindowsPathsStayDistinct_PROP_14(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("path keys fold case only where the filesystem does")
	}

	distinct := []string{
		`C:\Users\ola\code\app`,
		`C:\Users\ola\code\app2`,
		`C:\Users\ola\work\app`,
		`D:\Users\ola\code\app`,
		`C:\`,
		`D:\`,
	}

	seen := map[string]string{}
	for _, path := range distinct {
		key := PathKey(path)
		if other, clash := seen[key]; clash {
			t.Errorf("%s and %s share the key %q", other, path, key)
		}
		seen[key] = path
	}
}

// A drive root is a path whose trailing separator is part of its name. Trimming
// it would turn C:\ into C:, which names the drive's current directory rather
// than its root — a different place entirely.
func TestPathKey_DriveRootKeepsItsSeparator_REQ_18_13(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive roots are a Windows concept")
	}

	if got := PathKey(`C:\`); !strings.HasSuffix(got, `c:\`) {
		t.Errorf("PathKey(`C:\\`) = %q, want it to keep the root separator", got)
	}
}

// On a case-sensitive filesystem, folding case would merge two directories that
// really can both exist. The key is therefore the path itself (REQ-11.2).
func TestPathKey_IsCaseSensitiveOnUnix_REQ_11_2(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this is the case-sensitive host's rule")
	}

	if PathKey("/Users/dev/Code") == PathKey("/Users/dev/code") {
		t.Error("two directories a case-sensitive filesystem keeps apart share one identity")
	}
	if got := PathKey("/Users/dev/code"); got != "/Users/dev/code" {
		t.Errorf("PathKey = %q, want the resolved path itself: every identity avar has written is the hash of exactly this", got)
	}
}

// The identity a project record carries has to be the identity of its own key,
// or projects.json would describe a derivation that did not happen.
func TestEnsureProject_RecordsTheKeyItsIdentityCameFrom_REQ_18_13(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	dir := mkdir(t, "code", "app")

	rec, err := st.EnsureProject(dir)
	if err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	resolved, err := ResolveProjectPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rec.PathKey != PathKey(resolved) {
		t.Errorf("PathKey = %q, want %q", rec.PathKey, PathKey(resolved))
	}
	if rec.ID != hashPath(resolved) {
		t.Errorf("ID = %q, want the hash of the key", rec.ID)
	}
	// The displayed path keeps the filesystem's own spelling: a lower-cased
	// path would be correct to Windows and wrong on screen, and it is also the
	// directory avar shares into the guest.
	if rec.Path != resolved {
		t.Errorf("Path = %q, want the resolved spelling %q", rec.Path, resolved)
	}
}

// Two spellings of one directory must reach one record, which is the whole
// point of the key and the thing a hash of the raw path would get wrong.
func TestEnsureProject_TwoSpellingsReachOneRecord_REQ_18_13(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "windows" {
		t.Skip("only a case-insensitive filesystem has two spellings of one directory")
	}

	st := newTestStore(t)
	dir := mkdir(t, "code", "app")

	first, err := st.EnsureProject(dir)
	if err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	second, err := st.EnsureProject(strings.ToUpper(dir))
	if err != nil {
		t.Fatalf("EnsureProject with an upper-cased spelling: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("two spellings of %s produced two projects (%s and %s)", dir, first.ID, second.ID)
	}

	projects, err := st.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Errorf("Projects() = %d records, want one: %v", len(projects), projects)
	}
}

// REQ-18.13: avar's state describes this machine's environments — distributions
// registered with this machine's WSL, disk images on this machine's filesystem,
// process ids of sessions running on it. A roaming profile is copied to every
// machine the user signs in to, which would carry records of environments that
// cannot exist onto machines that cannot have them.
func TestDefaultStateDir_IsPerUserAndNotRoaming_REQ_18_13(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("roaming profiles are a Windows concept")
	}

	local := filepath.Join(t.TempDir(), "AppData", "Local")
	t.Setenv("LocalAppData", local)

	got, err := defaultStateDir()
	if err != nil {
		t.Fatalf("defaultStateDir: %v", err)
	}
	if want := filepath.Join(local, stateDirName); got != want {
		t.Errorf("defaultStateDir() = %q, want %q", got, want)
	}
	// Local and Roaming are sibling directories in the profile, so the
	// difference is one path component and that is what is checked — a
	// substring search would match any enclosing directory that happened to
	// have the word in its name.
	for _, component := range strings.Split(got, string(filepath.Separator)) {
		if strings.EqualFold(component, "Roaming") {
			t.Errorf("the state directory %q is in the roaming profile", got)
		}
	}
}

// LocalAppData is set for every interactive session, but a service account or a
// stripped environment can be without it. The documented location beneath the
// profile is the same directory by another name.
func TestDefaultStateDir_FallsBackToTheProfile_REQ_18_13(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("this is the Windows fallback")
	}

	home := t.TempDir()
	t.Setenv("LocalAppData", "")
	t.Setenv("USERPROFILE", home)

	got, err := defaultStateDir()
	if err != nil {
		t.Fatalf("defaultStateDir: %v", err)
	}
	if want := filepath.Join(home, "AppData", "Local", stateDirName); got != want {
		t.Errorf("defaultStateDir() = %q, want %q", got, want)
	}
}
