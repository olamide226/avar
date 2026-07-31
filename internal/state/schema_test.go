package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olamide226/avar/internal/types"
)

// v1Store copies the committed schema-v1 fixture into a fresh state directory
// and opens it.
//
// The fixture is a real v1 directory — machines.json with []string mounts and
// vm_type, no provider anywhere, and no schema.json, exactly as an avr from
// before this change left it. A migration that has only ever been run against
// records the migration's own author wrote is a guess; this is the closest a
// unit test gets to the state directory on a user's disk.
func v1Store(t *testing.T) *Store {
	t.Helper()

	root := filepath.Join(t.TempDir(), ".avr")
	if err := os.MkdirAll(root, dirPerm); err != nil {
		t.Fatalf("create state directory: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join("testdata", "schema-v1"))
	if err != nil {
		t.Fatalf("read the v1 fixture: %v", err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join("testdata", "schema-v1", entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(root, entry.Name()), data, filePerm); err != nil {
			t.Fatalf("write %s: %v", entry.Name(), err)
		}
	}

	st, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return st
}

// The migration is what a user upgrading avr actually runs. It has to convert
// every record faithfully, be visible in schema.json afterwards, and not run a
// second time.
func TestStore_MigratesASchemaV1DirectoryOnRead_REQ_17_5(t *testing.T) {
	t.Parallel()

	st := v1Store(t)

	machines, err := st.Machines()
	if err != nil {
		t.Fatalf("Machines: %v", err)
	}
	if len(machines) != 4 {
		t.Fatalf("Machines() = %d records, want the 4 in the fixture", len(machines))
	}

	byName := make(map[string]types.MachineRecord, len(machines))
	for _, rec := range machines {
		byName[rec.Name] = rec
	}

	shared, ok := byName["avr-ubuntu-24.04-arm64"]
	if !ok {
		t.Fatalf("the shared machine did not survive migration: %v", byName)
	}
	// A v1 mount was a host directory Lima shared writable at the identical
	// guest path, so the conversion records what the machine actually has
	// rather than an assumption about it (REQ-6.1).
	want := []types.MountSpec{
		{HostPath: "/Users/dev/code/api", GuestPath: "/Users/dev/code/api", Writable: true},
		{HostPath: "/Users/dev/code/web", GuestPath: "/Users/dev/code/web", Writable: true},
	}
	if !types.EqualMounts(shared.Mounts, want) {
		t.Errorf("migrated mounts = %v, want %v", shared.Mounts, want)
	}
	// vm_type becomes Runtime, and a pre-existing record could only have been
	// written by the one backend that existed.
	if shared.Runtime != "vz" {
		t.Errorf("migrated runtime = %q, want the recorded vm_type %q", shared.Runtime, "vz")
	}
	if shared.Provider != types.ProviderLima {
		t.Errorf("migrated provider = %q, want %q", shared.Provider, types.ProviderLima)
	}
	// Everything else is carried across untouched.
	if shared.Kind != types.KindShared || shared.Selector.Distro != types.DistroUbuntu || shared.Selector.Version != "24.04" {
		t.Errorf("migrated record lost its identity: %+v", shared)
	}
	if got, want := shared.CreatedAt.UTC(), time.Date(2025, time.March, 27, 14, 22, 5, 463091000, time.UTC); !got.Equal(want) {
		t.Errorf("migrated CreatedAt = %s, want the recorded %s", got, want)
	}

	if qemu := byName["avr-ubuntu-24.04-amd64"]; qemu.Runtime != "qemu" {
		t.Errorf("migrated runtime = %q, want qemu — the mode is copied, not assumed", qemu.Runtime)
	}
	// An isolated machine keeps the project it serves: migration converts a
	// shape, it does not re-derive facts.
	isolated := byName["avr-prj-3fa9c2b1d0-ubuntu-24.04-arm64"]
	if isolated.Kind != types.KindIsolated || isolated.ProjectID == "" {
		t.Errorf("migrated isolated machine = %+v, want its project preserved", isolated)
	}
	// A machine with no mounts is not a machine with a mount of nothing.
	if base := byName["avr-base-ubuntu-24.04-arm64"]; len(base.Mounts) != 0 {
		t.Errorf("migrated base machine has mounts %v, want none", base.Mounts)
	}

	// Project records are unchanged by v2 and must come through as they were.
	projects, err := st.Projects()
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("Projects() = %d records, want the 2 in the fixture", len(projects))
	}
	for _, rec := range projects {
		if rec.Path == "/Users/dev/code/secret-project" && !rec.Isolated {
			t.Error("a remembered isolation choice was lost in migration (REQ-11.2)")
		}
	}
}

// Migrating writes the converted records back, so the next invocation reads v2
// directly. A migration that ran on every invocation would be a migration
// nobody could reason about.
func TestStore_MigrationIsAppliedOnceAndRecorded_REQ_17_5(t *testing.T) {
	t.Parallel()

	st := v1Store(t)
	if _, err := st.Machines(); err != nil {
		t.Fatalf("Machines: %v", err)
	}

	schemaPath := filepath.Join(st.Root(), schemaFileName)
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema.json after migration: %v", err)
	}
	var schema schemaRecord
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("schema.json is not valid JSON: %v", err)
	}
	if schema.Version != SchemaVersion {
		t.Errorf("schema version = %d, want %d", schema.Version, SchemaVersion)
	}
	if len(schema.Migrations) != 1 || schema.Migrations[0] != migrationID(1, 2) {
		t.Errorf("recorded migrations = %v, want [%s]", schema.Migrations, migrationID(1, 2))
	}
	if schema.UpdatedAt.IsZero() {
		t.Error("schema.json does not say when it was written")
	}

	// The registry on disk is now v2: mounts are objects, and every record
	// names its provider.
	registry, err := os.ReadFile(filepath.Join(st.Root(), machinesFile))
	if err != nil {
		t.Fatalf("read machines.json after migration: %v", err)
	}
	if strings.Contains(string(registry), `"vm_type"`) {
		t.Errorf("the migrated registry still carries v1 fields:\n%s", registry)
	}
	for _, want := range []string{`"provider": "lima"`, `"guest_path"`, `"host_path"`, `"runtime"`} {
		if !strings.Contains(string(registry), want) {
			t.Errorf("the migrated registry does not contain %s:\n%s", want, registry)
		}
	}

	// A second read finds a v2 directory and converts nothing: same records,
	// and schema.json is not rewritten.
	before, err := os.Stat(schemaPath)
	if err != nil {
		t.Fatalf("stat schema.json: %v", err)
	}
	again, err := Open(st.Root())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	machines, err := again.Machines()
	if err != nil {
		t.Fatalf("Machines: %v", err)
	}
	if len(machines) != 4 {
		t.Errorf("Machines() = %d records after a second read, want 4", len(machines))
	}
	after, err := os.Stat(schemaPath)
	if err != nil {
		t.Fatalf("stat schema.json: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("reading an already-migrated directory rewrote schema.json")
	}
}

// Migrated records are ordinary records: they can be read, mutated and written
// back through the same API as any other, which is the point of converting them
// rather than tolerating both shapes forever.
func TestStore_MigratedRecordsRoundTrip_REQ_17_5(t *testing.T) {
	t.Parallel()

	st := v1Store(t)
	const name = "avr-ubuntu-24.04-arm64"

	rec, ok, err := st.Machine(name)
	if err != nil || !ok {
		t.Fatalf("Machine(%s) = (%t, %v)", name, ok, err)
	}
	if err := st.PutMachine(rec); err != nil {
		t.Fatalf("writing a migrated record back: %v", err)
	}

	project := resolved(t, mkdir(t, "new-project"))
	if err := st.AddMount(name, share(project)); err != nil {
		t.Fatalf("AddMount on a migrated machine: %v", err)
	}

	reopened, err := Open(st.Root())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok, err := reopened.Machine(name)
	if err != nil || !ok {
		t.Fatalf("Machine(%s) after reopening = (%t, %v)", name, ok, err)
	}
	// The migrated mounts are still there and the new one is at the end: a
	// machine's mount list only grows (REQ-6.4).
	if len(got.Mounts) != 3 || got.Mounts[2] != share(project) {
		t.Errorf("mounts after registering a project = %v", got.Mounts)
	}
	if got.Provider != types.ProviderLima || got.Runtime != "vz" {
		t.Errorf("a round trip lost what migration recovered: %+v", got)
	}
}

// A directory written by a newer avar is refused rather than read: this version
// would drop fields it does not know about on the next write, and silently
// discarding a user's records is worse than telling them to upgrade.
func TestStore_RefusesAStateDirectoryFromTheFuture(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	if err := st.PutMachine(sharedMachine("avr-ubuntu-24.04-arm64")); err != nil {
		t.Fatalf("PutMachine: %v", err)
	}
	future, err := json.Marshal(schemaRecord{Version: SchemaVersion + 1, UpdatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(st.Root(), schemaFileName), future, filePerm); err != nil {
		t.Fatalf("write schema.json: %v", err)
	}

	_, err = st.Machines()
	if err == nil {
		t.Fatal("a state directory from a newer avar was read anyway")
	}
	if !strings.Contains(err.Error(), "upgrade avr") {
		t.Errorf("the error does not say what to do about it: %v", err)
	}
}

func TestStore_RefusesAnUnreadableSchemaFile(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"not JSON":    "schema 2\n",
		"no version":  `{"migrations":["1-to-2"]}`,
		"nonsensical": `{"version":0}`,
	} {
		t.Run(name, func(t *testing.T) {
			st := newTestStore(t)
			if err := os.WriteFile(filepath.Join(st.Root(), schemaFileName), []byte(content), filePerm); err != nil {
				t.Fatalf("write schema.json: %v", err)
			}
			if _, err := st.Machines(); err == nil {
				t.Fatal("an unreadable schema.json was accepted")
			}
		})
	}
}

// Reading is a read. A directory that needs no conversion is left exactly as it
// was found, so `avr status` on a state directory does not write to it.
func TestStore_ReadingDoesNotWriteSchemaJSON(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)
	if _, err := st.Machines(); err != nil {
		t.Fatalf("Machines: %v", err)
	}
	if _, err := os.Stat(filepath.Join(st.Root(), schemaFileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("reading an empty state directory wrote schema.json (%v)", err)
	}

	// The first write stamps the version, so a directory avar has put records
	// in always says which shape they are in.
	if err := st.PutMachine(sharedMachine("avr-ubuntu-24.04-arm64")); err != nil {
		t.Fatalf("PutMachine: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(st.Root(), schemaFileName))
	if err != nil {
		t.Fatalf("read schema.json: %v", err)
	}
	var schema schemaRecord
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("schema.json is not valid JSON: %v", err)
	}
	if schema.Version != SchemaVersion {
		t.Errorf("schema version = %d, want %d", schema.Version, SchemaVersion)
	}
	// A fresh directory was never migrated, so it has no migration history.
	if len(schema.Migrations) != 0 {
		t.Errorf("a new state directory recorded migrations %v", schema.Migrations)
	}
}
