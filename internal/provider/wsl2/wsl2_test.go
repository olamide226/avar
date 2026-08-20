//go:build windows

// This backend's tests are Windows tests, for the same reason the Lima
// backend's are Unix tests: they assert on Windows paths, and what counts as an
// absolute path is the host's question, not avar's. `C:\Users\ola\code\app` is
// absolute on Windows and a relative path everywhere else, so path/filepath —
// and with it MapProjectPath, MountSpec.Validate and every mount the two plan —
// answers differently off Windows. Verified by cross-compiling this package's
// tests for linux/amd64 and running them under WSL: fourteen fail, all of them
// on that one difference.
//
// Prefixing a drive letter is not available as a fix here the way it was for
// the host-neutral packages. There the fixture was a stand-in for whatever the
// host calls an absolute path; here the Windows path *is* the subject.
//
// What the macOS build claims for this package is therefore that it compiles —
// which is what keeps `avr` linkable while both backends live in one binary —
// not that a backend which cannot run there passes its behaviour tests.

package wsl2

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/olamide226/avar/internal/deps"
	"github.com/olamide226/avar/internal/provider"
	"github.com/olamide226/avar/internal/types"
)

// These are unit tests against a fake wsl.exe. What they assert is the decision:
// which command avar ran, with which arguments, in which order, and what it did
// when the answer was not what it expected. That is the whole of this package —
// every line of it turns an avar concept into a wsl.exe invocation — and it is
// checkable without registering a distribution, downloading a root filesystem,
// or having WSL at all, which is what makes it checkable on a Mac too.
//
// The fake answers in UTF-16LE, because that is what wsl.exe writes to a
// redirected stream. Encoding the fixtures the way the tool encodes its output
// means the decoder is exercised by every test rather than by one.

const (
	testMachine = "avr-ubuntu-24.04-amd64"
	testUser    = "dev"
)

func ubuntuSelector() types.EnvironmentSelector {
	return types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchAMD64}
}

// utf16LE encodes s the way wsl.exe writes to a redirected stream.
func utf16LE(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(units)*2)
	for _, u := range units {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

// healthyFacts is what verifyScript reports from a distribution avar has
// finished setting up.
func healthyFacts() string {
	return "os-id=ubuntu\n" +
		"os-version=24.04\n" +
		`marker={"provider":"wsl2","machine":"` + testMachine + `","user":"` + testUser + `"}` + "\n" +
		"user=" + testUser + "\n" +
		"sudo=yes\n" +
		"mounts=\n"
}

// fakeWSL is a wsl.exe that answers from a registry the test sets up, and
// records everything it was asked to do.
type fakeWSL struct {
	// registered maps a distribution name to its WSL version. It is the
	// world `wsl --list` describes.
	registered map[string]int
	// running is the subset that is currently running.
	running map[string]bool
	// facts is what the verification script reports, per machine. A machine
	// with no entry reports healthyFacts.
	facts map[string]string
	// failOn fails any invocation whose argv contains the key.
	failOn map[string]error
	// installRegisters says whether a successful --install actually registers
	// the distribution. False models an install that reports success and
	// produces nothing.
	installRegisters bool
	// mountsSilentlyFail models the case Provider.SetMounts exists to catch: a
	// mount command that reports success and mounts nothing.
	mountsSilentlyFail bool
	// mounted is what each distribution currently has mounted, guest path to
	// host path, maintained from the scripts avar sends.
	mounted map[string]map[string]string
	// listeners are the TCP ports a guest process is listening on.
	listeners []int

	calls      [][]string
	provisions []string
	scripts    []string
}

func newFakeWSL() *fakeWSL {
	return &fakeWSL{
		registered:       map[string]int{},
		running:          map[string]bool{},
		facts:            map[string]string{},
		failOn:           map[string]error{},
		mounted:          map[string]map[string]string{},
		installRegisters: true,
	}
}

// register adds a distribution as WSL already has it.
func (f *fakeWSL) register(name string, version int, running bool) {
	f.registered[name] = version
	f.running[name] = running
}

func (f *fakeWSL) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if err := f.failure(args); err != nil {
		return nil, err
	}
	out, err := f.respond(args)
	return utf16LE(out), err
}

func (f *fakeWSL) Stream(_ context.Context, w io.Writer, name string, args ...string) error {
	f.calls = append(f.calls, append([]string{name}, args...))
	if err := f.failure(args); err != nil {
		return err
	}
	out, err := f.respond(args)
	if _, writeErr := w.Write(utf16LE(out)); writeErr != nil {
		return writeErr
	}
	return err
}

// exitStatusError is a wsl.exe that ran and exited non-zero, which is a
// different thing from a wsl.exe that could not be run at all. The real runner
// reports the first as *exec.ExitError; the fake cannot construct one of those,
// because it wraps an os.ProcessState only a real process produces, so it
// reports the status the same way the interface asks for it.
//
// The distinction is the point rather than a detail: avar treats a status with
// empty output as "nothing is running", and everything else as a broken WSL. A
// fake that returned a bare error here would let the narrow case be written as
// the broad one and still pass.
type exitStatusError struct {
	code int
	msg  string
}

func (e exitStatusError) Error() string { return e.msg }
func (e exitStatusError) ExitCode() int { return e.code }

