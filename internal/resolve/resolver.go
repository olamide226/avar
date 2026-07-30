// Package resolve answers avar's central question: given where the user is
// standing and what they typed, which single machine does this invocation
// target?
//
// The user never names a machine (REQ-1.5), so something has to. This package
// is that something, and it is the only place that decides:
//
//   - which environment applies, by precedence — explicit flags, then the
//     project's remembered choice, then avar's global configuration, then
//     avar's built-in defaults;
//   - which (distro, version, arch) combinations exist at all, and what a bare
//     distribution name means (see matrix.go);
//   - what the target machine is called, deterministically, so that the same
//     inputs always reach the same machine and distinct environments never
//     share one (REQ-4.3, PROP-2).
//
// Resolve is a deterministic function of its arguments. It reads neither the
// environment, the clock, nor the filesystem: everything it needs — the current
// directory, the host architecture, the global configuration layer — arrives as
// a parameter. Its only side effects go through the injected Store, which is
// how a project's first use gets recorded and how `--isolate` is remembered
// (REQ-11.2). That is what makes PROP-2 checkable against a fake store with no
// VM in sight.
//
// Nothing here names a virtualization technology, an image, or a command-line
// tool. The resolver decides *what* environment is wanted and hands the answer
// to a provider, which decides how to produce it (REQ-17.3).
package resolve

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/types"
)

// projectIDPrefixLen is how much of a Project_Identity an isolated machine's
// name carries: 10 hex characters, 40 bits. Short enough to read in a machine
// listing, long enough that two of one developer's projects colliding is not a
// practical concern.
const projectIDPrefixLen = 10

// Store is the part of avar's state the resolver needs, and no more: the
// project records it reads to honour a remembered choice, plus the two writes
// that keep those records true.
//
// The interface is declared here, by the consumer, rather than taken as a
// concrete store. That keeps the resolver testable with a small fake, keeps it
// from quietly growing a dependency on the rest of the store's surface, and
// keeps the direction of dependency pointing at avar's own vocabulary rather
// than at a particular persistence layer. *state.Store satisfies it
// structurally; the package's tests assert that at compile time, which is
// deliberately the only place internal/resolve and internal/state meet.
type Store interface {
	// Projects returns every project avar has recorded. Order does not matter
	// to the resolver.
	Projects() ([]types.ProjectRecord, error)

	// EnsureProject records the directory at path as a project if it is not
	// recorded yet, marks it used now, and returns the record. The returned
	// Path is canonical: absolute, with symlinks resolved.
	EnsureProject(path string) (types.ProjectRecord, error)

	// UpdateProject applies mutate to the record identified by id and stores
	// the result. The record must already exist; mutate must not change the
	// record's identity or path.
	UpdateProject(id string, mutate func(*types.ProjectRecord)) (types.ProjectRecord, error)
}

// Preference is one layer of the precedence chain: a partially specified
// opinion about the environment. A zero-valued field means the layer has no
// opinion and the layer below it decides.
type Preference struct {
	// Distro, when set, chooses the distribution.
	Distro types.Distro

	// Version pins Distro's release. It is only meaningful alongside Distro —
	// a version identifies a release of a particular distribution, so a layer
	// that names a version without one is a mistake rather than a preference,
	// and Resolve says so instead of guessing which distribution was meant.
	Version string

	// Arch, when set, chooses the guest architecture.
	Arch types.Arch
}

// Options carries what Resolve needs about the world it is resolving in.
// The zero value means "this host, avar's built-in defaults", which is what
// production code wants; tests set the fields explicitly so that a result does
// not depend on which machine the test runs on.
type Options struct {
	// HostArch is the architecture guests run on without emulation. Empty means
	// the architecture avar itself was built for.
	//
	// It is a parameter rather than a lookup so that resolution for an
	// Apple Silicon host and for an Intel host are both testable anywhere,
	// which is what REQ-4.6's emulation reporting needs.
	HostArch types.Arch

	// Config is the global-configuration layer: avar's own `~/.avr/config.toml`
	// defaults, which sit below a project's remembered choice and above avar's
	// built-in defaults.
	//
	// Parsing that file is not this package's business (task 22, REQ-15.1);
	// this field is the seam it lands in. Until then callers leave it zero and
	// the built-in defaults apply, and no part of the precedence chain has to
	// be restructured when it stops being zero.
	Config Preference
}

