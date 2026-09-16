package editor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
)

// Editor is one editor avar can open a project in: `avr code`, `avr cursor`
// and `avr zed` differ only in which of these they are handed.
//
// What varies between editors is how their command-line launcher is spelled
// and how it is told to open a directory on a remote machine. Everything else —
// bringing the environment up, asking the backend how an editor reaches it,
// writing SSH configuration — is the same for every editor and lives in the
// command layer, which never needs to know which editor it is driving.
type Editor struct {
	// Name is how the user knows the editor, and appears in every message.
	Name string

	// Command is the launcher the editor installs on PATH.
	Command string

	// installHint explains, for a host operating system, how to get Command
	// onto PATH, so that a missing launcher is an error the user can act on
	// without opening a browser.
	installHint func(goos string) string

	// args builds the launcher's arguments that open guestPath through an
	// EditorTarget authority, or fails with ErrUnsupportedTarget when the
	// editor has no way to make that kind of connection.
	args func(authority, guestPath string) ([]string, error)
}

// ErrUnsupportedTarget reports that an editor cannot open a project in an
// environment reached the way the backend describes, as opposed to anything
// having gone wrong.
var ErrUnsupportedTarget = errors.New("the editor cannot connect to this kind of environment")

// Remote authority kinds, in VS Code's vocabulary, which is the vocabulary
// provider.EditorTarget speaks. Editors that do not share it translate from it.
const (
	sshAuthority = "ssh-remote"
	wslAuthority = "wsl"
)

// VSCode opens a project with `code --remote <authority> <path>`.
var VSCode = Editor{
	Name:        "VS Code",
	Command:     "code",
	installHint: vscodeInstallHint,
	args:        remoteAuthorityArgs,
}

// vscodeInstallHint returns platform-appropriate instructions for adding
// `code` to the PATH.
func vscodeInstallHint(goos string) string {
	switch goos {
	case "darwin":
		return "Open VS Code, press Cmd+Shift+P and run \"Shell Command: Install 'code' command in PATH\""
	case "windows":
		return "The VS Code installer adds `code` to your PATH. If it is missing, re-run the installer and check the \"Add to PATH\" option."
	default:
		return "Install VS Code from https://code.visualstudio.com and ensure `code` is on your PATH. On most distributions it is added automatically by the package manager."
	}
}

// remoteAuthorityArgs is the `--remote <authority> <path>` form VS Code's
// launcher accepts, and which editors built from VS Code accept unchanged.
//
// The authority is passed through whatever its kind: resolving it is the job
// of the editor's remote extensions, and refusing a kind here would second-guess
// an editor that may well know it.
func remoteAuthorityArgs(authority, guestPath string) ([]string, error) {
	return []string{"--remote", authority, guestPath}, nil
}

// Locate finds the editor's launcher on PATH.
//
// It is separate from Launch so that a caller can check before doing anything
// slow: a missing launcher discovered after an environment has spent minutes
// provisioning is a much worse answer than the same one given at once.
func (e Editor) Locate() (string, error) {
	path, err := exec.LookPath(e.Command)
	if err != nil {
		return "", fmt.Errorf("the `%s` command was not found on your PATH (%w). %s",
			e.Command, err, e.installHint(runtime.GOOS))
	}
	return path, nil
}

// Args returns the launcher arguments that open guestPath in the environment
// reached through authority, or an error wrapping ErrUnsupportedTarget when
// this editor cannot make that connection.
func (e Editor) Args(authority, guestPath string) ([]string, error) {
	return e.args(authority, guestPath)
}

// Launch runs the launcher found by Locate with the arguments from Args.
//
// The launcher is a thin CLI that hands the request to the editor's GUI and
// returns; avar does not wait for the remote connection, which can take several
// seconds. What the launcher itself writes to stderr goes to stderr, because
// when it refuses — an editor too old to know a flag, say — its own words are
// the only explanation there is.
func (e Editor) Launch(ctx context.Context, launcher string, args []string, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, launcher, args...)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launch %s with `%s %s`: %w", e.Name, e.Command, strings.Join(args, " "), err)
	}
	return nil
}
