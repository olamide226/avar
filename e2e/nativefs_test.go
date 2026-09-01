//go:build e2e && windows

// Linux-native workspace mode against a real WSL 2 distribution (REQ-14).
//
// These are the tests that put the generated guest scripts in front of the real
// `find`, `sha256sum` and `cp`. That matters more here than almost anywhere else
// in avar: the scan is a parse of another tool's output, and a parse that agrees
// only with its author's idea of the format is exactly the mistake this
// repository has already made once (docs/lessons.md, "A test double that shares
// the code's assumption confirms the assumption, not the behaviour"). Everything
// below reads what really landed on disk rather than what avar said it did.

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// REQ-14.1: `avr --native-fs` puts a copy of the project on the distribution's
// own filesystem and runs the session there, not in the DrvFS share.
func TestWSLNative_RunsOnTheLinuxFilesystem_REQ_14_1(t *testing.T) {
	requireWSL(t)
	dir := project(t, "native")
	write(t, filepath.Join(dir, "main.go"), "package main\n")
	write(t, filepath.Join(dir, "sub", "note.txt"), "hello\n")

	stdout, stderr, code := avr(t, dir, suiteEnv(t), "--native-fs", "sh", "-c", "pwd; cat main.go")
	if code != 0 {
		t.Fatalf("avr --native-fs exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	cwd := strings.TrimSpace(lines[0])
	if strings.HasPrefix(cwd, "/mnt/") {
		t.Errorf("the session ran at %q, which is still the Windows filesystem", cwd)
	}
	if !strings.Contains(cwd, "/workspaces/") {
		t.Errorf("the session ran at %q, want the native workspace", cwd)
	}
	if !strings.Contains(stdout, "package main") {
		t.Errorf("the project was not copied in:\n%s", stdout)
	}

	// The copy is the whole tree, subdirectories included, and it belongs to
	// the user's account rather than to root — every script avar sends runs as
	// root, so a workspace nobody can write to is one wrong `chown` away.
	stdout, stderr, code = avr(t, dir, suiteEnv(t), "--native-fs", "sh", "-c",
		`cat sub/note.txt; stat -c '%U' . main.go`)
	if code != 0 {
		t.Fatalf("inspecting the workspace exited %d\nstderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "hello") {
		t.Errorf("a subdirectory was not copied:\n%s", stdout)
	}
	if strings.Contains(stdout, "root") {
		t.Errorf("the workspace belongs to root, so the user cannot write to their own project:\n%s", stdout)
	}
}

// REQ-14.2: work done in Linux comes back to the host, and only after the user
// has been shown what will change.
func TestWSLNative_SyncsGuestChangesBackAfterReview_REQ_14_2(t *testing.T) {
	requireWSL(t)
	dir := project(t, "native-sync")
	write(t, filepath.Join(dir, "main.go"), "package main\n")

	if _, stderr, code := avr(t, dir, suiteEnv(t), "--native-fs", "true"); code != 0 {
		t.Fatalf("creating the workspace exited %d\nstderr:\n%s", code, stderr)
	}

	// Change the project inside Linux only.
	if _, stderr, code := avr(t, dir, suiteEnv(t), "--native-fs", "sh", "-c",
		`printf 'package main // edited in linux\n' > main.go; printf 'new\n' > added.txt`); code != 0 {
		t.Fatalf("editing in the workspace exited %d\nstderr:\n%s", code, stderr)
	}

	// A bare `avr sync` reviews and applies nothing.
	stdout, stderr, code := avr(t, dir, suiteEnv(t), "sync")
	if code != 0 {
		t.Fatalf("avr sync exited %d\nstderr:\n%s", code, stderr)
	}
	for _, want := range []string{"main.go", "added.txt", "modified", "added"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the review does not mention %q:\n%s", want, stdout)
		}
	}
	if body := read(t, filepath.Join(dir, "main.go")); strings.Contains(body, "edited in linux") {
		t.Fatalf("a bare `avr sync` changed the host copy: %q", body)
	}
	if _, err := os.Stat(filepath.Join(dir, "added.txt")); err == nil {
		t.Fatal("a bare `avr sync` created a file on the host")
	}

	// With a direction and --yes it applies, and the bytes really arrive.
	if _, stderr, code = avr(t, dir, suiteEnv(t), "sync", "--to-host", "--yes"); code != 0 {
		t.Fatalf("avr sync --to-host exited %d\nstderr:\n%s", code, stderr)
	}
	if body := read(t, filepath.Join(dir, "main.go")); !strings.Contains(body, "edited in linux") {
		t.Errorf("main.go on the host is %q, want the Linux edit", body)
	}
	if body := read(t, filepath.Join(dir, "added.txt")); strings.TrimSpace(body) != "new" {
		t.Errorf("added.txt on the host is %q", body)
	}

	// And the two copies now agree, so a second sync has nothing to do.
	stdout, _, _ = avr(t, dir, suiteEnv(t), "sync")
	if !strings.Contains(stdout, "synchronized") {
		t.Errorf("after syncing, avar still reports work outstanding:\n%s", stdout)
	}
}

// REQ-14.3: both copies changed the same file differently. avar reports it and
// leaves both exactly as they were.
func TestWSLNative_NeverOverwritesOnConflict_REQ_14_3(t *testing.T) {
	requireWSL(t)
	dir := project(t, "native-conflict")
	write(t, filepath.Join(dir, "shared.txt"), "original\n")

	if _, stderr, code := avr(t, dir, suiteEnv(t), "--native-fs", "true"); code != 0 {
		t.Fatalf("creating the workspace exited %d\nstderr:\n%s", code, stderr)
	}

	// Both sides edit the same file, differently.
	if _, stderr, code := avr(t, dir, suiteEnv(t), "--native-fs", "sh", "-c",
		`printf 'linux version\n' > shared.txt`); code != 0 {
		t.Fatalf("editing in the workspace exited %d\nstderr:\n%s", code, stderr)
	}
	write(t, filepath.Join(dir, "shared.txt"), "windows version\n")

	stdout, stderr, code := avr(t, dir, suiteEnv(t), "sync", "--to-host", "--yes")
	if code == 0 {
		t.Fatalf("a conflicted sync succeeded\nstdout:\n%s", stdout)
	}
	report := stdout + stderr
	if !strings.Contains(report, "shared.txt") || !strings.Contains(report, "will not overwrite") {
		t.Errorf("the conflict was not surfaced:\n%s", report)
	}

	// Neither copy moved.
	if body := read(t, filepath.Join(dir, "shared.txt")); strings.TrimSpace(body) != "windows version" {
		t.Errorf("the host copy is %q, want it untouched", body)
	}
	stdout, _, code = avr(t, dir, suiteEnv(t), "--native-fs", "cat", "shared.txt")
	if code != 0 {
		t.Fatalf("reading the Linux copy exited %d", code)
	}
	if strings.TrimSpace(stdout) != "linux version" {
		t.Errorf("the Linux copy is %q, want it untouched", stdout)
	}
}

// The build output native mode exists to keep in Linux must stay in Linux. A
// sync that carried node_modules back to the Windows filesystem would undo the
// entire benefit in the one direction the user asks for most.
func TestWSLNative_DoesNotCarryBuildOutputBack_REQ_14_1(t *testing.T) {
	requireWSL(t)
	dir := project(t, "native-exclude")
	write(t, filepath.Join(dir, "package.json"), "{}\n")

	if _, stderr, code := avr(t, dir, suiteEnv(t), "--native-fs", "true"); code != 0 {
		t.Fatalf("creating the workspace exited %d\nstderr:\n%s", code, stderr)
	}
	if _, stderr, code := avr(t, dir, suiteEnv(t), "--native-fs", "sh", "-c",
		`mkdir -p node_modules/pkg && echo module > node_modules/pkg/index.js && echo real > src.txt`); code != 0 {
		t.Fatalf("writing build output exited %d\nstderr:\n%s", code, stderr)
	}

	stdout, stderr, code := avr(t, dir, suiteEnv(t), "sync", "--to-host", "--yes")
	if code != 0 {
		t.Fatalf("avr sync --to-host exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); err == nil {
		t.Error("node_modules was copied onto the Windows filesystem")
	}
	if body := read(t, filepath.Join(dir, "src.txt")); strings.TrimSpace(body) != "real" {
		t.Errorf("the file that was not build output did not arrive: %q", body)
	}
}

// PROP-10/PROP-16: nothing about native mode changes what avar shares. The
// project is still mounted, and still the only Windows directory the guest can
// reach — a second copy is not a second door.
func TestWSLNative_AddsNoMounts_PROP_5(t *testing.T) {
	requireWSL(t)
	dir := project(t, "native-confinement")
	write(t, filepath.Join(dir, "main.go"), "package main\n")

	if _, stderr, code := avr(t, dir, suiteEnv(t), "--native-fs", "true"); code != 0 {
		t.Fatalf("creating the workspace exited %d\nstderr:\n%s", code, stderr)
	}

	stdout, stderr, code := avr(t, dir, suiteEnv(t), "sh", "-c",
		`awk '($3 == "drvfs" || index($4, "aname=drvfs") > 0) {print $2}' /proc/mounts`)
	if code != 0 {
		t.Fatalf("listing the guest's mounts: exit %d\nstderr:\n%s", code, stderr)
	}
	if len(strings.Fields(stdout)) == 0 {
		t.Fatalf("no Windows directory is mounted at all, so this test proves nothing:\n%s", stdout)
	}
	for _, mount := range strings.Fields(stdout) {
		if !strings.HasPrefix(mount, "/mnt/avr/projects/") {
			t.Errorf("native mode left %s mounted, which avar did not register", mount)
		}
	}
}

// write creates a file and any directories above it.
func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// read returns a file's contents, failing the test if it is not there.
func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(body)
}
