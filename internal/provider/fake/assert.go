package fake

import (
	"fmt"
	"sort"
	"strings"

	"github.com/olamide226/avar/internal/types"
)

// TB is the part of *testing.T the assertion helpers need.
//
// It is a local interface rather than testing.TB because testing.TB cannot be
// implemented outside the testing package, and these helpers are themselves
// tested with a recording double. *testing.T and *testing.B satisfy it.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// AssertOps fails unless the Fake was asked to do exactly want, in exactly that
// order. This is the assertion most flow tests want: it catches a missing call,
// a stray call, and a wrong order in one line.
func (f *Fake) AssertOps(t TB, want ...Op) {
	t.Helper()
	got := f.Ops()
	if !equalOps(got, want) {
		t.Fatalf("provider call sequence:\n got: %s\nwant: %s\ncalls:\n%s",
			formatOps(got), formatOps(want), f.Transcript())
	}
}

// AssertOpsInOrder fails unless want appears as a subsequence of what happened —
// every listed operation occurred, in the listed relative order, with anything
// else allowed in between. Use it when a flow's exact call count is not the
// point but its ordering is (ensure before mount before shell).
func (f *Fake) AssertOpsInOrder(t TB, want ...Op) {
	t.Helper()
	got := f.Ops()
	i := 0
	for _, op := range got {
		if i < len(want) && op == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("provider calls did not contain %s in order\n got: %s\ncalls:\n%s",
			formatOps(want), formatOps(got), f.Transcript())
	}
}

// AssertCalled fails unless the operation happened at least once, and returns
// its most recent call so the test can assert on the arguments.
func (f *Fake) AssertCalled(t TB, op Op) Call {
	t.Helper()
	call, ok := f.LastCall(op)
	if !ok {
		t.Fatalf("expected %s to be called, but it was not\ncalls:\n%s", op, f.Transcript())
		return Call{}
	}
	return call
}

// AssertNotCalled fails if the operation happened at all, which is how a test
// proves a warm path did no work — no restart, no provisioning.
func (f *Fake) AssertNotCalled(t TB, op Op) {
	t.Helper()
	if n := f.Count(op); n != 0 {
		t.Fatalf("expected %s never to be called, but it was called %d time(s)\ncalls:\n%s", op, n, f.Transcript())
	}
}

// AssertCallCount fails unless the operation happened exactly want times.
func (f *Fake) AssertCallCount(t TB, op Op, want int) {
	t.Helper()
	if got := f.Count(op); got != want {
		t.Fatalf("expected %s to be called %d time(s), got %d\ncalls:\n%s", op, want, got, f.Transcript())
	}
}

// AssertMachineState fails unless the machine is in the expected state, which is
// how a test proves a flow's effect rather than merely its calls.
func (f *Fake) AssertMachineState(t TB, name string, want types.MachineState) {
	t.Helper()
	if got := f.MachineState(name); got != want {
		t.Fatalf("machine %s: state is %q, want %q", name, got, want)
	}
}

// AssertMounts fails unless the machine shares exactly the given host
// directories. Order is irrelevant.
//
// It asserts on host paths because that is what a flow test is about: which of
// the user's projects the machine can reach. Where each one lands inside the
// guest is the provider's answer, and asserting it here would restate
// MapProjectPath rather than check the flow — AssertMountSpecs is for the tests
// that are genuinely about the mapping.
func (f *Fake) AssertMounts(t TB, name string, want ...string) {
	t.Helper()
	got := types.MountHostPaths(f.Mounts(name))
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	if !equalStrings(got, sorted) {
		t.Fatalf("machine %s: mounts are %v, want %v", name, got, sorted)
	}
}

// AssertMountSpecs fails unless the machine has exactly these shares, host path
// and guest path both.
func (f *Fake) AssertMountSpecs(t TB, name string, want ...types.MountSpec) {
	t.Helper()
	normalized, err := types.NormalizeMounts(want)
	if err != nil {
		t.Fatalf("machine %s: the expected mount set is not a valid one: %v", name, err)
		return
	}
	if got := f.Mounts(name); !types.EqualMounts(got, normalized) {
		t.Fatalf("machine %s: mounts are %v, want %v", name, got, normalized)
	}
}

// AssertRestarts fails unless the machine was cycled exactly want times
// (REQ-6.4: adding a project restarts once, revisiting it does not restart).
func (f *Fake) AssertRestarts(t TB, name string, want int) {
	t.Helper()
	if got := f.Restarts(name); got != want {
		t.Fatalf("machine %s: restarted %d time(s), want %d", name, got, want)
	}
}

// AssertProgressKinds fails unless exactly these kinds of progress event were
// emitted, in this order. It asserts on kinds rather than message text so a
// reworded message does not break a flow test.
func (f *Fake) AssertProgressKinds(t TB, want ...types.ProgressKind) {
	t.Helper()
	events := f.Events()
	got := make([]types.ProgressKind, len(events))
	for i, e := range events {
		got[i] = e.Kind
	}
	if len(got) != len(want) {
		t.Fatalf("progress events: got %v, want %v", got, want)
		return
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("progress events: got %v, want %v", got, want)
			return
		}
	}
}

// Transcript renders the recorded calls one per line, for failure messages and
// for debugging a flow that did something unexpected.
func (f *Fake) Transcript() string {
	calls := f.Calls()
	if len(calls) == 0 {
		return "  (no provider calls)"
	}
	var b strings.Builder
	for i, c := range calls {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, c)
	}
	return strings.TrimRight(b.String(), "\n")
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

func equalOps(a, b []Op) bool {
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

func formatOps(ops []Op) string {
	if len(ops) == 0 {
		return "[]"
	}
	parts := make([]string, len(ops))
	for i, op := range ops {
		parts[i] = string(op)
	}
	return "[" + strings.Join(parts, " -> ") + "]"
}
