package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// The narrow interface internal/state declares is a subset of the backend
// contract, so the real provider can be handed to Reconcile directly. Asserting
// it here rather than in reconcile.go keeps internal/provider out of the
// production package's imports, which is the layering design §3.3 requires: the
// store must not know about backends.
var _ Backend = (provider.Provider)(nil)

// --- test backend ---------------------------------------------------------

// fakeBackend is an in-process Backend whose listing and failures the test
// programs directly. It is deliberately not internal/provider/fake: that Fake
// filters machines avar does not own out of Status, and several tests here have
// to prove reconciliation filters them for itself rather than trusting a
// well-behaved backend to have done it (PROP-6).
type fakeBackend struct {
	mu       sync.Mutex
	machines []types.MachineStatus
	deleted  []string

	statusErr   error
	deleteErr   map[string]error
	statusCalls int

	// onStatus and onDelete run inside the corresponding call, which is how a
	// test observes what the store lock is doing while backend work is in
	// flight.
	onStatus func()
	onDelete func(name string)
}

func newBackend(machines ...types.MachineStatus) *fakeBackend {
	return &fakeBackend{machines: machines, deleteErr: map[string]error{}}
}

func (b *fakeBackend) Status(context.Context) ([]types.MachineStatus, error) {
	b.mu.Lock()
	b.statusCalls++
	hook, err := b.onStatus, b.statusErr
	out := append([]types.MachineStatus(nil), b.machines...)
	b.mu.Unlock()

	if hook != nil {
		hook()
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (b *fakeBackend) Delete(_ context.Context, name string) error {
	b.mu.Lock()
	b.deleted = append(b.deleted, name)
	hook, err := b.onDelete, b.deleteErr[name]
	if err == nil {
		kept := b.machines[:0:0]
		for _, m := range b.machines {
			if m.Name != name {
				kept = append(kept, m)
			}
		}
		b.machines = kept
	}
	b.mu.Unlock()

	if hook != nil {
		hook(name)
	}
	return err
}

func (b *fakeBackend) deletions() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.deleted...)
}

func (b *fakeBackend) names() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.machines))
	for _, m := range b.machines {
		out = append(out, m.Name)
	}
	sort.Strings(out)
	return out
}

// --- fixtures -------------------------------------------------------------

func ubuntuSelector() types.EnvironmentSelector {
	return types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64}
}

// backendMachine is what the backend reports for a machine avar created: enough
// for adoption to reconstruct a record without inventing anything.
func backendMachine(name string, state types.MachineState) types.MachineStatus {
	return types.MachineStatus{
		Name:     name,
		Provider: types.ProviderLima,
		Selector: ubuntuSelector(),
		Kind:     types.KindShared,
		State:    state,
		Runtime:  "vz",
	}
}

func changeSummary(r Result) []string {
	out := make([]string, 0, len(r.Changes))
	for _, c := range r.Changes {
		out = append(out, c.Machine+":"+string(c.Action))
	}
	return out
}

