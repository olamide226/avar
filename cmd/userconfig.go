package cmd

import (
	"fmt"

	"github.com/olamide226/avar/internal/cli"
)

// survivesBrokenConfig names the subcommands that still run when config.toml
// cannot be read. Every other command refuses before any machine work.
//
// A broken file must not stop the user from seeing and releasing what avar is
// running, and none of these reads a setting from it: `status` shows the
// environments, `stop` and `destroy` release them. Each says the file is broken
// before carrying on. `help` and `version` never reach dispatch at all.
//
// `internal` is the scheduled idle check, which makes its own decision, because
// nobody is watching it: see runIdleCheck.
var survivesBrokenConfig = map[string]bool{
	"status":   true,
	"stop":     true,
	"destroy":  true,
	"internal": true,
}

// checkUserConfig reads config.toml before a command runs, and refuses the
// command when the file cannot be read exactly (REQ-17.7).
//
// Refusing is the deliberate choice. The file is the user's own and was once
// read leniently so that a typo could never stop a shell, but a setting that
// silently does not apply is the worse failure: the user believes idle
// auto-stop is off, or that a variable is forwarded, and nothing tells them
// otherwise. So the first command they run says which line is wrong and what to
// write, and nothing is started or changed until it is fixed.
func checkUserConfig(app *App, inv cli.Invocation) error {
	if _, err := app.Store(); err != nil {
		return err
	}
	_, err := app.Config()
	if err == nil {
		return nil
	}

	if inv.Mode == cli.ModeSubcommand && survivesBrokenConfig[inv.Subcommand] {
		if inv.Subcommand != "internal" {
			fmt.Fprintf(app.Err, "avr: %v\n", err)
			fmt.Fprintf(app.Err, "     `avr %s` carries on without it. Other commands refuse to run, and idle auto-stop is paused, until the file is fixed.\n", inv.Subcommand)
		}
		return nil
	}
	return fmt.Errorf("%w\n     Nothing was started or changed. Fix the file and run the command again; until then idle auto-stop is paused, and `avr status`, `avr stop` and `avr destroy` still work", err)
}
