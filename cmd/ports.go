package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

func init() {
	registerSubcommand("ports", runPorts)
	registerSubcommand("open", runOpen)
}

// portsAllFlag asks for every running environment rather than the one this
// directory and these selector flags resolve to, as `avr stop --all` does.
const portsAllFlag = "--all"

// maxProcessWidth bounds how much of a guest command line a listing shows. A
// JVM or a bundler's command line runs to hundreds of characters, and one of
// those would push every other row of the table off the screen.
const maxProcessWidth = 60

// runPorts lists the ports forwarded from the selected environment to this
// computer, with the guest process listening on each where it can be
// determined; --all does the same for every running environment (REQ-16.1).
//
// It is a read-only command: an environment that does not exist or is not
// running is reported as forwarding nothing, and is never created or started to
// find that out.
func runPorts(ctx context.Context, app *App, inv cli.Invocation) error {
	all, err := parsePortsArgs(inv.SubcommandArgs)
	if err != nil {
		return Exit(exitUsage, err)
	}

	diagnoser, machines, err := portsBackend(ctx, app)
	if err != nil {
		return err
	}
	if all {
		return listPortsEverywhere(ctx, app, diagnoser, machines)
	}

	target, err := app.Resolve(inv)
	if err != nil {
		return err
	}
	label := target.Selector.Label()

	machine, found := findMachine(machines, target.MachineName)
	switch {
	case !found:
		fmt.Fprintf(app.Out, "There is no %s environment yet, so nothing is forwarded from it. Run `avr` to create it.\n", label)
		return nil
	case machine.State != types.StateRunning:
		fmt.Fprintf(app.Out, "%s is not running, so nothing is forwarded from it. Run `avr` to start it.\n", label)
		return nil
	}

	diagnostics, err := diagnoser.PortDiagnostics(ctx, machine.Name)
	if err != nil {
		return fmt.Errorf("listing the ports forwarded from %s: %w; `avr status` shows whether the environment is healthy", label, err)
	}
	writePorts(app.Out, label, diagnostics)
	return nil
}

// runOpen opens http://localhost:<port> in the host's default browser, after
// checking that the selected environment really forwards that port; when it
// does not, it says so and opens nothing (REQ-16.2).
//
// Like `avr ports`, it never creates or starts an environment: a server cannot
// be listening in one that is not running, so starting it would only turn a
// clear answer into a slow one.
func runOpen(ctx context.Context, app *App, inv cli.Invocation) error {
	port, err := parseOpenArgs(inv.SubcommandArgs)
	if err != nil {
		return Exit(exitUsage, err)
	}

	diagnoser, machines, err := portsBackend(ctx, app)
	if err != nil {
		return err
	}

	target, err := app.Resolve(inv)
	if err != nil {
		return err
	}
	label := target.Selector.Label()

	machine, found := findMachine(machines, target.MachineName)
	switch {
	case !found:
		return fmt.Errorf("port %d is not forwarded: there is no %s environment yet. Start your server in it with `avr <command>`, then run `avr open %d` again", port, label, port)
	case machine.State != types.StateRunning:
		return fmt.Errorf("port %d is not forwarded: %s is not running. Start your server in it with `avr <command>`, then run `avr open %d` again", port, label, port)
	}

	diagnostics, err := diagnoser.PortDiagnostics(ctx, machine.Name)
	if err != nil {
		return fmt.Errorf("checking whether port %d is forwarded from %s: %w", port, label, err)
	}

	diagnostic, found := findPort(diagnostics, port)
	switch {
	case !found:
		return fmt.Errorf("port %d is not forwarded from %s: nothing inside it is listening on that port. %s",
			port, label, portsHint(countOtherRunning(machines, machine.Name)))
	case !diagnostic.Forwarded:
		return fmt.Errorf("port %d is listening in %s but is not reachable from this computer: %s",
			port, label, unforwardedReason(diagnostic))
	}

	address := localAddress(diagnostic)
	if err := app.Browser().Open(ctx, address); err != nil {
		return err
	}
	fmt.Fprintf(app.Out, "Opened %s in your browser%s.\n", address, servedBy(diagnostic))
	return nil
}

// portsBackend obtains what both commands start from: the backend's port
// capability, and its listing of the environments avar owns.
func portsBackend(ctx context.Context, app *App) (provider.PortDiagnoser, []types.MachineStatus, error) {
	p, err := app.Provider(ctx)
	if err != nil {
		return nil, nil, err
	}
	diagnoser, ok := p.(provider.PortDiagnoser)
	if !ok {
		return nil, nil, fmt.Errorf("the %s backend cannot report which ports it forwards; open http://localhost:<port> in a browser yourself", p.ID())
	}
	machines, err := p.Status(ctx)
	if err != nil {
		return nil, nil, err
	}
	return diagnoser, avarOwned(machines), nil
}

// listPortsEverywhere lists the forwarded ports of every running environment
// (REQ-16.1).
//
// One environment whose ports cannot be read does not hide the others': its
// failure is shown where its ports would have been, and the command still exits
// non-zero so a script is not told everything worked.
func listPortsEverywhere(ctx context.Context, app *App, diagnoser provider.PortDiagnoser, machines []types.MachineStatus) error {
	var failed []error
	shown := 0
	for _, machine := range machines {
		if machine.State != types.StateRunning {
			continue
		}
		if shown > 0 {
			fmt.Fprintln(app.Out)
		}
		shown++

		label := environmentLabel(machine)
		diagnostics, err := diagnoser.PortDiagnostics(ctx, machine.Name)
		if err != nil {
			fmt.Fprintf(app.Out, "avar could not read the ports forwarded from %s.\n", label)
			failed = append(failed, fmt.Errorf("%s: %w", label, err))
			continue
		}
		writePorts(app.Out, label, diagnostics)
	}

	if shown == 0 {
		fmt.Fprintln(app.Out, "None of avar's Linux environments is running, so nothing is forwarded. Run `avr` to start one.")
		return nil
	}
	if len(failed) > 0 {
		return fmt.Errorf("listing the ports forwarded from every running environment: %w\n`avr status` shows whether those environments are healthy", errors.Join(failed...))
	}
	return nil
}

