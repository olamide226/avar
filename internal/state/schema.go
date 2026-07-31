package state

// The on-disk schema, and how a state directory written by an older avar is
// read by a newer one.
//
// avar's state is a handful of human-inspectable JSON files rather than a
// database, which is what makes crash recovery simple (design §3.3) — and what
// makes changing a record's shape a real event: the files on a user's disk were
// written by whatever version of avr they ran last. schema.json records which
// shape they are in, so a version that has learned a new shape can convert them
// once, in place, instead of every reader guessing.
//
// Migration runs inside the ordinary state transaction: it is a read that
// produces converted records, and the converted records are written back
// through the same atomic replace and the same lock as any other write
// (REQ-17.5). A migration that is interrupted therefore leaves the previous
// file intact and is simply run again by the next invocation.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/olamide226/avar/internal/types"
)

// SchemaVersion is the shape this version of avar reads and writes.
//
//	v1  MachineRecord.Mounts was []string — a host directory shared at the
//	    identical guest path — and the backend's virtualization mode was
//	    VMType. There was no provider: Lima was the only one.
//	v2  Mounts are []types.MountSpec, VMType is Runtime, and every record names
//	    the provider that made it (design §3.3, §4).
const SchemaVersion = 2

// schemaFileName holds the version of the records beside it.
const schemaFileName = "schema.json"

// schemaRecord is the content of schema.json.
type schemaRecord struct {
	// Version is the shape the other files in this directory are in.
	Version int `json:"version"`
	// Migrations names the conversions that have been applied, oldest first.
	// It is bookkeeping for a human reading the directory and for a bug report
	// that has to establish what a state directory has been through; avar
	// itself decides what to do from Version alone.
	Migrations []string  `json:"migrations,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// readSchema reports the version the state directory is in, and the migrations
// already applied to it.
//
// A directory with no schema.json is either brand new or was written by an avar
// that predates the file. The two are told apart by whether there are records
// at all: records without a schema file are v1, since v1 is the only shape that
// was ever written without one. Nothing is written here — a read must stay a
// read on a directory that needs no conversion.
func (s *Store) readSchema() (rec schemaRecord, recorded bool, err error) {
	data, readErr := os.ReadFile(s.path(schemaFileName))
	switch {
	case readErr == nil:
		if err := json.Unmarshal(data, &rec); err != nil {
			return schemaRecord{}, false, fmt.Errorf("avar state file %s is not valid JSON: %w; move it aside and re-run avr to rebuild it", s.path(schemaFileName), err)
		}
		if rec.Version <= 0 {
			return schemaRecord{}, false, fmt.Errorf("avar state file %s does not say which schema version it describes; move it aside and re-run avr to rebuild it", s.path(schemaFileName))
		}
		recorded = true
	case errors.Is(readErr, fs.ErrNotExist):
		rec.Version = SchemaVersion
		if s.hasRecords() {
			rec.Version = 1
		}
	default:
		return schemaRecord{}, false, fmt.Errorf("read avar state file %s: %w", s.path(schemaFileName), readErr)
	}

	if rec.Version > SchemaVersion {
		// Refusing beats guessing: a newer avar may have written fields this
		// version would drop on the next write, and dropping a user's records
		// silently is worse than asking them to run the version that wrote them.
		return schemaRecord{}, recorded, fmt.Errorf("avar's state directory %s was written by a newer version of avar (schema %d; this avr understands %d): upgrade avr",
			s.root, rec.Version, SchemaVersion)
	}
	return rec, recorded, nil
}

// hasRecords reports whether the directory holds records that predate the
// schema file.
func (s *Store) hasRecords() bool {
	for _, name := range []string{machinesFile, projectsFile} {
		if _, err := os.Stat(s.path(name)); err == nil {
			return true
		}
	}
	return false
}

// writeSchema records the version the directory is now in.
func (s *Store) writeSchema(rec schemaRecord) error {
	rec.Version = SchemaVersion
	rec.UpdatedAt = time.Now().UTC()
	return writeJSONAtomic(s.path(schemaFileName), rec)
}

// migrationID names one conversion, for the bookkeeping in schema.json.
func migrationID(from, to int) string { return fmt.Sprintf("%d-to-%d", from, to) }

// readMachines loads the machine registry, converting it from an older shape if
// that is what is on disk. It reports whether anything was converted, which is
// what makes the transaction write the converted records back.
func readMachines(path string, version int) (map[string]types.MachineRecord, bool, error) {
	if version >= SchemaVersion {
		machines := map[string]types.MachineRecord{}
		if err := readJSON(path, &machines); err != nil {
			return nil, false, err
		}
		return machines, false, nil
	}

	legacy := map[string]machineRecordV1{}
	if err := readJSON(path, &legacy); err != nil {
		return nil, false, fmt.Errorf("%w; it was written by an older version of avar, so avar tried to read it in the older format", err)
	}
	machines := make(map[string]types.MachineRecord, len(legacy))
	for name, rec := range legacy {
		machines[name] = rec.migrate()
	}
	// A v1 directory with no machines still has to be stamped v2, or every
	// later invocation would try to migrate it again.
	return machines, true, nil
}

// machineRecordV1 is the schema-v1 shape of a machine record. It exists only to
// be read: nothing writes it, and the fields it does not mention are unchanged
// between the two versions and decode into the same JSON names.
type machineRecordV1 struct {
	Name      string                    `json:"name"`
	Selector  types.EnvironmentSelector `json:"selector"`
	Kind      types.MachineKind         `json:"kind"`
	ProjectID string                    `json:"project_id,omitempty"`
	// Mounts were host directories, shared at the identical path in the guest —
	// which is what makes the conversion below exact rather than a guess.
	Mounts    []string  `json:"mounts"`
	CreatedAt time.Time `json:"created_at"`
	// VMType was Lima's virtualization mode.
	VMType string `json:"vm_type,omitempty"`
}

// migrate converts a v1 record to the current shape.
//
// Both conversions are lossless because v1 could only ever describe one world.
// A v1 mount was a host directory that Lima shared writable at the identical
// guest path (REQ-6.1), so HostPath and GuestPath are both that directory and
// Writable is true — this is a record of what the machine actually has, not an
// assumption about it. And a v1 record could only have been written by Lima,
// because it was the only backend that existed, so the provider is Lima's.
func (r machineRecordV1) migrate() types.MachineRecord {
	mounts := make([]types.MountSpec, 0, len(r.Mounts))
	for _, dir := range r.Mounts {
		mounts = append(mounts, types.CleanMount(types.MountSpec{
			HostPath:  dir,
			GuestPath: dir,
			Writable:  true,
		}))
	}
	return types.MachineRecord{
		Name:      r.Name,
		Provider:  types.ProviderLima,
		Selector:  r.Selector,
		Kind:      r.Kind,
		ProjectID: r.ProjectID,
		Mounts:    mounts,
		CreatedAt: r.CreatedAt,
		Runtime:   r.VMType,
	}
}