// failure reports the error a test asked for on this invocation.
func (f *fakeWSL) failure(args []string) error {
	for _, arg := range args {
		if err, ok := f.failOn[arg]; ok {
			return err
		}
	}
	return nil
}

// respond is the fake wsl.exe itself.
func (f *fakeWSL) respond(args []string) (string, error) {
	switch {
	case has(args, "--list") && has(args, "--running"):
		if len(f.runningNames()) == 0 {
			return "", exitStatusError{code: 1, msg: "there are no running distributions"}
		}
		return strings.Join(f.runningNames(), "\r\n") + "\r\n", nil
	case has(args, "--list") && has(args, "--verbose"):
		return f.verboseList(), nil
	case has(args, "--list"):
		return strings.Join(f.names(), "\r\n") + "\r\n", nil
	case has(args, "--install"):
		if f.installRegisters {
			f.register(argAfter(args, "--name"), 2, true)
		}
		return "Installing: Ubuntu 24.04 LTS\r\nDistribution successfully installed.\r\n", nil
	case has(args, "--export"):
		// The real tool writes a disk image. The fake writes a file, because
		// what avar's own logic depends on is that a file appears where it
		// asked for one and does not when the export fails.
		return "", os.WriteFile(args[2], []byte("virtual disk"), 0o600)
	case has(args, "--import"):
		f.register(args[1], 2, false)
		return "", nil
	case has(args, "--terminate"):
		f.running[argAfter(args, "--terminate")] = false
		return "", nil
	case has(args, "--unregister"):
		name := argAfter(args, "--unregister")
		delete(f.registered, name)
		delete(f.running, name)
		return "", nil
	case has(args, "--exec"):
		return f.guestCommand(args)
	default:
		return "", fmt.Errorf("fake wsl.exe was asked something it does not model: %v", args)
	}
}

// guestCommand answers a command run inside a distribution.
func (f *fakeWSL) guestCommand(args []string) (string, error) {
	machine := argAfter(args, "--distribution")
	f.running[machine] = true

	script, ok := decodeGuestScript(args)
	if !ok {
		// `--exec /bin/true`, the start probe.
		return "", nil
	}
	f.scripts = append(f.scripts, script)

	// The verification script also reads /proc/mounts — that is how it proves
	// the Windows drives are not mounted — so it is recognised first, by the
	// facts it reports rather than by the file it happens to read.
	switch {
	case strings.Contains(script, "os-id="):
		if facts, ok := f.facts[machine]; ok {
			return facts, nil
		}
		return healthyFacts(), nil
	case strings.Contains(script, "/proc/net/tcp"):
		return f.reportListeners(), nil
	case strings.Contains(script, "/proc/mounts"):
		return f.reportMounts(machine), nil
	case strings.Contains(script, "mount -t drvfs") || strings.Contains(script, "umount"):
		f.applyMountScript(machine, script)
		return "", nil
	default:
		f.provisions = append(f.provisions, script)
		return "", nil
	}
}

// mountLine and umountLine recognise the two commands the mount script issues,
// so that the fake's idea of what is mounted comes from what avar actually told
// the guest to do rather than from what the test wished for.
var (
	mountLine  = regexp.MustCompile(`mount -t drvfs '((?:[^']|'\\'')*)' '((?:[^']|'\\'')*)'`)
	umountLine = regexp.MustCompile(`umount '((?:[^']|'\\'')*)'`)
)

// applyMountScript updates the fake's mount table from the script avar sent.
func (f *fakeWSL) applyMountScript(machine, script string) {
	if f.mountsSilentlyFail {
		return
	}
	if f.mounted[machine] == nil {
		f.mounted[machine] = map[string]string{}
	}
	for _, line := range strings.Split(script, "\n") {
		if m := mountLine.FindStringSubmatch(line); m != nil {
			f.mounted[machine][unquoteShell(m[2])] = unquoteShell(m[1])
			continue
		}
		if m := umountLine.FindStringSubmatch(line); m != nil {
			delete(f.mounted[machine], unquoteShell(m[1]))
		}
	}
}

// reportMounts renders the mount table the way /proc/mounts would, escaping
// included, so the parser is exercised rather than bypassed.
func (f *fakeWSL) reportMounts(machine string) string {
	targets := make([]string, 0, len(f.mounted[machine]))
	for target := range f.mounted[machine] {
		targets = append(targets, target)
	}
	sortStrings(targets)

	b := &strings.Builder{}
	for _, target := range targets {
		fmt.Fprintf(b, "%s\t%s\n", escapeFstabField(f.mounted[machine][target]), escapeFstabField(target))
	}
	return b.String()
}

// lastGuestScript returns the most recent script containing want, which is how a
// test asserts on what avar told the guest to do rather than on the fact that
// something was sent.
func (f *fakeWSL) lastGuestScript(t *testing.T, want string) string {
	t.Helper()
	for i := len(f.scripts) - 1; i >= 0; i-- {
		if strings.Contains(f.scripts[i], want) {
			return f.scripts[i]
		}
	}
	t.Fatalf("no guest script contained %q; scripts were:\n%s", want, strings.Join(f.scripts, "\n---\n"))
	return ""
}

// unquoteShell reverses shellQuote.
func unquoteShell(s string) string { return strings.ReplaceAll(s, `'\''`, "'") }

