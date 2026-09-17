package cmd

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/olamide226/avar/internal/envpolicy"
	"github.com/olamide226/avar/internal/projconfig"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/resolve"
	"github.com/olamide226/avar/internal/types"
)

// This file applies the parts of a project's .avr.toml that are more than a
// selection (design §3.11). The resolver has already applied distro and arch.
// What is left divides in two:
//
//   - packages and forward_env are grants. A file in a repository the user has
//     just cloned is not the user, so neither applies until the user approves
//     each name on this host (REQ-15.3, PROP-23).
//   - cpus and memory size the project's own environment when it is created,
//     and never anything else: a shared machine serves every project, and
//     resizing one would restart other projects' sessions.

// applyProjectConfig does what a project's file asks of an environment that is
// now running with the project shared into it: installs the approved packages
// it does not have, and advises once when the file's size could not apply.
//
// It belongs to the commands a user enters an environment through — the shell,
// one-shot commands, and the editors — and not to reset, which promises a clean
// environment: the next time the user enters it, the approved packages return.
func applyProjectConfig(ctx context.Context, app *App, p provider.Provider, target resolve.ResolvedTarget, guestCwd string) error {
	if !target.Config.Present() {
		return nil
	}
	if err := installProjectPackages(ctx, app, p, target, guestCwd); err != nil {
		return err
	}
	adviseProjectResources(ctx, app, p, target)
	return nil
}

// reviewProjectGrants asks the user to approve whatever packages and variables
// the project's file declares that they have not approved yet, and returns the
// target with the project's record as it now stands.
//
// It runs before any machine work, so nobody waits through a boot to be asked.
// Only an explicit yes approves. Without a terminal nothing is asked and nothing
// is approved: the pending names are named on stderr and the invocation carries
// on without them, so a script is never blocked and never silently granted.
func reviewProjectGrants(app *App, target resolve.ResolvedTarget) (resolve.ResolvedTarget, error) {
	cfg := target.Config
	if !cfg.Present() {
		return target, nil
	}

	if len(cfg.Packages) > 0 && !cfg.PackagesApplyTo(target.Selector.Distro) {
		fmt.Fprintf(app.Err, "avr: %s lists packages for %s, so none are installed in %s.\n",
			cfg.Path, cfg.Distro, target.Selector.Label())
	}

	packages := pendingPackages(target)
	variables := missingFrom(cfg.ForwardEnv, target.Project.ApprovedForwardEnv)
	if len(packages) == 0 && len(variables) == 0 {
		return target, nil
	}

	if !app.interactive() {
		fmt.Fprintf(app.Err, "avr: %s asks to %s, which needs your approval first.\n",
			cfg.Path, describeGrants(packages, variables))
		fmt.Fprintf(app.Err, "     Run avr in %s from a terminal to review it. Continuing without %s.\n",
			target.Project.Path, pluralize(len(packages)+len(variables), "it", "them"))
		return target, nil
	}

	fmt.Fprintf(app.Err, "avr: %s asks for things avar does only with your approval.\n\n", cfg.Path)
	if len(packages) > 0 {
		fmt.Fprintf(app.Err, "  Install %s into %s:\n", pluralize(len(packages), "this package", "these packages"), environmentPhrase(target))
		fmt.Fprintf(app.Err, "    %s\n", strings.Join(packages, ", "))
	}
	if len(variables) > 0 {
		fmt.Fprintf(app.Err, "  Forward %s from your host into every avr session in this project:\n",
			pluralize(len(variables), "this variable's value", "these variables' values"))
		fmt.Fprintf(app.Err, "    %s\n", strings.Join(variables, ", "))
	}
	fmt.Fprintln(app.Err)

	if !app.confirmYesNo("  Approve? (y/N) ") {
		fmt.Fprintf(app.Err, "avr: not approved. Continuing without %s; avr will ask again next time.\n",
			pluralize(len(packages)+len(variables), "it", "them"))
		return target, nil
	}

	store, err := app.Store()
	if err != nil {
		return target, err
	}
	updated, err := store.UpdateProject(target.Project.ID, func(rec *types.ProjectRecord) {
		if len(packages) > 0 {
			if rec.ApprovedPackages == nil {
				rec.ApprovedPackages = map[string][]string{}
			}
			rec.ApprovedPackages[target.MachineName] = appendMissing(rec.ApprovedPackages[target.MachineName], packages)
		}
		rec.ApprovedForwardEnv = appendMissing(rec.ApprovedForwardEnv, variables)
	})
	if err != nil {
		return target, fmt.Errorf("remember what you approved for %s: %w", target.Project.Path, err)
	}
	target.Project = updated
	fmt.Fprintf(app.Err, "avr: approved. avr asks again only if %s asks for something new.\n", projconfig.FileName)
	return target, nil
}