// ResolvedTarget is one fully specified answer: everything a caller needs to
// bring the right machine up and enter it, with nothing left to decide.
type ResolvedTarget struct {
	// Selector is the environment, fully specified: no empty version, no
	// implied architecture, and checked against the supported matrix.
	Selector types.EnvironmentSelector

	// MachineName is the machine this invocation targets, derived
	// deterministically from Selector, and from the project as well when the
	// selector is isolated. It carries avar's ownership prefix and satisfies
	// types.ValidateMachineName.
	MachineName string

	// Kind is the role the machine plays, which follows from the resolved
	// isolation: a per-project machine or the machine every project shares.
	Kind types.MachineKind

	// Project is the record for the project this invocation belongs to, which
	// is not necessarily the directory avr was run from — see the note on
	// project resolution in resolveProject. Its Path is the directory that has
	// to be shared into the guest; its ID identifies an isolated machine's
	// project.
	Project types.ProjectRecord

	// GuestCwd is the directory the guest process starts in. It equals the host
	// directory avr was run from, because the project is mounted at the
	// identical absolute path, which is what makes a subdirectory of a project
	// land in the matching subdirectory inside Linux (REQ-6.1, REQ-6.6,
	// PROP-1).
	GuestCwd string

	// Emulated reports that this environment can only run under CPU emulation
	// on this host, which costs enough performance to be worth telling the user
	// about once, at provision time (REQ-4.6).
	//
	// Resolving never warns — it reports. It is Selector.Emulated() evaluated
	// against the host architecture resolution actually used, so that a
	// resolution performed for a different host stays self-consistent.
	Emulated bool
}

// Resolve maps (cwd, flags, state) onto the single machine this invocation
// targets.
//
// cwd is the host directory avr was run from, and must be absolute and
// symlink-resolved — the caller canonicalises it (state.ResolveProjectPath),
// because a project's identity is the identity of a real directory and
// resolving it is filesystem work that would make this function impure.
//
// The environment is resolved by precedence, highest first: the flags the user
// typed, the project's own remembered choices, opts.Config, then avar's
// built-in defaults. A layer that names a distribution also fixes its version —
// so `--distro fedora` never inherits Ubuntu's release number from a layer
// below — and a distribution named without a version gets that distribution's
// pinned release.
//
// Resolve writes through st: the project is registered on first use, and
// --isolate is remembered so that a later bare `avr` in the same project goes
// to the same private machine (REQ-11.2). --shared deliberately writes nothing.
func Resolve(cwd string, sel cli.Selector, st Store, opts Options) (ResolvedTarget, error) {
	if st == nil {
		return ResolvedTarget{}, errors.New("resolve the target environment: no state store was provided")
	}

	guestCwd, err := guestWorkdir(cwd)
	if err != nil {
		return ResolvedTarget{}, err
	}

	hostArch := opts.HostArch
	if hostArch == "" {
		hostArch = types.HostArch()
	}

	project, err := resolveProject(guestCwd, st)
	if err != nil {
		return ResolvedTarget{}, err
	}

	isolated, project, err := resolveIsolation(sel, project, st)
	if err != nil {
		return ResolvedTarget{}, err
	}

	env, err := resolveEnvironment(sel, project, opts.Config, hostArch)
	if err != nil {
		return ResolvedTarget{}, err
	}
	env.Isolated = isolated

	name, err := machineName(env, project.ID)
	if err != nil {
		return ResolvedTarget{}, err
	}

	kind := types.KindShared
	if isolated {
		kind = types.KindIsolated
	}

	return ResolvedTarget{
		Selector:    env,
		MachineName: name,
		Kind:        kind,
		Project:     project,
		GuestCwd:    guestCwd,
		Emulated:    env.Arch != hostArch,
	}, nil
}

// guestWorkdir checks the directory avar was invoked from and returns the path
// the guest will use for it, which is the same path (REQ-6.1).
//
// The absolute check is not pedantry: every later comparison against a recorded
// project path is a path comparison, and a relative or unresolved directory
// would silently match nothing and register a second project for a directory
// avar already knows.
func guestWorkdir(cwd string) (string, error) {
	trimmed := strings.TrimSpace(cwd)
	if trimmed == "" {
		return "", errors.New("resolve the target environment: no current directory was given")
	}
	if !filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("resolve the target environment for %q: avar needs an absolute, symlink-resolved directory", cwd)
	}
	return filepath.Clean(trimmed), nil
}