func (f *fakeWSL) names() []string {
	out := make([]string, 0, len(f.registered))
	for name := range f.registered {
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

func (f *fakeWSL) runningNames() []string {
	out := make([]string, 0, len(f.running))
	for name, up := range f.running {
		if up {
			out = append(out, name)
		}
	}
	sortStrings(out)
	return out
}

// verboseList renders the columnar listing, header and all.
func (f *fakeWSL) verboseList() string {
	b := &strings.Builder{}
	b.WriteString("  NAME                   STATE           VERSION\r\n")
	for _, name := range f.names() {
		marker := " "
		state := "Stopped"
		if f.running[name] {
			state = "Running"
		}
		fmt.Fprintf(b, "%s %-22s %-15s %d\r\n", marker, name, state, f.registered[name])
	}
	return b.String()
}

// argvs renders every invocation for a failure message.
func (f *fakeWSL) argvs() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, strings.Join(c[1:], " "))
	}
	return out
}

// ran reports whether some invocation contained all of args, in order.
func (f *fakeWSL) ran(args ...string) bool {
	for _, c := range f.calls {
		if containsSequence(c, args) {
			return true
		}
	}
	return false
}

// ranAny reports whether some invocation contained the argument at all.
func (f *fakeWSL) ranAny(arg string) bool {
	for _, c := range f.calls {
		if has(c, arg) {
			return true
		}
	}
	return false
}

// records is a Records that answers from a fixed set.
type records map[string]types.MachineRecord

func (r records) Machine(name string) (types.MachineRecord, bool, error) {
	rec, ok := r[name]
	return rec, ok, nil
}

func (r records) Machines() ([]types.MachineRecord, error) {
	out := make([]types.MachineRecord, 0, len(r))
	for _, rec := range r {
		out = append(out, rec)
	}
	return out, nil
}

func recorded(names ...string) records {
	out := records{}
	for _, name := range names {
		out[name] = types.MachineRecord{
			Name:     name,
			Provider: types.ProviderWSL2,
			Selector: ubuntuSelector(),
			Kind:     types.KindShared,
		}
	}
	return out
}

