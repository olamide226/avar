package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/projconfig"
	"github.com/olamide226/avar/internal/resolve"
)

func init() { registerSubcommand("init", runInit) }

// runInit proposes a .avr.toml from the project's manifests and writes it only
// after the user confirms (REQ-15.2).
//
// It starts no environment and installs nothing. Writing the file approves
// nothing either: the first `avr` in the project asks about its packages like
// any other file's (design §3.11). There is no --yes, because Requirement 15.2
// grants no bypass of the confirmation, unlike the ones reset and destroy have.
func runInit(ctx context.Context, app *App, inv cli.Invocation) error {
	if len(inv.SubcommandArgs) > 0 {
		return Exit(exitUsage, fmt.Errorf("`avr init` takes no arguments, but got %q; selector flags such as --distro go before it, as in `avr --distro fedora init`",
			strings.Join(inv.SubcommandArgs, " ")))
	}

	target, err := app.resolve(inv, nil)
	if err != nil {
		return err
	}
	dir := target.Project.Path
	path := filepath.Join(dir, projconfig.FileName)

	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists, and avr init only writes a new one; nothing was changed. Edit it, or remove it and run avr init again", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("check for an existing %s: %w", path, err)
	}

	findings, err := projconfig.Detect(dir)
	if err != nil {
		return fmt.Errorf("inspect %s for manifests: %w", dir, err)
	}
	if len(findings) == 0 {
		fmt.Fprintf(app.Out, "avr init found no manifests it recognises in %s, so it has nothing to propose.\n", dir)
		fmt.Fprintln(app.Out, "avar needs no configuration: run `avr` to get a Linux shell here.")
		return nil
	}

	proposal := projconfig.Propose(findings, projconfig.Choice{
		Distro:  inv.Selector.Distro,
		Version: inv.Selector.DistroVersion,
		Arch:    inv.Selector.Arch,
	}, resolve.DefaultDistro)
	body, err := projconfig.Render(proposal.Config)
	if err != nil {
		return err
	}

	writeProposal(app, dir, path, proposal, body)

	if !app.interactive() {
		return fmt.Errorf("nothing was written: avr init writes %s only after you confirm, so run it from a terminal", projconfig.FileName)
	}
	if !app.confirmYesNo(fmt.Sprintf("Write %s? (y/N) ", path)) {
		fmt.Fprintln(app.Out, "Nothing was written.")
		return nil
	}

	if err := writeNewFile(path, body); err != nil {
		return err
	}
	fmt.Fprintf(app.Out, "Wrote %s.\n", path)
	if len(proposal.Config.Packages) > 0 {
		fmt.Fprintln(app.Out, "Nothing is installed yet: the next `avr` here lists the packages and asks before installing them.")
	}
	return nil
}

// writeProposal shows the detected stack, what the proposal leaves out, and the
// exact file that would be written.
func writeProposal(app *App, dir, path string, proposal projconfig.Proposal, body []byte) {
	fmt.Fprintf(app.Out, "avr init looked at %s and found:\n\n", dir)
	width := 0
	for _, f := range proposal.Findings {
		width = max(width, len(f.Manifest))
	}
	for _, f := range proposal.Findings {
		line := f.Stack
		if f.Detail != "" && f.Detail != f.Stack {
			line += ": " + f.Detail
		}
		fmt.Fprintf(app.Out, "  %-*s  %s\n", width, f.Manifest, line)
	}
	for _, note := range proposal.Notes {
		fmt.Fprintf(app.Out, "\n  %s\n", note)
	}

	fmt.Fprintf(app.Out, "\nProposed %s:\n\n", path)
	for _, line := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
		fmt.Fprintf(app.Out, "  %s\n", line)
	}
	fmt.Fprintln(app.Out)
}

// writeNewFile creates path with body, refusing to replace a file that
// appeared after avr init checked. A confirmation given for a new file is not a
// confirmation to overwrite somebody's.
func writeNewFile(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s appeared while avr init was asking; nothing was changed", path)
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
