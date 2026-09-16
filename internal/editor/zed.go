package editor

import (
	"fmt"
	"net/url"
	"path"
	"strings"
)

// Zed opens a project through Zed's own remote development, which does not
// speak VS Code's authorities, so the target a backend describes is translated:
//
//   - `ssh-remote+<host>` becomes `zed ssh://<host>/<path>`. Zed runs the
//     system `ssh` and inherits ~/.ssh/config, so the host is the alias avar
//     writes, and the user, port and key stay in that stanza rather than on a
//     command line.
//   - `wsl+<distribution>` becomes `zed --wsl <distribution> <path>`, the form
//     Zed's Windows launcher passes for itself when `zed` is run inside WSL.
//     The flag exists only in the Windows build of the launcher, which is the
//     only host whose backend describes a WSL target.
//
// Any other kind of target is refused rather than guessed at.
var Zed = Editor{
	Name:        "Zed",
	Command:     "zed",
	installHint: zedInstallHint,
	args:        zedArgs,
}

// zedInstallHint returns platform-appropriate instructions for adding `zed` to
// the PATH.
func zedInstallHint(goos string) string {
	switch goos {
	case "darwin":
		return "Open Zed, press Cmd+Shift+P and run \"cli: install cli binary\" to put `zed` on your PATH, then open a new terminal."
	case "windows":
		return "Zed's installer puts `zed` on your PATH when its \"Add to PATH\" option is selected. Re-run the installer with that option, or add the `bin` folder of Zed's install directory to your PATH, then open a new terminal."
	default:
		return "Install Zed from https://zed.dev and ensure `zed` is on your PATH (some distributions name it `zeditor`)."
	}
}

// zedArgs translates an EditorTarget authority into Zed's launcher arguments.
func zedArgs(authority, guestPath string) ([]string, error) {
	kind, name, found := strings.Cut(authority, "+")
	if !found || name == "" {
		// Not an authority at all, so there is no kind worth repeating — and
		// what is there may be a bare machine name.
		return nil, zedUnsupported("unrecognised")
	}

	switch kind {
	case sshAuthority:
		if !path.IsAbs(guestPath) {
			return nil, fmt.Errorf("open %q in Zed: an SSH project path must be absolute", guestPath)
		}
		// url.URL escapes the path, so a directory whose name holds a space,
		// '#' or '?' reaches Zed as that directory rather than as a fragment or
		// query; Zed percent-decodes the path it parses back out.
		target := url.URL{Scheme: "ssh", Host: name, Path: guestPath}
		return []string{target.String()}, nil
	case wslAuthority:
		return []string{"--wsl", name, guestPath}, nil
	default:
		return nil, zedUnsupported(kind)
	}
}

// zedUnsupported names only the kind of connection, never the machine behind
// it: machine names are avar's business, not the user's (REQ-1.5).
func zedUnsupported(kind string) error {
	return fmt.Errorf("Zed cannot open a project in this Linux environment: it is reached through a %q connection, and Zed connects only over SSH or to WSL (%w). Run `avr code` to open it in VS Code instead",
		kind, ErrUnsupportedTarget)
}