// newProvider wires a Provider onto a fake, with directories under the test's
// own temporary tree so nothing reaches a developer's real state.
func newProvider(t *testing.T, f *fakeWSL, recs Records) *Provider {
	t.Helper()

	root := t.TempDir()
	p, err := New(Options{
		WSL:          deps.WSL{Path: `C:\Windows\System32\wsl.exe`},
		Runner:       f,
		Records:      recs,
		DistrosDir:   filepath.Join(root, "distros"),
		LogsDir:      filepath.Join(root, "logs"),
		SnapshotsDir: filepath.Join(root, "snapshots"),
		GuestUser:    testUser,
		HostArch:     types.ArchAMD64,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// --- Creation -------------------------------------------------------------

// REQ-18.7: a new environment is a distribution avar owns outright — its own
// name, its own directory — and is never launched into a first-run wizard.
func TestEnsureMachine_CreatesADistributionAvarOwns_REQ_18_7(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	p := newProvider(t, f, recorded())

	spec := provider.MachineSpec{Name: testMachine, Selector: ubuntuSelector(), Kind: types.KindShared}
	if err := p.EnsureMachine(context.Background(), spec, types.DiscardProgress); err != nil {
		t.Fatalf("EnsureMachine: %v", err)
	}

	if !f.ran("--install", "Ubuntu-24.04") {
		t.Errorf("the environment was not installed from WSL's Ubuntu-24.04 entry: %v", f.argvs())
	}
	if !f.ran("--name", testMachine) {
		t.Errorf("the distribution was not registered under avar's own name: %v", f.argvs())
	}
	if !f.ran("--location", p.installDir(testMachine)) {
		t.Errorf("the distribution was not installed into avar's own directory: %v", f.argvs())
	}
	// Launching runs the distribution's first-run setup, which asks the user
	// for a username and password at a prompt they never asked for.
	if !f.ranAny("--no-launch") {
		t.Errorf("the install would have launched the distribution's setup wizard: %v", f.argvs())
	}
}

// REQ-9.3, PROP-5: WSL mounts every Windows drive into a new distribution and
// appends the whole Windows PATH to the guest's. Both are turned off before the
// environment is ever entered.
func TestEnsureMachine_ClosesTheDoorsWSLOpensByDefault_REQ_9_3(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	p := newProvider(t, f, recorded())

	spec := provider.MachineSpec{Name: testMachine, Selector: ubuntuSelector()}
	if err := p.EnsureMachine(context.Background(), spec, types.DiscardProgress); err != nil {
		t.Fatalf("EnsureMachine: %v", err)
	}
	if len(f.provisions) != 1 {
		t.Fatalf("the guest was provisioned %d times, want once", len(f.provisions))
	}
	script := f.provisions[0]

	for _, want := range []string{
		// Without this the guest can read every file on the host's drives.
		"enabled=false",
		// Without this the Windows PATH is appended to the guest's.
		"appendWindowsPath=false",
		// A developer's tooling assumes a machine where services start.
		"systemd=true",
		// A non-root account matching the host user, with sudo that does not
		// stop to ask for a password the user was never given (REQ-1.4).
		"useradd --create-home --shell /bin/bash '" + testUser + "'",
		"NOPASSWD:ALL",
		"default=" + testUser,
		markerPath,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the provisioning script does not contain %q:\n%s", want, script)
		}
	}
}

// /etc/wsl.conf is read when a distribution starts, and the distribution is
// already running by the time avar has written it. Without the restart the mount
// and interop policy is a file nobody has read.
func TestEnsureMachine_RestartsSoTheGuestConfigurationTakesEffect_REQ_9_3(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	p := newProvider(t, f, recorded())

	spec := provider.MachineSpec{Name: testMachine, Selector: ubuntuSelector()}
	if err := p.EnsureMachine(context.Background(), spec, types.DiscardProgress); err != nil {
		t.Fatalf("EnsureMachine: %v", err)
	}

	provisionAt, terminateAt := -1, -1
	for i, c := range f.calls {
		if _, ok := decodeGuestScript(c); ok && provisionAt < 0 && !strings.Contains(mustDecodeGuestScript(t, c), "os-id=") {
			provisionAt = i
		}
		if has(c, "--terminate") && terminateAt < 0 {
			terminateAt = i
		}
	}
	if provisionAt < 0 || terminateAt < 0 || terminateAt < provisionAt {
		t.Errorf("the distribution was not restarted after being configured: %v", f.argvs())
	}
}

// REQ-17.1: the warm path is the one every invocation pays for. A running
// environment must cost the listings that established it is running, and
// nothing else — no probe, no re-verification, no restart.
func TestEnsureMachine_WarmPathDoesNoWork_REQ_17_1(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	events := 0
	sink := types.ProgressFunc(func(types.ProgressEvent) { events++ })
	spec := provider.MachineSpec{Name: testMachine, Selector: ubuntuSelector()}
	if err := p.EnsureMachine(context.Background(), spec, sink); err != nil {
		t.Fatalf("EnsureMachine: %v", err)
	}

	for _, c := range f.calls {
		if !has(c, "--list") {
			t.Errorf("the warm path ran something other than a listing: %s", strings.Join(c[1:], " "))
		}
	}
	if events != 0 {
		t.Errorf("the warm path emitted %d progress events; a running environment has nothing to report", events)
	}
}

// A registered but stopped distribution has no separate start step: WSL brings
// one up on demand, so running something trivial in it is both the start and the
// proof that it can be entered.
func TestEnsureMachine_StartsAStoppedEnvironment_REQ_1_3(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	p := newProvider(t, f, recorded(testMachine))

	spec := provider.MachineSpec{Name: testMachine, Selector: ubuntuSelector()}
	if err := p.EnsureMachine(context.Background(), spec, types.DiscardProgress); err != nil {
		t.Fatalf("EnsureMachine: %v", err)
	}
	if f.ranAny("--install") {
		t.Errorf("an existing environment was reinstalled: %v", f.argvs())
	}
	if !f.ran("--distribution", testMachine, "--user", "root", "--exec", "/bin/true") {
		t.Errorf("the environment was never started: %v", f.argvs())
	}
}

// --- Refusals -------------------------------------------------------------

// REQ-18.6: WSL runs Linux on the host's own processor. A foreign architecture
// is not a slow request, it is an impossible one, and saying so before a
// gigabyte is downloaded is the difference between an error and a wasted ten
// minutes.
func TestEnsureMachine_RefusesAForeignArchitectureBeforeDownloading_REQ_18_6(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	p := newProvider(t, f, recorded())

	sel := ubuntuSelector()
	sel.Arch = types.ArchARM64
	spec := provider.MachineSpec{Name: "avr-ubuntu-24.04-arm64", Selector: sel}

	err := p.EnsureMachine(context.Background(), spec, types.DiscardProgress)
	if !errors.Is(err, provider.ErrUnsupportedCapability) {
		t.Fatalf("error = %v, want an unsupported-capability error", err)
	}
	if !strings.Contains(err.Error(), string(types.ArchAMD64)) {
		t.Errorf("the message does not list the architecture that is supported:\n%v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("avar ran something before refusing: %v", f.argvs())
	}
}

// An environment in avar's matrix that this backend cannot serve is a real
// difference between backends, not a failure, and the message says what Windows
// does offer.
func TestEnsureMachine_RefusesAnUnservableRelease_REQ_18_6(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	p := newProvider(t, f, recorded())

	sel := types.EnvironmentSelector{Distro: types.DistroUbuntu, Version: "18.04", Arch: types.ArchAMD64}
	err := p.EnsureMachine(context.Background(), provider.MachineSpec{Name: "avr-ubuntu-18.04-amd64", Selector: sel}, types.DiscardProgress)
	if !errors.Is(err, provider.ErrUnsupportedCapability) {
		t.Fatalf("error = %v, want an unsupported-capability error", err)
	}
	if !strings.Contains(err.Error(), "24.04") {
		t.Errorf("the message does not say which releases are offered:\n%v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("avar ran something before refusing: %v", f.argvs())
	}
}

// REQ-18.4, PROP-15: a WSL 1 registration is refused with the exact conversion
// command and is never converted, entered, or repaired.
func TestEnsureMachine_RefusesWSL1WithoutConvertingIt_REQ_18_4(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 1, false)
	p := newProvider(t, f, recorded(testMachine))

	spec := provider.MachineSpec{Name: testMachine, Selector: ubuntuSelector()}
	err := p.EnsureMachine(context.Background(), spec, types.DiscardProgress)
	if err == nil {
		t.Fatal("EnsureMachine succeeded against a WSL 1 registration")
	}
	if !errors.Is(err, deps.ErrWSL1) {
		t.Errorf("error %v does not identify the WSL 1 condition", err)
	}
	if !errors.Is(err, provider.ErrUnsupportedCapability) {
		t.Errorf("error %v is not an unsupported capability, so the command layer reports it as a failure", err)
	}
	if !strings.Contains(err.Error(), "wsl --set-version "+testMachine+" 2") {
		t.Errorf("the message does not carry the exact command to run:\n%v", err)
	}
	if f.ranAny("--set-version") {
		t.Errorf("avar converted the distribution instead of leaving it to the user: %v", f.argvs())
	}
	if f.ranAny("--exec") {
		t.Errorf("avar entered a WSL 1 distribution: %v", f.argvs())
	}
}

// --- Verification and cleanup ---------------------------------------------

// PROP-7, REQ-18.12: an environment that does not verify is removed rather than
// recorded. A distribution imported but not configured has no avar account, no
// marker, and the Windows drives mounted; leaving it registered would let the
// next invocation mistake it for a working one.
func TestEnsureMachine_AnUnverifiableEnvironmentIsRemoved_PROP_7(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		facts string
		want  string
	}{
		{
			name:  "no marker: avar cannot confirm it built this",
			facts: "os-id=ubuntu\nos-version=24.04\nuser=" + testUser + "\nsudo=yes\nmounts=\n",
			want:  "marker",
		},
		{
			name:  "no account: the user would land in a root shell",
			facts: "os-id=ubuntu\nos-version=24.04\nmarker={\"machine\":\"" + testMachine + "\"}\nuser=\nsudo=yes\nmounts=\n",
			want:  testUser,
		},
		{
			name:  "sudo asks for a password the user was never given",
			facts: "os-id=ubuntu\nos-version=24.04\nmarker={\"machine\":\"" + testMachine + "\"}\nuser=" + testUser + "\nsudo=no\nmounts=\n",
			want:  "sudo",
		},
		{
			name:  "the Windows drives are still mounted",
			facts: "os-id=ubuntu\nos-version=24.04\nmarker={\"machine\":\"" + testMachine + "\"}\nuser=" + testUser + "\nsudo=yes\nmounts=/mnt/c,/mnt/d,\n",
			want:  "/mnt/c",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := newFakeWSL()
			f.facts = map[string]string{testMachine: tc.facts}
			p := newProvider(t, f, recorded())

			spec := provider.MachineSpec{Name: testMachine, Selector: ubuntuSelector()}
			err := p.EnsureMachine(context.Background(), spec, types.DiscardProgress)
			if err == nil {
				t.Fatal("EnsureMachine recorded an environment that did not verify")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the message does not say what was wrong (%q):\n%v", tc.want, err)
			}
			if _, still := f.registered[testMachine]; still {
				t.Error("the unverifiable distribution was left registered")
			}
			if _, err := os.Stat(p.installDir(testMachine)); !errors.Is(err, os.ErrNotExist) {
				t.Error("the unverifiable distribution's directory was left behind")
			}
		})
	}
}

// REQ-18.6: WSL's registry names some entries by track rather than by version —
// "Debian" is whatever Debian stable is today. Reading the guest's own
// /etc/os-release turns a registry that has moved on from an environment quietly
// not being what the user asked for into a refusal naming both versions.
func TestEnsureMachine_RefusesAReleaseTheRegistryMovedOn_REQ_18_6(t *testing.T) {
	t.Parallel()

	const machine = "avr-debian-13-amd64"
	f := newFakeWSL()
	f.facts = map[string]string{machine: "os-id=debian\nos-version=14\n" +
		`marker={"machine":"` + machine + `"}` + "\nuser=" + testUser + "\nsudo=yes\nmounts=\n"}
	p := newProvider(t, f, recorded())

	sel := types.EnvironmentSelector{Distro: types.DistroDebian, Version: "13", Arch: types.ArchAMD64}
	err := p.EnsureMachine(context.Background(), provider.MachineSpec{Name: machine, Selector: sel}, types.DiscardProgress)
	if err == nil {
		t.Fatal("EnsureMachine accepted a release the user did not ask for")
	}
	if !strings.Contains(err.Error(), "13") || !strings.Contains(err.Error(), "14") {
		t.Errorf("the message does not name both the release asked for and the one installed:\n%v", err)
	}
	if _, still := f.registered[machine]; still {
		t.Error("the wrong-release distribution was left registered")
	}
}

// A point release of the release avar asked for is the release avar asked for.
func TestEnsureMachine_AcceptsAPointRelease_REQ_18_6(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.facts = map[string]string{testMachine: strings.Replace(healthyFacts(), "os-version=24.04", "os-version=24.04.2", 1)}
	p := newProvider(t, f, recorded())

	spec := provider.MachineSpec{Name: testMachine, Selector: ubuntuSelector()}
	if err := p.EnsureMachine(context.Background(), spec, types.DiscardProgress); err != nil {
		t.Fatalf("24.04.2 was refused for a 24.04 selector: %v", err)
	}
}

// An install that reports success and registers nothing must not be believed.
func TestEnsureMachine_AnInstallThatProducedNothingIsAFailure_PROP_7(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.installRegisters = false
	p := newProvider(t, f, recorded())

	spec := provider.MachineSpec{Name: testMachine, Selector: ubuntuSelector()}
	if err := p.EnsureMachine(context.Background(), spec, types.DiscardProgress); err == nil {
		t.Fatal("EnsureMachine succeeded although nothing was registered")
	}
}

// --- Stop and delete ------------------------------------------------------

// REQ-18.7: `wsl --shutdown` would be one command instead of one per
// environment, and would also kill every distribution the user has open. avar
// terminates its own and never the WSL virtual machine as a whole.
func TestStop_TerminatesOnlyThisEnvironment_REQ_18_7(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	f.register("Ubuntu", 2, true) // the user's own, running alongside
	p := newProvider(t, f, recorded(testMachine))

	if err := p.Stop(context.Background(), testMachine, types.DiscardProgress); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !f.ran("--terminate", testMachine) {
		t.Errorf("the environment was not terminated: %v", f.argvs())
	}
	if f.ranAny("--shutdown") {
		t.Error("avar shut down the whole WSL virtual machine, stopping the user's own distributions with it")
	}
	if !f.running["Ubuntu"] {
		t.Error("the user's own distribution was stopped")
	}
}

// Stopping an environment that is already stopped converges rather than acts.
func TestStop_AlreadyStoppedIsNotAnError_REQ_5_2(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	p := newProvider(t, f, recorded(testMachine))

	if err := p.Stop(context.Background(), testMachine, types.DiscardProgress); err != nil {
		t.Fatalf("Stop on a stopped environment: %v", err)
	}
	if f.ranAny("--terminate") {
		t.Errorf("a stopped environment was terminated anyway: %v", f.argvs())
	}
}

// Stopping something that was never created is distinguishable from stopping
// something that is already stopped.
func TestStop_UnknownEnvironmentIsNotFound_REQ_5_2(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	p := newProvider(t, f, recorded(testMachine))

	err := p.Stop(context.Background(), testMachine, types.DiscardProgress)
	if !errors.Is(err, provider.ErrMachineNotFound) {
		t.Fatalf("error = %v, want ErrMachineNotFound", err)
	}
}

// Delete unregisters first and removes the files second: the other order would
// leave WSL with a registration pointing at nothing.
func TestDelete_UnregistersThenRemovesTheFiles_REQ_10_3(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	p := newProvider(t, f, recorded(testMachine))

	dir := p.installDir(testMachine)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ext4.vhdx"), []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := p.Delete(context.Background(), testMachine); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !f.ran("--unregister", testMachine) {
		t.Errorf("the distribution was not unregistered: %v", f.argvs())
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Error("the distribution's files were left behind, so its disk was not reclaimed")
	}
}

