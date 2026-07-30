package state

// Reconciliation: converge avar's records on the machines the backend has.
//
// The store records what avar *intends*; the backend is the source of truth for
// what *exists* (design §3.3). The two disagree whenever avr is killed between
// the two writes — a machine created but not yet recorded, a machine deleted by
// hand while its record stayed behind — and REQ-17.5's promise is that the
// *next* invocation repairs the disagreement by itself, never that the user
// repairs it with `limactl`.
//
// # Ownership, and the one asymmetry in it
//
// Everywhere else in avar, "avar owns this machine" means the `avr-` prefix
// *and* an entry in machines.json (REQ-5.4, PROP-6). Reconciliation is the one
// place where the prefix alone is enough to act, and the asymmetry is worth
// stating precisely because it is the subtlest rule in the package.
//
// It is sound only because the prefix is the marker avar itself stamps: avar is
// the only thing that creates `avr-`-named machines, so the prefix without a
// record can only mean "avar created this and did not finish recording it".
// Demanding the record here would be circular — the missing record is the very
// damage being repaired.
//
// The licence it grants is deliberately narrow:
//
//   - A name that fails types.ValidateMachineName is *invisible*. It is not
//     adopted, not deleted, and not even mentioned in the result: reporting
//     somebody's `default` or `docker-desktop` instance would already be avar
//     taking an interest in a machine that is none of its business (PROP-6).
//   - Prefix without a record permits exactly two outcomes, and both end with
//     the two sides agreeing: write the missing record (adopt), or delete a
//     machine the backend says cannot work. Nothing outside avar's namespace is
//     reachable either way.
//   - Prefix *with* a record is the ordinary, consistent case and grants
//     nothing new. In particular a recorded machine is never deleted here, however
//     unhealthy the backend says it is: that machine may hold weeks of the
//     user's work, and design §6 ("Start fails on existing machine") is explicit
//     that avar reports it rather than auto-deleting user data. Deletion is
//     confined to machines avar has no record of, which bounds the blast radius
//     of the whole mechanism to environments nothing has ever used.
//
// # Ordering, and why the lock is not held over backend work
//
// Reconcile runs in three phases:
//
//  1. Ask the backend what it has. No lock is held: it is a subprocess in the
//     real provider, and holding the state lock across it would make every
//     other invocation wait out the lock timeout (see fileLock on re-entrancy —
//     the lock is taken once, by Store.Update, and never nested).
//  2. One Store.Update: read the records, drop the dangling ones, write the
//     records of the machines being adopted. Pure store work, atomic, and the
//     only phase that writes.
//  3. Outside the lock again: delete the unusable machines through the backend.
//
// Phase 3 needs no follow-up transaction, which is what makes the ordering
// safe: the machines it deletes have no record, so there is nothing to
// un-record once they are gone. If a delete fails, or avar is killed between
// phase 2 and phase 3, the next invocation derives the same plan from the same
// evidence — the machine is still there, still unusable, still unrecorded — so
// the repair is idempotent rather than half-applied.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/olamide226/avar/internal/types"
)

// Backend is the part of the backend contract reconciliation needs: a listing
// of the machines that exist, and a way to remove one.
//
// It is declared here, by the consumer, rather than imported from
// internal/provider, because internal/state must not know about backends at all
// (design §3.3). provider.Provider satisfies it structurally, so the real
// LimaProvider can be passed straight in; reconcile_test.go asserts that at
// compile time, where a test-only import of internal/provider costs the
// production package nothing.
//
// Two obligations that Provider states in general terms matter sharply here:
//
//   - Status must report every machine carrying avar's prefix, including ones
//     absent from machines.json. Those are exactly the orphans reconciliation
//     exists to adopt; a listing filtered by avar's own records would make them
//     invisible and adoption unreachable. Filtering by record membership is
//     meaningful for `avr status` — after reconciliation has run, prefix and
//     record agree — but it cannot be applied on this path.
//   - Delete must be idempotent, since reconciliation deletes machines whose
//     state it read a moment ago and which another invocation may have removed
//     in between.
type Backend interface {
	Status(ctx context.Context) ([]types.MachineStatus, error)
	Delete(ctx context.Context, machine string) error
}