// resolveProject finds the project an invocation belongs to and records its use.
//
// avar has no repository-root marker: a project is simply a directory the user
// has run avr in (glossary). The consequence for a subdirectory is a real
// design decision, and this is it — the project for a directory is the nearest
// enclosing directory that avar has *already recorded*, and only when nothing
// at or above the directory is recorded does the directory itself become a new
// project.
//
// So the first `avr` in ~/code/app registers ~/code/app; a later `avr` in
// ~/code/app/api belongs to ~/code/app, reuses its mount and its environment,
// and starts the guest in ~/code/app/api (REQ-6.6). The alternative — every
// subdirectory becoming its own project — would give one repository many
// overlapping mounts and would silently drop a subdirectory out of a project's
// remembered isolated environment, which is the same bug in a worse disguise.
func resolveProject(dir string, st Store) (types.ProjectRecord, error) {
	recorded, err := st.Projects()
	if err != nil {
		return types.ProjectRecord{}, fmt.Errorf("read avar's project records to resolve %s: %w", dir, err)
	}

	// The nearest enclosing recorded directory: with nested projects recorded,
	// the longest match is the innermost one, which is the one whose mount and
	// remembered choices are most specific to where the user is standing.
	root := ""
	for _, rec := range recorded {
		if covers(rec.Path, dir) && len(rec.Path) > len(root) {
			root = rec.Path
		}
	}
	if root == "" {
		root = dir
	}

	rec, err := st.EnsureProject(root)
	if err != nil {
		return types.ProjectRecord{}, err
	}
	if !covers(rec.Path, dir) {
		// The store canonicalises what it records. If that no longer contains
		// the directory we resolved for, the caller handed us a path that was
		// not symlink-resolved, and continuing would mount one directory and
		// enter another (REQ-6.5).
		return types.ProjectRecord{}, fmt.Errorf("resolve the project for %s: avar recorded that directory as %s; pass an absolute, symlink-resolved directory", dir, rec.Path)
	}
	return rec, nil
}

// covers reports whether dir is root itself or a directory beneath it. Both
// arguments are cleaned absolute paths, which makes this an exact answer rather
// than an approximate one: /a/b covers /a/b and /a/b/c, but not /a/bc.
func covers(root, dir string) bool {
	if root == "" {
		return false
	}
	if root == dir {
		return true
	}
	sep := string(filepath.Separator)
	if root == sep {
		return strings.HasPrefix(dir, sep)
	}
	return strings.HasPrefix(dir, root+sep)
}

// resolveIsolation decides whether this invocation targets the project's own
// machine or the machine every project shares, and keeps the project record in
// step with that decision.
//
// The asymmetry between the two flags is the point (REQ-11.3): --isolate is a
// choice about the project and is remembered, while --shared is an override for
// one invocation and deliberately changes nothing, so that a single `avr
// --shared` cannot quietly undo a project's isolation.
func resolveIsolation(sel cli.Selector, project types.ProjectRecord, st Store) (bool, types.ProjectRecord, error) {
	switch {
	case sel.Isolate && sel.Shared:
		// internal/cli rejects this argv, so reaching here means a caller built
		// the selector by hand. Refuse rather than let flag order decide.
		return false, types.ProjectRecord{}, errors.New("resolve the target environment: --isolate and --shared contradict each other")

	case sel.Isolate:
		if project.Isolated {
			return true, project, nil
		}
		updated, err := st.UpdateProject(project.ID, func(rec *types.ProjectRecord) {
			rec.Isolated = true
		})
		if err != nil {
			return false, types.ProjectRecord{}, fmt.Errorf("remember that %s uses its own Linux environment: %w", project.Path, err)
		}
		return true, updated, nil

	case sel.Shared:
		return false, project, nil

	default:
		// A project that was isolated stays isolated on a bare `avr`
		// (REQ-11.2). Clearing that default is `avr isolate off` (task 17),
		// which writes Isolated=false and lands right back here.
		return project.Isolated, project, nil
	}
}