// Delete is how avar cleans up after a failed create and after a crash, where
// the caller cannot know what survived, so it has to be idempotent.
func TestDelete_IsIdempotent_PROP_7(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	p := newProvider(t, f, recorded(testMachine))

	if err := p.Delete(context.Background(), testMachine); err != nil {
		t.Fatalf("deleting an environment that does not exist: %v", err)
	}
	if f.ranAny("--unregister") {
		t.Errorf("avar tried to unregister a distribution WSL does not have: %v", f.argvs())
	}
}

// --- Ownership ------------------------------------------------------------

// PROP-6, REQ-18.7: a distribution the user installed is invisible to avar. Not
// listed and skipped — never reached at all, whatever avar is asked to do.
func TestOperations_RefuseADistributionAvarDidNotCreate_PROP_6(t *testing.T) {
	t.Parallel()

	// The names a Windows developer actually has, plus one shaped like avar's
	// but absent from its records.
	for _, machine := range []string{"Ubuntu", "docker-desktop", "kali-linux", testMachine} {
		t.Run(machine, func(t *testing.T) {
			t.Parallel()

			f := newFakeWSL()
			f.register(machine, 2, true)
			// Records deliberately empty: even avar's own name is not enough.
			p := newProvider(t, f, recorded())

			if err := p.Stop(context.Background(), machine, types.DiscardProgress); !errors.Is(err, provider.ErrNotOwned) {
				t.Errorf("Stop(%s) = %v, want ErrNotOwned", machine, err)
			}
			// Delete checks the name prefix only, because it is also the
			// cleanup path for a create whose record does not exist yet, so
			// avar's own name does reach it. No other name may.
			if machine != testMachine {
				if err := p.Delete(context.Background(), machine); !errors.Is(err, provider.ErrNotOwned) {
					t.Errorf("Delete(%s) = %v, want ErrNotOwned", machine, err)
				}
				if f.ranAny("--unregister") {
					t.Errorf("avar unregistered a distribution it does not own: %v", f.argvs())
				}
			}
			if f.ranAny("--terminate") {
				t.Errorf("avar stopped a distribution it does not own: %v", f.argvs())
			}
		})
	}
}

