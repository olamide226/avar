package wsl2

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// Reading what WSL has is three commands rather than one, and that is a
// deliberate trade against parsing text Windows translates.
//
// `wsl --list --verbose` is the only form that reports a distribution's WSL
// version, but its STATE column is localized: a French Windows says "En cours
// d'exécution" where an English one says "Running", and matching either is a
// bug on the other. Its header row is localized too, and has the same three
// columns as a data row, so it cannot be told from one by shape alone.
//
// `wsl --list --quiet` and `wsl --list --running --quiet` print nothing but
// names — no header, no state, nothing to translate. Set membership between
// them answers "is it running" exactly, in every locale. The verbose form is
// then read only for the version number, which is a digit and therefore the same
// everywhere, and only for rows whose name avar already knows from the quiet
// listing.
//
// Three cheap subprocesses that are right in every locale beat one that is right
// in English (design §3.6).

// distribution is what avar knows about one WSL distribution.
type distribution struct {
	// Name is the registered name, which for an avar-owned distribution is
	// the machine name.
	Name string
	// Running reports whether the distribution is currently running.
	Running bool
	// WSLVersion is 1 or 2, or 0 when the verbose listing did not report one.
	WSLVersion int
}

// state maps a distribution onto avar's vocabulary.
//
// A WSL distribution has no "broken" state to report: it is registered or it is
// not, and a registered one either has a running virtual machine behind it or
// starts one on demand. The one unhealthy state avar recognises is a
// registration under WSL 1, which is not something to repair silently — it is
// refused with the conversion command and left exactly as it is (REQ-18.4,
// PROP-15).
func (d distribution) state() types.MachineState {
	switch {
	case d.WSLVersion == 1:
		return types.StateBroken
	case d.Running:
		return types.StateRunning
	default:
		return types.StateStopped
	}
}

// view is one invocation's picture of what WSL has.
//
// It is loaded at most once per view and never cached across invocations: a
// command reads the world, decides, and exits (design §3.6). Callers that need
// to see a change they just made forget the view first.
type view struct {
	provider *Provider
	loaded   bool
	distros  map[string]distribution
}

func (p *Provider) newView() *view { return &view{provider: p} }

// load reads the three listings and reconciles them into one picture.
func (v *view) load(ctx context.Context) error {
	if v.loaded {
		return nil
	}

	registered, err := v.provider.listNames(ctx)
	if err != nil {
		return err
	}
	running, err := v.provider.listRunningNames(ctx)
	if err != nil {
		return err
	}
	versions, err := v.provider.listVersions(ctx, registered)
	if err != nil {
		return err
	}

	v.distros = make(map[string]distribution, len(registered))
	for _, name := range registered {
		v.distros[name] = distribution{
			Name:       name,
			Running:    running[name],
			WSLVersion: versions[name],
		}
	}
	v.loaded = true
	return nil
}

// lookup reports one distribution, and whether WSL has it.
func (v *view) lookup(ctx context.Context, name string) (distribution, bool, error) {
	if err := v.load(ctx); err != nil {
		return distribution{}, false, err
	}
	d, ok := v.distros[name]
	return d, ok, nil
}

// require reports one distribution, or ErrMachineNotFound.
func (v *view) require(ctx context.Context, name string) (distribution, error) {
	d, ok, err := v.lookup(ctx, name)
	if err != nil {
		return distribution{}, err
	}
	if !ok {
		return distribution{}, fmt.Errorf("%w: %s", provider.ErrMachineNotFound, name)
	}
	return d, nil
}

// all reports every distribution WSL has, avar's and the user's alike, ordered
// by name. Filtering to avar's own is the caller's job, because the reconciler
// needs to see an unrecorded avr- distribution that Status must not show.
func (v *view) all(ctx context.Context) ([]distribution, error) {
	if err := v.load(ctx); err != nil {
		return nil, err
	}
	out := make([]distribution, 0, len(v.distros))
	for _, d := range v.distros {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// forget drops the loaded picture so the next question is asked of WSL again.
func (v *view) forget() { v.loaded = false }

// listNames reports every registered distribution.
func (p *Provider) listNames(ctx context.Context) ([]string, error) {
	out, err := p.run(ctx, "--list", "--quiet")
	if err != nil {
		return nil, fmt.Errorf("listing WSL distributions: %w", err)
	}
	return parseQuietList(out), nil
}

// listRunningNames reports the distributions that are running, as a set.
func (p *Provider) listRunningNames(ctx context.Context) (map[string]bool, error) {
	out, err := p.run(ctx, "--list", "--running", "--quiet")
	if err != nil {
		// WSL reports "there are no running distributions" by failing, which
		// is an answer rather than an error: nothing is running.
		return map[string]bool{}, nil
	}
	running := make(map[string]bool)
	for _, name := range parseQuietList(out) {
		running[name] = true
	}
	return running, nil
}

// listVersions reports the WSL version of each named distribution.
func (p *Provider) listVersions(ctx context.Context, names []string) (map[string]int, error) {
	if len(names) == 0 {
		return map[string]int{}, nil
	}
	out, err := p.run(ctx, "--list", "--verbose")
	if err != nil {
		return nil, fmt.Errorf("reading the WSL version of each distribution: %w", err)
	}
	return parseVerboseVersions(out, names), nil
}

// parseQuietList reads the names out of a `wsl --list --quiet` listing.
//
// The output is one name per line and nothing else — no header, no columns, no
// translated word anywhere — which is the whole reason avar asks for it in this
// form. Blank lines are dropped: WSL pads its UTF-16 output with them, and a
// distribution can never be named "".
func parseQuietList(out string) []string {
	lines := strings.Split(out, "\n")
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		name := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names
}

// parseVerboseVersions reads the WSL version of each known distribution out of a
// `wsl --list --verbose` listing.
//
// Only the numbers are read, and only for names the quiet listing already
// reported. A row is matched by containing its name as a whole field, so the
// default-distribution marker in the first column and any amount of column
// padding are irrelevant; the version is the last field, which is a digit in
// every locale. A row avar cannot match, or whose last field is not a number, is
// left out rather than guessed at — a distribution with no version reported is
// one avar declines to act on, which is the safe direction.
func parseVerboseVersions(out string, names []string) map[string]int {
	known := make(map[string]bool, len(names))
	for _, name := range names {
		known[name] = true
	}

	versions := make(map[string]int, len(names))
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSuffix(line, "\r"))
		if len(fields) < 2 {
			continue
		}
		version, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil {
			// The header row ends in a translated word, not a number.
			continue
		}
		for _, field := range fields[:len(fields)-1] {
			if known[field] {
				versions[field] = version
				break
			}
		}
	}
	return versions
}
