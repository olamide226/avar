package deps

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The prompt defaults to no: only an explicit yes installs software
// (REQ-8.2 consent, REQ-8.3 declining).
func TestPromptYesNo_DefaultsToNo_REQ_8_2(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "y", input: "y\n", want: true},
		{name: "yes", input: "yes\n", want: true},
		{name: "uppercase Y", input: "Y\n", want: true},
		{name: "yes with spaces", input: "  yes  \n", want: true},
		{name: "empty line", input: "\n", want: false},
		{name: "n", input: "n\n", want: false},
		{name: "no", input: "no\n", want: false},
		{name: "eof with nothing typed", input: "", want: false},
		{name: "anything else", input: "maybe later\n", want: false},
		{name: "yeah is not yes", input: "yeah\n", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			got, err := promptYesNo(context.Background(), strings.NewReader(tc.input), &out, "Install it now?")
			if err != nil {
				t.Fatalf("promptYesNo returned an unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("promptYesNo(%q) = %t, want %t", tc.input, got, tc.want)
			}
			if !strings.Contains(out.String(), "Install it now?") {
				t.Errorf("the question was not shown to the user, got %q", out.String())
			}
			if !strings.Contains(out.String(), "[y/N]") {
				t.Errorf("the prompt must show that no is the default, got %q", out.String())
			}
		})
	}
}

// Ctrl-C at the prompt returns instead of blocking on a line that will never
// arrive.
func TestPromptYesNo_CancelledContextReturns(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	got, err := promptYesNo(ctx, blockingReaderFor(t), &out, "Install it now?")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("promptYesNo error = %v, want context.Canceled", err)
	}
	if got {
		t.Error("a cancelled prompt must not be read as consent")
	}
}

// blockingReader stands in for a terminal nobody types into. It unblocks when
// the test ends so the reading goroutine is not left parked.
type blockingReader struct{ done chan struct{} }

func (r blockingReader) Read([]byte) (int, error) {
	<-r.done
	return 0, io.EOF
}

func blockingReaderFor(t *testing.T) blockingReader {
	t.Helper()
	r := blockingReader{done: make(chan struct{})}
	t.Cleanup(func() { close(r.done) })
	return r
}

func TestIsExecutableFile(t *testing.T) {
	t.Parallel()

	// The execute bit is what makes a file a program on Unix. Windows decides
	// from the extension instead, so os.WriteFile with mode 0755 produces a
	// file this function correctly calls non-executable — and the function
	// only ever runs on macOS anyway, looking for limactl in the Homebrew
	// directories (REQ-8.1).
	if runtime.GOOS == "windows" {
		t.Skip("file execute bits are a Unix concept; the caller of this is the macOS-only Lima search")
	}

	dir := t.TempDir()

	executable := filepath.Join(dir, "limactl")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(plain, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(dir, "bin")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "executable file", path: executable, want: true},
		{name: "non-executable file", path: plain, want: false},
		{name: "directory", path: subdir, want: false},
		{name: "missing path", path: filepath.Join(dir, "nope"), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := isExecutableFile(tc.path); got != tc.want {
				t.Errorf("isExecutableFile(%q) = %t, want %t", tc.path, got, tc.want)
			}
		})
	}
}

// The real runner passes an argv and never a command string: a metacharacter in
// an argument reaches the child verbatim instead of being interpreted.
func TestExecRunner_PassesArgvWithoutAShell(t *testing.T) {
	t.Parallel()

	echo, err := lookPathOrSkip(t, "echo")
	if err != nil {
		t.Skipf("no echo available: %v", err)
	}

	var runner execRunner
	out, err := runner.Output(context.Background(), echo, "a; b && c")
	if err != nil {
		t.Fatalf("Output returned an unexpected error: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "a; b && c" {
		t.Errorf("Output = %q, want the argument passed through verbatim", got)
	}

	var streamed bytes.Buffer
	if err := runner.Stream(context.Background(), &streamed, echo, "$HOME"); err != nil {
		t.Fatalf("Stream returned an unexpected error: %v", err)
	}
	if got := strings.TrimSpace(streamed.String()); got != "$HOME" {
		t.Errorf("Stream wrote %q, want the argument passed through verbatim", got)
	}
}

// A cancelled context stops the real runner instead of waiting on a child.
func TestExecRunner_CancelledContextTerminatesTheChild(t *testing.T) {
	t.Parallel()

	sleep, err := lookPathOrSkip(t, "sleep")
	if err != nil {
		t.Skipf("no sleep available: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	if err := (execRunner{}).Stream(ctx, &out, sleep, "60"); err == nil {
		t.Fatal("Stream succeeded against a cancelled context, want an error")
	}
}

func lookPathOrSkip(t *testing.T, name string) (string, error) {
	t.Helper()
	for _, dir := range []string{"/bin", "/usr/bin"} {
		candidate := filepath.Join(dir, name)
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New(name + " not found in /bin or /usr/bin")
}
