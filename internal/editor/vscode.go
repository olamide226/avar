package editor

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

// ErrCodeNotFound explains how to get `code` onto the PATH so that the user
// can act on the error without opening a browser (REQ-13.2).
const ErrCodeNotFound = "the `code` command was not found on your PATH"

// codeInstallInstructions explains how to install the `code` CLI so that the
// error message is self-contained (REQ-13.2).
const codeInstallInstructions = "Open VS Code, press Cmd+Shift+P and run \"Shell Command: Install 'code' command in PATH\""

// codeInstallMessage returns platform-appropriate instructions for adding
// `code` to the PATH (REQ-13.2).
func codeInstallMessage() string {
	switch runtime.GOOS {
	case "darwin":
		return codeInstallInstructions
	case "windows":
		return "The VS Code installer adds `code` to your PATH. If it is missing, re-run the installer and check the \"Add to PATH\" option."
	default:
		// Linux and others: suggest the common path.
		return "Install VS Code from https://code.visualstudio.com and ensure `code` is on your PATH. On most distributions it is added automatically by the package manager."
	}
}

// Launch opens VS Code connected to the given remote authority at the
// given guest path (REQ-13.1).
//
// It checks for the `code` CLI on PATH and returns a clear error with
// installation instructions when it is not found (REQ-13.2). VS Code
// is a GUI application and is launched in the background; avar's own
// process does not wait for it.
func Launch(ctx context.Context, authority, guestPath string) error {
	codePath, err := exec.LookPath("code")
	if err != nil {
		return fmt.Errorf("%s. %s", ErrCodeNotFound, codeInstallMessage())
	}

	cmd := exec.CommandContext(ctx, codePath, "--remote", authority, guestPath)

	// VS Code is a GUI app launched through a thin CLI. The CLI detaches
	// from the child process and exits quickly; avar should not hold up
	// the user waiting for the remote connection, which can take several
	// seconds. A user who wants to watch the output can run `code` by
	// hand — `avr code` is the shortcut that saves them from doing the
	// SSH-config plumbing themselves.
	//
	// The one thing worth surfacing is a failure to start the command at
	// all, because that is almost always "code is broken" rather than
	// "the remote is taking a moment."
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launch VS Code: %w", err)
	}
	return nil
}
