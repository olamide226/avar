//go:build windows

package deps

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// kernel32 and getConsoleWindow answer "does this process have a console
// window?". x/sys/windows does not wrap GetConsoleWindow, and the answer is
// needed once per child at most, so it is resolved lazily here.
var (
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procConsoleWindow = kernel32.NewProc("GetConsoleWindow")
)

// hideConsoleWindow stops a child opening a console window of its own when avar
// itself has no console.
//
// Windows "creates a new console when it starts a console process", and a
// process that has no console to lend — a GUI-subsystem binary such as the
// scheduled idle check's avrw.exe — therefore gives every console child it
// starts a brand new console, with a window, on the user's desktop. Redirecting
// the child's standard handles does not prevent that; the flag does:
// CREATE_NO_WINDOW means "the process is a console application that is being run
// without a console window"
// (https://learn.microsoft.com/en-us/windows/win32/procthread/process-creation-flags).
//
// It is applied only when avar has no console window of its own. An interactive
// `avr` shares its console with wsl.exe, which is how Ctrl-C reaches the guest
// (see internal/provider/wsl2), and a flag that gave the child its own console
// instead would break that. GetConsoleWindow answers NULL exactly in the case
// this is for: no console at all.
func hideConsoleWindow(cmd *exec.Cmd) {
	if hasConsoleWindow() {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}

func hasConsoleWindow() bool {
	handle, _, _ := procConsoleWindow.Call()
	return handle != 0
}
