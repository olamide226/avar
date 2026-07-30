package lima

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/olamide226/avar/internal/deps"
	"github.com/olamide226/avar/internal/types"
)

// Lima is not installed where these tests run, and provisioning a virtual
// machine is not a unit test in any case. Every limactl interaction therefore
// goes through fakeRunner, which replays recorded output and records the argv it
// was given.
//
// The argv is the real contract with the backend, so the tests assert on it
// rather than on the fact that "something ran": it is what proves avar asks Lima
// for what it means to, and — because every invocation is a program plus
// arguments — that no shell is ever involved.

// invocation is one recorded subprocess.
type invocation struct {
	// Program is the executable path.
	Program string
	// Args is the argv after the program name.
	Args []string
	// Streamed reports whether output was streamed rather than captured, which
	// is how provisioning differs from a query.
	Streamed bool
}

// argv renders the invocation the way an assertion failure should read. The
// generated configuration's temporary path is replaced by a placeholder so that
// an argv assertion is stable across runs.
func (i invocation) argv() string {
	args := make([]string, 0, len(i.Args)+1)
	args = append(args, filepath.Base(i.Program))
	for _, arg := range i.Args {
		if strings.HasSuffix(arg, ".yaml") && filepath.IsAbs(arg) {
			arg = configPathPlaceholder
		}
		args = append(args, arg)
	}
	return strings.Join(args, " ")
}

// fakeRunner is a deps.Runner that answers from a script and records calls.
type fakeRunner struct {
	mu          sync.Mutex
	invocations []invocation

	// listOutputs are returned by successive `limactl list` calls. The last
	// entry repeats, so a test that does not care about later calls supplies
	// one.
	listOutputs [][]byte
	listCalls   int

	// errs fails a limactl subcommand, keyed by the subcommand name.
	errs map[string]error

	// streamOutput is what a streamed invocation writes.
	streamOutput string
	// onStream runs inside Stream, so a test can cancel a context or mutate the
	// script part-way through provisioning.
	onStream func(ctx context.Context) error

	// memsize is what `sysctl -n hw.memsize` reports.
	memsize string
	// sysctlErr fails the memory probe.
	sysctlErr error

	// configWritten is the instance configuration avar pointed limactl at,
	// read at the moment limactl was invoked. It is captured here because the
	// file lives in a temporary directory that avar removes on return, and the
	// content is the whole of what provisioning was asked to build.
	configWritten string
}

// configPathPlaceholder stands in for the temporary path of a generated
// configuration, which changes on every run.
const configPathPlaceholder = "<config.yaml>"

var _ deps.Runner = (*fakeRunner)(nil)

func newFakeRunner() *fakeRunner {
	return &fakeRunner{errs: map[string]error{}, memsize: "17179869184"}
}

// listing programs the JSON `limactl list --json` returns, once per call.
func (r *fakeRunner) listing(outputs ...[]byte) *fakeRunner {
	r.listOutputs = outputs
	return r
}

// failOn programs a limactl subcommand to fail.
func (r *fakeRunner) failOn(subcommand string, err error) *fakeRunner {
	r.errs[subcommand] = err
	return r
}

func (r *fakeRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.record(invocation{Program: name, Args: args, Streamed: false})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.HasSuffix(name, "sysctl") {
		if r.sysctlErr != nil {
			return nil, r.sysctlErr
		}
		return []byte(r.memsize + "\n"), nil
	}
	if len(args) == 0 {
		return nil, nil
	}
	if err := r.errs[args[0]]; err != nil {
		return nil, err
	}
	if args[0] == "list" {
		return r.nextListing(), nil
	}
	return nil, nil
}

