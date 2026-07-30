package lima

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/olamide226/avar/internal/types"
)

// guestProbeWorkdir is where avar's own in-guest checks are run from. It is
// always present, so a probe never fails merely because the shell could not
// enter its working directory.
const guestProbeWorkdir = "/"

// AppliedMounts reports the host directories Lima currently shares into the
// machine, sorted.
//
// It is a read-only query and does not need the machine to be running: Lima
// records the mounts in the instance's own configuration, which `limactl list
// --json` returns whatever state the machine is in. The answer is what Lima has
// applied, not what avar's records expect — comparing the two is how the caller
// decides whether the current project still needs registering (REQ-6.4).
func (p *Provider) AppliedMounts(ctx context.Context, machine string) ([]string, error) {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return nil, err
	}
	inst, err := p.newView().require(ctx, machine)
	if err != nil {
		return nil, err
	}
	return inst.mounts(), nil
}

// SetMounts makes dirs the machine's complete set of shared host directories.
//
// Replace-not-append is what makes mount confinement checkable: the guest can
// reach exactly the project roots avar registered and nothing else — never the
// home directory, never an unregistered sibling (REQ-6.3, REQ-9.3, REQ-9.4,
// PROP-5).
//
// Lima cannot change file sharing on a running machine — `limactl edit` refuses
// a running instance outright — so an actual change costs a stop, an edit and a
// start. That is explained through a ProgressMounting event before any of it
// happens (REQ-6.4), and it is why the no-change case returns before doing
// anything at all: revisiting a known project has to stay instant (REQ-17.1).
func (p *Provider) SetMounts(ctx context.Context, machine string, dirs []string, progress types.ProgressSink) error {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return err
	}
	want, err := normalizeMounts(dirs)
	if err != nil {
		return fmt.Errorf("sharing directories with machine %s: %w", machine, err)
	}

	v := p.newView()
	inst, err := v.require(ctx, machine)
	if err != nil {
		return err
	}
	if equalStrings(inst.mounts(), want) {
		// Nothing to apply, so nothing to restart and nothing to say.
		return nil
	}

	// A directory that is not there cannot be shared, and finding that out
	// after a restart would have cost the user ten seconds to learn nothing
	// (REQ-6.5).
	//
	// Only the additions are checked. A directory registered earlier was
	// checked when it was registered, and refusing to add today's project
	// because a project from last month has since been deleted would be a
	// failure the user cannot act on.
	added := addedDirs(inst.mounts(), want)
	if err := checkHostDirs(added); err != nil {
		return fmt.Errorf("sharing directories with machine %s: %w", machine, err)
	}

	wasRunning := inst.running()
	progress.Progress(types.ProgressEvent{
		Kind:    types.ProgressMounting,
		Machine: machine,
		Message: mountingMessage(added, wasRunning),
	})

	if wasRunning {
		if _, err := p.run(ctx, "stop", machine); err != nil {
			return fmt.Errorf("stopping machine %s to share new directories with it: %w; "+
				"Lima cannot change file sharing while a machine is running", machine, err)
		}
	}
	if _, err := p.run(ctx, "edit", machine, "--set", mountsExpression(want), "--tty=false"); err != nil {
		return fmt.Errorf("updating the directories shared with machine %s: %w", machine, err)
	}
	if !wasRunning {
		// The machine was stopped when avar was asked, and starting it would be
		// a side effect the caller did not request. The new configuration
		// applies the next time it starts.
		return nil
	}
	if _, err := p.run(ctx, "start", "--tty=false", machine); err != nil {
		return fmt.Errorf("restarting machine %s after sharing new directories with it: %w; "+
			"run `avr status` to see its state", machine, err)
	}

	// The machine is up with the new configuration. Prove the shares landed
	// rather than assume it: dropping the user into a shell at an empty path is
	// worse than failing outright (REQ-6.5).
	return p.verifyMounts(ctx, machine, want)
}

// verifyMounts checks in the guest that every directory really is there and
// really is writable (REQ-6.5).
//
// Each check is its own argv — `test -d DIR`, then `test -w DIR` — rather than
// one shell command per machine. A path handed to a guest shell would have to be
// quoted for that shell; a path handed to `test` as an argument cannot be
// anything but a path. Mount changes are rare by design, so the extra
// round-trips cost nothing that matters.
func (p *Provider) verifyMounts(ctx context.Context, machine string, dirs []string) error {
	for _, dir := range dirs {
		for _, check := range []struct {
			flag     string
			expected string
		}{
			{"-d", "a directory"},
			{"-w", "writable"},
		} {
			if _, err := p.run(ctx, "shell", "--workdir", guestProbeWorkdir, machine, "--", "test", check.flag, dir); err != nil {
				return fmt.Errorf("checking that %s is available inside your Linux environment: it is not %s there: %w; "+
					"macOS will not share a network volume or an external disk into a virtual machine — move the project onto the internal disk, or run avr from a directory that is on it",
					dir, check.expected, err)
			}
		}
	}
	return nil
}

// mountsExpression builds the yq expression that replaces the instance's mount
// list.
//
// Every path is emitted as a JSON string literal, which yq reads as a quoted
// scalar, so a directory name containing a quote or a bracket is data and cannot
// restructure the expression. No shell is involved at any point: the expression
// is a single argv element.
func mountsExpression(dirs []string) string {
	entries := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		// A directory has already been checked to be an absolute, existing
		// path, so marshalling a plain string cannot fail.
		quoted, _ := json.Marshal(dir)
		// mountPoint is set to the host path so the guest sees the project at
		// the identical absolute path (REQ-6.1, PROP-1).
		entries = append(entries, fmt.Sprintf(`{"location": %s, "mountPoint": %s, "writable": true}`, quoted, quoted))
	}
	return ".mounts = [" + strings.Join(entries, ", ") + "]"
}

// mountingMessage explains the change, and the one-time delay when there is one,
// in a sentence the presentation layer can show verbatim (REQ-6.4).
func mountingMessage(added []string, restarting bool) string {
	var subject string
	switch {
	case len(added) == 1:
		subject = fmt.Sprintf("Adding %s to your Linux environment", added[0])
	case len(added) > 1:
		subject = fmt.Sprintf("Adding %s to your Linux environment", strings.Join(added, ", "))
	default:
		subject = "Updating the directories shared with your Linux environment"
	}
	if !restarting {
		return subject + "."
	}
	return subject + " — this needs a one-time restart and takes about ten seconds. Returning to this project later is instant."
}

// addedDirs reports the entries of want that are not already applied.
func addedDirs(applied, want []string) []string {
	present := make(map[string]struct{}, len(applied))
	for _, dir := range applied {
		present[dir] = struct{}{}
	}
	var out []string
	for _, dir := range want {
		if _, ok := present[dir]; !ok {
			out = append(out, dir)
		}
	}
	return out
}
