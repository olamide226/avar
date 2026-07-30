package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/olamide226/avar/internal/types"
)

// newTestStore opens a store under a per-test temporary directory. Tests must
// never touch a developer's real ~/.avr, which is exactly why the root is a
// constructor parameter.
func newTestStore(t *testing.T, opts ...Option) *Store {
	t.Helper()

	st, err := Open(filepath.Join(t.TempDir(), ".avr"), opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return st
}

// mkdir creates a directory under a temporary root and returns it.
func mkdir(t *testing.T, parts ...string) string {
	t.Helper()

	dir := filepath.Join(append([]string{t.TempDir()}, parts...)...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	return dir
}

func sharedMachine(name string) types.MachineRecord {
	return types.MachineRecord{
		Name: name,
		Selector: types.EnvironmentSelector{
			Distro:  types.DistroUbuntu,
			Version: "24.04",
			Arch:    types.ArchARM64,
		},
		Kind:   types.KindShared,
		VMType: "vz",
	}
}

// deadPID returns the pid of a process that has certainly exited. Pid reuse
// makes this theoretically racy, which is a property of pid liveness probing
// in general and is documented on processAlive.
func deadPID(t *testing.T) int {
	t.Helper()

	bin, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("no `true` binary to spawn: %v", err)
	}
	cmd := exec.Command(bin)
	if err := cmd.Run(); err != nil {
		t.Fatalf("run %s: %v", bin, err)
	}
	return cmd.Process.Pid
}

func TestStore_ProjectAndMachineRecordsRoundTrip_REQ_11_2(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	dir := mkdir(t, "code", "my-project")

	first, err := st.EnsureProject(dir)
	if err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	resolved, err := ResolveProjectPath(dir)
	if err != nil {
		t.Fatalf("ResolveProjectPath: %v", err)
	}
	if first.Path != resolved {
		t.Errorf("record path = %s, want the resolved path %s", first.Path, resolved)
	}
	if first.CreatedAt.IsZero() || first.LastUsedAt.IsZero() {
		t.Errorf("record timestamps not set: %+v", first)
	}

	// Reached again from a different spelling: the same record, not a second one.
	second, err := st.EnsureProject(dir + string(os.PathSeparator))
	if err != nil {
		t.Fatalf("EnsureProject again: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second EnsureProject created a new identity %s (first was %s)", second.ID, first.ID)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("CreatedAt moved from %s to %s", first.CreatedAt, second.CreatedAt)
	}
	if second.LastUsedAt.Before(first.LastUsedAt) {
		t.Errorf("LastUsedAt went backwards: %s then %s", first.LastUsedAt, second.LastUsedAt)
	}

	rec := sharedMachine("avr-ubuntu-24.04-arm64")
	rec.Mounts = []string{resolved}
	if err := st.PutMachine(rec); err != nil {
		t.Fatalf("PutMachine: %v", err)
	}

	// A fresh handle on the same directory: everything is on disk, not in RAM.
	reopened, err := Open(st.Root())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}

	got, ok, err := reopened.Project(first.ID)
	if err != nil || !ok {
		t.Fatalf("Project(%s) = (%v, %t, %v), want the stored record", first.ID, got, ok, err)
	}
	if got.Path != resolved {
		t.Errorf("reloaded path = %s, want %s", got.Path, resolved)
	}

	gotMachine, ok, err := reopened.Machine(rec.Name)
	if err != nil || !ok {
		t.Fatalf("Machine(%s) = (%v, %t, %v), want the stored record", rec.Name, gotMachine, ok, err)
	}
	if gotMachine.Selector != rec.Selector || gotMachine.Kind != rec.Kind || gotMachine.VMType != rec.VMType {
		t.Errorf("reloaded machine = %+v, want selector/kind/vmtype from %+v", gotMachine, rec)
	}
	if len(gotMachine.Mounts) != 1 || gotMachine.Mounts[0] != resolved {
		t.Errorf("reloaded mounts = %v, want [%s]", gotMachine.Mounts, resolved)
	}
	if gotMachine.CreatedAt.IsZero() {
		t.Error("PutMachine did not stamp CreatedAt")
	}

	projects, err := reopened.Projects()
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 1 {
		t.Errorf("Projects() = %d records, want 1", len(projects))
	}
	machines, err := reopened.Machines()
	if err != nil {
		t.Fatalf("Machines: %v", err)
	}
	if len(machines) != 1 {
		t.Errorf("Machines() = %d records, want 1", len(machines))
	}
}

