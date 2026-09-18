// Command avrw runs avar's idle check on Windows without opening a console
// window. The scheduled task avar registers runs it; nothing else does.
//
// It exists because of how Windows decides to create a console. The system
// "creates a new console when it starts a console process, a character-mode
// process whose entry point is the main function"
// (https://learn.microsoft.com/en-us/windows/console/creation-of-a-console),
// and avr.exe is such a process. A Task Scheduler entry that runs it in the
// user's own session therefore opened a console window every time the idle
// check fired.
//
// A GUI-subsystem binary gets no console: "GUI processes are not attached to a
// console when they are created" (same page). The release links this command
// with `-H windowsgui`, which the Go linker documents as writing "a 'GUI
// binary' instead of a 'console binary'" (https://pkg.go.dev/cmd/link). Built
// without that flag it is an ordinary console program and opens the same
// window avr.exe did, which is why only the release build ships it. The task
// still runs as the signed-in user with their own token, so wsl.exe still sees
// that user's distributions.
//
// It ignores its own arguments. It has one job and no console to explain
// itself on, and a windowless avar that could be asked to open a shell would
// be a shell nobody could see or type into.
package main

import (
	"context"
	"os"

	"github.com/olamide226/avar/cmd"
)

// version is set at build time by the release pipeline, as it is for avr.
var version = "dev"

// idleCheck is the only command this binary runs.
var idleCheck = []string{"internal", "idle-check"}

func main() {
	os.Exit(cmd.Execute(context.Background(), version, idleCheck))
}