// environmentPhrase names where a package would be installed, saying plainly
// when that environment is one every other project shares.
func environmentPhrase(target resolve.ResolvedTarget) string {
	if target.Kind == types.KindIsolated {
		return fmt.Sprintf("this project's own %s environment", target.Selector.Label())
	}
	return fmt.Sprintf("the shared %s environment, which every project without its own environment also uses", target.Selector.Label())
}

// describeGrants renders pending names for a one-line notice.
func describeGrants(packages, variables []string) string {
	var parts []string
	if len(packages) > 0 {
		parts = append(parts, "install "+strings.Join(packages, ", "))
	}
	if len(variables) > 0 {
		parts = append(parts, "forward "+strings.Join(variables, ", "))
	}
	return strings.Join(parts, " and ")
}

// pendingPackages is the file's packages for the target's environment that the
// user has not approved for that environment.
func pendingPackages(target resolve.ResolvedTarget) []string {
	cfg := target.Config
	if !cfg.PackagesApplyTo(target.Selector.Distro) {
		return nil
	}
	return missingFrom(cfg.Packages, target.Project.ApprovedPackages[target.MachineName])
}

// approvedPackages is the file's packages that the user approved for the
// target's environment: the only packages avar will install (PROP-23).
func approvedPackages(target resolve.ResolvedTarget) []string {
	cfg := target.Config
	if !cfg.PackagesApplyTo(target.Selector.Distro) {
		return nil
	}
	return presentIn(cfg.Packages, target.Project.ApprovedPackages[target.MachineName])
}

// approvedForwardEnv is the file's variables that the user approved for the
// project: the only ones the file adds to what crosses (PROP-23, PROP-4).
func approvedForwardEnv(target resolve.ResolvedTarget) []string {
	return presentIn(target.Config.ForwardEnv, target.Project.ApprovedForwardEnv)
}

// installProjectPackages installs the approved packages the environment does
// not have yet, as recorded by avar, and records them once installed.
//
// The check is against avar's record rather than the guest, so a warm
// invocation costs no guest round-trip. A failed install is reported and does
// not stop the session: nothing is recorded, so the next invocation tries
// again, and refusing a shell because a mirror is unreachable would turn the
// file into a way to lose access to one's own environment.
func installProjectPackages(ctx context.Context, app *App, p provider.Provider, target resolve.ResolvedTarget, guestCwd string) error {
	approved := approvedPackages(target)
	if len(approved) == 0 {
		return nil
	}

	store, err := app.Store()
	if err != nil {
		return err
	}
	rec, _, err := store.Machine(target.MachineName)
	if err != nil {
		return err
	}
	missing := missingFrom(approved, rec.Packages)
	if len(missing) == 0 {
		return nil
	}

	commands, err := projconfig.InstallCommands(target.Selector.Distro, missing)
	if err != nil {
		return err
	}

	fmt.Fprintf(app.Err, "Installing %s from %s into %s\n", strings.Join(missing, ", "), projconfig.FileName, target.Selector.Label())
	for _, argv := range commands {
		code, err := p.Shell(ctx, target.MachineName, provider.ShellOpts{
			Workdir: guestCwd,
			Argv:    argv,
			Env:     envpolicy.Compose(envpolicy.Input{Host: envpolicy.HostEnviron()}),
			Stdin:   strings.NewReader(""),
			// avar's own work never reaches stdout, which belongs to the
			// command the user ran (REQ-2.3).
			Stdout: app.Err,
			Stderr: app.Err,
		})
		if err == nil && code != 0 {
			err = fmt.Errorf("`%s` exited with status %d", strings.Join(argv, " "), code)
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			fmt.Fprintf(app.Err, "avr: installing %s from %s failed: %v\n", strings.Join(missing, ", "), target.Config.Path, err)
			fmt.Fprintf(app.Err, "     Nothing was recorded as installed, so avr will try again next time.\n")
			return nil
		}
	}

	if err := store.AddPackages(target.MachineName, missing); err != nil {
		return fmt.Errorf("record the packages installed into %s: %w", target.Selector.Label(), err)
	}
	return nil
}

// machineSize is the size to ask for when the target's machine is created: the
// file's cpus and memory for a project's own environment on a backend that
// sizes machines individually, and the backend's defaults otherwise.
func machineSize(p provider.Provider, target resolve.ResolvedTarget) (cpus int, memoryGB float64) {
	if _, ok := projectSizer(p, target); !ok {
		return 0, 0
	}
	return target.Config.CPUs, target.Config.MemoryGB()
}

// projectSizer returns the backend as a MachineSizer when the file's cpus and
// memory would size the target's machine on creation: the file sets one of
// them, the target is the project's own environment, and the backend sizes
// machines individually.
func projectSizer(p provider.Provider, target resolve.ResolvedTarget) (provider.MachineSizer, bool) {
	if target.Config.ResourceDeclaration() == "" || target.Kind != types.KindIsolated {
		return nil, false
	}
	sizer, ok := p.(provider.MachineSizer)
	return sizer, ok
}