// The remembered isolation choice is the whole point of a project record: it
// has to be there for the *next* process, with no file added to the repository.
func TestStore_RemembersIsolationPerProject_REQ_11_2(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	dir := mkdir(t, "proj")

	rec, err := st.EnsureProject(dir)
	if err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	if rec.Isolated {
		t.Error("a new project record must not default to isolated")
	}

	updated, err := st.UpdateProject(rec.ID, func(p *types.ProjectRecord) {
		p.Isolated = true
		p.Selector = &types.EnvironmentSelector{Distro: types.DistroFedora, Version: "41", Arch: types.ArchARM64}
	})
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	if !updated.Isolated || updated.Selector == nil {
		t.Fatalf("UpdateProject returned %+v, want isolated with a selector", updated)
	}

	reopened, err := Open(st.Root())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	// Reached from a different spelling of the same directory, as a later
	// invocation from a subdirectory-normalised path would.
	id, err := ProjectID(filepath.Join(dir, "..", filepath.Base(dir)))
	if err != nil {
		t.Fatalf("ProjectID: %v", err)
	}
	got, ok, err := reopened.Project(id)
	if err != nil || !ok {
		t.Fatalf("Project(%s) = (%t, %v), want the remembered record", id, ok, err)
	}
	if !got.Isolated {
		t.Error("the isolation choice was not remembered")
	}
	if got.Selector == nil || got.Selector.Distro != types.DistroFedora {
		t.Errorf("remembered selector = %+v, want fedora", got.Selector)
	}

	// Nothing may be written into the project directory itself (REQ-11.2).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read project dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("the store wrote %d entries into the project directory, want none", len(entries))
	}

	if _, err := st.UpdateProject(rec.ID, func(p *types.ProjectRecord) { p.Path = "/somewhere/else" }); err == nil {
		t.Error("UpdateProject allowed the path to change, which would orphan the record")
	}
	if _, err := st.UpdateProject("0000", func(*types.ProjectRecord) {}); err == nil {
		t.Error("UpdateProject on an unknown project must fail rather than invent a record")
	}
}

