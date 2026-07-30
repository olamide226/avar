package deps

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// execRunner is the production Runner: os/exec with an argv, never a shell.
type execRunner struct{}

// NewRunner returns the production Runner, which executes a program with an
// argv and never involves a shell.
//
// It exists because every caller that drives a backend needs the same runner
// this package already uses for limactl, and the alternative — each package
// writing its own os/exec wrapper — would duplicate the parts that are easy to
// get wrong: folding stderr into the error, killing the child when the context
// is cancelled, and bounding how long a finished child's pipes may keep avar
// waiting.
func NewRunner() Runner { return execRunner{} }

// childWaitDelay bounds how long a finished child's output copying may keep
// avar waiting, so a subprocess that hands its pipes to a grandchild cannot
// wedge the invocation.
const childWaitDelay = 5 * time.Second

// Output runs name with args and returns its standard output. Standard error is
// folded into the returned error, since that is where a failing tool explains
// itself.
func (execRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = childWaitDelay

	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return out, fmt.Errorf("%w: %s", err, truncate(string(bytes.TrimSpace(exitErr.Stderr)), 200))
		}
		return out, err
	}
	return out, nil
}

// Stream runs name with args, copying stdout and stderr to w as they arrive.
//
// The child gets no stdin: `brew install lima` must not be able to block on a
// prompt that the user cannot see. Cancelling ctx kills the child rather than
// orphaning it, so Ctrl-C during an install stops the install.
func (execRunner) Stream(ctx context.Context, w io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = nil
	cmd.Stdout = w
	cmd.Stderr = w
	cmd.WaitDelay = childWaitDelay
	return cmd.Run()
}

// stdinIsTerminal reports whether there is a user on the other end of stdin.
//
// This is a character-device check rather than golang.org/x/term because it is
// all avar needs here and it keeps the dependency set unchanged.
func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// promptYesNo asks question and reads a yes/no answer, defaulting to no.
//
// The read happens on its own goroutine so a cancelled context (Ctrl-C at the
// prompt) returns instead of blocking on a line that will never arrive.
func promptYesNo(ctx context.Context, in io.Reader, out io.Writer, question string) (bool, error) {
	fmt.Fprintf(out, "%s [y/N] ", question)

	type answer struct {
		line string
		err  error
	}
	answers := make(chan answer, 1)
	go func() {
		line, err := bufio.NewReader(in).ReadString('\n')
		answers <- answer{line: line, err: err}
	}()

	select {
	case <-ctx.Done():
		fmt.Fprintln(out)
		return false, ctx.Err()
	case a := <-answers:
		// EOF with nothing typed is not a yes.
		if a.err != nil && !errors.Is(a.err, io.EOF) {
			return false, fmt.Errorf("reading your answer: %w", a.err)
		}
		switch strings.ToLower(strings.TrimSpace(a.line)) {
		case "y", "yes":
			return true, nil
		default:
			return false, nil
		}
	}
}

// isExecutableFile reports whether path is a regular file with an execute bit.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}
