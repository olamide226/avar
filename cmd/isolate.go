package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/types"
)

func init() { registerSubcommand("isolate", runIsolate) }

// runIsolate manages a project's isolation: turning it on, off, or reporting
// whether the project defaults to its own machine (REQ-11.2, REQ-11.3).
func runIsolate(ctx context.Context, app *App, inv cli.Invocation) error {
	args := inv.SubcommandArgs

	if len(args) == 0 {
		return showIsolation(ctx, app)
	}

	switch args[0] {
	case "on":
		return turnOn(ctx, app, args[1:])
	case "off":
		return turnOff(ctx, app, args[1:])
	default:
		return Exit(exitUsage,
			fmt.Errorf("`avr isolate` does not understand %q: it takes `on`, `off`, or no arguments to show the current isolation status", args[0]))
	}
}

// showIsolation reports whether the current project defaults to an isolated
// machine (REQ-11.2).
func showIsolation(ctx context.Context, app *App) error {
	store, err := app.Store()
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find the current directory: %w", err)
	}

	projects, err := store.Projects()
	if err != nil {
		return fmt.Errorf("read avar's project records: %w", err)
	}

	// Find the project this directory belongs to.
	rec, ok := findProject(projects, cwd)
	if !ok {
		fmt.Fprintf(app.Out, "No avar project is recorded for %s yet. Run `avr` to create one.\n", cwd)
		return nil
	}

	if rec.Isolated {
		fmt.Fprintf(app.Out, "%s defaults to its own Linux environment.\n", rec.Path)
		fmt.Fprintf(app.Out, "Run `avr isolate off` to switch it back to a shared environment.\n")
	} else {
		fmt.Fprintf(app.Out, "%s does not default to its own Linux environment.\n", rec.Path)
		fmt.Fprintf(app.Out, "Run `avr isolate on` to give it one.\n")
	}
	return nil
}

// turnOn remembers that the current project should use its own machine
// (REQ-11.2). The machine is created on the next `avr`, not here — this just
// changes the default.
func turnOn(ctx context.Context, app *App, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("`avr isolate on` takes no arguments, got %q", strings.Join(args, " "))
	}

	store, err := app.Store()
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find the current directory: %w", err)
	}

	projects, err := store.Projects()
	if err != nil {
		return fmt.Errorf("read avar's project records: %w", err)
	}

	rec, ok := findProject(projects, cwd)
	if !ok {
		return fmt.Errorf("no avar project is recorded for %s yet — run `avr` first to create one", cwd)
	}

	if rec.Isolated {
		fmt.Fprintf(app.Out, "%s already defaults to its own Linux environment.\n", rec.Path)
		return nil
	}

	if _, err := store.UpdateProject(rec.ID, func(r *types.ProjectRecord) {
		r.Isolated = true
	}); err != nil {
		return fmt.Errorf("remembering that %s uses an isolated Linux environment: %w", rec.Path, err)
	}

	fmt.Fprintf(app.Out, "%s now defaults to its own Linux environment. Run `avr` to create it.\n", rec.Path)
	return nil
}

// turnOff clears the project's isolation default and offers to delete the
// isolated machine (REQ-11.3).
func turnOff(ctx context.Context, app *App, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("`avr isolate off` takes no arguments, got %q", strings.Join(args, " "))
	}

	store, err := app.Store()
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find the current directory: %w", err)
	}

	projects, err := store.Projects()
	if err != nil {
		return fmt.Errorf("read avar's project records: %w", err)
	}

	rec, ok := findProject(projects, cwd)
	if !ok {
		return fmt.Errorf("no avar project is recorded for %s yet — run `avr` first to create one", cwd)
	}

	if !rec.Isolated {
		fmt.Fprintf(app.Out, "%s is not using its own Linux environment.\n", rec.Path)
		return nil
	}

	// Resolve the target so we can name the isolated machine before clearing
	// the flag. We use --isolate to force the isolated resolution path.
	inv := cli.Invocation{Mode: cli.ModeSubcommand, Subcommand: "isolate"}
	inv.Selector.Isolate = true
	target, err := app.Resolve(inv)
	if err != nil {
		return fmt.Errorf("resolving the isolated environment: %w", err)
	}

	// Clear the flag.
	if _, err := store.UpdateProject(rec.ID, func(r *types.ProjectRecord) {
		r.Isolated = false
	}); err != nil {
		return fmt.Errorf("clearing the isolation default for %s: %w", rec.Path, err)
	}

	fmt.Fprintf(app.Out, "%s no longer defaults to its own Linux environment.\n", rec.Path)
	fmt.Fprintf(app.Out, "Its isolated machine, %s, still exists.\n", target.MachineName)

	if stdinIsTerminal() {
		if promptDelete(app, target.MachineName) {
			p, err := app.Provider(ctx)
			if err != nil {
				return err
			}
			if err := p.Delete(ctx, target.MachineName); err != nil {
				return fmt.Errorf("deleting the isolated machine %s: %w. "+
					"Run `limactl delete --force %s` to remove it by hand.",
					target.MachineName, err, target.MachineName)
			}
			fmt.Fprintf(app.Out, "Deleted %s.\n", target.MachineName)
		} else {
			fmt.Fprintf(app.Out, "Left %s alone.\n", target.MachineName)
		}
	} else {
		fmt.Fprintf(app.Out, "Run `limactl delete --force %s` to remove it.\n", target.MachineName)
	}

	return nil
}

// promptDelete asks whether the user wants the isolated machine removed.
func promptDelete(app *App, machine string) bool {
	fmt.Fprintf(app.Err, "      Delete %s? (y/N) ", machine)

	r := bufio.NewReader(os.Stdin)
	reply, err := r.ReadString('\n')
	if err != nil {
		return false
	}
	reply = strings.TrimSpace(reply)
	return reply == "y" || reply == "Y"
}

// findProject locates the project record that covers the given directory.
func findProject(projects []types.ProjectRecord, dir string) (types.ProjectRecord, bool) {
	best := ""
	bestIdx := -1
	for i, rec := range projects {
		if coversProject(rec.Path, dir) && len(rec.Path) > len(best) {
			best = rec.Path
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return types.ProjectRecord{}, false
	}
	return projects[bestIdx], true
}

// coversProject reports whether dir is root itself or a directory beneath it.
func coversProject(root, dir string) bool {
	if root == dir {
		return true
	}
	return strings.HasPrefix(dir, root+string(os.PathSeparator))
}