// A reader outside the lock — another avr, or a human with `cat` — must never
// observe a truncated or half-written registry, however often it is rewritten.
func TestStore_AtomicWriteSurvivesInterruption_REQ_17_5(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	names := []string{
		"avr-ubuntu-24.04-arm64", "avr-ubuntu-24.04-amd64", "avr-debian-12-arm64",
		"avr-fedora-41-arm64", "avr-prj-0123456789", "avr-base-ubuntu-24.04-arm64",
	}
	big := mkdir(t, "big")
	for _, name := range names {
		rec := sharedMachine(name)
		if name == "avr-prj-0123456789" {
			rec.Kind = types.KindIsolated
			rec.ProjectID = "0123456789"
		}
		rec.Mounts = []string{big, filepath.Join(big, "..")}
		if err := st.PutMachine(rec); err != nil {
			t.Fatalf("PutMachine(%s): %v", name, err)
		}
	}

	registry := filepath.Join(st.Root(), machinesFile)
	done := make(chan struct{})
	reads := make(chan int, 1)

	go func() {
		defer close(reads)
		observed := 0
		for {
			select {
			case <-done:
				reads <- observed
				return
			default:
			}
			data, err := os.ReadFile(registry)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				t.Errorf("read registry: %v", err)
				reads <- observed
				return
			}
			var machines map[string]types.MachineRecord
			if err := json.Unmarshal(data, &machines); err != nil {
				t.Errorf("registry was observed half-written (%d bytes): %v", len(data), err)
				reads <- observed
				return
			}
			// Only the two states the writer alternates between are valid; a
			// count in between would mean a reader saw a partial rewrite.
			if len(machines) != 1 && len(machines) != len(names) {
				t.Errorf("registry observed with %d machines, want 1 or %d", len(machines), len(names))
				reads <- observed
				return
			}
			observed++
		}
	}()

	for i := 0; i < 40; i++ {
		err := st.Update(func(tx *Tx) error {
			if i%2 == 0 {
				for _, name := range names[1:] {
					if err := tx.DeleteMachine(name); err != nil {
						return err
					}
				}
				return nil
			}
			for _, name := range names[1:] {
				rec := sharedMachine(name)
				if name == "avr-prj-0123456789" {
					rec.Kind = types.KindIsolated
					rec.ProjectID = "0123456789"
				}
				rec.Mounts = []string{big}
				if err := tx.PutMachine(rec); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("rewrite %d: %v", i, err)
		}
	}
	close(done)
	if observed := <-reads; observed == 0 {
		t.Error("the reader never managed to read the registry; the test proved nothing")
	}

	if leftovers := tempLeftovers(t, st.Root()); len(leftovers) != 0 {
		t.Errorf("atomic writes left temporary files behind: %v", leftovers)
	}
}

// A write that cannot complete must leave the previous record exactly as it
// was — the store degrades to "no new information", never to "no information".
func TestStore_FailedWriteLeavesThePreviousRecordIntact_REQ_17_5(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the write cannot be made to fail this way")
	}

	st := newTestStore(t)
	if err := st.PutMachine(sharedMachine("avr-ubuntu-24.04-arm64")); err != nil {
		t.Fatalf("PutMachine: %v", err)
	}
	registry := filepath.Join(st.Root(), machinesFile)
	before, err := os.ReadFile(registry)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}

	// Make the state directory unwritable so the temp file cannot be created.
	if err := os.Chmod(st.Root(), 0o500); err != nil {
		t.Fatalf("chmod state dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(st.Root(), dirPerm) })

	if err := st.PutMachine(sharedMachine("avr-fedora-41-arm64")); err == nil {
		t.Fatal("PutMachine succeeded although the state directory is unwritable")
	}

	after, err := os.ReadFile(registry)
	if err != nil {
		t.Fatalf("read registry after the failed write: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("the registry changed after a failed write:\nbefore: %s\nafter:  %s", before, after)
	}

	machines, err := st.Machines()
	if err != nil {
		t.Fatalf("Machines after the failed write: %v", err)
	}
	if len(machines) != 1 || machines[0].Name != "avr-ubuntu-24.04-arm64" {
		t.Errorf("Machines() = %v, want only the record that was already there", machines)
	}
	if leftovers := tempLeftovers(t, st.Root()); len(leftovers) != 0 {
		t.Errorf("failed write left temporary files behind: %v", leftovers)
	}
}

// A transaction is all-or-nothing: an operation that fails half way through
// records none of its intent, which is what stops a failed provision from
// leaving a machine record behind (design §4, REQ-1.6).
func TestStore_FailedTransactionRecordsNothing_REQ_17_5(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	dir := mkdir(t, "proj")

	wantErr := errors.New("provisioning failed")
	err := st.Update(func(tx *Tx) error {
		if _, err := tx.EnsureProject(dir); err != nil {
			return err
		}
		if err := tx.PutMachine(sharedMachine("avr-ubuntu-24.04-arm64")); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Update error = %v, want %v", err, wantErr)
	}

	machines, err := st.Machines()
	if err != nil {
		t.Fatalf("Machines: %v", err)
	}
	if len(machines) != 0 {
		t.Errorf("a failed transaction recorded %v", machines)
	}
	projects, err := st.Projects()
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("a failed transaction recorded %v", projects)
	}
	for _, name := range []string{projectsFile, machinesFile} {
		if _, err := os.Stat(filepath.Join(st.Root(), name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s exists after a failed transaction (err = %v)", name, err)
		}
	}
}

// Concurrent avr invocations must queue on the lock rather than overwrite each
// other's records: every write has to be present at the end.
func TestStore_ConcurrentMutationsAllLand_REQ_17_5(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	shared := sharedMachine("avr-ubuntu-24.04-arm64")
	if err := st.PutMachine(shared); err != nil {
		t.Fatalf("PutMachine: %v", err)
	}

	const workers = 12
	dirs := make([]string, workers)
	for i := range dirs {
		dirs[i] = mkdir(t, "proj", string(rune('a'+i)))
	}

	var wg sync.WaitGroup
	errs := make(chan error, workers*4)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			// All three files, all from the same registry entry, so any lost
			// read-modify-write shows up as a missing record.
			if _, err := st.EnsureProject(dirs[i]); err != nil {
				errs <- err
			}
			isolated := types.MachineRecord{
				Name:      "avr-prj-" + strings.Repeat(string(rune('a'+i)), 4),
				Selector:  shared.Selector,
				Kind:      types.KindIsolated,
				ProjectID: "project-" + string(rune('a'+i)),
			}
			if err := st.PutMachine(isolated); err != nil {
				errs <- err
			}
			if err := st.AddMount(shared.Name, dirs[i]); err != nil {
				errs <- err
			}
			if err := st.AddSession(types.SessionRecord{Machine: isolated.Name, PID: os.Getpid()}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent mutation: %v", err)
	}

	projects, err := st.Projects()
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != workers {
		t.Errorf("Projects() = %d records, want %d — a write was lost", len(projects), workers)
	}
	machines, err := st.Machines()
	if err != nil {
		t.Fatalf("Machines: %v", err)
	}
	if len(machines) != workers+1 {
		t.Errorf("Machines() = %d records, want %d — a write was lost", len(machines), workers+1)
	}
	reloaded, ok, err := st.Machine(shared.Name)
	if err != nil || !ok {
		t.Fatalf("Machine(%s) = (%t, %v)", shared.Name, ok, err)
	}
	if len(reloaded.Mounts) != workers {
		t.Errorf("shared machine has %d mounts, want %d — a mount registration was lost", len(reloaded.Mounts), workers)
	}
	sessions, err := st.Sessions()
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != workers {
		t.Errorf("Sessions() = %d records, want %d — a session was lost", len(sessions), workers)
	}
}

// Stale sessions are the residue of a crashed avr. They have to disappear on
// read, or idle auto-stop would think a machine is in use forever (PROP-11);
// live sessions must survive, or it would stop a machine somebody is using.
func TestStore_PrunesSessionsWithDeadPids_REQ_17_5(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	machine := sharedMachine("avr-ubuntu-24.04-arm64")
	if err := st.PutMachine(machine); err != nil {
		t.Fatalf("PutMachine: %v", err)
	}

	live := os.Getpid()
	dead := deadPID(t)
	for _, pid := range []int{live, dead} {
		if err := st.AddSession(types.SessionRecord{Machine: machine.Name, PID: pid}); err != nil {
			t.Fatalf("AddSession(%d): %v", pid, err)
		}
	}

	sessions, err := st.Sessions()
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].PID != live {
		t.Fatalf("Sessions() = %+v, want only the live pid %d", sessions, live)
	}
	if sessions[0].StartedAt.IsZero() {
		t.Error("AddSession did not stamp StartedAt")
	}

	// The prune is persisted, not just filtered on the way out.
	data, err := os.ReadFile(filepath.Join(st.Root(), sessionsFile))
	if err != nil {
		t.Fatalf("read sessions file: %v", err)
	}
	var onDisk []types.SessionRecord
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("decode sessions file: %v", err)
	}
	if len(onDisk) != 1 || onDisk[0].PID != live {
		t.Errorf("sessions.json = %+v, want only the live pid %d", onDisk, live)
	}

	forMachine, err := st.SessionsFor(machine.Name)
	if err != nil {
		t.Fatalf("SessionsFor: %v", err)
	}
	if len(forMachine) != 1 {
		t.Errorf("SessionsFor(%s) = %+v, want one session", machine.Name, forMachine)
	}

	if err := st.RemoveSession(machine.Name, live); err != nil {
		t.Fatalf("RemoveSession: %v", err)
	}
	// Removing a session that is already gone is not an error: callers remove
	// from a defer that also runs after a crash-pruned entry.
	if err := st.RemoveSession(machine.Name, live); err != nil {
		t.Errorf("RemoveSession twice: %v", err)
	}
	sessions, err = st.Sessions()
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("Sessions() = %+v, want none", sessions)
	}

	if err := st.AddSession(types.SessionRecord{Machine: machine.Name, PID: 0}); err == nil {
		t.Error("AddSession accepted pid 0, which addresses a process group rather than a process")
	}
	if err := st.AddSession(types.SessionRecord{Machine: "avr-not-recorded", PID: live}); err == nil {
		t.Error("AddSession accepted a session on a machine avar has no record of")
	}
}

