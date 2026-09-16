package editor

// Cursor opens a project with `cursor --remote <authority> <path>`.
//
// Cursor is built from VS Code and its launcher keeps VS Code's `--remote`
// flag and authority forms: `ssh-remote+<host>` resolves through its Remote-SSH
// extension, which reads the same OpenSSH configuration VS Code's does, and
// `wsl+<distribution>` through its WSL extension. So the target a backend
// describes for VS Code needs no translation for Cursor, and nothing about the
// connection differs — only the launcher's name.
var Cursor = Editor{
	Name:        "Cursor",
	Command:     "cursor",
	installHint: cursorInstallHint,
	args:        remoteAuthorityArgs,
}

// cursorInstallHint returns platform-appropriate instructions for adding
// `cursor` to the PATH.
func cursorInstallHint(goos string) string {
	switch goos {
	case "darwin":
		return "Open Cursor, press Cmd+Shift+P and run \"Shell Command: Install 'cursor' command in PATH\", then open a new terminal."
	case "windows":
		return "Cursor's installer puts `cursor` on your PATH. If it is missing, re-run the installer with its \"Add to PATH\" option selected, then open a new terminal."
	default:
		return "Install Cursor from https://cursor.com and ensure `cursor` is on your PATH."
	}
}
