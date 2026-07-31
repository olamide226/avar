package cmd

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

func init() { registerSubcommand("status", runStatus) }

// runStatus shows every Linux environment avar manages (REQ-5.1), or explains
// how to get one when there are none (REQ-5.3).
//
// It is a read-only command: nothing is created, started or stopped, and a
// machine avar has no record of is displayed rather than acted on.
func runStatus(ctx context.Context, app *App, inv cli.Invocation) error {
	if len(inv.SubcommandArgs) > 0 {
		return Exit(exitUsage, fmt.Errorf("`avr status` takes no arguments, but got %q; it shows every Linux environment avar manages", strings.Join(inv.SubcommandArgs, " ")))
	}

	p, err := app.Provider(ctx)
	if err != nil {
		return err
	}
	machines, err := p.Status(ctx)
	if err != nil {
		return err
	}

	machines = avarOwned(machines)
	if len(machines) == 0 {
		writeNoEnvironments(app.Out)
		return nil
	}

	store, err := app.Store()
	if err != nil {
		return err
	}
	sessions, err := store.Sessions()
	if err != nil {
		return err
	}

	entries := make([]statusEntry, 0, len(machines))
	for _, machine := range machines {
		entry := statusEntry{Machine: machine, Sessions: countSessions(sessions, machine.Name)}
		entry.Ports, entry.PortsErr = portDiagnostics(ctx, p, machine)
		entries = append(entries, entry)
	}
	writeStatus(app.Out, entries)
	return nil
}

// statusEntry is one machine as `avr status` shows it: what the backend
// reported, plus the two things only avar knows — how many sessions are
// attached, and what the backend was able to say about its forwarded ports.
type statusEntry struct {
	Machine  types.MachineStatus
	Sessions int
	Ports    []provider.PortDiagnostic
	// PortsErr records a diagnostics query that failed. It is displayed rather
	// than returned: a status listing that dies because one machine's port log
	// could not be read is worse than one that says so on that machine's line.
	PortsErr error
}

// avarOwned drops anything outside avar's namespace.
//
// Every backend already filters its own listing (PROP-6), so in practice this
// removes nothing. It is here because `avr status` is the surface where a
// mistake would be visible — showing a user their own virtual machines under
// avar's name — and because the check is one string comparison against the
// prefix that defines ownership.
//
// It is the prefix alone, deliberately. Requiring a record as well would hide
// exactly the machine a crash mid-create leaves behind, which is the damage
// reconciliation exists to repair and which the user needs to be able to see
// (design §3.5, PROP-6).
func avarOwned(machines []types.MachineStatus) []types.MachineStatus {
	out := make([]types.MachineStatus, 0, len(machines))
	for _, machine := range machines {
		if types.ValidateMachineName(machine.Name) != nil {
			continue
		}
		out = append(out, machine)
	}
	return out
}

// portDiagnostics asks the backend what it knows about a machine's forwarded
// ports (REQ-7.2).
//
// The capability is optional by design: a backend that cannot explain
// forwarding simply does not implement provider.PortDiagnoser, and avar shows
// the rest of the machine's status rather than refusing to show anything. A
// machine that is not running has nothing to forward, so it is not asked.
func portDiagnostics(ctx context.Context, p provider.Provider, machine types.MachineStatus) ([]provider.PortDiagnostic, error) {
	diagnoser, ok := p.(provider.PortDiagnoser)
	if !ok || machine.State != types.StateRunning {
		return nil, nil
	}
	return diagnoser.PortDiagnostics(ctx, machine.Name)
}

// countSessions reports how many live avar sessions are attached to a machine
// (REQ-5.1: which environments are in use).
func countSessions(sessions []types.SessionRecord, machine string) int {
	n := 0
	for _, session := range sessions {
		if session.Machine == machine {
			n++
		}
	}
	return n
}

// writeNoEnvironments is the first-run answer (REQ-5.3).
//
// It is not an error and must not read like one: a user who has just installed
// avar and typed `avr status` has done nothing wrong, and what they need is the
// one sentence that gets them a Linux shell.
func writeNoEnvironments(w io.Writer) {
	fmt.Fprintln(w, "avar is not managing any Linux environments yet.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run `avr` in any project directory to get one. avar creates the environment")
	fmt.Fprintln(w, "on first use and drops you into a Linux shell in that same directory;")
	fmt.Fprintln(w, "`avr <command>` runs a single command there instead.")
}

// writeStatus renders the machines avar manages.
//
// The at-a-glance facts go in one aligned table so that two environments can be
// compared at a glance, and the per-machine detail — which projects it shares,
// what happened to its ports — follows underneath, where it has room to be
// several lines long.
func writeStatus(w io.Writer, entries []statusEntry) {
	fmt.Fprintf(w, "avar is managing %s.\n\n", countLabel(len(entries), "Linux environment", "Linux environments"))

	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "ENVIRONMENT\tMODE\tSTATE\tCPU\tMEMORY\tDISK\tSESSIONS")
	for _, entry := range entries {
		machine := entry.Machine
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			environmentLabel(machine),
			modeLabel(machine.Kind),
			stateLabel(machine.State),
			countOrUnknown(machine.CPUs),
			sizeOrUnknown(machine.MemoryGB),
			diskLabel(machine),
			entry.Sessions,
		)
	}
	_ = table.Flush()

	for _, entry := range entries {
		writeDetail(w, entry)
	}
}