// avar must never record or act on a machine outside its own namespace, however
// the name arrives — from a caller or from a hand-edited registry (PROP-6).
func TestStore_RefusesMachinesOutsideItsNamespace_REQ_5_4(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	dir := mkdir(t, "proj")

	foreign := []string{"docker-desktop", "default", "ubuntu", "AVR-shouty", "avr", "", "colima-avr-"}
	for _, name := range foreign {
		rec := sharedMachine(name)
		if err := st.PutMachine(rec); err == nil {
			t.Errorf("PutMachine(%q) succeeded; avar must only record its own machines", name)
		}
		if err := st.DeleteMachine(name); err == nil {
			t.Errorf("DeleteMachine(%q) succeeded; avar must never delete a machine it does not own", name)
		}
		if err := st.AddMount(name, dir); err == nil {
			t.Errorf("AddMount(%q) succeeded; avar must never modify a machine it does not own", name)
		}
		if _, _, err := st.Machine(name); err == nil {
			t.Errorf("Machine(%q) succeeded; a name outside avar's namespace is not a machine avar knows", name)
		}
		if err := st.AddSession(types.SessionRecord{Machine: name, PID: os.Getpid()}); err == nil {
			t.Errorf("AddSession on %q succeeded", name)
		}
		if _, err := st.SessionsFor(name); err == nil {
			t.Errorf("SessionsFor(%q) succeeded", name)
		}
	}

	// A hand-edited registry is refused loudly: machine records cannot be
	// rebuilt from nothing, so avar says what is wrong instead of dropping it.
	if err := os.WriteFile(filepath.Join(st.Root(), machinesFile),
		[]byte(`{"docker-desktop":{"name":"docker-desktop","kind":"shared","mounts":[]}}`), filePerm); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	_, err := st.Machines()
	if err == nil {
		t.Fatal("Machines() accepted a registry naming a machine avar does not own")
	}
	if !strings.Contains(err.Error(), "docker-desktop") {
		t.Errorf("error %q does not name the offending machine", err)
	}

	// A hand-edited session file is different: sessions are ephemeral, so an
	// entry avar must not act on is simply dropped.
	if err := os.Remove(filepath.Join(st.Root(), machinesFile)); err != nil {
		t.Fatalf("remove registry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(st.Root(), sessionsFile),
		[]byte(`[{"machine":"docker-desktop","pid":`+strconv.Itoa(os.Getpid())+`}]`), filePerm); err != nil {
		t.Fatalf("write sessions: %v", err)
	}
	sessions, err := st.Sessions()
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("Sessions() = %+v, want the foreign entry dropped", sessions)
	}
}