// Action is what reconciliation did about one machine. The store never prints
// (CLAUDE.md): the caller renders these, and per design §6 deleting somebody's
// environment must be something the caller can tell the user about.
type Action string

const (
	// ActionAdopted reports that a machine the backend had but avar did not
	// know about was taken into machines.json rather than left orphaned.
	ActionAdopted Action = "adopted"

	// ActionForgotten reports that a dangling record — a machine avar had a
	// record of and the backend no longer has — was dropped.
	ActionForgotten Action = "forgotten"

	// ActionDeleted reports that an unrecorded machine the backend says cannot
	// work was destroyed, so the next invocation provisions cleanly. This is
	// the one action the user must be told about: it removes an environment.
	ActionDeleted Action = "deleted"

	// ActionLeft reports a machine reconciliation deliberately did not touch
	// because the listing does not describe something avar can record, and
	// destroying a machine that may well work is worse than leaving it. Reason
	// says which. Such a machine is not wedged: its name is deterministic
	// (design §3.2), so the invocation that targets it calls EnsureMachine —
	// idempotent on an existing machine — and then records it with the
	// information only that invocation has.
	ActionLeft Action = "left"
)

// Change is one machine reconciliation acted on, or deliberately did not.
type Change struct {
	// Machine is the machine's name. It always carries avar's prefix.
	Machine string
	// Action is what was done about it.
	Action Action
	// Selector is the environment the machine provides, as far as avar knows
	// it: from the record for ActionForgotten, from the backend's listing
	// otherwise. It is the zero value when neither side could say.
	Selector types.EnvironmentSelector
	// Reason is one user-facing sentence explaining the action, for the caller
	// to render. It never contains a backend command or a machine name the user
	// is expected to type: repairing this is avar's job, not theirs.
	Reason string
}

// Result is what one reconciliation did. It is meaningful even when Reconcile
// also returns an error: everything in Changes happened.
type Result struct {
	// Changes are the machines acted on, ordered by name then action, so output
	// and tests are deterministic.
	Changes []Change
}

// Empty reports that reconciliation found nothing to say — the overwhelmingly
// common case, in which the store and the backend already agreed and nothing
// was written.
func (r Result) Empty() bool { return len(r.Changes) == 0 }

// WithAction returns the changes with the given action.
func (r Result) WithAction(a Action) []Change {
	out := make([]Change, 0, len(r.Changes))
	for _, c := range r.Changes {
		if c.Action == a {
			out = append(out, c)
		}
	}
	return out
}

