// Package state is avar's durable, crash-consistent record of what it knows:
// the host directories it has been run from, the machines it created, and the
// sessions currently attached to them. It owns the State_Dir (`~/.avr` by
// default).
//
// Two invariants shape the whole package:
//
//   - Every file is replaced atomically — temp file in the same directory,
//     fsync, rename — so an avr killed mid-write can never leave a partial or
//     truncated record behind (REQ-17.5).
//   - Every read and every write happens inside an exclusive advisory lock on
//     `<root>/lock`, so concurrent avr invocations queue instead of racing
//     (design §6, "Concurrent avr invocations racing on create").
//
// The store knows nothing about Lima, or about any backend. It records what
// avar *intends*; the provider is the source of truth for what *exists*, and
// reconciling the two (task 13) is how crash recovery works. That is why the
// registry is exposed as plain records a reconciler can diff against a backend
// listing without reaching inside the store.
//
// Cross-file note: each file is individually atomic, but a transaction that
// changes two files performs two renames. A crash between them leaves both
// files valid and internally consistent, never truncated — the reconciler
// tolerates a machine record whose project record is missing (and vice versa),
// which is the same situation it already has to handle for records that never
// got written at all.
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/olamide226/avar/internal/types"
)

// HomeEnv overrides the State_Dir location. It exists so tests and sandboxed
// runs never touch a developer's real `~/.avr`.
const HomeEnv = "AVR_HOME"

// DefaultLockTimeout bounds how long an invocation waits for another
// invocation's state transaction before giving up with ErrLockTimeout. Waiting
// forever would turn a stale lock into a hung terminal.
const DefaultLockTimeout = 10 * time.Second

// File permissions. avar's state reveals which directories the user works in,
// so the directory is private to the user and so is every file in it.
const (
	dirPerm  fs.FileMode = 0o700
	filePerm fs.FileMode = 0o600
)

const (
	projectsFile = "projects.json"
	machinesFile = "machines.json"
	sessionsFile = "sessions.json"
	configFile   = "config.toml"
	lockFile     = "lock"
	sshDirName   = "ssh"
	logsDirName  = "logs"
)

// Store is a handle on the State_Dir. It is safe for concurrent use by
// multiple goroutines and by multiple processes: mutual exclusion comes from
// the advisory file lock, not from in-process state, so the guarantees are the
// same either way.
type Store struct {
	root        string
	lockTimeout time.Duration
}

// Option adjusts a Store at construction time.
type Option func(*Store)

// WithLockTimeout overrides how long the store waits for the state lock.
func WithLockTimeout(d time.Duration) Option {
	return func(s *Store) { s.lockTimeout = d }
}

// DefaultRoot reports the State_Dir the real CLI uses: $AVR_HOME when set,
// otherwise ~/.avr. This is the only place the environment is consulted; a
// Store resolves its root once, at construction, so nothing deeper down can
// disagree about where state lives.
func DefaultRoot() (string, error) {
	if v := strings.TrimSpace(os.Getenv(HomeEnv)); v != "" {
		abs, err := filepath.Abs(v)
		if err != nil {
			return "", fmt.Errorf("resolve %s=%q: %w", HomeEnv, v, err)
		}
		return abs, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate your home directory for avar's state directory: %w; set %s to choose one explicitly", err, HomeEnv)
	}
	return filepath.Join(home, ".avr"), nil
}

// OpenDefault opens the State_Dir reported by DefaultRoot.
func OpenDefault(opts ...Option) (*Store, error) {
	root, err := DefaultRoot()
	if err != nil {
		return nil, err
	}
	return Open(root, opts...)
}