func recordNames(t *testing.T, st *Store) []string {
	t.Helper()

	machines, err := st.Machines()
	if err != nil {
		t.Fatalf("Machines: %v", err)
	}
	out := make([]string, 0, len(machines))
	for _, m := range machines {
		out = append(out, m.Name)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- the decision table ---------------------------------------------------

// The full cross-product of {record present, absent} × {machine healthy,
// broken, absent} × {owned name, foreign name}. Every cell asserts both the
// resulting store state and what was reported, because either one being wrong
// is a different bug: a silent repair and an unreported one are both failures
// of design §6.
func TestReconcile_DecisionTable_PROP_7(t *testing.T) {
	t.Parallel()

	const (
		owned   = "avr-ubuntu-24.04-arm64"
		foreign = "docker-desktop"
	)

	cases := []struct {
		name string
		// record seeds machines.json (owned names only; a foreign record cannot
		// be created through the API and is covered separately).
		record bool
		// backend is what the backend reports it has.
		backend []types.MachineStatus

		wantChanges  []string
		wantRecords  []string
		wantDeleted  []string
		wantBackend  []string
		wantReasoned bool
	}{
		{
			name:        "no record, no machine: nothing to do",
			backend:     nil,
			wantRecords: []string{},
			wantBackend: []string{},
		},
		{
			name:         "no record, healthy machine: adopted",
			backend:      []types.MachineStatus{backendMachine(owned, types.StateRunning)},
			wantChanges:  []string{owned + ":adopted"},
			wantRecords:  []string{owned},
			wantBackend:  []string{owned},
			wantReasoned: true,
		},
		{
			name:         "no record, stopped machine: adopted, not destroyed",
			backend:      []types.MachineStatus{backendMachine(owned, types.StateStopped)},
			wantChanges:  []string{owned + ":adopted"},
			wantRecords:  []string{owned},
			wantBackend:  []string{owned},
			wantReasoned: true,
		},
		{
			name:         "no record, broken machine: deleted so the next run provisions cleanly",
			backend:      []types.MachineStatus{backendMachine(owned, types.StateBroken)},
			wantChanges:  []string{owned + ":deleted"},
			wantRecords:  []string{},
			wantDeleted:  []string{owned},
			wantBackend:  []string{},
			wantReasoned: true,
		},
		{
			name:         "no record, machine in an unknown state: deleted (REQ-17.5)",
			backend:      []types.MachineStatus{backendMachine(owned, types.StateUnknown)},
			wantChanges:  []string{owned + ":deleted"},
			wantRecords:  []string{},
			wantDeleted:  []string{owned},
			wantBackend:  []string{},
			wantReasoned: true,
		},
		{
			name:         "record, no machine: the dangling record is dropped",
			record:       true,
			backend:      nil,
			wantChanges:  []string{owned + ":forgotten"},
			wantRecords:  []string{},
			wantBackend:  []string{},
			wantReasoned: true,
		},
		{
			name:        "record and healthy machine: consistent, nothing to do",
			record:      true,
			backend:     []types.MachineStatus{backendMachine(owned, types.StateRunning)},
			wantRecords: []string{owned},
			wantBackend: []string{owned},
		},
		{
			// A recorded machine may hold weeks of the user's work, and design
			// §6 ("Start fails on existing machine") says avar reports rather
			// than auto-deletes. Reconciliation repairs disagreements about
			// what exists; both sides agree here.
			name:        "record and broken machine: not a disagreement, left for the user",
			record:      true,
			backend:     []types.MachineStatus{backendMachine(owned, types.StateBroken)},
			wantRecords: []string{owned},
			wantBackend: []string{owned},
		},
		{
			name:        "foreign healthy machine: invisible",
			backend:     []types.MachineStatus{backendMachine(foreign, types.StateRunning)},
			wantRecords: []string{},
			wantBackend: []string{foreign},
		},
		{
			name:        "foreign broken machine: invisible, never deleted",
			backend:     []types.MachineStatus{backendMachine(foreign, types.StateBroken)},
			wantRecords: []string{},
			wantBackend: []string{foreign},
		},
		{
			name:        "foreign machine in an unknown state: invisible",
			backend:     []types.MachineStatus{backendMachine(foreign, types.StateUnknown)},
			wantRecords: []string{},
			wantBackend: []string{foreign},
		},
		{
			name:         "a foreign machine alongside a repair: only avar's own is touched",
			record:       true,
			backend:      []types.MachineStatus{backendMachine(foreign, types.StateBroken)},
			wantChanges:  []string{owned + ":forgotten"},
			wantRecords:  []string{},
			wantBackend:  []string{foreign},
			wantReasoned: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			st := newTestStore(t)
			if tc.record {
				if err := st.PutMachine(sharedMachine(owned)); err != nil {
					t.Fatalf("PutMachine: %v", err)
				}
			}
			backend := newBackend(tc.backend...)

			result, err := st.Reconcile(context.Background(), backend)
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}

			if got := changeSummary(result); !equalStrings(got, tc.wantChanges) {
				t.Errorf("reported %v, want %v", got, tc.wantChanges)
			}
			if result.Empty() != (len(tc.wantChanges) == 0) {
				t.Errorf("Result.Empty() = %t with changes %v", result.Empty(), changeSummary(result))
			}
			if got := recordNames(t, st); !equalStrings(got, tc.wantRecords) {
				t.Errorf("machines.json holds %v, want %v", got, tc.wantRecords)
			}
			if got := backend.deletions(); !equalStrings(got, tc.wantDeleted) {
				t.Errorf("backend deletions = %v, want %v", got, tc.wantDeleted)
			}
			if got := backend.names(); !equalStrings(got, tc.wantBackend) {
				t.Errorf("backend still has %v, want %v", got, tc.wantBackend)
			}
			if tc.wantReasoned {
				for _, c := range result.Changes {
					if strings.TrimSpace(c.Reason) == "" {
						t.Errorf("change %+v carries no reason for the caller to render", c)
					}
					if c.Selector != ubuntuSelector() {
						t.Errorf("change %+v does not carry the environment it concerns", c)
					}
				}
			}

			// Whatever happened, the state directory is left clean: reconciling
			// is a repair, not a source of new debris (REQ-17.5).
			if leftovers := tempLeftovers(t, st.Root()); len(leftovers) != 0 {
				t.Errorf("reconciliation left temporary files behind: %v", leftovers)
			}
		})
	}
}