// Reconcile compares avar's machine registry against the machines the backend
// actually has and converges the two (REQ-1.6, REQ-17.5, PROP-7).
//
// Four cases, of which only the first two change anything:
//
//   - The backend has an avar-prefixed machine avar has no record of: a run
//     died before it could write the record. If the machine is usable it is
//     adopted; if the backend says it is broken it is deleted so the next
//     invocation provisions cleanly.
//   - avar has a record and the backend has no such machine (`limactl delete`
//     by hand, or a crash landing between the two): the dangling record is
//     dropped, together with any session attached to it.
//   - Both sides agree, or neither has anything: nothing to do, and nothing is
//     written. This is the warm path (REQ-17.1), so it costs one backend
//     listing, one lock acquisition, and zero writes.
//
// It is safe to call concurrently with other invocations, including other
// reconciliations: the store transaction serializes them, and each derives its
// plan from the records as they are inside its own transaction, so the second
// simply finds nothing left to repair.
//
// The result describes what happened; nothing is printed. A non-nil error is
// returned with a partially populated Result rather than in place of one — a
// backend delete that failed must not hide the records that were repaired.
func (s *Store) Reconcile(ctx context.Context, backend Backend) (Result, error) {
	if backend == nil {
		return Result{}, errors.New("reconcile avar's machine registry: no backend was given to compare it against")
	}

	// Sampled before the listing, never after: a record written while the
	// listing was being taken must not be judged by a listing that predates
	// it. Without this, an invocation reconciling while another invocation
	// creates a machine would see the machine missing from its own snapshot,
	// find the freshly written record, and drop it — destroying the other
	// invocation's work (design §6, "Concurrent avr invocations").
	listedAt := time.Now().UTC()

	listing, err := backend.Status(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("list the machines the backend has, to reconcile them with avar's records: %w", err)
	}
	owned := ownedMachines(listing)

	var (
		changes  []Change
		unusable []types.MachineStatus
	)
	if err := s.Update(func(tx *Tx) error {
		changes, unusable = nil, nil

		// Records with no machine behind them. Tx.DeleteMachine also drops the
		// sessions attached to the machine, which is the one thing this needs
		// beyond the stale-pid pruning the store already does on read: a
		// session can be live (its avr is still running) and still belong to a
		// machine that has gone away underneath it.
		for _, rec := range tx.Machines() {
			if _, ok := owned[rec.Name]; ok {
				continue
			}
			if !rec.CreatedAt.Before(listedAt) {
				continue
			}
			if err := tx.DeleteMachine(rec.Name); err != nil {
				return err
			}
			changes = append(changes, Change{
				Machine:  rec.Name,
				Action:   ActionForgotten,
				Selector: rec.Selector,
				Reason:   "avar had a record of this environment but the backend no longer has the machine, so the record was dropped",
			})
		}

		// Machines with no record behind them.
		for _, m := range owned.sorted() {
			if _, recorded, err := tx.Machine(m.Name); err != nil {
				return err
			} else if recorded {
				continue
			}

			switch classify(m.State) {
			case healthUsable:
				rec, why := recordFor(m)
				if why != "" {
					changes = append(changes, Change{
						Machine:  m.Name,
						Action:   ActionLeft,
						Selector: m.Selector,
						Reason:   why,
					})
					continue
				}
				if err := tx.PutMachine(rec); err != nil {
					return err
				}
				changes = append(changes, Change{
					Machine:  m.Name,
					Action:   ActionAdopted,
					Selector: m.Selector,
					Reason:   "an interrupted run left this environment unregistered; avar has taken it back over rather than orphaning a working environment",
				})
			case healthUnusable:
				// Deleted in phase 3, outside the lock.
				unusable = append(unusable, m)
			default:
				changes = append(changes, Change{
					Machine:  m.Name,
					Action:   ActionLeft,
					Selector: m.Selector,
					Reason:   fmt.Sprintf("the backend reports this unregistered environment as %q, a state avar cannot interpret, so it was left untouched", m.State),
				})
			}
		}
		return nil
	}); err != nil {
		// A transaction that fails applies nothing: the plan is discarded along
		// with the changes it would have reported, and the next invocation
		// derives the same plan from the same evidence.
		return Result{}, fmt.Errorf("reconcile avar's machine registry with the backend: %w", err)
	}

	// Phase 3: backend work, with the lock released. Every failure is
	// collected rather than aborting the sweep, so one machine avar cannot
	// remove does not stop it repairing the rest.
	var errs []error
	for _, m := range unusable {
		if err := backend.Delete(ctx, m.Name); err != nil {
			errs = append(errs, fmt.Errorf("delete the unusable Linux environment %s left behind by an interrupted run: %w", m.Name, err))
			continue
		}
		changes = append(changes, Change{
			Machine:  m.Name,
			Action:   ActionDeleted,
			Selector: m.Selector,
			Reason:   fmt.Sprintf("an interrupted run left this environment unregistered and the backend reports it as %s, so it was removed; the next run will create a fresh one", m.State),
		})
	}

	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Machine != changes[j].Machine {
			return changes[i].Machine < changes[j].Machine
		}
		return changes[i].Action < changes[j].Action
	})
	return Result{Changes: changes}, errors.Join(errs...)
}

// listing is the backend's report, indexed by machine name.
type listing map[string]types.MachineStatus

// ownedMachines keeps only the entries reconciliation may act on: those whose
// name is in avar's namespace.
//
// Provider.Status is already contracted to filter by the prefix, so in practice
// this drops nothing. It is applied anyway because PROP-6 is a security
// property, and a security property that holds only as long as every backend
// implementation is well behaved is not one. Filtered entries are dropped
// silently and completely: they are not reported, because a machine avar does
// not own is not avar's to have an opinion about.
func ownedMachines(in []types.MachineStatus) listing {
	out := make(listing, len(in))
	for _, m := range in {
		if types.ValidateMachineName(m.Name) != nil {
			continue
		}
		out[m.Name] = m
	}
	return out
}