// PROP-6: Status filters rather than annotates. A developer's own Ubuntu does
// not appear at all, because `avr stop --all` is built directly on this.
func TestStatus_ShowsOnlyAvarsOwnEnvironments_PROP_6(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, true)
	f.register("Ubuntu", 2, true)
	f.register("docker-desktop", 2, false)
	f.register("avr-fedora-43-amd64", 2, false) // avar's name, no record
	p := newProvider(t, f, recorded(testMachine))

	got, err := p.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(got) != 1 || got[0].Name != testMachine {
		t.Fatalf("Status = %v, want only the recorded avar environment", got)
	}
	if got[0].Provider != types.ProviderWSL2 {
		t.Errorf("Provider = %q, want %q", got[0].Provider, types.ProviderWSL2)
	}
	if got[0].State != types.StateRunning {
		t.Errorf("State = %q, want running", got[0].State)
	}
	if got[0].Runtime != "wsl2" {
		t.Errorf("Runtime = %q, want the WSL version", got[0].Runtime)
	}
}

// An avar environment somehow registered as WSL 1 is reported as broken rather
// than as something to enter.
func TestStatus_ReportsAWSL1EnvironmentAsBroken_PROP_15(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 1, false)
	p := newProvider(t, f, recorded(testMachine))

	got, err := p.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(got) != 1 || got[0].State != types.StateBroken {
		t.Fatalf("Status = %v, want the WSL 1 environment reported as broken", got)
	}
}