// --- adoption -------------------------------------------------------------

// Adoption exists so a run that died after creating a machine but before
// recording it does not orphan a working environment (design §6). It must
// preserve what the backend can tell it and invent nothing it cannot know.
func TestReconcile_AdoptsHealthyOrphan_PROP_7(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	project := mkdir(t, "code", "orphan-project")

	orphan := backendMachine("avr-ubuntu-24.04-arm64", types.StateRunning)
	orphan.Mounts = []types.MountSpec{share(project), {HostPath: "not/absolute", GuestPath: "/not/absolute"}}
	orphan.CPUs, orphan.MemoryGB, orphan.DiskGB = 4, 8, 100

	before := time.Now().UTC()
	result, err := st.Reconcile(context.Background(), newBackend(orphan))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := changeSummary(result); !equalStrings(got, []string{orphan.Name + ":adopted"}) {
		t.Fatalf("reported %v, want the machine adopted", got)
	}
	if adopted := result.WithAction(ActionAdopted); len(adopted) != 1 || adopted[0].Machine != orphan.Name {
		t.Errorf("WithAction(ActionAdopted) = %+v, want the adopted machine", adopted)
	}

	rec, ok, err := st.Machine(orphan.Name)
	if err != nil || !ok {
		t.Fatalf("Machine(%s) = (%t, %v), want the adopted record", orphan.Name, ok, err)
	}

	// Recovered from the backend, because the backend knows it.
	if rec.Selector != orphan.Selector {
		t.Errorf("adopted selector = %+v, want %+v", rec.Selector, orphan.Selector)
	}
	if rec.Kind != types.KindShared {
		t.Errorf("adopted kind = %q, want %q", rec.Kind, types.KindShared)
	}
	if rec.Runtime != "vz" {
		t.Errorf("adopted runtime = %q, want the reported %q", rec.Runtime, "vz")
	}
	// The backend that listed the machine is the backend that owns it, which
	// is the one thing about a machine a backend can always say for certain
	// about itself (REQ-18.14).
	if rec.Provider != types.ProviderLima {
		t.Errorf("adopted provider = %q, want the listing backend %q", rec.Provider, types.ProviderLima)
	}
	if len(rec.Mounts) != 1 || rec.Mounts[0] != share(project) {
		t.Errorf("adopted mounts = %v, want only the mapping the backend actually describes (%s)", rec.Mounts, share(project))
	}

	// Not recoverable, and therefore not fabricated: CreatedAt means "since
	// when avar has known about this machine", which is now.
	if rec.CreatedAt.Before(before) {
		t.Errorf("adopted CreatedAt = %s, want the moment of adoption (after %s)", rec.CreatedAt, before)
	}
	// Not recoverable at all: the backend cannot say which project an
	// environment serves, so nothing was guessed.
	if rec.ProjectID != "" {
		t.Errorf("adoption invented a project id %q", rec.ProjectID)
	}

	// Adoption is a convergence, so running it again finds nothing to do and
	// leaves the adopted record exactly as it was.
	again, err := st.Reconcile(context.Background(), newBackend(orphan))
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if !again.Empty() {
		t.Errorf("second reconciliation reported %v, want nothing left to repair", changeSummary(again))
	}
	settled, _, err := st.Machine(orphan.Name)
	if err != nil {
		t.Fatalf("Machine after the second reconciliation: %v", err)
	}
	if !settled.CreatedAt.Equal(rec.CreatedAt) {
		t.Errorf("CreatedAt moved from %s to %s on a second pass", rec.CreatedAt, settled.CreatedAt)
	}
}