// parsePortsArgs reads `ports`'s own flags.
func parsePortsArgs(args []string) (all bool, err error) {
	for _, arg := range args {
		switch arg {
		case portsAllFlag:
			all = true
		default:
			return false, fmt.Errorf("`avr ports` does not understand %q: it takes no arguments, or %s to list the ports of every running Linux environment", arg, portsAllFlag)
		}
	}
	return all, nil
}

// parseOpenArgs reads the one port number `open` takes.
func parseOpenArgs(args []string) (int, error) {
	if len(args) != 1 {
		return 0, fmt.Errorf("`avr open` takes exactly one port number, as in `avr open 3000`, but got %d arguments; run `avr ports` to see the ports that are forwarded", len(args))
	}
	port, err := strconv.Atoi(args[0])
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("`avr open` needs a port number between 1 and 65535, but got %q", args[0])
	}
	return port, nil
}

// findPort finds the diagnostic for the port a user would type into a browser,
// which is the host side of the forward.
func findPort(diagnostics []provider.PortDiagnostic, port int) (provider.PortDiagnostic, bool) {
	for _, d := range diagnostics {
		if hostPort(d) == port {
			return d, true
		}
	}
	return provider.PortDiagnostic{}, false
}

// hostPort is where a guest port is reachable on this computer. A backend that
// publishes at the same number may leave HostPort unset.
func hostPort(d provider.PortDiagnostic) int {
	if d.HostPort != 0 {
		return d.HostPort
	}
	return d.GuestPort
}

// localAddress is the address `avr open` hands to the browser and `avr ports`
// prints, so the two can never disagree.
func localAddress(d provider.PortDiagnostic) string {
	return fmt.Sprintf("http://localhost:%d", hostPort(d))
}

// countOtherRunning counts the running environments other than the selected
// one, which decides whether pointing at `avr ports --all` could help.
func countOtherRunning(machines []types.MachineStatus, selected string) int {
	n := 0
	for _, machine := range machines {
		if machine.Name != selected && machine.State == types.StateRunning {
			n++
		}
	}
	return n
}

// portsHint says where to look next after a port was not found.
func portsHint(otherRunning int) string {
	if otherRunning == 0 {
		return "Run `avr ports` to see the ports that are"
	}
	return fmt.Sprintf("Run `avr ports` to see the ports that are, or `avr ports --all` to include your %s",
		countLabel(otherRunning, "other running environment", "other running environments"))
}

// unforwardedReason is the backend's explanation, or a plain one when it gave
// none.
func unforwardedReason(d provider.PortDiagnostic) string {
	if reason := strings.TrimSpace(d.Reason); reason != "" {
		return reason
	}
	return "the backend did not say why"
}

// servedBy names the process behind an opened port, when it is known.
func servedBy(d provider.PortDiagnostic) string {
	if d.Process == "" && d.PID == 0 {
		return ""
	}
	return ", served by " + processLabel(d)
}

// processLabel renders the guest process holding a port: its command line,
// shortened, and its process id. A port whose holder could not be determined
// says so with a dash rather than being left blank.
func processLabel(d provider.PortDiagnostic) string {
	command := strings.TrimSpace(d.Process)
	if runes := []rune(command); len(runes) > maxProcessWidth {
		command = strings.TrimSpace(string(runes[:maxProcessWidth-1])) + "…"
	}
	switch {
	case command == "" && d.PID == 0:
		return unknownValue
	case command == "":
		return fmt.Sprintf("pid %d", d.PID)
	case d.PID == 0:
		return command
	default:
		return fmt.Sprintf("%s (pid %d)", command, d.PID)
	}
}

// writePorts renders one environment's ports: the forwarded ones as a table a
// user can copy an address from, then any that are listening but cannot be
// reached, with the backend's reason (REQ-7.2), because a server the user
// expected to find is exactly what they ran `avr ports` to look for.
func writePorts(w io.Writer, label string, diagnostics []provider.PortDiagnostic) {
	var forwarded, unreachable []provider.PortDiagnostic
	for _, d := range diagnostics {
		if d.Forwarded {
			forwarded = append(forwarded, d)
		} else {
			unreachable = append(unreachable, d)
		}
	}

	if len(forwarded) == 0 && len(unreachable) == 0 {
		fmt.Fprintf(w, "%s is running, but nothing inside it is listening on a port that reaches this computer.\n", label)
		fmt.Fprintln(w, "A server you start there — `avr npm run dev`, say — appears here once it is listening.")
		return
	}

	if len(forwarded) == 0 {
		fmt.Fprintf(w, "%s forwards no ports to this computer.\n", label)
	} else {
		fmt.Fprintf(w, "%s forwards %s to this computer:\n\n", label, countLabel(len(forwarded), "port", "ports"))
		table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(table, "  PORT\tADDRESS\tPROCESS")
		for _, d := range forwarded {
			fmt.Fprintf(table, "  %d\t%s\t%s\n", hostPort(d), localAddress(d), processLabel(d))
		}
		_ = table.Flush()
	}

	if len(unreachable) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Listening in Linux but not reachable from this computer:")
		table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, d := range unreachable {
			fmt.Fprintf(table, "  %d\t%s\n", d.GuestPort, unforwardedReason(d))
		}
		_ = table.Flush()
	}
}