func (r *fakeRunner) Stream(ctx context.Context, w io.Writer, name string, args ...string) error {
	r.record(invocation{Program: name, Args: args, Streamed: true})
	r.captureConfig(args)
	if r.streamOutput != "" {
		if _, err := io.WriteString(w, r.streamOutput); err != nil {
			return err
		}
	}
	if r.onStream != nil {
		if err := r.onStream(ctx); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(args) > 0 {
		if err := r.errs[args[0]]; err != nil {
			return err
		}
	}
	return nil
}

// captureConfig reads the generated configuration limactl was pointed at, while
// it still exists.
func (r *fakeRunner) captureConfig(args []string) {
	for _, arg := range args {
		if !strings.HasSuffix(arg, ".yaml") {
			continue
		}
		if data, err := os.ReadFile(arg); err == nil {
			r.mu.Lock()
			r.configWritten = string(data)
			r.mu.Unlock()
		}
	}
}

func (r *fakeRunner) record(inv invocation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inv.Args = append([]string(nil), inv.Args...)
	r.invocations = append(r.invocations, inv)
}

func (r *fakeRunner) nextListing() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.listOutputs) == 0 {
		return nil
	}
	index := r.listCalls
	if index >= len(r.listOutputs) {
		index = len(r.listOutputs) - 1
	}
	r.listCalls++
	return r.listOutputs[index]
}

// calls reports every recorded invocation.
func (r *fakeRunner) calls() []invocation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]invocation(nil), r.invocations...)
}

// argvs renders every recorded invocation for assertion.
func (r *fakeRunner) argvs() []string {
	calls := r.calls()
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.argv())
	}
	return out
}

// limactlArgvs renders only the limactl invocations, which is what most
// assertions are about: the memory probe is not part of the backend contract.
func (r *fakeRunner) limactlArgvs() []string {
	var out []string
	for _, c := range r.calls() {
		if filepath.Base(c.Program) == "limactl" {
			out = append(out, c.argv())
		}
	}
	return out
}

// recordingSink captures progress events in order.
type recordingSink struct {
	mu     sync.Mutex
	events []types.ProgressEvent
}

func (s *recordingSink) Progress(e types.ProgressEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

// kinds reports the event kinds in order, which is what most progress
// assertions are about.
func (s *recordingSink) kinds() []types.ProgressKind {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]types.ProgressKind, 0, len(s.events))
	for _, e := range s.events {
		out = append(out, e.Kind)
	}
	return out
}

// of reports the events of one kind.
func (s *recordingSink) of(kind types.ProgressKind) []types.ProgressEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []types.ProgressEvent
	for _, e := range s.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// fakeRecords is avar's machine registry in memory. Ownership is the avr- prefix
// *and* membership here (REQ-5.4, PROP-6), so a test can prove that an instance
// avar has no record of is untouchable even though its name looks like avar's.
type fakeRecords struct {
	machines map[string]types.MachineRecord
	err      error
}

func newFakeRecords(records ...types.MachineRecord) *fakeRecords {
	byName := make(map[string]types.MachineRecord, len(records))
	for _, record := range records {
		byName[record.Name] = record
	}
	return &fakeRecords{machines: byName}
}

func (r *fakeRecords) Machine(name string) (types.MachineRecord, bool, error) {
	if r.err != nil {
		return types.MachineRecord{}, false, r.err
	}
	record, ok := r.machines[name]
	return record, ok, nil
}

func (r *fakeRecords) Machines() ([]types.MachineRecord, error) {
	if r.err != nil {
		return nil, r.err
	}
	out := make([]types.MachineRecord, 0, len(r.machines))
	for _, record := range r.machines {
		out = append(out, record)
	}
	return out, nil
}

// newTestProvider builds a Provider wired to fakes, with a log directory inside
// the test's temporary directory.
func newTestProvider(t *testing.T, runner *fakeRunner, records Records) *Provider {
	t.Helper()
	p, err := New(Options{
		Lima:    deps.Lima{Path: "/opt/homebrew/bin/limactl"},
		Runner:  runner,
		Records: records,
		LogsDir: filepath.Join(t.TempDir(), "logs"),
		Host:    testHost,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// fixture reads a recorded limactl output.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "limactl", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return data
}