// An orphan the backend's report does not fully describe is left alone rather
// than adopted on a guess or destroyed while it may still work. It is not
// wedged: the invocation that targets it by name records it with the
// information only that invocation has.
func TestReconcile_LeavesAnOrphanItCannotDescribe_PROP_7(t *testing.T) {
	t.Parallel()

	isolated := backendMachine("avr-prj-0123456789", types.StateRunning)
	isolated.Kind = types.KindIsolated

	unknownKind := backendMachine("avr-ubuntu-24.04-amd64", types.StateRunning)
	unknownKind.Kind = "ephemeral"

	noSelector := backendMachine("avr-debian-12-arm64", types.StateRunning)
	noSelector.Selector = types.EnvironmentSelector{}

	unclearState := backendMachine("avr-fedora-41-arm64", types.MachineState("starting"))

	cases := []struct {
		name       string
		machine    types.MachineStatus
		reasonPart string
	}{
		{"isolated machine: avar cannot tell which project it serves", isolated, "which"},
		{"kind avar does not recognise", unknownKind, "ephemeral"},
		{"backend does not say what environment it provides", noSelector, "which Linux environment"},
		{"state avar cannot interpret is never a licence to delete", unclearState, "starting"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			st := newTestStore(t)
			backend := newBackend(tc.machine)

			result, err := st.Reconcile(context.Background(), backend)
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if got := changeSummary(result); !equalStrings(got, []string{tc.machine.Name + ":left"}) {
				t.Fatalf("reported %v, want the machine reported as left alone", got)
			}
			if reason := result.Changes[0].Reason; !strings.Contains(reason, tc.reasonPart) {
				t.Errorf("reason %q does not explain the situation (%q)", reason, tc.reasonPart)
			}
			if got := recordNames(t, st); len(got) != 0 {
				t.Errorf("machines.json holds %v, want nothing recorded on a guess", got)
			}
			if got := backend.deletions(); len(got) != 0 {
				t.Errorf("a machine avar could not describe was deleted: %v", got)
			}
		})
	}
}

// --- ownership ------------------------------------------------------------

// A machine outside avar's namespace is invisible to reconciliation in every
// combination: not adopted, not deleted, not reported — whatever state it is in
// and whatever avar is doing alongside it (REQ-5.4, PROP-6).
func TestReconcile_IgnoresForeignMachines_PROP_6(t *testing.T) {
	t.Parallel()

	foreignNames := []string{"docker-desktop", "default", "ubuntu", "AVR-shouty", "avr", "colima-avr-", "avr-"}
	states := []types.MachineState{types.StateRunning, types.StateStopped, types.StateBroken, types.StateUnknown, types.MachineState("")}

	st := newTestStore(t)
	// A repair of avar's own machines is in flight at the same time, so the
	// sweep proves foreign machines survive an active reconciliation rather
	// than a no-op one.
	if err := st.PutMachine(sharedMachine("avr-debian-12-arm64")); err != nil {
		t.Fatalf("PutMachine: %v", err)
	}

	var machines []types.MachineStatus
	for _, name := range foreignNames {
		for _, state := range states {
			machines = append(machines, backendMachine(name, state))
		}
	}
	machines = append(machines, backendMachine("avr-ubuntu-24.04-arm64", types.StateRunning))
	backend := newBackend(machines...)

	result, err := st.Reconcile(context.Background(), backend)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Exactly avar's own two machines are acted on: one adopted, one forgotten.
	want := []string{"avr-debian-12-arm64:forgotten", "avr-ubuntu-24.04-arm64:adopted"}
	if got := changeSummary(result); !equalStrings(got, want) {
		t.Fatalf("reported %v, want %v", got, want)
	}
	for _, c := range result.Changes {
		if types.ValidateMachineName(c.Machine) != nil {
			t.Errorf("reconciliation reported %q, which is not avar's to have an opinion about", c.Machine)
		}
	}
	if got := backend.deletions(); len(got) != 0 {
		t.Fatalf("reconciliation deleted %v; no foreign machine may be touched", got)
	}
	if got := recordNames(t, st); !equalStrings(got, []string{"avr-ubuntu-24.04-arm64"}) {
		t.Errorf("machines.json holds %v, want only the adopted avar machine", got)
	}

	// Every foreign machine is still there, in every state it was in.
	stillThere := backend.names()
	for _, name := range foreignNames {
		found := 0
		for _, got := range stillThere {
			if got == name {
				found++
			}
		}
		if found != len(states) {
			t.Errorf("foreign machine %q appears %d times after reconciliation, want %d", name, found, len(states))
		}
	}
}