// refuseOversizedProjectSize refuses a file that asks for more CPUs or memory
// than this computer has, when this invocation would create the project's own
// environment at that size.
//
// It runs before any machine work, so nobody waits through a provision to be
// told the file cannot be satisfied. An environment avar already has a record
// of is not created again, so its size does not apply and nothing is refused:
// adviseProjectResources says what the file would change instead. The record
// is avar's own and read locally, so a warm invocation asks the backend
// nothing.
func refuseOversizedProjectSize(ctx context.Context, app *App, p provider.Provider, target resolve.ResolvedTarget) error {
	if _, ok := projectSizer(p, target); !ok {
		return nil
	}
	store, err := app.Store()
	if err != nil {
		return err
	}
	if _, exists, err := store.Machine(target.MachineName); err != nil || exists {
		return err
	}
	return checkProjectSizeFits(ctx, p, target)
}

// checkProjectSizeFits refuses the file's cpus and memory if they exceed this
// computer and would size the target's machine. `avr reset` calls it directly,
// because it recreates an environment that exists.
func checkProjectSizeFits(ctx context.Context, p provider.Provider, target resolve.ResolvedTarget) error {
	sizer, ok := projectSizer(p, target)
	if !ok {
		return nil
	}
	cfg := target.Config
	host, err := sizer.HostCapacity(ctx)
	if err != nil {
		return fmt.Errorf("check the cpus and memory %s asks for against this computer: %w\n"+
			"     Remove cpus and memory from the file to let avar choose the size, then try again.", cfg.Path, err)
	}
	excess := cfg.ExceedsHost(host)
	if len(excess) == 0 {
		return nil
	}
	return oversizedProjectSizeError(cfg, host, excess)
}

