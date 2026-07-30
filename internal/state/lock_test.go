package state

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olamide226/avar/internal/types"
)

// helperMachineEnv makes a test binary run as a child "avr invocation": when it
// is set, TestLockHelperProcess_RecordsAMachine records that machine in the
// store at $AVR_HOME instead of skipping.
const helperMachineEnv = "AVR_TEST_HELPER_MACHINE"

// Two invocations must not be able to hold the state lock at once: the second
// waits for the first to finish and then sees its result.
func TestLock_SecondAcquirerWaitsForTheHolder_REQ_17_5(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), lockFile)

	first, err := acquireLock(path, time.Second)
	if err != nil {
		t.Fatalf("acquire the lock: %v", err)
	}

	acquired := make(chan error, 1)
	go func() {
		second, err := acquireLock(path, 10*time.Second)
		if err == nil {
			err = second.release()
		}
		acquired <- err
	}()

	select {
	case err := <-acquired:
		t.Fatalf("a second acquirer took the lock while it was held (err = %v)", err)
	case <-time.After(200 * time.Millisecond):
	}

	if err := first.release(); err != nil {
		t.Fatalf("release the lock: %v", err)
	}
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("the waiting acquirer failed after the lock was released: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting acquirer never got the lock after it was released")
	}
}

// Waiting is bounded: a stale lock must fail with an explanation, not hang a
// terminal forever.
func TestLock_BoundedWaitTimesOutWithAdvice_REQ_17_5(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), lockFile)
	held, err := acquireLock(path, time.Second)
	if err != nil {
		t.Fatalf("acquire the lock: %v", err)
	}
	t.Cleanup(func() { _ = held.release() })

	start := time.Now()
	if _, err := acquireLock(path, 50*time.Millisecond); err == nil {
		t.Fatal("acquireLock succeeded although the lock was held")
	} else {
		if !errors.Is(err, ErrLockTimeout) {
			t.Errorf("error %v is not an ErrLockTimeout, so callers cannot distinguish contention from failure", err)
		}
		for _, want := range []string{path, "avr"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("waited %s for a 50ms bounded wait", waited)
	}
}

// A failed mutation must not leave the lock behind: the next invocation would
// look wedged.
func TestStore_LockIsReleasedAfterAFailedMutation_REQ_17_5(t *testing.T) {
	t.Parallel()

	// A tight timeout so a leaked lock fails fast instead of stalling the test.
	st := newTestStore(t, WithLockTimeout(200*time.Millisecond))

	boom := errors.New("boom")
	if err := st.Update(func(*Tx) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("Update error = %v, want %v", err, boom)
	}
	if err := st.PutMachine(sharedMachine("avr-ubuntu-24.04-arm64")); err != nil {
		t.Fatalf("the store is unusable after a failed mutation: %v", err)
	}

	// The same must hold when the callback panics part-way through.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not propagate")
			}
		}()
		_ = st.Update(func(tx *Tx) error {
			panic("interrupted")
		})
	}()
	if err := st.PutMachine(sharedMachine("avr-debian-12-arm64")); err != nil {
		t.Fatalf("the store is unusable after a panicking mutation: %v", err)
	}

	machines, err := st.Machines()
	if err != nil {
		t.Fatalf("Machines: %v", err)
	}
	if len(machines) != 2 {
		t.Errorf("Machines() = %v, want both records", machines)
	}
}

// The lock has to serialise real invocations, not just goroutines: separate
// processes racing on the same state directory must not lose each other's
// records.
func TestLock_SerializesMutationsAcrossProcesses_REQ_17_5(t *testing.T) {
	t.Parallel()

	st := newTestStore(t)

	const children = 4
	var wg sync.WaitGroup
	failures := make(chan error, children+1)

	for i := 0; i < children; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess_RecordsAMachine$")
			cmd.Env = append(os.Environ(),
				HomeEnv+"="+st.Root(),
				helperMachineEnv+"=avr-child-"+strconv.Itoa(i),
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				failures <- errors.New("child " + strconv.Itoa(i) + ": " + err.Error() + ": " + string(out))
			}
		}(i)
	}
	// The parent races the children for the same file.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := st.PutMachine(sharedMachine("avr-parent")); err != nil {
			failures <- err
		}
	}()

	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}

	machines, err := st.Machines()
	if err != nil {
		t.Fatalf("Machines: %v", err)
	}
	if len(machines) != children+1 {
		t.Errorf("Machines() = %d records (%v), want %d — a process overwrote another's write",
			len(machines), machines, children+1)
	}
}

// TestLockHelperProcess_RecordsAMachine is not a test of its own: it is the
// body of the child process spawned by
// TestLock_SerializesMutationsAcrossProcesses. It opens the store the same way
// the real CLI does, from $AVR_HOME.
func TestLockHelperProcess_RecordsAMachine(t *testing.T) {
	name := os.Getenv(helperMachineEnv)
	if name == "" {
		t.Skip("helper process for the cross-process lock test; runs only when " + helperMachineEnv + " is set")
	}

	st, err := OpenDefault(WithLockTimeout(30 * time.Second))
	if err != nil {
		t.Fatalf("OpenDefault: %v", err)
	}
	rec := types.MachineRecord{
		Name:     name,
		Selector: types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64},
		Kind:     types.KindShared,
	}
	if err := st.PutMachine(rec); err != nil {
		t.Fatalf("PutMachine(%s): %v", name, err)
	}
}
