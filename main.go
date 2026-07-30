// Command avr switches the current directory into a Linux environment.
package main

import (
	"context"
	"os"

	"github.com/olamide226/avar/cmd"
)

// version is set at build time by the release pipeline.
var version = "dev"

// Signal handling deliberately lives below this point rather than here: an
// interactive session must let Ctrl-C reach the guest shell (REQ-3.3) while
// provisioning must abort on Ctrl-C (REQ-1.6), so each phase installs the
// disposition it needs.
func main() {
	os.Exit(cmd.Execute(context.Background(), version, os.Args[1:]))
}
