//go:build !windows

package deps

import "os/exec"

// hideConsoleWindow is a Windows concern: no other host creates a window for a
// child process that writes to a pipe.
func hideConsoleWindow(*exec.Cmd) {}