// resolveEnvironment applies the precedence chain to the distribution, version
// and architecture, and checks the result against the supported matrix.
func resolveEnvironment(sel cli.Selector, project types.ProjectRecord, config Preference, hostArch types.Arch) (types.EnvironmentSelector, error) {
	layers := []struct {
		source string
		pref   Preference
	}{
		{"the command line", Preference{Distro: sel.Distro, Version: sel.DistroVersion, Arch: sel.Arch}},
		{fmt.Sprintf("the environment remembered for %s", project.Path), preferenceOf(project.Selector)},
		{"avar's configuration", config},
		{"avar's built-in defaults", Preference{Distro: DefaultDistro, Arch: hostArch}},
	}

	var (
		distro  types.Distro
		version string
		arch    types.Arch
	)
	for _, layer := range layers {
		pref := normalise(layer.pref)

		if pref.Version != "" && pref.Distro == "" {
			return types.EnvironmentSelector{}, fmt.Errorf("resolve the target environment: %s names version %q without naming a distribution", layer.source, pref.Version)
		}

		// A layer that chooses the distribution also fixes its version, so
		// `--distro fedora` cannot inherit a version pinned for Ubuntu by a
		// layer underneath it.
		if distro == "" && pref.Distro != "" {
			distro, version = pref.Distro, pref.Version
		}
		if arch == "" && pref.Arch != "" {
			arch = pref.Arch
		}
	}
	if distro == "" || arch == "" {
		// Unreachable while the last layer names both, which is what makes the
		// chain total. Reported rather than assumed so that removing that
		// guarantee fails loudly instead of producing a nameless machine.
		return types.EnvironmentSelector{}, fmt.Errorf("resolve the target environment: avar's defaults are incomplete (distribution %q, architecture %q)", distro, arch)
	}

	if version == "" {
		pinned, ok := PinnedVersion(distro)
		if !ok {
			return types.EnvironmentSelector{}, fmt.Errorf("avar cannot run %q: %w; supported distributions are %s", distro, ErrUnsupportedEnvironment, joinDistros(SupportedDistros()))
		}
		version = pinned
	}

	if err := checkSupported(distro, version, arch); err != nil {
		return types.EnvironmentSelector{}, err
	}
	return types.EnvironmentSelector{Distro: distro, Version: version, Arch: arch}, nil
}

// preferenceOf reads a project's remembered distro/arch override.
//
// The record's Isolated field, not the selector's, is what remembers isolation:
// isolation is about which machine serves the project, and is resolved
// separately (resolveIsolation), so only the environment fields are taken here.
func preferenceOf(sel *types.EnvironmentSelector) Preference {
	if sel == nil {
		return Preference{}
	}
	return Preference{Distro: sel.Distro, Version: sel.Version, Arch: sel.Arch}
}

// normalise lower-cases and trims a layer's values. Machine names are built
// from them and avar's name pattern rejects upper case, so a configuration file
// spelling "Ubuntu" must resolve rather than produce a name no later operation
// would accept.
func normalise(p Preference) Preference {
	return Preference{
		Distro:  types.Distro(lower(string(p.Distro))),
		Version: lower(p.Version),
		Arch:    types.Arch(lower(string(p.Arch))),
	}
}

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// machineName derives the name of the machine a target is served by. It is
// deterministic — the same inputs always produce the same name — and injective:
// no two distinct targets can be served by one machine (REQ-4.3, PROP-2).
//
// Every name spells its environment out (`avr-ubuntu-24.04-arm64`). That makes
// distinct (distro, version, arch) triples distinct names by construction, and
// makes a machine listing readable to a user who never asked for machines.
//
// An isolated machine carries the project's identity as well
// (`avr-prj-3fa9c2b1d0-ubuntu-24.04-arm64`), and needs both halves. The project
// hash is what keeps the name stable however deep in the project the user is
// standing. The environment is part of the identity because an isolated
// environment is derived from a clean base image of *the selected* (distro,
// arch) (REQ-11.1) — naming it after the project alone would mean `avr
// --isolate --distro fedora` resolving to a machine already built from Ubuntu,
// so avar would silently hand the user the wrong distribution (REQ-4.2). It
// also keeps isolation consistent with REQ-4.3 rather than making it the one
// place where each (distro, arch) does not get its own machine.
//
// The `avr-` prefix is also avar's ownership marker (REQ-5.4, PROP-6), so the
// result is validated here: a name outside types.MachineNamePattern would make
// every later operation refuse to touch the machine, and the failure is far
// easier to understand at the point where the offending environment can be
// named.
func machineName(env types.EnvironmentSelector, projectID string) (string, error) {
	environment := fmt.Sprintf("%s-%s-%s", env.Distro, env.Version, env.Arch)

	name := types.MachineNamePrefix + environment
	if env.Isolated {
		if len(projectID) < projectIDPrefixLen {
			return "", fmt.Errorf("name the machine for %s: project identity %q is too short to name a machine from", env.Label(), projectID)
		}
		name = fmt.Sprintf("%s%s-%s-%s", types.MachineNamePrefix, isolatedNameToken, projectID[:projectIDPrefixLen], environment)
	}

	name = lower(name)
	if err := types.ValidateMachineName(name); err != nil {
		return "", fmt.Errorf("name the machine for %s: %w", env.Label(), err)
	}
	return name, nil
}