// A machine's mount list only grows (design §4). A caller writing back a record
// it read earlier must not be able to unregister another project's directory.
func TestStore_MachineMountsOnlyGrow_REQ_6_4(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	first := mkdir(t, "one")
	second := mkdir(t, "two")

	rec := sharedMachine("avr-ubuntu-24.04-arm64")
	rec.Mounts = []string{first}
	if err := st.PutMachine(rec); err != nil {
		t.Fatalf("PutMachine: %v", err)
	}
	if err := st.AddMount(rec.Name, second); err != nil {
		t.Fatalf("AddMount: %v", err)
	}
	// Registering the same directory twice, and via a different spelling, must
	// not duplicate it.
	if err := st.AddMount(rec.Name, second+string(os.PathSeparator)); err != nil {
		t.Fatalf("AddMount again: %v", err)
	}

	// A stale record written back keeps the mount it never knew about.
	if err := st.PutMachine(rec); err != nil {
		t.Fatalf("PutMachine with a stale record: %v", err)
	}

	got, ok, err := st.Machine(rec.Name)
	if err != nil || !ok {
		t.Fatalf("Machine: (%t, %v)", ok, err)
	}
	resolvedSecond, err := ResolveProjectPath(second)
	if err != nil {
		t.Fatalf("ResolveProjectPath: %v", err)
	}
	if len(got.Mounts) != 2 {
		t.Fatalf("mounts = %v, want both project directories exactly once", got.Mounts)
	}
	if got.Mounts[1] != resolvedSecond {
		t.Errorf("mounts = %v, want the second registration at the end", got.Mounts)
	}

	// A returned record is a copy: mutating it cannot reach into the store.
	got.Mounts[0] = "/etc"
	again, _, err := st.Machine(rec.Name)
	if err != nil {
		t.Fatalf("Machine: %v", err)
	}
	if again.Mounts[0] == "/etc" {
		t.Error("mutating a returned record changed the stored one")
	}

	if err := st.AddMount("avr-unknown-machine", first); err == nil {
		t.Error("AddMount succeeded for a machine avar has no record of")
	}
	if err := st.AddMount(rec.Name, filepath.Join(first, "missing")); err == nil {
		t.Error("AddMount accepted a directory that does not exist")
	}
}

