package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The archives built here carry what the real ones carry, in the same layout:
// the macOS tar.gz holds avr, LICENSE and README.md at the top level, and the
// Windows zip holds avr.exe, avar.exe, avrw.exe, LICENSE and README.md. Both were
// listed
// from release v0.12.12 while this was written (avar.exe was added by #109).
func writeTarGz(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	path := filepath.Join(dir, "avar_1.0.0_darwin_all.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create the test archive: %v", err)
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		header := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatalf("write the tar header for %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("write %s into the tar: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close the tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close the gzip stream: %v", err)
	}
	return path
}

func writeZip(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	path := filepath.Join(dir, "avar_1.0.0_windows_amd64.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create the test archive: %v", err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("add %s to the zip: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write %s into the zip: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close the zip: %v", err)
	}
	return path
}

func TestExtract_TakesTheNamedMembersOnly_REQ_19_6(t *testing.T) {
	dir := t.TempDir()
	archive := writeTarGz(t, dir, map[string]string{
		"avr":       "the new avr",
		"LICENSE":   "Apache-2.0",
		"README.md": "# avar",
	})

	install := t.TempDir()
	target := func(member string) string { return filepath.Join(install, member+NewSuffix) }
	if err := Extract(archive, Members("darwin"), target); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(install, "avr"+NewSuffix))
	if err != nil {
		t.Fatalf("reading the extracted binary: %v", err)
	}
	if string(body) != "the new avr" {
		t.Errorf("extracted %q, want the archive's avr", body)
	}
	entries, err := os.ReadDir(install)
	if err != nil {
		t.Fatalf("listing the install directory: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("the install directory holds %d files, want only the staged binary: %v", len(entries), entries)
	}
	// Only where the mode decides what may be executed. Windows decides by
	// the extension, and Go reports 0666 or 0444 for every file there, so
	// this assertion would fail on a host it says nothing about.
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Join(install, "avr"+NewSuffix)); err != nil {
			t.Fatalf("stat: %v", err)
		} else if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("the staged binary has mode %v, and a binary avar cannot execute is not an update", info.Mode())
		}
	}
}

func TestExtract_WindowsArchiveCarriesEveryProgram_REQ_19_7(t *testing.T) {
	dir := t.TempDir()
	archive := writeZip(t, dir, map[string]string{
		"avr.exe":   "the new avr.exe",
		"avar.exe":  "the new avar.exe",
		"avrw.exe":  "the new avrw.exe",
		"LICENSE":   "Apache-2.0",
		"README.md": "# avar",
	})

	install := t.TempDir()
	target := func(member string) string { return filepath.Join(install, member+NewSuffix) }
	if err := Extract(archive, Members("windows"), target); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	for member, want := range map[string]string{"avr.exe": "the new avr.exe", "avar.exe": "the new avar.exe", "avrw.exe": "the new avrw.exe"} {
		body, err := os.ReadFile(filepath.Join(install, member+NewSuffix))
		if err != nil {
			t.Fatalf("reading the extracted %s: %v", member, err)
		}
		if string(body) != want {
			t.Errorf("%s = %q, want %q", member, body, want)
		}
	}
}

// An archive missing one of the programs is refused, and what was already
// written is removed: a new avr.exe beside a helper from an older release is
// the broken idle check of #100, and half an update is worse than none.
func TestExtract_RefusesAnArchiveMissingAMember_REQ_19_7(t *testing.T) {
	dir := t.TempDir()
	archive := writeZip(t, dir, map[string]string{"avr.exe": "the new avr.exe", "avar.exe": "the new avar.exe"})

	install := t.TempDir()
	target := func(member string) string { return filepath.Join(install, member+NewSuffix) }
	err := Extract(archive, Members("windows"), target)
	if err == nil {
		t.Fatal("Extract accepted an archive with no avrw.exe")
	}
	if !strings.Contains(err.Error(), "avrw.exe") {
		t.Errorf("error = %v, want it to name the missing program", err)
	}
	entries, err := os.ReadDir(install)
	if err != nil {
		t.Fatalf("listing the install directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a refused extraction left %v behind", entries)
	}
}

// Nothing an archive says about where a file goes is obeyed. Members are
// matched on their base name and written to paths avar computed, so an entry
// pointing out of the directory is simply not one of the names it asked for.
func TestExtract_IgnoresWhereTheArchiveSaysAFileGoes_REQ_19_6(t *testing.T) {
	dir := t.TempDir()
	archive := writeTarGz(t, dir, map[string]string{
		"../../../../tmp/avr": "somebody else's avr",
		"avr":                 "the new avr",
	})

	install := t.TempDir()
	target := func(member string) string { return filepath.Join(install, member+NewSuffix) }
	if err := Extract(archive, Members("darwin"), target); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	entries, err := os.ReadDir(install)
	if err != nil {
		t.Fatalf("listing the install directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "avr"+NewSuffix {
		t.Fatalf("the install directory holds %v, want only the staged binary", entries)
	}
	// Both entries have the base name avr, so whichever the tar reader
	// reaches first wins; what matters is that only the computed path was
	// written, and that nothing landed outside the directory.
	if _, err := os.Stat(filepath.Join(dir, "..", "..", "..", "..", "tmp", "avr")); err == nil {
		t.Error("the traversal entry was written outside the install directory")
	}
}

// A file avar cannot make sense of is an error naming it rather than a partial
// install.
func TestExtract_RefusesAnArchiveItCannotRead(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "avar_1.0.0_darwin_all.tar.gz")
	if err := os.WriteFile(broken, []byte("not a gzip stream"), 0o644); err != nil {
		t.Fatalf("writing the broken archive: %v", err)
	}
	install := t.TempDir()
	target := func(member string) string { return filepath.Join(install, member+NewSuffix) }
	if err := Extract(broken, Members("darwin"), target); err == nil {
		t.Error("Extract accepted a file that is not an archive")
	}

	unknown := filepath.Join(dir, "avar_1.0.0_darwin_all.tar.xz")
	if err := os.WriteFile(unknown, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing the unknown archive: %v", err)
	}
	if err := Extract(unknown, Members("darwin"), target); err == nil {
		t.Error("Extract accepted an archive format avar does not unpack")
	}
}