// oversizedProjectSizeError says which sizes in the file this computer cannot
// give, what it has, and what to do: lower them, or remove them and let avar
// choose. There is no flag that overrides a size for one invocation, so none
// is suggested.
func oversizedProjectSizeError(cfg projconfig.Config, host types.HostCapacity, excess []projconfig.HostExcess) error {
	var limits []string
	for _, e := range excess {
		limits = append(limits, fmt.Sprintf("%s to at most %s", e.Key, hostLimit(e, cfg, host)))
	}
	remedy := fmt.Sprintf("     Lower %s, or remove %s to let avar choose the size, then try again. Nothing was changed.",
		strings.Join(limits, " and "), pluralize(len(excess), "that line", "those lines"))

	if len(excess) == 1 {
		return fmt.Errorf("%s line %d: %s\n%s", cfg.Path, excess[0].Line, excessPhrase(excess[0], cfg, host), remedy)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s asks for more than this computer has:\n", cfg.Path)
	for _, e := range excess {
		fmt.Fprintf(&b, "       line %d: %s\n", e.Line, excessPhrase(e, cfg, host))
	}
	b.WriteString(remedy)
	return errors.New(b.String())
}

// hostExcessNote is a sentence for the advice given when the file's size cannot
// apply, saying that it would also exceed this computer, or "" when it would
// not or when the backend cannot say. Like the advice, it never fails.
func hostExcessNote(ctx context.Context, p provider.Provider, cfg projconfig.Config) string {
	sizer, ok := p.(provider.MachineSizer)
	if !ok {
		return ""
	}
	host, err := sizer.HostCapacity(ctx)
	if err != nil {
		return ""
	}
	excess := cfg.ExceedsHost(host)
	if len(excess) == 0 {
		return ""
	}
	phrases := make([]string, len(excess))
	for i, e := range excess {
		phrases[i] = excessPhrase(e, cfg, host)
	}
	return fmt.Sprintf("     It also asks for more than this computer has (%s), so avr would refuse to create an environment of that size until it is lowered.\n",
		strings.Join(phrases, "; "))
}

// excessPhrase renders one excess with what the computer has, e.g.
// `memory = "64GiB", but this computer has 16 GiB of memory`.
func excessPhrase(e projconfig.HostExcess, cfg projconfig.Config, host types.HostCapacity) string {
	if e.Key == "cpus" {
		return fmt.Sprintf("%s, but this computer has %s", e.Setting, countLabel(host.CPUs, "CPU", "CPUs"))
	}
	return fmt.Sprintf("%s, but this computer has %s of memory", e.Setting, hostMemory(cfg, host))
}

// hostLimit is the largest value of the excess's key the file could hold on
// this computer, in the unit the file used.
func hostLimit(e projconfig.HostExcess, cfg projconfig.Config, host types.HostCapacity) string {
	if e.Key == "cpus" {
		return strconv.Itoa(host.CPUs)
	}
	if cfg.MemoryMiB%1024 != 0 {
		return fmt.Sprintf("%d MiB", host.MemoryBytes>>20)
	}
	return fmt.Sprintf("%d GiB", host.MemoryBytes>>30)
}

// hostMemory renders the computer's memory in the unit the file used, so the
// two numbers in a message can be compared at a glance. Gibibytes are shown
// to a tenth, rounded down, so a host just short of a request never reads as
// equal to it.
func hostMemory(cfg projconfig.Config, host types.HostCapacity) string {
	if cfg.MemoryMiB%1024 != 0 {
		return fmt.Sprintf("%d MiB", host.MemoryBytes>>20)
	}
	tenths := (host.MemoryBytes * 10) >> 30
	return formatGB(float64(tenths)/10) + " GiB"
}

// adviseProjectResources tells the user, once for each declaration, when the
// file's cpus and memory could not apply to this project's environment, and
// what to do about it.
//
// It never resizes or restarts anything. Checking an existing environment's size
// costs one Status call, made only while the declaration has not been checked,
// so an ordinary warm invocation pays nothing. Like the native-workspace
// advisory, every failure is silent: this is advice on the way to a shell.
func adviseProjectResources(ctx context.Context, app *App, p provider.Provider, target resolve.ResolvedTarget) {
	cfg := target.Config
	declaration := cfg.ResourceDeclaration()
	if declaration == "" || target.Project.AdvisedResources == declaration {
		return
	}

	var advice string
	switch _, sizer := p.(provider.MachineSizer); {
	case target.Kind != types.KindIsolated:
		advice = fmt.Sprintf("avr: %s sets %s for this project's own environment, and this project uses the shared one, which avr never resizes.\n"+
			"     Run `avr --isolate` to give the project its own environment at that size.\n",
			cfg.Path, describeDeclared(cfg))
	case !sizer:
		advice = fmt.Sprintf("avr: %s sets %s, but on this host every Linux environment shares one allocation, so the size of one environment cannot be set.\n",
			cfg.Path, describeDeclared(cfg))
	default:
		machines, err := p.Status(ctx)
		if err != nil {
			return
		}
		machine, found := findMachine(machines, target.MachineName)
		if found && !sizeMatches(cfg, machine) {
			advice = fmt.Sprintf("avr: %s sets %s, and this project's environment was created with %s. avr sets a size only when it creates an environment.\n"+
				"     Run `avr reset` to recreate it at the declared size; everything installed inside it is lost.\n",
				cfg.Path, describeDeclared(cfg), describeSize(machine.CPUs, machine.MemoryGB))
		}
	}
	if advice != "" {
		advice += hostExcessNote(ctx, p, cfg)
	}

	store, err := app.Store()
	if err != nil {
		return
	}
	// Recorded before the advice is printed, as the native-workspace advisory
	// does, so an interrupted invocation does not repeat it.
	if _, err := store.UpdateProject(target.Project.ID, func(rec *types.ProjectRecord) {
		rec.AdvisedResources = declaration
	}); err != nil {
		return
	}
	fmt.Fprint(app.Err, advice)
}

// sizeMatches reports whether a machine has the size the file declares, for
// whichever of cpus and memory it declares. Memory is compared to within a
// hundredth of a gibibyte, which is the precision backends report it at.
func sizeMatches(cfg projconfig.Config, machine types.MachineStatus) bool {
	if cfg.CPUs > 0 && machine.CPUs != cfg.CPUs {
		return false
	}
	if cfg.MemoryMiB > 0 && math.Abs(machine.MemoryGB-cfg.MemoryGB()) > 0.01 {
		return false
	}
	return true
}

// describeDeclared renders the file's declaration the way avar describes an
// environment's resources elsewhere, e.g. "4 CPU · 8 GB RAM".
func describeDeclared(cfg projconfig.Config) string {
	return describeSize(cfg.CPUs, cfg.MemoryGB())
}

func describeSize(cpus int, memoryGB float64) string {
	var parts []string
	if cpus > 0 {
		parts = append(parts, fmt.Sprintf("%d CPU", cpus))
	}
	if memoryGB > 0 {
		parts = append(parts, formatGB(memoryGB)+" GB RAM")
	}
	return strings.Join(parts, " · ")
}

// missingFrom returns the names in want that have is missing, in want's order.
func missingFrom(want, have []string) []string {
	var out []string
	for _, name := range want {
		if !slices.Contains(have, name) {
			out = append(out, name)
		}
	}
	return out
}

// presentIn returns the names in want that have also holds, in want's order.
func presentIn(want, have []string) []string {
	var out []string
	for _, name := range want {
		if slices.Contains(have, name) {
			out = append(out, name)
		}
	}
	return out
}

// appendMissing adds to list the names it does not already hold.
func appendMissing(list, names []string) []string {
	return append(list, missingFrom(names, list)...)
}
