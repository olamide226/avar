package cmd

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/update"
)

// These are flow tests for `avr update`: they drive the real command against a
// recorded release and an install directory of their own.
//
// Nothing here reaches the network, and nothing here replaces a binary outside
// t.TempDir(). Both matter more for this command than for any other: its
// successful path downloads a program and overwrites an executable, and a test
// that did either for real would be the third entry in docs/lessons.md about a
// test that escaped into the host. The App's updateHost seam is what keeps it
// in: a flow test that forgot to set it would reach github.com, so every test
// below goes through newUpdateTest.

// updateTest is an App whose whole idea of this computer is a temporary
// directory and a recorded release.
type updateTest struct {
	*App
	out, err *bytes.Buffer
	// dir is the install directory: what avar would replace files in.
	dir string
	// binary is the running avr inside it.
	binary string
	http   *recordedRelease
}

func newUpdateTest(t *testing.T, goos, goarch, version string) *updateTest {
	t.Helper()

	dir := t.TempDir()
	name := "avr"
	if goos == "windows" {
		name = "avr.exe"
	}
	binary := filepath.Join(dir, name)

	served := &recordedRelease{replies: map[string][]byte{}, statuses: map[string]int{}}
	app := &App{Version: version, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	app.updateHost = &updateHost{
		GOOS:       goos,
		GOARCH:     goarch,
		Executable: func() (string, error) { return binary, nil },
		// Identity rather than the real resolveLinks: on macOS a path
		// under t.TempDir() resolves through /private, and a test that
		// compared the two would be testing the temporary directory.
		// Detection over real cask and winget paths, links and all, is
		// a table test in internal/update.
		Resolve: func(path string) string { return path },
		// The install directory here *is* a temporary one, which the
		// real guard exists to refuse. It is given its own test below,
		// with the real guard.
		Temporary: func(string) bool { return false },
		HTTP:      served,
		Ops:       update.OSFileOps{},
		Repo:      update.DefaultRepo,
	}
	// A test must never reach the real state directory or a real backend
	// either, and `avr update` asks for neither; mark both done so that a
	// future change which does is a failure rather than a visit to ~/.avr.
	app.once.provider.Do(func() {})
	app.once.store.Do(func() {})

	return &updateTest{App: app, out: app.Out.(*bytes.Buffer), err: app.Err.(*bytes.Buffer), dir: dir, binary: binary, http: served}
}

// install writes a file into the install directory, as though it had been
// unpacked there.
func (u *updateTest) install(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(u.dir, name), []byte(body), 0o755); err != nil {
		t.Fatalf("writing %s into the test install: %v", name, err)
	}
}

// contents is the install directory as a sorted "name=body" list.
func (u *updateTest) contents(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(u.dir)
	if err != nil {
		t.Fatalf("listing the test install: %v", err)
	}
	var lines []string
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(u.dir, entry.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", entry.Name(), err)
		}
		lines = append(lines, entry.Name()+"="+string(body))
	}
	return strings.Join(lines, " ")
}

func (u *updateTest) run(t *testing.T, args ...string) error {
	t.Helper()
	return runUpdate(context.Background(), u.App, cli.Invocation{
		Mode: cli.ModeSubcommand, Subcommand: "update", SubcommandArgs: args,
	})
}

// recordedRelease answers the two or three requests an update makes, and
// records every URL it was asked for, so a test can assert that a command
// asked for nothing at all.
type recordedRelease struct {
	replies  map[string][]byte
	statuses map[string]int
	requests []string
}

func (r *recordedRelease) Do(req *http.Request) (*http.Response, error) {
	url := req.URL.String()
	r.requests = append(r.requests, url)
	status := http.StatusOK
	if s, ok := r.statuses[url]; ok {
		status = s
	}
	body, ok := r.replies[url]
	if !ok {
		status = http.StatusNotFound
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}, nil
}

const (
	latestReleaseURL = "https://api.github.com/repos/olamide226/avar/releases/latest"
	archiveURL       = "https://releases.invalid/archive"
	checksumsURL     = "https://releases.invalid/checksums.txt"
)

// serveRelease records a release of the given version whose archive is the
// bytes given, with a checksums.txt that matches them.
func (r *recordedRelease) serveRelease(tag, archiveName string, archive []byte, checksum string) {
	if checksum == "" {
		sum := sha256.Sum256(archive)
		checksum = hex.EncodeToString(sum[:])
	}
	sums := fmt.Sprintf("%s  %s\n", checksum, archiveName)
	r.replies[latestReleaseURL] = []byte(fmt.Sprintf(`{
	  "tag_name": %q,
	  "assets": [
	    {"name": %q, "browser_download_url": %q, "size": %d},
	    {"name": "checksums.txt", "browser_download_url": %q, "size": %d}
	  ]
	}`, tag, archiveName, archiveURL, len(archive), checksumsURL, len(sums)))
	r.replies[archiveURL] = archive
	r.replies[checksumsURL] = []byte(sums)
}