// The record is removed only once the machine is gone, and a retry of a
// half-finished delete must succeed rather than error (design §4, REQ-1.6).
func TestStore_DeleteMachineIsIdempotentAndDropsSessions_REQ_1_6(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	rec := sharedMachine("avr-ubuntu-24.04-arm64")
	other := sharedMachine("avr-debian-12-arm64")
	for _, m := range []types.MachineRecord{rec, other} {
		if err := st.PutMachine(m); err != nil {
			t.Fatalf("PutMachine(%s): %v", m.Name, err)
		}
		if err := st.AddSession(types.SessionRecord{Machine: m.Name, PID: os.Getpid()}); err != nil {
			t.Fatalf("AddSession(%s): %v", m.Name, err)
		}
	}

	if err := st.DeleteMachine(rec.Name); err != nil {
		t.Fatalf("DeleteMachine: %v", err)
	}
	if err := st.DeleteMachine(rec.Name); err != nil {
		t.Errorf("DeleteMachine on an already-deleted machine: %v", err)
	}

	if _, ok, err := st.Machine(rec.Name); err != nil || ok {
		t.Errorf("Machine(%s) = (%t, %v), want it gone", rec.Name, ok, err)
	}
	sessions, err := st.Sessions()
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].Machine != other.Name {
		t.Errorf("Sessions() = %+v, want only the surviving machine's session", sessions)
	}
}

// avar's state lists every directory the user works in, so it is private to the
// user (CLAUDE.md security standard; the state directory is the one place avar
// writes host paths down).
func TestStore_StateDirectoryAndFilesArePrivateToTheUser(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	dir := mkdir(t, "proj")
	if _, err := st.EnsureProject(dir); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	machine := sharedMachine("avr-ubuntu-24.04-arm64")
	if err := st.PutMachine(machine); err != nil {
		t.Fatalf("PutMachine: %v", err)
	}
	if err := st.AddSession(types.SessionRecord{Machine: machine.Name, PID: os.Getpid()}); err != nil {
		t.Fatalf("AddSession: %v", err)
	}

	for _, d := range []string{st.Root(), st.SSHDir(), st.LogsDir()} {
		info, err := os.Stat(d)
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}
		if got := info.Mode().Perm(); got != dirPerm {
			t.Errorf("directory %s has mode %o, want %o", d, got, dirPerm)
		}
	}
	for _, name := range []string{projectsFile, machinesFile, sessionsFile, lockFile} {
		path := filepath.Join(st.Root(), name)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != filePerm {
			t.Errorf("file %s has mode %o, want %o", path, got, filePerm)
		}
	}

	// An existing state directory that is too open is tightened on open.
	loose := filepath.Join(t.TempDir(), "loose")
	if err := os.MkdirAll(loose, 0o755); err != nil {
		t.Fatalf("create loose dir: %v", err)
	}
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatalf("chmod loose dir: %v", err)
	}
	if _, err := Open(loose); err != nil {
		t.Fatalf("Open(%s): %v", loose, err)
	}
	info, err := os.Stat(loose)
	if err != nil {
		t.Fatalf("stat %s: %v", loose, err)
	}
	if got := info.Mode().Perm(); got != dirPerm {
		t.Errorf("existing state directory left at mode %o, want it tightened to %o", got, dirPerm)
	}
}

func TestStore_CorruptStateFileIsReportedNotIgnored_REQ_17_5(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	path := filepath.Join(st.Root(), projectsFile)
	if err := os.WriteFile(path, []byte("{not json"), filePerm); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	_, err := st.Projects()
	if err == nil {
		t.Fatal("Projects() ignored a corrupt state file")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the file the user has to deal with", err)
	}

	// An empty file is the normal state after a truncating editor or a crash
	// before the first record; it reads as "nothing recorded yet".
	if err := os.WriteFile(path, nil, filePerm); err != nil {
		t.Fatalf("truncate file: %v", err)
	}
	projects, err := st.Projects()
	if err != nil {
		t.Fatalf("Projects() on an empty file: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("Projects() = %v, want none", projects)
	}
}

func TestDefaultRoot_HonoursAVRHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv(HomeEnv, root)

	got, err := DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	if got != root {
		t.Errorf("DefaultRoot() = %s, want %s", got, root)
	}

	st, err := OpenDefault()
	if err != nil {
		t.Fatalf("OpenDefault: %v", err)
	}
	if st.Root() != root {
		t.Errorf("OpenDefault root = %s, want %s", st.Root(), root)
	}
	if st.ConfigPath() != filepath.Join(root, configFile) {
		t.Errorf("ConfigPath() = %s, want it inside the state directory", st.ConfigPath())
	}

	t.Setenv(HomeEnv, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err = DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot without %s: %v", HomeEnv, err)
	}
	if want := filepath.Join(home, ".avr"); got != want {
		t.Errorf("DefaultRoot() = %s, want %s", got, want)
	}
}