// writeDetail renders one machine's projects and ports, and says nothing at all
// about a machine that has neither.
func writeDetail(w io.Writer, entry statusEntry) {
	lines := detailLines(entry)
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n", environmentLabel(entry.Machine))
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, line := range lines {
		fmt.Fprintf(table, "  %s\t%s\n", line.label, line.value)
	}
	_ = table.Flush()
}

// detailLine is one label/value row of a machine's detail block. Continuation
// rows carry an empty label so that a list of projects lines up under the first.
type detailLine struct {
	label string
	value string
}

func detailLines(entry statusEntry) []detailLine {
	var lines []detailLine
	label := "projects"
	for _, mount := range entry.Machine.Mounts {
		lines = append(lines, detailLine{label: label, value: projectLabel(mount)})
		label = ""
	}

	if entry.PortsErr != nil {
		return append(lines, detailLine{label: "ports", value: fmt.Sprintf("avar could not read this environment's port forwarding: %v", entry.PortsErr)})
	}
	label = "ports"
	for _, port := range entry.Ports {
		lines = append(lines, detailLine{label: label, value: portLabel(port)})
		label = ""
	}
	return lines
}

// projectLabel renders one shared directory. The guest path is shown only when
// it differs from the host path, which it does on backends that cannot share a
// directory where the host keeps it (REQ-18.5).
func projectLabel(mount types.MountSpec) string {
	if mount.GuestPath == "" || mount.GuestPath == mount.HostPath {
		return mount.HostPath
	}
	return fmt.Sprintf("%s (as %s in Linux)", mount.HostPath, mount.GuestPath)
}

// portLabel renders one port's forwarding state. A port that could not be
// published says why, which is the whole point of showing ports here (REQ-7.2).
func portLabel(port provider.PortDiagnostic) string {
	if port.Forwarded {
		host := port.HostPort
		if host == 0 {
			host = port.GuestPort
		}
		return fmt.Sprintf("%d → localhost:%d", port.GuestPort, host)
	}
	reason := strings.TrimSpace(port.Reason)
	if reason == "" {
		reason = "it is not reachable from the host"
	}
	return fmt.Sprintf("%d — %s", port.GuestPort, reason)
}

// environmentLabel names the environment a machine provides, in the user's
// vocabulary rather than the backend's.
//
// A machine avar cannot describe — an orphan whose selector could not be
// recovered from its name — is named by the only thing that is certainly true
// about it, which is what the backend calls it. Saying "unrecognised" is honest;
// rendering a half-empty selector as "  · arm64" is not.
func environmentLabel(machine types.MachineStatus) string {
	selector := machine.Selector
	if selector.Distro == "" || selector.Version == "" || selector.Arch == "" {
		return fmt.Sprintf("unrecognised environment (%s)", machine.Name)
	}
	return selector.Label()
}

// modeLabel says which role a machine plays, in the terms the user chose it
// with: `--isolate` gives a project its own environment, everything else shares
// one (REQ-5.1).
func modeLabel(kind types.MachineKind) string {
	switch kind {
	case types.KindShared:
		return "shared"
	case types.KindIsolated:
		return "this project"
	case types.KindBase:
		return "base image"
	default:
		return unknownValue
	}
}

// stateLabel renders a machine's state, including a state avar's own vocabulary
// does not have a word for — a backend may report one avar has not met.
func stateLabel(state types.MachineState) string {
	if strings.TrimSpace(string(state)) == "" {
		return unknownValue
	}
	return string(state)
}

// unknownValue is what a column shows when the backend could not tell avar the
// answer. A dash reads as "not known" where a zero would read as "none".
const unknownValue = "—"

func countOrUnknown(n int) string {
	if n <= 0 {
		return unknownValue
	}
	return strconv.Itoa(n)
}

func sizeOrUnknown(gb float64) string {
	if gb <= 0 {
		return unknownValue
	}
	return formatGB(gb) + " GB"
}

// diskLabel renders what the environment costs the host today (REQ-5.1: disk
// usage), not the ceiling it may never reach: avar's machines are given a large
// sparse disk on purpose, so the allocated figure says nothing about the space
// the user has actually spent. It is shown only when the used figure is the one
// the backend could not work out.
func diskLabel(machine types.MachineStatus) string {
	switch {
	case machine.DiskUsed > 0:
		return sizeOrUnknown(machine.DiskUsed)
	case machine.DiskGB > 0:
		return "up to " + sizeOrUnknown(machine.DiskGB)
	default:
		return unknownValue
	}
}

// formatGB renders a gibibyte count without trailing zeroes: 8 rather than
// 8.00, 12.4 rather than 12.40.
func formatGB(gb float64) string {
	return strconv.FormatFloat(gb, 'f', -1, 64)
}

// countLabel renders "1 Linux environment" and "3 Linux environments".
func countLabel(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, plural)
}