// tarGz and zipped build the archives a release publishes, in the shape the
// real ones have: the macOS tar.gz carries avr, the Windows zip carries
// avr.exe, avar.exe and avrw.exe, and both carry LICENSE and README.md.
func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatalf("building the test archive: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("building the test archive: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("building the test archive: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("building the test archive: %v", err)
	}
	return buf.Bytes()
}

func zipped(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("building the test archive: %v", err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("building the test archive: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("building the test archive: %v", err)
	}
	return buf.Bytes()
}

// A package manager's installation is its own to update. avar says which
// command does it and stops: no request, and not a byte changed (PROP-26).
func TestUpdate_HomebrewCaskPrintsTheCommandAndChangesNothing_REQ_19_2(t *testing.T) {
	u := newUpdateTest(t, "darwin", "arm64", "0.12.11")
	u.install(t, "avr", "the installed avar")
	before := u.contents(t)
	u.App.updateHost.Executable = func() (string, error) {
		return "/opt/homebrew/Caskroom/avar/0.12.11/avr", nil
	}

	if err := u.run(t); err != nil {
		t.Fatalf("avr update: %v", err)
	}
	if !strings.Contains(u.out.String(), "brew upgrade --cask avar") {
		t.Errorf("output does not name the command that updates a cask:\n%s", u.out)
	}
	if !strings.Contains(u.out.String(), "Homebrew") {
		t.Errorf("output does not say who owns this installation:\n%s", u.out)
	}
	if len(u.http.requests) != 0 {
		t.Errorf("it made %v; a cask install needs nothing from the network", u.http.requests)
	}
	if got := u.contents(t); got != before {
		t.Errorf("the install directory changed:\n%s\nwant\n%s", got, before)
	}
}

func TestUpdate_WingetPrintsTheCommandAndChangesNothing_REQ_19_3(t *testing.T) {
	u := newUpdateTest(t, "windows", "amd64", "0.12.11")
	u.install(t, "avr.exe", "the installed avar")
	u.install(t, "avar.exe", "the installed alias")
	u.install(t, "avrw.exe", "the installed helper")
	before := u.contents(t)
	u.App.updateHost.Executable = func() (string, error) {
		return `C:\Users\dev\AppData\Local\Microsoft\WinGet\Packages\olamide226.avar_Microsoft.Winget.Source_8wekyb3d8bbwe\avr.exe`, nil
	}

	if err := u.run(t); err != nil {
		t.Fatalf("avr update: %v", err)
	}
	if !strings.Contains(u.out.String(), "winget upgrade olamide226.avar") {
		t.Errorf("output does not name the command that updates a winget package:\n%s", u.out)
	}
	// REQ-19.8: say when an update needs elevation or a new shell, rather
	// than meeting either halfway through.
	if !strings.Contains(u.out.String(), "administrator") || !strings.Contains(u.out.String(), "already open") {
		t.Errorf("output does not mention elevation and the shell that is already open:\n%s", u.out)
	}
	if len(u.http.requests) != 0 {
		t.Errorf("it made %v; a winget install needs nothing from the network", u.http.requests)
	}
	if got := u.contents(t); got != before {
		t.Errorf("the install directory changed:\n%s\nwant\n%s", got, before)
	}
}

// Up to date is an answer, not an error, and it costs one request.
func TestUpdate_SaysSoWhenThereIsNothingNewer_REQ_19_4(t *testing.T) {
	u := newUpdateTest(t, "darwin", "arm64", "0.12.12")
	u.install(t, "avr", "the installed avar")
	before := u.contents(t)
	u.http.serveRelease("v0.12.12", "avar_0.12.12_darwin_all.tar.gz", tarGz(t, map[string]string{"avr": "the new avar"}), "")

	if err := u.run(t); err != nil {
		t.Fatalf("avr update: %v", err)
	}
	if !strings.Contains(u.out.String(), "0.12.12 is the latest release") {
		t.Errorf("output does not say avar is up to date:\n%s", u.out)
	}
	if len(u.http.requests) != 1 || u.http.requests[0] != latestReleaseURL {
		t.Errorf("requests = %v, want the release document alone", u.http.requests)
	}
	if got := u.contents(t); got != before {
		t.Errorf("the install directory changed:\n%s\nwant\n%s", got, before)
	}
}

// The whole of the macOS path: verify, unpack, and rename over the installed
// binary.
func TestUpdate_ReplacesTheInstalledBinary_REQ_19_6(t *testing.T) {
	u := newUpdateTest(t, "darwin", "arm64", "0.12.11")
	u.install(t, "avr", "the installed avar")
	u.http.serveRelease("v0.12.12", "avar_0.12.12_darwin_all.tar.gz", tarGz(t, map[string]string{
		"avr": "the new avar", "LICENSE": "Apache-2.0", "README.md": "# avar",
	}), "")

	if err := u.run(t); err != nil {
		t.Fatalf("avr update: %v", err)
	}
	if got := u.contents(t); got != "avr=the new avar" {
		t.Errorf("the install directory holds %q, want the new binary and nothing else", got)
	}
	for _, want := range []string{"0.12.11 to 0.12.12", "checksums.txt", "Gatekeeper"} {
		if !strings.Contains(u.out.String(), want) {
			t.Errorf("output does not mention %q:\n%s", want, u.out)
		}
	}
	// Only where the mode decides what may be executed. Windows decides by
	// the extension and reports 0666 for every file, so this assertion
	// would fail there on a question that host does not ask.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(u.binary)
		if err != nil {
			t.Fatalf("stat the updated binary: %v", err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("the installed binary has mode %v, and one avar cannot execute is not an update", info.Mode())
		}
	}
}

// Windows replaces every program the archive ships — avr.exe, the avar.exe
// alias of REQ-18.17, and the avrw.exe idle check of REQ-18.16 — and keeps
// each file it replaced, because it cannot delete the image of a running
// program.
func TestUpdate_WindowsReplacesEveryProgramAndKeepsTheOldOnes_REQ_19_7(t *testing.T) {
	u := newUpdateTest(t, "windows", "amd64", "0.12.11")
	u.install(t, "avr.exe", "the installed avar")
	u.install(t, "avar.exe", "the installed alias")
	u.install(t, "avrw.exe", "the installed helper")
	u.http.serveRelease("v0.12.12", "avar_0.12.12_windows_amd64.zip", zipped(t, map[string]string{
		"avr.exe": "the new avar", "avar.exe": "the new alias", "avrw.exe": "the new helper", "LICENSE": "Apache-2.0",
	}), "")

	if err := u.run(t); err != nil {
		t.Fatalf("avr update: %v", err)
	}
	want := "avar.exe=the new alias avar.exe.avr-old=the installed alias " +
		"avr.exe=the new avar avr.exe.avr-old=the installed avar " +
		"avrw.exe=the new helper avrw.exe.avr-old=the installed helper"
	if got := u.contents(t); got != want {
		t.Errorf("the install directory holds\n  %s\nwant\n  %s", got, want)
	}
	for _, phrase := range []string{"avr.exe, avar.exe and avrw.exe", "avr.exe.avr-old", "SmartScreen"} {
		if !strings.Contains(u.out.String(), phrase) {
			t.Errorf("output does not mention %q:\n%s", phrase, u.out)
		}
	}
}

// A Windows install that has only avr.exe — somebody who copied one file out
// of the zip — gains the other two rather than having the update refused.
func TestUpdate_WindowsAddsTheProgramsTheInstallLacks_REQ_19_7(t *testing.T) {
	u := newUpdateTest(t, "windows", "amd64", "0.12.11")
	u.install(t, "avr.exe", "the installed avar")
	u.http.serveRelease("v0.12.12", "avar_0.12.12_windows_amd64.zip", zipped(t, map[string]string{
		"avr.exe": "the new avar", "avar.exe": "the new alias", "avrw.exe": "the new helper",
	}), "")

	if err := u.run(t); err != nil {
		t.Fatalf("avr update: %v", err)
	}
	want := "avar.exe=the new alias avr.exe=the new avar avr.exe.avr-old=the installed avar avrw.exe=the new helper"
	if got := u.contents(t); got != want {
		t.Errorf("the install directory holds\n  %s\nwant\n  %s", got, want)
	}
}

// A binary saved under a name the archive does not have is still the one the
// user runs, so it is the one replaced.
func TestUpdate_ReplacesARenamedBinary_REQ_19_6(t *testing.T) {
	u := newUpdateTest(t, "darwin", "arm64", "0.12.11")
	u.install(t, "avr-1.2", "the installed avar")
	u.App.updateHost.Executable = func() (string, error) { return filepath.Join(u.dir, "avr-1.2"), nil }
	u.http.serveRelease("v0.12.12", "avar_0.12.12_darwin_all.tar.gz", tarGz(t, map[string]string{"avr": "the new avar"}), "")

	if err := u.run(t); err != nil {
		t.Fatalf("avr update: %v", err)
	}
	if got := u.contents(t); got != "avr-1.2=the new avar" {
		t.Errorf("the install directory holds %q, want the binary the user runs replaced in place", got)
	}
}

// The file an earlier update left aside is removed by this one, which is the
// later run that can do it.
func TestUpdate_SweepsWhatAnEarlierUpdateLeft_REQ_19_7(t *testing.T) {
	u := newUpdateTest(t, "windows", "amd64", "0.12.12")
	u.install(t, "avr.exe", "the installed avar")
	u.install(t, "avr.exe.avr-old", "an avar from two releases ago")
	u.http.serveRelease("v0.12.12", "avar_0.12.12_windows_amd64.zip", zipped(t, map[string]string{"avr.exe": "x", "avar.exe": "y", "avrw.exe": "z"}), "")

	if err := u.run(t); err != nil {
		t.Fatalf("avr update: %v", err)
	}
	if got := u.contents(t); got != "avr.exe=the installed avar" {
		t.Errorf("the install directory holds %q, want the leftover gone and nothing else touched", got)
	}
	if !strings.Contains(u.err.String(), "avr.exe.avr-old") {
		t.Errorf("stderr does not say what was removed:\n%s", u.err)
	}
}

// PROP-26's first clause, through the whole command: a download that is not
// the file the release published is never unpacked.
func TestProp_UpdateWritesNothingWithoutAMatchingChecksum_PROP_26(t *testing.T) {
	archive := tarGz(t, map[string]string{"avr": "an avar from somewhere else"})
	good := sha256.Sum256(archive)

	tests := []struct {
		name     string
		archive  []byte
		checksum string
		wantIn   string
	}{
		{
			name:     "the bytes were altered",
			archive:  archive,
			checksum: strings.Repeat("a", 64),
			wantIn:   "does not match the checksum",
		},
		{
			name:     "the checksums name a different file",
			archive:  archive,
			checksum: hex.EncodeToString(good[:]),
			wantIn:   "does not match the checksum",
		},
		{
			name:     "the download ended early",
			archive:  archive[:len(archive)/2],
			checksum: hex.EncodeToString(good[:]),
			wantIn:   "ended after",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newUpdateTest(t, "darwin", "arm64", "0.12.11")
			u.install(t, "avr", "the installed avar")
			before := u.contents(t)

			archiveName := "avar_0.12.12_darwin_all.tar.gz"
			u.http.serveRelease("v0.12.12", archiveName, archive, tt.checksum)
			if tt.name == "the checksums name a different file" {
				// The file is fine and its checksum is right;
				// the release simply does not record one under
				// this name, which avar cannot verify either.
				u.http.replies[checksumsURL] = []byte(hex.EncodeToString(good[:]) + "  some_other_file.tar.gz\n")
			}
			if tt.name == "the download ended early" {
				u.http.replies[archiveURL] = tt.archive
			}

			err := u.run(t)
			if err == nil {
				t.Fatalf("avr update accepted an archive it could not verify")
			}
			if !strings.Contains(err.Error(), tt.wantIn) {
				t.Errorf("error = %v, want it to mention %q", err, tt.wantIn)
			}
			if got := u.contents(t); got != before {
				t.Errorf("the install directory holds\n  %s\nwant what was there before\n  %s", got, before)
			}
		})
	}
}