// sorted returns the listing ordered by name, so that reconciliation acts and
// reports in a deterministic order.
func (l listing) sorted() []types.MachineStatus {
	out := make([]types.MachineStatus, 0, len(l))
	for _, m := range l {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// health is reconciliation's reading of a machine's reported state.
type health int

const (
	// healthUsable: the machine can serve as an environment, now or after a
	// start.
	healthUsable health = iota
	// healthUnusable: the backend says it cannot work.
	healthUnusable
	// healthUnclear: the state means nothing to this version of avar.
	healthUnclear
)

// classify maps a reported state onto what reconciliation may do about an
// unrecorded machine in it.
//
// Stopped counts as usable: a stopped machine is one EnsureMachine starts, and
// destroying it would throw away whatever is installed inside for no reason.
//
// Unknown counts as unusable, which is the one place a state that merely
// *sounds* uncertain licenses a deletion. REQ-17.5 names that case in its own
// words — "no wedged 'unknown' machines requiring manual Lima surgery" — and it
// only ever applies to a machine avar has no record of, so nothing that has
// been used can be reached this way.
//
// Any other value — a state string a future backend reports and this version
// does not know — is unclear rather than unusable. Deleting an environment
// because avar cannot read the word describing it would be the worst possible
// reading of an unrecognised value.
func classify(state types.MachineState) health {
	switch state {
	case types.StateRunning, types.StateStopped:
		return healthUsable
	case types.StateBroken, types.StateUnknown:
		return healthUnusable
	default:
		return healthUnclear
	}
}

// recordFor builds the record that adopting m would write, or returns one
// user-facing sentence saying why the backend's report does not describe a
// machine avar can record.
//
// Adoption copies what the backend knows and invents nothing:
//
//   - Recovered from the listing: Name, Selector, Kind, Mounts, VMType.
//   - Not recoverable, and not fabricated: CreatedAt, which the listing does
//     not carry. Tx.PutMachine stamps it now, so an adopted record's CreatedAt
//     means "since when avar has known about this machine" rather than when the
//     machine was built. It feeds `avr status` display only, and adoption is
//     genuinely the moment avar learned of the machine.
//   - Not recoverable, and fatal to adoption: ProjectID. types.MachineStatus
//     has no field that could carry it, because which project an isolated
//     machine serves is avar's own bookkeeping and not something a backend can
//     know. The machine name holds a truncated hash of the project identity,
//     and matching on that would be a guess — filing a machine under the wrong
//     project would mount the wrong directory into it. So an isolated orphan is
//     structurally unadoptable and is left alone (ActionLeft) rather than
//     adopted on a guess or destroyed while healthy.
func recordFor(m types.MachineStatus) (types.MachineRecord, string) {
	switch m.Kind {
	case types.KindShared, types.KindBase:
	case types.KindIsolated:
		return types.MachineRecord{}, "this unregistered environment belongs to a single project and avar cannot tell which, so it was left alone; it will be registered again the next time that project is used"
	default:
		return types.MachineRecord{}, fmt.Sprintf("the backend describes this unregistered environment as %q, which avar cannot record, so it was left alone", m.Kind)
	}
	if m.Selector.Distro == "" || m.Selector.Arch == "" {
		return types.MachineRecord{}, "the backend does not report which Linux environment this unregistered machine provides, so avar left it alone rather than record a guess"
	}
	return types.MachineRecord{
		Name:     m.Name,
		Selector: m.Selector,
		Kind:     m.Kind,
		Mounts:   absoluteMounts(m.Mounts),
		VMType:   m.VMType,
	}, ""
}

// absoluteMounts keeps the mount paths a record may hold. A record's mounts are
// host project roots, which are absolute by construction (Tx.PutMachine refuses
// anything else); anything else in a backend's report is not a project root avar
// registered, so it is not adopted as one. Dropping it here rather than letting
// PutMachine refuse the whole record keeps one odd entry in a listing from
// aborting a repair that is otherwise correct.
func absoluteMounts(in []string) []string {
	out := make([]string, 0, len(in))
	for _, m := range in {
		if filepath.IsAbs(m) {
			out = append(out, m)
		}
	}
	return out
}