// Nothing is created before the ownership check, so an unowned name produces an
// error and no side effect at all.
func TestEnsureMachine_RefusesAForeignName_PROP_6(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	p := newProvider(t, f, recorded())

	spec := provider.MachineSpec{Name: "Ubuntu", Selector: ubuntuSelector()}
	if err := p.EnsureMachine(context.Background(), spec, types.DiscardProgress); !errors.Is(err, provider.ErrNotOwned) {
		t.Fatalf("error = %v, want ErrNotOwned", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("avar ran something for a name it does not own: %v", f.argvs())
	}
}

// A spec addressed to another backend is refused rather than built.
func TestEnsureMachine_RefusesASpecForAnotherBackend_REQ_18_14(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	p := newProvider(t, f, recorded())

	spec := provider.MachineSpec{Name: testMachine, Provider: types.ProviderLima, Selector: ubuntuSelector()}
	if err := p.EnsureMachine(context.Background(), spec, types.DiscardProgress); err == nil {
		t.Fatal("a spec addressed to the Lima backend was built by the WSL one")
	}
	if len(f.calls) != 0 {
		t.Errorf("avar ran something for a spec it should have refused: %v", f.argvs())
	}
}

func TestID_IsTheWSLBackend_REQ_18_14(t *testing.T) {
	t.Parallel()

	if got := newProvider(t, newFakeWSL(), nil).ID(); got != types.ProviderWSL2 {
		t.Errorf("ID() = %q, want %q", got, types.ProviderWSL2)
	}
}

// --- Listing --------------------------------------------------------------

// The verbose listing's header and STATE column are translated; its VERSION
// column is a digit. Reading the digit and nothing else is what makes avar work
// on a Windows that is not in English.
func TestParseVerboseVersions_IgnoresTranslatedColumns_REQ_18_2(t *testing.T) {
	t.Parallel()

	// A French Windows, as `wsl --list --verbose` renders it there.
	const french = "  NOM                     ÉTAT            VERSION\r\n" +
		"* Ubuntu                  En cours d'exécution  2\r\n" +
		"  avr-ubuntu-24.04-amd64  Arrêté          2\r\n" +
		"  legacy-distro           Arrêté          1\r\n"

	got := parseVerboseVersions(french, []string{"Ubuntu", "avr-ubuntu-24.04-amd64", "legacy-distro"})
	want := map[string]int{"Ubuntu": 2, "avr-ubuntu-24.04-amd64": 2, "legacy-distro": 1}
	for name, version := range want {
		if got[name] != version {
			t.Errorf("version of %s = %d, want %d (parsed %v)", name, got[name], version, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("parsed %v, want exactly the three data rows and no header", got)
	}
}

// The default-distribution marker is in the first column, so a name is matched
// as a field rather than by position.
func TestParseQuietList_ReadsNamesAndNothingElse(t *testing.T) {
	t.Parallel()

	got := parseQuietList("Ubuntu\r\n\r\navr-ubuntu-24.04-amd64\r\n")
	if len(got) != 2 || got[0] != "Ubuntu" || got[1] != "avr-ubuntu-24.04-amd64" {
		t.Errorf("parseQuietList = %q, want the two names with the padding dropped", got)
	}
}

// WSL reports "there are no running distributions" by failing, which is an
// answer and not an error: nothing is running.
func TestListRunning_NoRunningDistributionsIsNotAFailure(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	p := newProvider(t, f, recorded(testMachine))

	got, err := p.Status(context.Background())
	if err != nil {
		t.Fatalf("Status with nothing running: %v", err)
	}
	if len(got) != 1 || got[0].State != types.StateStopped {
		t.Fatalf("Status = %v, want the environment reported as stopped", got)
	}
}

// The empty-set answer is recognised narrowly, so a WSL that is actually broken
// reports as broken rather than as idle.
//
// The two are indistinguishable from the exit status alone, which is why the
// wider reading is tempting and wrong: a failure to run wsl.exe at all reports
// no status, and turning that into "nothing is running" would send a user whose
// service is stopped to look at their environment instead of their WSL.
func TestListRunning_ABrokenWSLIsNotReportedAsIdle(t *testing.T) {
	t.Parallel()

	f := newFakeWSL()
	f.register(testMachine, 2, false)
	f.failOn = map[string]error{"--running": errors.New("the WSL service could not be reached")}
	p := newProvider(t, f, recorded(testMachine))

	_, err := p.Status(context.Background())
	if err == nil {
		t.Fatal("Status reported success on a WSL that could not answer, want the failure surfaced")
	}
	if !strings.Contains(err.Error(), "the WSL service could not be reached") {
		t.Errorf("error = %v, want it to carry what actually went wrong", err)
	}
}

// --- Guest account --------------------------------------------------------

// REQ-1.4: the guest account is a non-root one belonging to the person at the
// keyboard. A Windows account name is not a Linux user name, and the mapping has
// to produce a valid one from whatever the user's is.
func TestGuestUserName_MapsAWindowsAccountOntoALinuxOne_REQ_1_4(t *testing.T) {
	t.Parallel()

	tests := []struct {
		windows string
		want    string
	}{
		{windows: "ola", want: "ola"},
		{windows: "Ola", want: "ola"},
		{windows: "Ola Adebayo", want: "ola-adebayo"},
		{windows: `CONTOSO\ola.adebayo`, want: "ola-adebayo"},
		{windows: "ola@example.com", want: "ola-example-com"},
		// Nothing usable survives, and a fixed name is better than a failure:
		// what REQ-1.4 asks for is a non-root account, not a name that
		// round-trips.
		{windows: "管理者", want: guestUserFallback},
		{windows: "", want: guestUserFallback},
		{windows: "   ", want: guestUserFallback},
		// A Linux user name may not begin with a digit.
		{windows: "1ola", want: guestUserFallback},
		// avar's whole promise here is that the account is not root.
		{windows: "root", want: guestUserFallback},
		{windows: "Root", want: guestUserFallback},
		{windows: strings.Repeat("a", 40), want: strings.Repeat("a", guestUserMaxLen)},
	}

	for _, tc := range tests {
		t.Run(tc.windows, func(t *testing.T) {
			t.Parallel()

			got := GuestUserName(tc.windows)
			if got != tc.want {
				t.Errorf("GuestUserName(%q) = %q, want %q", tc.windows, got, tc.want)
			}
			if err := validateGuestUser(got); err != nil {
				t.Errorf("GuestUserName(%q) produced an unusable account name: %v", tc.windows, err)
			}
		})
	}
}

// --- Construction ---------------------------------------------------------

// A Provider missing a collaborator fails at construction rather than at the
// first invocation that needed it.
func TestNew_RequiresItsCollaborators(t *testing.T) {
	t.Parallel()

	full := Options{
		WSL:          deps.WSL{Path: `C:\Windows\System32\wsl.exe`},
		Runner:       newFakeWSL(),
		DistrosDir:   `C:\state\distros`,
		LogsDir:      `C:\state\logs`,
		SnapshotsDir: `C:\state\snapshots`,
		GuestUser:    testUser,
		HostArch:     types.ArchAMD64,
	}

	tests := map[string]func(*Options){
		"no wsl.exe":       func(o *Options) { o.WSL.Path = "" },
		"no runner":        func(o *Options) { o.Runner = nil },
		"no distros dir":   func(o *Options) { o.DistrosDir = "" },
		"no snapshots dir": func(o *Options) { o.SnapshotsDir = "" },
		"no logs dir":      func(o *Options) { o.LogsDir = "" },
		"root as the guest account": func(o *Options) {
			o.GuestUser = "root"
		},
	}
	for name, break_ := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := full
			break_(&opts)
			if _, err := New(opts); err == nil {
				t.Errorf("New succeeded with %s", name)
			}
		})
	}

	if _, err := New(full); err != nil {
		t.Errorf("New with everything supplied: %v", err)
	}
}

