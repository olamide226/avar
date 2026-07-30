package lima

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/olamide226/avar/internal/deps"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// Lima's instance states, as `limactl list` reports them.
const (
	limaStatusRunning       = "Running"
	limaStatusStopped       = "Stopped"
	limaStatusBroken        = "Broken"
	limaStatusUninitialized = "Uninitialized"
	limaStatusInstalling    = "Installing"
)

// instance is the part of one `limactl list --json` record avar reads.
//
// It is deliberately a small subset. Lima's output carries its whole instance
// configuration, and unmarshalling into a narrow struct means a field avar does
// not use cannot become a field avar breaks on when Lima adds to it.
type instance struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	// Dir is the instance directory, where the disk image lives.
	Dir    string `json:"dir"`
	VMType string `json:"vmType"`
	Arch   string `json:"arch"`
	CPUs   int    `json:"cpus"`
	// Memory and Disk are byte counts.
	Memory int64 `json:"memory"`
	Disk   int64 `json:"disk"`
	// Config is the instance's own Lima configuration, which is where the
	// applied mounts are recorded.
	Config *instanceLimaConfig `json:"config"`
}

// instanceLimaConfig is the subset of the instance's Lima configuration avar
// reads back.
type instanceLimaConfig struct {
	Mounts []instanceMount `json:"mounts"`
}

// instanceMount is one applied file share.
type instanceMount struct {
	// Location is the host directory being shared.
	Location string `json:"location"`
	// MountPoint is where it appears in the guest. avar always makes it equal
	// to Location (REQ-6.1).
	MountPoint string `json:"mountPoint"`
	Writable   bool   `json:"writable"`
}

// StateInstalling reports a machine that is being created right now: Lima has
// an instance directory for it but has not finished bringing it up.
//
// It is deliberately not StateUnknown or StateBroken. A machine mid-creation
// has no record yet — the record is written only once creation succeeds — so to
// a concurrent avr invocation it looks exactly like the orphan a crash leaves
// behind, and reconciliation deletes unrecorded machines it considers broken or
// unknown. Classifying "coming up" as either of those would let one invocation
// delete the machine another is still provisioning. Anything reconciliation
// does not recognise it leaves alone, which is the safe answer here.
//
// types.MachineState is a plain string type, so this extends the vocabulary
// without changing the shared types package.
const StateInstalling = types.MachineState("installing")

// state maps Lima's instance status onto avar's vocabulary.
//
// A status avar does not recognise becomes StateUnknown rather than being
// guessed at, which is the honest answer and, for anything that acts on state,
// the cautious one.
func (i instance) state() types.MachineState {
	switch strings.ToLower(i.Status) {
	case strings.ToLower(limaStatusRunning):
		return types.StateRunning
	case strings.ToLower(limaStatusStopped):
		return types.StateStopped
	case strings.ToLower(limaStatusBroken):
		return types.StateBroken
	case strings.ToLower(limaStatusInstalling), strings.ToLower(limaStatusUninitialized):
		return StateInstalling
	default:
		return types.StateUnknown
	}
}

// running reports whether the instance is up and usable.
func (i instance) running() bool { return i.state() == types.StateRunning }

// mounts reports the host directories the instance currently shares, sorted and
// deduplicated so that comparing them against a desired set is a plain equality
// check.
func (i instance) mounts() []string {
	if i.Config == nil {
		return nil
	}
	dirs := make([]string, 0, len(i.Config.Mounts))
	for _, m := range i.Config.Mounts {
		if m.Location == "" {
			continue
		}
		dirs = append(dirs, m.Location)
	}
	return sortedUnique(dirs)
}

// view is one invocation's picture of what Lima has.
//
// avar shells out to limactl once per invocation and answers every question from
// the result: EnsureMachine's warm path is measured in whole subprocess
// round-trips, and the budget is ~500 ms for everything (REQ-17.1). The cache is
// per-view and a view is per-call, so nothing is ever remembered across
// invocations — the next `avr` sees the truth even if a machine died in between.
type view struct {
	provider  *Provider
	loaded    bool
	instances map[string]instance
	order     []string
}

// newView begins a fresh look at Lima's instances.
func (p *Provider) newView() *view { return &view{provider: p} }

// load reads every Lima instance once.
func (v *view) load(ctx context.Context) error {
	if v.loaded {
		return nil
	}
	out, err := v.provider.run(ctx, "list", "--json")
	if err != nil {
		return fmt.Errorf("asking Lima which machines exist (%s list --json): %w; "+
			"if that command fails by hand too, Lima's own state may be damaged", v.provider.limactl, err)
	}
	instances, err := parseInstances(out)
	if err != nil {
		return fmt.Errorf("reading the output of %s list --json: %w; "+
			"avar supports Lima %s and newer, and this output does not look like it", v.provider.limactl, err, deps.MinLimaVersion)
	}
	v.instances = make(map[string]instance, len(instances))
	v.order = make([]string, 0, len(instances))
	for _, inst := range instances {
		if inst.Name == "" {
			continue
		}
		v.instances[inst.Name] = inst
		v.order = append(v.order, inst.Name)
	}
	sort.Strings(v.order)
	v.loaded = true
	return nil
}

// lookup reports the named instance, and whether Lima has it at all.
func (v *view) lookup(ctx context.Context, name string) (instance, bool, error) {
	if err := v.load(ctx); err != nil {
		return instance{}, false, err
	}
	inst, ok := v.instances[name]
	return inst, ok, nil
}

// require resolves an instance that must exist, reporting ErrMachineNotFound
// when it does not so a caller can tell "not running" from "never created".
func (v *view) require(ctx context.Context, name string) (instance, error) {
	inst, ok, err := v.lookup(ctx, name)
	if err != nil {
		return instance{}, err
	}
	if !ok {
		return instance{}, fmt.Errorf("%w: %s", provider.ErrMachineNotFound, name)
	}
	return inst, nil
}

// all reports every instance Lima knows about, ordered by name.
func (v *view) all(ctx context.Context) ([]instance, error) {
	if err := v.load(ctx); err != nil {
		return nil, err
	}
	out := make([]instance, 0, len(v.order))
	for _, name := range v.order {
		out = append(out, v.instances[name])
	}
	return out, nil
}

// forget drops the cache, which is what a caller does after changing an
// instance and needing to see the result.
func (v *view) forget() {
	v.loaded = false
	v.instances = nil
	v.order = nil
}

// parseInstances reads `limactl list --json`.
//
// Lima writes one JSON object per line rather than a JSON array, because its
// list command renders each instance through a `{{json .}}` template. A decoder
// loop reads that. It also reads a single JSON array, so that a future Lima
// which switches to array output does not make avar unable to see any machine at
// all — the failure mode of guessing wrong here is that avar tries to provision
// a machine that already exists.
func parseInstances(out []byte) ([]instance, error) {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		// No instances at all is not an error: it is the state of a machine
		// where nobody has ever run avr (REQ-5.3).
		return nil, nil
	}
	if trimmed[0] == '[' {
		var instances []instance
		if err := json.Unmarshal(trimmed, &instances); err != nil {
			return nil, fmt.Errorf("parsing the instance list as JSON: %w", err)
		}
		return instances, nil
	}
	var instances []instance
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	for {
		var inst instance
		switch err := dec.Decode(&inst); {
		case errors.Is(err, io.EOF):
			return instances, nil
		case err != nil:
			return nil, fmt.Errorf("parsing instance %d of the list as JSON: %w", len(instances)+1, err)
		}
		instances = append(instances, inst)
	}
}