// A release that does not publish this computer's archive is an error naming
// what was looked for. Nothing is guessed and nothing is downloaded.
func TestUpdate_RefusesWhenTheReleaseHasNoArchiveForThisHost_REQ_19_9(t *testing.T) {
	u := newUpdateTest(t, "darwin", "arm64", "0.12.11")
	u.install(t, "avr", "the installed avar")
	u.http.serveRelease("v0.12.12", "avar_0.12.12_windows_amd64.zip", zipped(t, map[string]string{"avr.exe": "x", "avar.exe": "y", "avrw.exe": "z"}), "")

	err := u.run(t)
	if err == nil {
		t.Fatal("avr update carried on without an archive for this computer")
	}
	if !strings.Contains(err.Error(), "avar_0.12.12_darwin_all.tar.gz") {
		t.Errorf("error = %v, want it to name the archive that is missing", err)
	}
	if got := u.contents(t); got != "avr=the installed avar" {
		t.Errorf("the install directory changed: %s", got)
	}
}

func TestUpdate_RefusesWhenThereIsNoReleaseToRead_REQ_19_9(t *testing.T) {
	u := newUpdateTest(t, "darwin", "arm64", "0.12.11")
	u.install(t, "avr", "the installed avar")
	// Nothing is recorded, so the double answers 404.

	err := u.run(t)
	if err == nil {
		t.Fatal("avr update carried on without a release")
	}
	if !strings.Contains(err.Error(), "no release") {
		t.Errorf("error = %v, want it to say there is no release to read", err)
	}
	if got := u.contents(t); got != "avr=the installed avar" {
		t.Errorf("the install directory changed: %s", got)
	}
}