// Open prepares root as a State_Dir and returns a handle on it, creating the
// directory layout (including the ssh/ and logs/ subdirectories later tasks
// write into) if it is not there yet.
func Open(root string, opts ...Option) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("open avar state directory: no path given")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve avar state directory %q: %w", root, err)
	}

	s := &Store{root: abs, lockTimeout: DefaultLockTimeout}
	for _, opt := range opts {
		opt(s)
	}

	for _, dir := range []string{s.root, s.SSHDir(), s.LogsDir()} {
		if err := os.MkdirAll(dir, dirPerm); err != nil {
			return nil, fmt.Errorf("create avar state directory %s: %w", dir, err)
		}
		if err := tightenPerm(dir); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// tightenPerm removes group and other permissions from an existing directory.
// A state directory created by an older version, a broken umask, or a stray
// chmod must not stay readable by other local users: it lists every project
// path the user works in.
func tightenPerm(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("inspect avar state directory %s: %w", dir, err)
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(dir, dirPerm); err != nil {
		return fmt.Errorf("restrict permissions on avar state directory %s: %w", dir, err)
	}
	return nil
}

// Root is the State_Dir this store owns.
func (s *Store) Root() string { return s.root }

// ConfigPath is the user-editable global defaults file. The store neither
// writes nor parses it (task 22 owns that); it only guarantees the directory
// it lives in.
func (s *Store) ConfigPath() string { return s.path(configFile) }

// SSHDir holds avar-owned SSH configuration (`avr code`, task 19).
func (s *Store) SSHDir() string { return s.path(sshDirName) }

// LogsDir holds provisioning logs referenced from error messages (task 7).
func (s *Store) LogsDir() string { return s.path(logsDirName) }

func (s *Store) path(name string) string { return filepath.Join(s.root, name) }

// Update runs fn inside the exclusive state lock against a snapshot of the
// state loaded once at the start, and flushes whatever fn changed when it
// returns nil. If fn returns an error nothing is written at all, so a failed
// multi-step operation cannot leave half of its intent recorded.
//
// This is the store's only critical section, and it is deliberately the only
// way to hold one: operations compose by passing the *Tx, never by taking the
// lock again (see fileLock's note on re-entrancy). Long backend work does not
// belong inside fn — it would make every other invocation wait out the lock
// timeout.
func (s *Store) Update(fn func(*Tx) error) (err error) {
	lock, err := acquireLock(s.path(lockFile), s.lockTimeout)
	if err != nil {
		return err
	}
	defer func() {
		// Released on every path, including fn panicking; a lock leaked into
		// the next invocation would look like a wedged avar.
		if rerr := lock.release(); rerr != nil && err == nil {
			err = rerr
		}
	}()

	tx, err := s.begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.flush()
}

// Tx is an in-progress state transaction: the loaded records plus the changes
// made to them so far. It is not safe for concurrent use and must not outlive
// the Update call that produced it.
type Tx struct {
	store    *Store
	projects map[string]types.ProjectRecord
	machines map[string]types.MachineRecord
	sessions []types.SessionRecord

	// schema is what the directory said it was when the transaction opened, and
	// schemaStale reports that schema.json does not state the current version —
	// because it is absent, or because it names an older one.
	schema      schemaRecord
	schemaStale bool

	projectsDirty bool
	machinesDirty bool
	sessionsDirty bool
}

func (s *Store) begin() (*Tx, error) {
	tx := &Tx{
		store:    s,
		projects: map[string]types.ProjectRecord{},
		machines: map[string]types.MachineRecord{},
	}

	schema, recorded, err := s.readSchema()
	if err != nil {
		return nil, err
	}
	tx.schema = schema
	tx.schemaStale = !recorded || schema.Version != SchemaVersion

	if err := readJSON(s.path(projectsFile), &tx.projects); err != nil {
		return nil, err
	}
	machines, migrated, err := readMachines(s.path(machinesFile), schema.Version)
	if err != nil {
		return nil, err
	}
	tx.machines = machines
	// Records converted on read are written back by this transaction, so the
	// conversion happens once rather than on every invocation. It goes through
	// the same atomic replace as any other write, so an interrupted migration
	// leaves the previous file intact (REQ-17.5, PROP-7).
	tx.machinesDirty = migrated
	if err := readJSON(s.path(sessionsFile), &tx.sessions); err != nil {
		return nil, err
	}

	// A registry entry outside avar's namespace is refused loudly rather than
	// ignored: machine records cannot be recreated from nothing, so avar says
	// what is wrong instead of quietly dropping the user's data (REQ-5.4).
	for name := range tx.machines {
		if err := types.ValidateMachineName(name); err != nil {
			return nil, fmt.Errorf("avar's machine registry %s lists %q, which avar must not manage: %w", s.path(machinesFile), name, err)
		}
	}

	// Stale sessions are pruned on every read; this is what keeps idle
	// auto-stop from being fooled by a crashed avr (design §6, PROP-11).
	if live := usableSessions(tx.sessions); len(live) != len(tx.sessions) {
		tx.sessions = live
		tx.sessionsDirty = true
	}
	return tx, nil
}

func (t *Tx) flush() error {
	// The version is stamped alongside the first write to a directory that does
	// not already state it. A transaction that only read leaves the directory
	// exactly as it found it, so reading avar's state never writes to it — and
	// a migration always writes, because reading migrated records dirties the
	// registry that has to be written back in the new shape.
	if t.schemaStale && (t.projectsDirty || t.machinesDirty || t.sessionsDirty) {
		schema := t.schema
		if schema.Version != SchemaVersion {
			schema.Migrations = append(schema.Migrations, migrationID(schema.Version, SchemaVersion))
		}
		if err := t.store.writeSchema(schema); err != nil {
			return err
		}
		schema.Version = SchemaVersion
		t.schema = schema
		t.schemaStale = false
	}
	if t.projectsDirty {
		if err := writeJSONAtomic(t.store.path(projectsFile), t.projects); err != nil {
			return err
		}
		t.projectsDirty = false
	}
	if t.machinesDirty {
		if err := writeJSONAtomic(t.store.path(machinesFile), t.machines); err != nil {
			return err
		}
		t.machinesDirty = false
	}
	if t.sessionsDirty {
		sort.Slice(t.sessions, func(i, j int) bool {
			if t.sessions[i].Machine != t.sessions[j].Machine {
				return t.sessions[i].Machine < t.sessions[j].Machine
			}
			return t.sessions[i].PID < t.sessions[j].PID
		})
		if err := writeJSONAtomic(t.store.path(sessionsFile), t.sessions); err != nil {
			return err
		}
		t.sessionsDirty = false
	}
	return nil
}

// --- Projects -------------------------------------------------------------

// EnsureProject records path as a project if it is not recorded yet and marks
// it used now. Project records are created on first use and never removed
// automatically, so this is the only way one comes into existence.
func (t *Tx) EnsureProject(path string) (types.ProjectRecord, error) {
	resolved, err := ResolveProjectPath(path)
	if err != nil {
		return types.ProjectRecord{}, err
	}
	id := hashPath(resolved)
	now := time.Now().UTC()

	rec, ok := t.projects[id]
	if !ok {
		rec = types.ProjectRecord{ID: id, Path: resolved, CreatedAt: now}
	}
	rec.Path = resolved
	rec.LastUsedAt = now
	t.projects[id] = rec
	t.projectsDirty = true
	return copyProject(rec), nil
}

// Project returns the record for a project identity.
func (t *Tx) Project(id string) (types.ProjectRecord, bool) {
	rec, ok := t.projects[id]
	if !ok {
		return types.ProjectRecord{}, false
	}
	return copyProject(rec), true
}

// Projects returns every project record, ordered by path.
func (t *Tx) Projects() []types.ProjectRecord {
	out := make([]types.ProjectRecord, 0, len(t.projects))
	for _, rec := range t.projects {
		out = append(out, copyProject(rec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// PutProject writes rec, which must carry the id its own path hashes to. The
// check is what keeps project identity stable: a record filed under anything
// else would be invisible to the next invocation from that directory.
func (t *Tx) PutProject(rec types.ProjectRecord) error {
	if !filepath.IsAbs(rec.Path) {
		return fmt.Errorf("record project %q: path must be absolute and symlink-resolved", rec.Path)
	}
	if want := hashPath(rec.Path); rec.ID != want {
		return fmt.Errorf("record project %s: id %q does not match its path (expected %q)", rec.Path, rec.ID, want)
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	t.projects[rec.ID] = copyProject(rec)
	t.projectsDirty = true
	return nil
}

// --- Machines -------------------------------------------------------------

// Machine returns the record for a machine avar created.
func (t *Tx) Machine(name string) (types.MachineRecord, bool, error) {
	if err := types.ValidateMachineName(name); err != nil {
		return types.MachineRecord{}, false, fmt.Errorf("look up machine: %w", err)
	}
	rec, ok := t.machines[name]
	if !ok {
		return types.MachineRecord{}, false, nil
	}
	return copyMachine(rec), true, nil
}

// Machines returns every machine avar created, ordered by name. This is the
// left-hand side of reconciliation: the caller diffs it against the backend's
// listing without needing anything else from the store.
func (t *Tx) Machines() []types.MachineRecord {
	out := make([]types.MachineRecord, 0, len(t.machines))
	for _, rec := range t.machines {
		out = append(out, copyMachine(rec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// PutMachine records a machine. Call it only once the machine is confirmed
// viable — the registry must never describe a machine that never worked
// (design §4, REQ-1.6).
//
// Mount lists only grow: the recorded mounts are the union of what is already
// registered and what rec carries, so a caller writing back a stale record
// cannot silently unregister another project's directory.
func (t *Tx) PutMachine(rec types.MachineRecord) error {
	if err := types.ValidateMachineName(rec.Name); err != nil {
		return fmt.Errorf("refusing to record a machine avar does not own: %w", err)
	}
	// A record that does not say which backend made it cannot be acted on: the
	// same machine name means a Lima instance on one host and a WSL
	// distribution on another, and "start this" is a different operation for
	// each (REQ-18.1, REQ-18.14).
	if err := types.ValidateProviderID(rec.Provider); err != nil {
		return fmt.Errorf("record machine %s: %w", rec.Name, err)
	}
	switch rec.Kind {
	case types.KindShared, types.KindIsolated, types.KindBase:
	default:
		return fmt.Errorf("record machine %s: unknown machine kind %q", rec.Name, rec.Kind)
	}
	if rec.Kind == types.KindIsolated && rec.ProjectID == "" {
		return fmt.Errorf("record machine %s: an isolated machine must name the project it serves", rec.Name)
	}

	mounts, err := checkMounts(rec.Mounts)
	if err != nil {
		return fmt.Errorf("record machine %s: %w", rec.Name, err)
	}
	if old, ok := t.machines[rec.Name]; ok {
		mounts, err = union(old.Mounts, mounts)
		if err != nil {
			return fmt.Errorf("record machine %s: %w", rec.Name, err)
		}
		if rec.CreatedAt.IsZero() {
			rec.CreatedAt = old.CreatedAt
		}
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	rec.Mounts = mounts

	t.machines[rec.Name] = rec
	t.machinesDirty = true
	return nil
}

// AddMount registers one project directory as a mount of a machine. Adding a
// directory that is already registered is a no-op, so callers can register
// unconditionally.
//
// mount is the mapping the provider planned (Provider.MapProjectPath): the
// store records where a directory appears in the guest and never invents it,
// because only the backend knows.
//
// The host path must already be canonical — the resolved absolute path a
// project record carries — and is checked, not silently rewritten. Rewriting it
// here would leave the guest path describing the path the caller *passed* while
// the host path described a different directory, so the recorded pair would no
// longer be a mapping of anything. Redundant separators are cleaned, so
// registering a directory twice by two spellings still registers it once
// (REQ-6.4, PROP-1).
func (t *Tx) AddMount(machine string, mount types.MountSpec) error {
	if err := types.ValidateMachineName(machine); err != nil {
		return fmt.Errorf("register a mount: %w", err)
	}
	rec, ok := t.machines[machine]
	if !ok {
		return fmt.Errorf("register mount %s: avar has no record of machine %s", mount.HostPath, machine)
	}
	mount = types.CleanMount(mount)
	resolved, err := ResolveProjectPath(mount.HostPath)
	if err != nil {
		return err
	}
	if resolved != mount.HostPath {
		return fmt.Errorf("register mount %s with machine %s: that directory is really %s; canonicalise a project path before asking a provider where it lands in the guest",
			mount.HostPath, machine, resolved)
	}

	merged, err := union(rec.Mounts, []types.MountSpec{mount})
	if err != nil {
		return fmt.Errorf("register mount %s with machine %s: %w", mount.HostPath, machine, err)
	}
	if types.EqualMounts(rec.Mounts, merged) {
		return nil
	}
	rec.Mounts = merged
	t.machines[machine] = rec
	t.machinesDirty = true
	return nil
}

// DeleteMachine drops a machine's record together with the sessions attached
// to it. Call it only after the backend confirms the machine is gone. Deleting
// a machine avar has no record of is not an error, so a half-finished delete
// can simply be retried.
func (t *Tx) DeleteMachine(name string) error {
	if err := types.ValidateMachineName(name); err != nil {
		return fmt.Errorf("refusing to delete a machine avar does not own: %w", err)
	}
	if _, ok := t.machines[name]; ok {
		delete(t.machines, name)
		t.machinesDirty = true
	}
	kept := make([]types.SessionRecord, 0, len(t.sessions))
	for _, s := range t.sessions {
		if s.Machine == name {
			t.sessionsDirty = true
			continue
		}
		kept = append(kept, s)
	}
	t.sessions = kept
	return nil
}

// --- Sessions -------------------------------------------------------------

// Sessions returns the live sessions, ordered by machine then pid. Entries
// whose pid is dead were already pruned when the transaction opened.
func (t *Tx) Sessions() []types.SessionRecord {
	out := append([]types.SessionRecord(nil), t.sessions...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Machine != out[j].Machine {
			return out[i].Machine < out[j].Machine
		}
		return out[i].PID < out[j].PID
	})
	return out
}

// AddSession records a live attachment to a machine avar owns. Re-adding the
// same (machine, pid) refreshes the existing entry rather than duplicating it.
func (t *Tx) AddSession(rec types.SessionRecord) error {
	if err := types.ValidateMachineName(rec.Machine); err != nil {
		return fmt.Errorf("record a session: %w", err)
	}
	if _, ok := t.machines[rec.Machine]; !ok {
		return fmt.Errorf("record session on %s: avar has no record of that machine", rec.Machine)
	}
	if rec.PID <= 0 {
		return fmt.Errorf("record session on %s: pid %d is not a process", rec.Machine, rec.PID)
	}
	if rec.StartedAt.IsZero() {
		rec.StartedAt = time.Now().UTC()
	}
	for i, s := range t.sessions {
		if s.Machine == rec.Machine && s.PID == rec.PID {
			t.sessions[i] = rec
			t.sessionsDirty = true
			return nil
		}
	}
	t.sessions = append(t.sessions, rec)
	t.sessionsDirty = true
	return nil
}

// RemoveSession forgets one attachment. It is best-effort by design: a session
// that is already gone (pruned as stale, or removed by another invocation) is
// not an error, so the caller can always call it from a defer.
func (t *Tx) RemoveSession(machine string, pid int) {
	kept := make([]types.SessionRecord, 0, len(t.sessions))
	for _, s := range t.sessions {
		if s.Machine == machine && s.PID == pid {
			t.sessionsDirty = true
			continue
		}
		kept = append(kept, s)
	}
	t.sessions = kept
}

// --- Single-operation convenience API -------------------------------------
//
// Each of these is one Update: it takes the lock, does one thing, and releases
// it. Anything that must be atomic across several operations — reconciliation,
// for instance — should use Update directly instead of chaining these.

// EnsureProject records the project at path (creating the record on first use)
// and marks it used now.
func (s *Store) EnsureProject(path string) (types.ProjectRecord, error) {
	var out types.ProjectRecord
	err := s.Update(func(tx *Tx) error {
		rec, err := tx.EnsureProject(path)
		out = rec
		return err
	})
	if err != nil {
		return types.ProjectRecord{}, err
	}
	return out, nil
}

// Project returns the record for a project identity, if avar has one.
func (s *Store) Project(id string) (types.ProjectRecord, bool, error) {
	var (
		out types.ProjectRecord
		ok  bool
	)
	err := s.Update(func(tx *Tx) error {
		out, ok = tx.Project(id)
		return nil
	})
	if err != nil {
		return types.ProjectRecord{}, false, err
	}
	return out, ok, nil
}

// Projects returns every project record, ordered by path.
func (s *Store) Projects() ([]types.ProjectRecord, error) {
	var out []types.ProjectRecord
	err := s.Update(func(tx *Tx) error {
		out = tx.Projects()
		return nil
	})
	return out, err
}

// UpdateProject applies mutate to an existing project record and stores the
// result — this is how a remembered choice such as isolation is written
// (REQ-11.2). mutate must not change the record's identity or path; those two
// fields are the identity itself. mutate must not call back into the store:
// the transaction already holds the lock.
func (s *Store) UpdateProject(id string, mutate func(*types.ProjectRecord)) (types.ProjectRecord, error) {
	var out types.ProjectRecord
	err := s.Update(func(tx *Tx) error {
		rec, ok := tx.Project(id)
		if !ok {
			return fmt.Errorf("update project %s: avar has no record of that project", id)
		}
		before := rec
		mutate(&rec)
		if rec.ID != before.ID || rec.Path != before.Path {
			return fmt.Errorf("update project %s: a project's id and path identify it and cannot be changed", id)
		}
		if err := tx.PutProject(rec); err != nil {
			return err
		}
		out = rec
		return nil
	})
	if err != nil {
		return types.ProjectRecord{}, err
	}
	return out, nil
}

// Machine returns the record for a machine avar created, if it has one.
func (s *Store) Machine(name string) (types.MachineRecord, bool, error) {
	var (
		out types.MachineRecord
		ok  bool
	)
	err := s.Update(func(tx *Tx) error {
		var err error
		out, ok, err = tx.Machine(name)
		return err
	})
	if err != nil {
		return types.MachineRecord{}, false, err
	}
	return out, ok, nil
}

// Machines returns every machine avar created, ordered by name.
func (s *Store) Machines() ([]types.MachineRecord, error) {
	var out []types.MachineRecord
	err := s.Update(func(tx *Tx) error {
		out = tx.Machines()
		return nil
	})
	return out, err
}

// PutMachine records a confirmed-viable machine (see Tx.PutMachine).
func (s *Store) PutMachine(rec types.MachineRecord) error {
	return s.Update(func(tx *Tx) error { return tx.PutMachine(rec) })
}

// AddMount registers a project directory as a mount of a machine.
func (s *Store) AddMount(machine string, mount types.MountSpec) error {
	return s.Update(func(tx *Tx) error { return tx.AddMount(machine, mount) })
}

// DeleteMachine drops a deleted machine's record and its sessions.
func (s *Store) DeleteMachine(name string) error {
	return s.Update(func(tx *Tx) error { return tx.DeleteMachine(name) })
}

// Sessions returns the live sessions across all machines.
func (s *Store) Sessions() ([]types.SessionRecord, error) {
	var out []types.SessionRecord
	err := s.Update(func(tx *Tx) error {
		out = tx.Sessions()
		return nil
	})
	return out, err
}

// SessionsFor returns the live sessions attached to one machine — what a
// caller needs before restarting a machine somebody may be using.
func (s *Store) SessionsFor(machine string) ([]types.SessionRecord, error) {
	if err := types.ValidateMachineName(machine); err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	all, err := s.Sessions()
	if err != nil {
		return nil, err
	}
	out := make([]types.SessionRecord, 0, len(all))
	for _, sess := range all {
		if sess.Machine == machine {
			out = append(out, sess)
		}
	}
	return out, nil
}

// AddSession records a live attachment.
func (s *Store) AddSession(rec types.SessionRecord) error {
	return s.Update(func(tx *Tx) error { return tx.AddSession(rec) })
}

// RemoveSession forgets an attachment; missing entries are not an error.
func (s *Store) RemoveSession(machine string, pid int) error {
	return s.Update(func(tx *Tx) error {
		tx.RemoveSession(machine, pid)
		return nil
	})
}

// --- Persistence ----------------------------------------------------------

// readJSON decodes path into value. A missing or empty file is the normal
// first-run state, not an error.
func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read avar state file %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("avar state file %s is not valid JSON: %w; move it aside and re-run avr to rebuild it", path, err)
	}
	return nil
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode avar state for %s: %w", path, err)
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// writeFileAtomic replaces path with data such that no reader — and no crash —
// can observe a half-written file: the bytes go to a temp file in the same
// directory, are fsynced, and are then renamed over the target. rename(2)
// within a directory is atomic, so an interrupted write leaves either the
// previous file or the new one, never a mixture (REQ-17.5, PROP-7).
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create a temporary file next to %s: %w", path, err)
	}
	tmpName := tmp.Name()

	if err := writeAndSync(tmp, data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	// Flush the directory entry too, so the rename itself survives a crash
	// rather than just the bytes it points at.
	return syncDir(dir)
}

func writeAndSync(f *os.File, data []byte) error {
	// os.CreateTemp already creates the file 0600; setting it explicitly keeps
	// the guarantee independent of that detail.
	if err := f.Chmod(filePerm); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s to flush its directory entry: %w", dir, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("flush directory %s: %w", dir, err)
	}
	return nil
}

// --- Helpers --------------------------------------------------------------

// checkMounts canonicalises and validates a record's mounts.
//
// Registration order is preserved rather than sorted: a record's mount list is
// the history of which projects were registered with the machine, and reordering
// it would rewrite that for no benefit. Canonical *ordering* is the providers'
// business, since they are the ones comparing an applied set against a desired
// one (types.NormalizeMounts).
func checkMounts(mounts []types.MountSpec) ([]types.MountSpec, error) {
	return union(nil, mounts)
}

// union merges added into existing, keeping existing order and appending what
// is new. It is what makes a machine's mount list only grow: a caller writing
// back a record it read earlier cannot unregister another project's directory
// (design §4).
//
// A directory already registered keeps its place and takes the newer mapping,
// so a provider that replans where a project appears in the guest is recorded
// once rather than twice. Two directories claiming one guest path is refused
// outright: the recorded set would then no longer describe what the guest can
// actually reach, which is exactly what PROP-5 asserts it does.
func union(existing, added []types.MountSpec) ([]types.MountSpec, error) {
	out := make([]types.MountSpec, 0, len(existing)+len(added))
	atHost := make(map[string]int, len(existing)+len(added))

	for _, m := range append(append([]types.MountSpec(nil), existing...), added...) {
		m = types.CleanMount(m)
		if err := m.Validate(); err != nil {
			return nil, err
		}
		if i, ok := atHost[m.HostPath]; ok {
			out[i] = m
			continue
		}
		atHost[m.HostPath] = len(out)
		out = append(out, m)
	}

	atGuest := make(map[string]string, len(out))
	for _, m := range out {
		if other, ok := atGuest[m.GuestPath]; ok {
			return nil, fmt.Errorf("%s and %s both claim the guest path %s", other, m.HostPath, m.GuestPath)
		}
		atGuest[m.GuestPath] = m.HostPath
	}
	return out, nil
}

func copyProject(rec types.ProjectRecord) types.ProjectRecord {
	if rec.Selector != nil {
		sel := *rec.Selector
		rec.Selector = &sel
	}
	return rec
}

func copyMachine(rec types.MachineRecord) types.MachineRecord {
	rec.Mounts = append([]types.MountSpec(nil), rec.Mounts...)
	return rec
}