// A registry naming a machine avar must not manage is refused loudly by the
// store, and reconciliation neither works around it nor touches the backend to
// resolve it: a hand-edited registry is for the user to fix (REQ-5.4).
func TestReconcile_ForeignRecordIsReportedNotActedOn_REQ_5_4(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	registry := filepath.Join(st.Root(), machinesFile)
	raw := []byte(`{"docker-desktop":{"name":"docker-desktop","kind":"shared","mounts":[]}}`)
	if err := os.WriteFile(registry, raw, filePerm); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	for _, machines := range [][]types.MachineStatus{
		nil,
		{backendMachine("docker-desktop", types.StateRunning)},
		{backendMachine("docker-desktop", types.StateBroken)},
	} {
		backend := newBackend(machines...)
		result, err := st.Reconcile(context.Background(), backend)
		if err == nil {
			t.Fatalf("Reconcile accepted a registry naming a machine avar does not own (result %v)", changeSummary(result))
		}
		if !strings.Contains(err.Error(), "docker-desktop") {
			t.Errorf("error %q does not name the offending machine", err)
		}
		if !result.Empty() {
			t.Errorf("a refused reconciliation reported %v", changeSummary(result))
		}
		if got := backend.deletions(); len(got) != 0 {
			t.Errorf("a refused reconciliation deleted %v", got)
		}
	}

	after, err := os.ReadFile(registry)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if string(after) != string(raw) {
		t.Errorf("the registry was rewritten:\nbefore: %s\nafter:  %s", raw, after)
	}
}

// --- cheapness ------------------------------------------------------------