// A build from source is not a release avar may replace, and it is refused
// before any request (REQ-19.9).
func TestUpdate_RefusesABuildThatIsNotARelease_REQ_19_9(t *testing.T) {
	for _, version := range []string{"dev", "v0.12.12-3-gabc1234"} {
		t.Run(version, func(t *testing.T) {
			u := newUpdateTest(t, "darwin", "arm64", version)
			u.install(t, "avr", "the installed avar")

			err := u.run(t)
			if err == nil {
				t.Fatal("avr update offered to replace a build from source")
			}
			if !strings.Contains(err.Error(), "not a release version") {
				t.Errorf("error = %v, want it to say this is not a release", err)
			}
			if len(u.http.requests) != 0 {
				t.Errorf("it made %v before refusing", u.http.requests)
			}
		})
	}
}

// The real guard, against the real temporary directories: a binary that will
// not be there later is not one to install into. This is the one test that
// uses inTemporaryDir itself, because every other test's install directory is
// a temporary one.
func TestUpdate_RefusesToInstallIntoATemporaryDirectory_REQ_19_9(t *testing.T) {
	u := newUpdateTest(t, "darwin", "arm64", "0.12.11")
	u.install(t, "avr", "the installed avar")
	u.App.updateHost.Temporary = inTemporaryDir

	err := u.run(t)
	if err == nil {
		t.Fatal("avr update offered to install into a temporary folder")
	}
	if !strings.Contains(err.Error(), "temporary folder") {
		t.Errorf("error = %v, want it to say why a temporary folder is refused", err)
	}
	if len(u.http.requests) != 0 {
		t.Errorf("it made %v before refusing", u.http.requests)
	}
}