func TestOpen_RejectsAnEmptyRoot(t *testing.T) {
	t.Parallel()

	if _, err := Open(""); err == nil {
		t.Error("Open(\"\") succeeded; a store must know where its state lives")
	}
}

// A record that cannot be acted on later is refused now: the registry is only
// useful if every entry in it describes a machine avar can actually operate.
func TestStore_RefusesIncoherentRecords_REQ_1_6(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	dir := mkdir(t, "proj")

	unknownKind := sharedMachine("avr-ubuntu-24.04-arm64")
	unknownKind.Kind = "ephemeral"
	if err := st.PutMachine(unknownKind); err == nil {
		t.Error("PutMachine accepted an unknown machine kind")
	}

	orphanIsolated := sharedMachine("avr-prj-abcdef0123")
	orphanIsolated.Kind = types.KindIsolated
	if err := st.PutMachine(orphanIsolated); err == nil {
		t.Error("PutMachine accepted an isolated machine that names no project")
	}

	relativeMount := sharedMachine("avr-ubuntu-24.04-arm64")
	relativeMount.Mounts = []string{"code/my-project"}
	if err := st.PutMachine(relativeMount); err == nil {
		t.Error("PutMachine accepted a relative mount path")
	}

	err := st.Update(func(tx *Tx) error {
		rec, err := tx.EnsureProject(dir)
		if err != nil {
			return err
		}
		if err := tx.PutProject(types.ProjectRecord{ID: rec.ID, Path: "code/my-project"}); err == nil {
			t.Error("PutProject accepted a relative project path")
		}
		if err := tx.PutProject(types.ProjectRecord{ID: "not-the-hash", Path: rec.Path}); err == nil {
			t.Error("PutProject accepted a record filed under the wrong identity")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	machines, err := st.Machines()
	if err != nil {
		t.Fatalf("Machines: %v", err)
	}
	if len(machines) != 0 {
		t.Errorf("Machines() = %v, want nothing recorded", machines)
	}
}

// Reconciliation (task 13) has to diff avar's records against the backend's
// listing and apply the outcome as one step. This is the shape of that
// transaction, exercised here so the API keeps supporting it.
func TestStore_UpdateSupportsAtomicReconciliation_REQ_17_5(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	for _, name := range []string{"avr-ubuntu-24.04-arm64", "avr-debian-12-arm64"} {
		if err := st.PutMachine(sharedMachine(name)); err != nil {
			t.Fatalf("PutMachine(%s): %v", name, err)
		}
	}

	// What the backend reports exists: one of the recorded machines is gone,
	// and one avr-prefixed machine avar has no record of has appeared.
	backend := map[string]bool{"avr-ubuntu-24.04-arm64": true, "avr-fedora-41-arm64": true}

	err := st.Update(func(tx *Tx) error {
		for _, rec := range tx.Machines() {
			if !backend[rec.Name] {
				if err := tx.DeleteMachine(rec.Name); err != nil {
					return err
				}
			}
		}
		for name := range backend {
			if _, ok, err := tx.Machine(name); err != nil {
				return err
			} else if !ok {
				if err := tx.PutMachine(sharedMachine(name)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	machines, err := st.Machines()
	if err != nil {
		t.Fatalf("Machines: %v", err)
	}
	if len(machines) != 2 {
		t.Fatalf("Machines() = %v, want the two machines the backend reports", machines)
	}
	if machines[0].Name != "avr-fedora-41-arm64" || machines[1].Name != "avr-ubuntu-24.04-arm64" {
		t.Errorf("Machines() = %v, want the adopted and the surviving machine, ordered by name", machines)
	}
}

// tempLeftovers lists the atomic-write temp files still present in dir.
func tempLeftovers(t *testing.T, dir string) []string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	return matches
}