// Reconciliation runs on the warm path, where avar's whole budget is ~500 ms
// (REQ-17.1). The consistent case is the overwhelmingly common one, and it must
// not make every invocation pay for a repair that is not needed: one backend
// listing, one lock acquisition, and not a single write.
func TestReconcile_ConsistentStoreWritesNothing_REQ_17_1(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	names := []string{"avr-ubuntu-24.04-arm64", "avr-debian-12-arm64", "avr-fedora-41-arm64"}
	var machines []types.MachineStatus
	for _, name := range names {
		if err := st.PutMachine(sharedMachine(name)); err != nil {
			t.Fatalf("PutMachine(%s): %v", name, err)
		}
		machines = append(machines, backendMachine(name, types.StateRunning))
	}
	if err := st.AddSession(types.SessionRecord{Machine: names[0], PID: os.Getpid()}); err != nil {
		t.Fatalf("AddSession: %v", err)
	}

	// os.SameFile compares device and inode, so it detects an atomic rewrite
	// (temp file + rename replaces the file) even within one mtime tick.
	stateFiles := []string{projectsFile, machinesFile, sessionsFile}
	before := make(map[string]os.FileInfo, len(stateFiles))
	for _, name := range stateFiles {
		info, err := os.Stat(filepath.Join(st.Root(), name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", name, err)
		}
		before[name] = info
	}

	backend := newBackend(machines...)
	result, err := st.Reconcile(context.Background(), backend)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !result.Empty() {
		t.Fatalf("a consistent store reported %v, want nothing", changeSummary(result))
	}

	for _, name := range stateFiles {
		info, err := os.Stat(filepath.Join(st.Root(), name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", name, err)
		}
		switch {
		case before[name] == nil && info == nil:
		case before[name] == nil || info == nil:
			t.Errorf("%s came into existence (or vanished) during a no-op reconciliation", name)
		case !os.SameFile(before[name], info):
			t.Errorf("%s was rewritten by a reconciliation that had nothing to repair", name)
		}
	}
	if backend.statusCalls != 1 {
		t.Errorf("the backend was listed %d times, want exactly once per reconciliation", backend.statusCalls)
	}
	if got := backend.deletions(); len(got) != 0 {
		t.Errorf("a consistent store triggered backend deletions: %v", got)
	}
}

// --- sessions -------------------------------------------------------------

// Dropping a dangling record drops the sessions attached to it. Stale-pid
// pruning does not cover this: the session's avr may still be alive, attached
// to a machine that has gone away underneath it.
func TestReconcile_DroppingARecordDropsItsSessions_REQ_17_5(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	gone := sharedMachine("avr-ubuntu-24.04-arm64")
	kept := sharedMachine("avr-debian-12-arm64")
	for _, rec := range []types.MachineRecord{gone, kept} {
		if err := st.PutMachine(rec); err != nil {
			t.Fatalf("PutMachine(%s): %v", rec.Name, err)
		}
		if err := st.AddSession(types.SessionRecord{Machine: rec.Name, PID: os.Getpid()}); err != nil {
			t.Fatalf("AddSession(%s): %v", rec.Name, err)
		}
	}

	backend := newBackend(backendMachine(kept.Name, types.StateRunning))
	result, err := st.Reconcile(context.Background(), backend)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := changeSummary(result); !equalStrings(got, []string{gone.Name + ":forgotten"}) {
		t.Fatalf("reported %v, want the vanished machine forgotten", got)
	}

	sessions, err := st.Sessions()
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Machine != kept.Name {
		t.Errorf("Sessions() = %+v, want only the surviving machine's session", sessions)
	}
}

// --- concurrency ----------------------------------------------------------

// Two invocations reconciling at once must converge on the same consistent
// state and never corrupt the registry. Whichever gets the lock first does the
// repair; the other finds nothing left to do.
func TestReconcile_ConcurrentReconciliationsConverge_REQ_17_5(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	dangling := []string{"avr-ubuntu-24.04-amd64", "avr-debian-12-arm64"}
	for _, name := range dangling {
		if err := st.PutMachine(sharedMachine(name)); err != nil {
			t.Fatalf("PutMachine(%s): %v", name, err)
		}
	}

	orphans := []types.MachineStatus{
		backendMachine("avr-ubuntu-24.04-arm64", types.StateRunning),
		backendMachine("avr-fedora-41-arm64", types.StateStopped),
		backendMachine("avr-ubuntu-22.04-arm64", types.StateBroken),
	}
	backend := newBackend(orphans...)

	const workers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		adopted int
		dropped int
		deleted int
	)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			result, err := st.Reconcile(context.Background(), backend)
			if err != nil {
				t.Errorf("Reconcile: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			adopted += len(result.WithAction(ActionAdopted))
			dropped += len(result.WithAction(ActionForgotten))
			deleted += len(result.WithAction(ActionDeleted))
		}()
	}
	wg.Wait()

	// Each repair is reported by exactly one invocation: the others find it
	// already done. The one exception is deletion, which is idempotent at the
	// backend and may legitimately be attempted by more than one invocation
	// whose listing predates the other's delete.
	if adopted != 2 {
		t.Errorf("%d adoptions reported across %d concurrent reconciliations, want 2", adopted, workers)
	}
	if dropped != len(dangling) {
		t.Errorf("%d dropped records reported, want %d", dropped, len(dangling))
	}
	if deleted < 1 {
		t.Error("the broken machine was never reported as deleted")
	}

	want := []string{"avr-fedora-41-arm64", "avr-ubuntu-24.04-arm64"}
	if got := recordNames(t, st); !equalStrings(got, want) {
		t.Errorf("machines.json holds %v, want %v", got, want)
	}
	if got := backend.names(); !equalStrings(got, want) {
		t.Errorf("the backend still has %v, want %v", got, want)
	}

	// One more pass over the settled state changes nothing at all.
	final, err := st.Reconcile(context.Background(), backend)
	if err != nil {
		t.Fatalf("final Reconcile: %v", err)
	}
	if !final.Empty() {
		t.Errorf("a converged state still reported %v", changeSummary(final))
	}
}

// The state lock is not re-entrant and is bounded by a timeout, so holding it
// across backend work would turn a slow `limactl` into every other invocation's
// ErrLockTimeout. Reconciliation therefore lists and deletes with the lock
// released, which this proves by taking the lock from another goroutine while
// the backend is being called.
func TestReconcile_DoesNotHoldTheLockOverBackendWork_REQ_17_1(t *testing.T) {
	t.Parallel()

	st := newTestStore(t, WithLockTimeout(2*time.Second))
	lockable := func(when string) {
		if err := st.Update(func(*Tx) error { return nil }); err != nil {
			t.Errorf("another invocation could not take the state lock during %s: %v", when, err)
		}
	}

	backend := newBackend(backendMachine("avr-ubuntu-24.04-arm64", types.StateBroken))
	backend.onStatus = func() { lockable("the backend listing") }
	backend.onDelete = func(string) { lockable("a backend delete") }

	result, err := st.Reconcile(context.Background(), backend)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := changeSummary(result); !equalStrings(got, []string{"avr-ubuntu-24.04-arm64:deleted"}) {
		t.Fatalf("reported %v, want the broken machine deleted", got)
	}
}

// A record written while the backend listing was being taken must survive it: a
// listing that predates the record is no evidence that the machine is missing.
// Without this an invocation reconciling during another invocation's create
// would drop the record that invocation had just written (design §6,
// "Concurrent avr invocations racing on create").
func TestReconcile_KeepsARecordWrittenAfterTheListing_REQ_17_5(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	created := sharedMachine("avr-ubuntu-24.04-arm64")
	stale := sharedMachine("avr-debian-12-arm64")
	stale.CreatedAt = time.Now().UTC().Add(-time.Hour)
	if err := st.PutMachine(stale); err != nil {
		t.Fatalf("PutMachine: %v", err)
	}

	// The backend reports neither machine. The other invocation's create lands
	// while the listing is in flight, exactly as it would in a real race.
	backend := newBackend()
	backend.onStatus = func() {
		if err := st.PutMachine(created); err != nil {
			t.Errorf("concurrent PutMachine: %v", err)
		}
	}

	result, err := st.Reconcile(context.Background(), backend)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := changeSummary(result); !equalStrings(got, []string{stale.Name + ":forgotten"}) {
		t.Fatalf("reported %v, want only the record that predates the listing dropped", got)
	}
	if got := recordNames(t, st); !equalStrings(got, []string{created.Name}) {
		t.Errorf("machines.json holds %v, want the record written during the listing to survive", got)
	}
}

// --- failure paths --------------------------------------------------------

// A backend that cannot be listed leaves avar's records exactly as they were:
// reconciliation repairs a disagreement it can see, and it cannot see one here.
func TestReconcile_ABackendListingFailureChangesNothing_REQ_17_5(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	if err := st.PutMachine(sharedMachine("avr-ubuntu-24.04-arm64")); err != nil {
		t.Fatalf("PutMachine: %v", err)
	}

	boom := errors.New("limactl is not installed")
	backend := newBackend()
	backend.statusErr = boom

	result, err := st.Reconcile(context.Background(), backend)
	if !errors.Is(err, boom) {
		t.Fatalf("Reconcile error = %v, want it to wrap %v", err, boom)
	}
	if !strings.Contains(err.Error(), "reconcile") && !strings.Contains(err.Error(), "records") {
		t.Errorf("error %q does not say what avar was attempting", err)
	}
	if !result.Empty() {
		t.Errorf("a failed reconciliation reported %v", changeSummary(result))
	}
	if got := recordNames(t, st); !equalStrings(got, []string{"avr-ubuntu-24.04-arm64"}) {
		t.Errorf("machines.json holds %v, want the untouched record", got)
	}
}

// One machine avar cannot remove must not stop it repairing the rest, and the
// failure must be reported rather than swallowed: the user is told that a
// broken environment is still there (REQ-1.6).
func TestReconcile_ReportsADeleteFailureAndStillRepairsTheRest_REQ_1_6(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	if err := st.PutMachine(sharedMachine("avr-debian-12-arm64")); err != nil {
		t.Fatalf("PutMachine: %v", err)
	}

	stubborn := backendMachine("avr-ubuntu-22.04-arm64", types.StateBroken)
	backend := newBackend(
		stubborn,
		backendMachine("avr-fedora-41-arm64", types.StateBroken),
		backendMachine("avr-ubuntu-24.04-arm64", types.StateRunning),
	)
	boom := errors.New("instance is locked by another process")
	backend.deleteErr[stubborn.Name] = boom

	result, err := st.Reconcile(context.Background(), backend)
	if !errors.Is(err, boom) {
		t.Fatalf("Reconcile error = %v, want it to wrap %v", err, boom)
	}
	if !strings.Contains(err.Error(), stubborn.Name) {
		t.Errorf("error %q does not name the environment that is still there", err)
	}

	want := []string{"avr-debian-12-arm64:forgotten", "avr-fedora-41-arm64:deleted", "avr-ubuntu-24.04-arm64:adopted"}
	if got := changeSummary(result); !equalStrings(got, want) {
		t.Errorf("reported %v, want the repairs that did succeed: %v", got, want)
	}
	if got := recordNames(t, st); !equalStrings(got, []string{"avr-ubuntu-24.04-arm64"}) {
		t.Errorf("machines.json holds %v, want the adopted machine only", got)
	}
	if got := backend.names(); !equalStrings(got, []string{stubborn.Name, "avr-ubuntu-24.04-arm64"}) {
		t.Errorf("the backend has %v, want the undeletable machine still there", got)
	}

	// The next invocation tries again: the repair is idempotent, not
	// half-applied (PROP-7).
	backend.deleteErr[stubborn.Name] = nil
	again, err := st.Reconcile(context.Background(), backend)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got := changeSummary(again); !equalStrings(got, []string{stubborn.Name + ":deleted"}) {
		t.Errorf("second pass reported %v, want the retried deletion", got)
	}
}

func TestReconcile_RequiresABackendToCompareAgainst(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	if _, err := st.Reconcile(context.Background(), nil); err == nil {
		t.Error("Reconcile with no backend succeeded; there is nothing to reconcile against")
	}
}

// A cancelled context surfaces as an error rather than as a repair based on a
// listing avar never received.
func TestReconcile_HonoursContextCancellation_REQ_17_5(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	backend := newBackend()
	backend.statusErr = ctx.Err()

	if _, err := st.Reconcile(ctx, backend); !errors.Is(err, context.Canceled) {
		t.Errorf("Reconcile error = %v, want it to wrap context.Canceled", err)
	}
}

// A backend that does not say which provider owns an unregistered environment
// has not given avar enough to record: the record would name a machine no later
// invocation could safely act on, so it is left alone instead (PROP-6).
func TestReconcile_LeavesAnOrphanWhoseProviderIsUnknown_PROP_6(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	orphan := backendMachine("avr-ubuntu-24.04-arm64", types.StateRunning)
	orphan.Provider = ""

	result, err := st.Reconcile(context.Background(), newBackend(orphan))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := changeSummary(result); !equalStrings(got, []string{orphan.Name + ":left"}) {
		t.Fatalf("reported %v, want the machine left alone", got)
	}
	if names := recordNames(t, st); len(names) != 0 {
		t.Errorf("records = %v, want nothing recorded from a listing avar cannot act on", names)
	}
	if reason := result.Changes[0].Reason; !strings.Contains(reason, "provider") {
		t.Errorf("the reason does not say what was missing: %q", reason)
	}
}