func TestUpdate_TakesNoArguments(t *testing.T) {
	u := newUpdateTest(t, "darwin", "arm64", "0.12.11")
	u.install(t, "avr", "the installed avar")

	err := u.run(t, "--force")
	var exit *ExitCodeError
	if !errors.As(err, &exit) || exit.Code != exitUsage {
		t.Fatalf("avr update --force = %v, want exit %d", err, exitUsage)
	}
	if len(u.http.requests) != 0 {
		t.Errorf("it made %v for a command line it could not read", u.http.requests)
	}
}

// The new binary is written beside the installed one before anything moves, so
// an install directory avar may not write to fails with everything still as it
// was — and the message says what would let the write through (REQ-19.6,
// REQ-19.8).
func TestUpdate_RefusesAnInstallDirectoryItCannotWrite_REQ_19_8(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only directory is not how Windows refuses a write")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, which no directory mode refuses")
	}

	u := newUpdateTest(t, "darwin", "arm64", "0.12.11")
	u.install(t, "avr", "the installed avar")
	u.http.serveRelease("v0.12.12", "avar_0.12.12_darwin_all.tar.gz", tarGz(t, map[string]string{"avr": "the new avar"}), "")

	if err := os.Chmod(u.dir, 0o555); err != nil {
		t.Fatalf("making the install directory read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(u.dir, 0o755) })

	err := u.run(t)
	if err == nil {
		t.Fatal("avr update reported success for a directory it cannot write to")
	}
	if !strings.Contains(err.Error(), "still installed and was not changed") {
		t.Errorf("error = %v, want it to say the installed avar is untouched", err)
	}
	if !strings.Contains(err.Error(), "owns it") {
		t.Errorf("error = %v, want it to say what would let the write through", err)
	}
	if err := os.Chmod(u.dir, 0o755); err != nil {
		t.Fatalf("restoring the install directory: %v", err)
	}
	if got := u.contents(t); got != "avr=the installed avar" {
		t.Errorf("the install directory holds %q, want the binary that was there", got)
	}
}