// --- Helpers --------------------------------------------------------------

func has(argv []string, arg string) bool {
	for _, got := range argv {
		if got == arg {
			return true
		}
	}
	return false
}

// containsSequence reports whether want appears in argv in order, contiguously.
func containsSequence(argv, want []string) bool {
	if len(want) == 0 || len(want) > len(argv) {
		return false
	}
	for i := 0; i+len(want) <= len(argv); i++ {
		match := true
		for j, w := range want {
			if argv[i+j] != w {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// argAfter returns the argument following flag.
func argAfter(argv []string, flag string) string {
	for i, got := range argv {
		if got == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// decodeGuestScript recovers the script avar sent into the guest.
//
// It is the inverse of guestShellArgv, and having it here rather than only in
// the production code is what lets a test assert on what the guest was actually
// told to do rather than on the fact that something base64-shaped was sent.
func decodeGuestScript(argv []string) (string, bool) {
	for _, arg := range argv {
		const prefix = "echo "
		if !strings.HasPrefix(arg, prefix) {
			continue
		}
		encoded, _, ok := strings.Cut(strings.TrimPrefix(arg, prefix), " ")
		if !ok {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			continue
		}
		return string(decoded), true
	}
	return "", false
}

func mustDecodeGuestScript(t *testing.T, argv []string) string {
	t.Helper()
	script, ok := decodeGuestScript(argv)
	if !ok {
		t.Fatalf("no guest script in %v", argv)
	}
	return script
}

// sortStrings keeps the fake's listings deterministic.
func sortStrings(in []string) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j] < in[j-1]; j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
}

// reportListeners renders the listening sockets the way /proc/net/tcp writes
// them — hexadecimal, on the wildcard address — so the parser is exercised
// rather than bypassed.
func (f *fakeWSL) reportListeners() string {
	b := &strings.Builder{}
	for _, port := range f.listeners {
		fmt.Fprintf(b, "00000000:%04X\n", port)
	}
	return b.String()
}
