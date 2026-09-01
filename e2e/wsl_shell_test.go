//go:build e2e && windows

// The WSL half of the suite: avar against a real WSL 2 installation.
//
// These tests register a real Linux distribution, download a real root
// filesystem, and run real commands in it. They keep their state in a directory
// of their own so nothing reaches a developer's own environments, and they
// unregister what they made when the suite ends.

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/olamide226/avar/internal/types"
)

// e2eStateDir is where the suite keeps avar's state, so its distributions and
// disks are its own and are removable in one place.
var (
	stateOnce sync.Once
	stateDir  string
)

// suiteEnv is the environment every real-WSL test runs avar with.
func suiteEnv(t *testing.T) []string {
	t.Helper()

	stateOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			panic("locate the home directory: " + err.Error())
		}
		stateDir = filepath.Join(home, "avr-e2e-state")
	})
	return []string{"AVR_HOME=" + stateDir}
}

// requireWSL skips the suite on a host that has no usable WSL, saying so rather
// than failing with something that looks like an avar bug.
//
// The prerequisite tests in wsl_prereq_test.go cover what avar says on such a
// host; these cover what it does when WSL works, and there is nothing honest for
// them to assert without it.
func requireWSL(t *testing.T) {
	t.Helper()

	out, err := exec.Command("wsl.exe", "--version").Output()
	if err != nil || !strings.Contains(decodeUTF16(out), "WSL") {
		t.Skip("this host has no usable WSL 2; these tests need one. `wsl --install` sets it up")
	}
}

// The cold path: no environment yet, so this registers one, provisions it, and
// runs the command, all within the single invocation (REQ-1.2, REQ-18.6).
//
// It is the slowest test in the suite by a wide margin — it downloads a root
// filesystem — and every test after it measures the warm path it leaves behind.
func TestWSL_ColdStartProvisionsAndRuns_REQ_1_2(t *testing.T) {
	requireWSL(t)
	dir := project(t, "cold")

	stdout, stderr, code := avr(t, dir, suiteEnv(t), "sh", "-c", "echo ready")
	if code != 0 {
		t.Fatalf("avr exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "ready" {
		t.Errorf("stdout = %q, want the guest's own output and nothing of avar's", stdout)
	}
}

// REQ-1.4: the account a session gets is the user's, not root. A shell that
// lands as root is one where every file the user creates in their own project is
// root-owned.
func TestWSL_GuestAccountIsNotRoot_REQ_1_4(t *testing.T) {
	requireWSL(t)
	dir := project(t, "account")

	stdout, stderr, code := avr(t, dir, suiteEnv(t), "sh", "-c", "id -un; id -u")
	if code != 0 {
		t.Fatalf("avr exited %d\nstderr:\n%s", code, stderr)
	}
	lines := strings.Fields(stdout)
	if len(lines) < 2 {
		t.Fatalf("stdout = %q, want a name and a uid", stdout)
	}
	if lines[0] == "root" || lines[1] == "0" {
		t.Errorf("the session runs as root (%s, uid %s)", lines[0], lines[1])
	}

	// And sudo must not stop to ask for a password the user was never given.
	stdout, stderr, code = avr(t, dir, suiteEnv(t), "sudo", "-n", "id", "-u")
	if code != 0 {
		t.Fatalf("passwordless sudo does not work: exit %d\nstderr:\n%s", code, stderr)
	}
	if strings.TrimSpace(stdout) != "0" {
		t.Errorf("sudo id -u = %q, want 0", stdout)
	}
}

// REQ-18.5, PROP-1: the guest starts in the project, at the Linux path avar
// planned — not at a translated Windows path, and not somewhere else entirely.
func TestWSL_StartsInTheProjectDirectory_REQ_18_5(t *testing.T) {
	requireWSL(t)
	dir := project(t, "cwd")

	stdout, stderr, code := avr(t, dir, suiteEnv(t), "pwd")
	if code != 0 {
		t.Fatalf("avr pwd exited %d\nstderr:\n%s", code, stderr)
	}
	got := strings.TrimSpace(stdout)

	if !strings.HasPrefix(got, "/mnt/avr/projects/") {
		t.Errorf("pwd = %q, want the project beneath avar's own guest root", got)
	}
	// The Windows path must not have leaked through: `--cd` translates a
	// Windows path rather than refusing it, so a leak would look like success.
	if strings.Contains(got, `\`) || strings.Contains(strings.ToLower(got), "/mnt/c/") {
		t.Errorf("pwd = %q, which is a translated Windows path rather than the planned one", got)
	}
}

// REQ-18.5: the share is live in both directions and is the same bytes, not a
// copy. This is the whole reason a project is shared rather than synchronised.
func TestWSL_FilesAreVisibleBothWays_REQ_18_5(t *testing.T) {
	requireWSL(t)
	dir := project(t, "visibility")

	// Windows writes, Linux reads.
	fromWindows := filepath.Join(dir, "from-windows.txt")
	if err := os.WriteFile(fromWindows, []byte("written on windows\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := avr(t, dir, suiteEnv(t), "cat", "from-windows.txt")
	if code != 0 {
		t.Fatalf("reading a Windows-written file from Linux: exit %d\nstderr:\n%s", code, stderr)
	}
	if strings.TrimSpace(stdout) != "written on windows" {
		t.Errorf("the guest read %q", stdout)
	}

	// Linux writes, Windows reads.
	if _, stderr, code = avr(t, dir, suiteEnv(t), "sh", "-c", "echo written in linux > from-linux.txt"); code != 0 {
		t.Fatalf("writing from Linux: exit %d\nstderr:\n%s", code, stderr)
	}
	body, err := os.ReadFile(filepath.Join(dir, "from-linux.txt"))
	if err != nil {
		t.Fatalf("Windows cannot see the file Linux wrote: %v", err)
	}
	if strings.TrimSpace(string(body)) != "written in linux" {
		t.Errorf("Windows read %q", body)
	}
}

// REQ-9.3, PROP-5: WSL mounts every Windows drive into a new distribution by
// default. avar turns that off, so the guest reaches the registered project and
// nothing else — not the home directory, not the whole of C:.
func TestWSL_TheGuestCannotReachTheWholeDrive_REQ_9_3(t *testing.T) {
	requireWSL(t)
	dir := project(t, "confinement")

	// The predicate is spelled out here rather than borrowed from the package
	// under test, and that is the point: this test exists to check avar against
	// what WSL actually writes. WSL 2 serves DrvFS over 9P, so /proc/mounts
	// reports the type as `9p` and names DrvFS only in the options. Asking the
	// question with avar's own answer would make this agree with a filter that
	// finds nothing — which is how the first version of it passed while the
	// confinement check saw no mounts at all.
	stdout, stderr, code := avr(t, dir, suiteEnv(t), "sh", "-c",
		`awk '($3 == "drvfs" || index($4, "aname=drvfs") > 0) {print $2}' /proc/mounts`)
	if code != 0 {
		t.Fatalf("listing the guest's mounts: exit %d\nstderr:\n%s", code, stderr)
	}

	// The project this test stands in must be one of them, or the filter is
	// finding nothing and the loop below proves nothing.
	if len(strings.Fields(stdout)) == 0 {
		t.Fatalf("no Windows directory is mounted at all, so this test cannot tell a confined guest from an unreadable one:\n%s", stdout)
	}
	for _, mount := range strings.Fields(stdout) {
		if !strings.HasPrefix(mount, "/mnt/avr/projects/") {
			t.Errorf("the guest has %s mounted, which avar did not register", mount)
		}
	}

	// The specific failure this exists to catch: automount left on gives the
	// guest the user's entire drive, and with it every credential file on it.
	//
	// The question is whether /mnt/c is *mounted*, not whether it exists. The
	// directory survives as an empty mount point from before /etc/wsl.conf took
	// effect, so `test -d` is true on a correctly confined guest and would fail
	// this test for the wrong reason.
	stdout, stderr, code = avr(t, dir, suiteEnv(t), "sh", "-c",
		`awk '$2 == "/mnt/c" {print "mounted"}' /proc/mounts; ls -A /mnt/c 2>/dev/null | head -1`)
	if code != 0 {
		t.Fatalf("checking whether the Windows drive is mounted: exit %d\nstderr:\n%s", code, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("the guest can read the whole of C: (%q), so avar's mount policy is not in effect", strings.TrimSpace(stdout))
	}
}

// REQ-9.1, PROP-4: nothing crosses into the guest that the user did not grant.
// A token sitting in a Windows environment variable must not appear in Linux
// merely because it was set.
func TestWSL_HostEnvironmentDoesNotLeak_PROP_4(t *testing.T) {
	requireWSL(t)
	dir := project(t, "env")

	env := append(suiteEnv(t), "AVR_E2E_SECRET=do-not-cross", "GITHUB_TOKEN=ghp_not_real")
	stdout, stderr, code := avr(t, dir, env, "sh", "-c", `printf '[%s][%s]' "$AVR_E2E_SECRET" "$GITHUB_TOKEN"`)
	if code != 0 {
		t.Fatalf("avr exited %d\nstderr:\n%s", code, stderr)
	}
	if strings.TrimSpace(stdout) != "[][]" {
		t.Errorf("host variables reached the guest: %q", stdout)
	}

	// The other door interop opens: the Windows PATH appended to the guest's,
	// so `python` in Linux can resolve to python.exe on Windows.
	if _, _, code := avr(t, dir, suiteEnv(t), "sh", "-c", `case "$PATH" in *"/mnt/c/"*) exit 0;; esac; exit 1`); code == 0 {
		t.Error("the Windows PATH was appended to the guest's")
	}

	// And what the user does grant must arrive (REQ-12.1).
	stdout, stderr, code = avr(t, dir, env, "--env", "AVR_E2E_SECRET", "sh", "-c", `printf '[%s]' "$AVR_E2E_SECRET"`)
	if code != 0 {
		t.Fatalf("avr --env exited %d\nstderr:\n%s", code, stderr)
	}
	if strings.TrimSpace(stdout) != "[do-not-cross]" {
		t.Errorf("an explicitly forwarded variable did not arrive: %q", stdout)
	}
}

// PROP-3, REQ-18.8: the guest's exit status is avar's exit status. A shell that
// turned a non-zero status into an avar failure would be unusable in a script.
func TestWSL_ExitCodeIsTheGuests_PROP_3(t *testing.T) {
	requireWSL(t)
	dir := project(t, "exit-codes")

	for _, want := range []int{0, 1, 42, 255} {
		_, stderr, code := avr(t, dir, suiteEnv(t), "sh", "-c", "exit "+itoa(want))
		if code != want {
			t.Errorf("avr sh -c 'exit %d' exited %d\nstderr:\n%s", want, code, stderr)
		}
	}
}

// REQ-2.5: the command reaches the guest as the arguments the user typed.
// Nothing on the way re-splits or re-quotes it.
func TestWSL_ArgumentsAreNotResplit_REQ_2_5(t *testing.T) {
	requireWSL(t)
	dir := project(t, "argv")

	stdout, stderr, code := avr(t, dir, suiteEnv(t), "sh", "-c", `printf '<%s>' "$@"`, "sh", "two words", "a'quote", `back\slash`)
	if code != 0 {
		t.Fatalf("avr exited %d\nstderr:\n%s", code, stderr)
	}
	if want := `<two words><a'quote><back\slash>`; strings.TrimSpace(stdout) != want {
		t.Errorf("the guest saw %q, want %q", stdout, want)
	}
}

// REQ-6.4, REQ-17.1: a second project is registered without restarting the
// environment. On Lima this costs a stop, an edit and a start; a DrvFS mount
// takes effect immediately, and both projects must be reachable afterwards.
func TestWSL_ASecondProjectNeedsNoRestart_REQ_6_4(t *testing.T) {
	requireWSL(t)
	first := project(t, "two-projects-a")
	second := project(t, "two-projects-b")

	if _, stderr, code := avr(t, first, suiteEnv(t), "true"); code != 0 {
		t.Fatalf("registering the first project: exit %d\nstderr:\n%s", code, stderr)
	}
	if err := os.WriteFile(filepath.Join(first, "marker-a"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "marker-b"), []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := avr(t, second, suiteEnv(t), "test", "-f", "marker-b"); code != 0 {
		t.Fatalf("the second project is not visible in the guest: exit %d\nstderr:\n%s", code, stderr)
	}
	// The first must still be there: replacing the mount set rather than
	// adding to it is only correct if what was registered stays registered.
	if _, stderr, code := avr(t, first, suiteEnv(t), "test", "-f", "marker-a"); code != 0 {
		t.Fatalf("registering a second project unshared the first: exit %d\nstderr:\n%s", code, stderr)
	}
}

// REQ-4.4, REQ-18.6: WSL runs Linux on the host's own processor and has no
// emulation, so a foreign architecture is impossible rather than slow — and avar
// says so before downloading anything.
func TestWSL_RefusesAForeignArchitecture_REQ_18_6(t *testing.T) {
	requireWSL(t)
	dir := project(t, "foreign-arch")

	foreign := "arm64"
	if types.HostArch() == types.ArchARM64 {
		foreign = "amd64"
	}

	stdout, stderr, code := avr(t, dir, suiteEnv(t), "--arch", foreign, "true")
	if code == 0 {
		t.Fatalf("avr --arch %s succeeded on a %s host", foreign, types.HostArch())
	}
	if !strings.Contains(stdout+stderr, string(types.HostArch())) {
		t.Errorf("the refusal does not name the architecture that is supported:\n%s%s", stdout, stderr)
	}
}

// REQ-5.1: `avr status` shows the environment this project uses, and shows the
// project as one of its registered directories.
func TestWSL_StatusReportsTheEnvironment_REQ_5_1(t *testing.T) {
	requireWSL(t)
	dir := project(t, "status")

	if _, stderr, code := avr(t, dir, suiteEnv(t), "true"); code != 0 {
		t.Fatalf("preparing the environment: exit %d\nstderr:\n%s", code, stderr)
	}

	stdout, stderr, code := avr(t, dir, suiteEnv(t), "status")
	if code != 0 {
		t.Fatalf("avr status exited %d\nstderr:\n%s", code, stderr)
	}
	for _, want := range []string{"ubuntu", "running", dir} {
		if !strings.Contains(strings.ToLower(stdout), strings.ToLower(want)) {
			t.Errorf("status does not mention %q:\n%s", want, stdout)
		}
	}
}

// PROP-6, REQ-18.7: a distribution the user installed themselves is not avar's
// to touch, and `avr stop --all` is where that matters most — it is the command
// most likely to reach further than it should, and reaching too far means
// throwing away work in somebody's own Ubuntu.
//
// The check is behavioural rather than textual. An earlier version of this test
// asserted that status did not *mention* a foreign distribution's name, and
// failed because the user's distribution is called "Ubuntu" while avar's own
// environment is labelled "ubuntu 24.04" — searching prose for a name that is
// also a distribution avar runs cannot tell the two apart. Whether the user's
// environment is still running afterwards can.
func TestWSL_StopAllLeavesTheUsersOwnDistributionsAlone_PROP_6(t *testing.T) {
	requireWSL(t)
	dir := project(t, "stop-all")

	foreign := runningForeignDistribution(t)

	if _, stderr, code := avr(t, dir, suiteEnv(t), "true"); code != 0 {
		t.Fatalf("preparing the environment: exit %d\nstderr:\n%s", code, stderr)
	}
	if _, stderr, code := avr(t, dir, suiteEnv(t), "stop", "--all"); code != 0 {
		t.Fatalf("avr stop --all exited %d\nstderr:\n%s", code, stderr)
	}

	if !runningDistributions(t)[foreign] {
		t.Errorf("avr stop --all stopped %q, which avar did not create", foreign)
	}
}

// requireForeignEnv makes the absence of a user-owned distribution a failure
// rather than a skip. CI sets it, because there the environment is arranged and
// a missing foreign distribution means the arrangement broke — not that the host
// happens to be bare.
const requireForeignEnv = "AVR_E2E_REQUIRE_FOREIGN"

// keepAliveFor is how long the process holding a foreign distribution open runs.
// It only has to outlast one test, which provisions an environment and stops it;
// the process is killed in cleanup either way.
const keepAliveFor = "600"

// runningForeignDistribution returns a distribution avar does not own, held open
// for the duration of the test, and cleans up whatever it started.
//
// The test above is the only end-to-end check of PROP-6 — that `avr stop --all`
// never touches somebody else's environment — and it used to skip whenever no
// foreign distribution happened to be *running*. That is a bad shape for a
// security property: on the machine this was written on it skipped silently, and
// on a CI runner, where nothing of the user's is ever running, it would have
// skipped every time while reporting green.
//
// Holding the distribution open is the part that is easy to get wrong, and
// getting it wrong is worse than the skip was. WSL reaps a distribution once
// nothing is running inside it: measured here, `wsl --exec true` returns
// immediately and WSL stops the distribution about thirty seconds later, with
// avar nowhere near it. A test that started one that way would watch it vanish
// while `avr` was still provisioning, and then report that `avr stop --all` had
// stopped somebody's environment — accusing avar of the exact violation this
// property exists to prevent, on no evidence.
//
// So a real process is started inside the guest and left running, which is what
// keeps the distribution genuinely alive and makes "is it still running
// afterwards" mean what it says. If avar wrongly terminated the distribution,
// that process dies with it and the assertion fails honestly.
func runningForeignDistribution(t *testing.T) string {
	t.Helper()

	// Whether the distribution was already running decides what may be cleaned
	// up afterwards, so it is recorded here rather than re-derived later: by
	// then this function has started a process inside it and the answer would
	// have changed.
	name, alreadyRunning := "", false
	for candidate := range runningDistributions(t) {
		if !strings.HasPrefix(candidate, types.MachineNamePrefix) {
			name, alreadyRunning = candidate, true
			break
		}
	}
	if name == "" {
		for _, candidate := range registeredDistributions(t) {
			if !strings.HasPrefix(candidate, types.MachineNamePrefix) {
				name = candidate
				break
			}
		}
	}

	if name == "" {
		if os.Getenv(requireForeignEnv) != "" {
			t.Fatalf("%s is set, so a distribution of the user's own was expected and none is registered; "+
				"PROP-6 cannot be checked without one, and skipping would report a security property as passing when it was never tested",
				requireForeignEnv)
		}
		t.Skip("no distribution of the user's own is registered, so there is nothing avar could wrongly stop; " +
			"set " + requireForeignEnv + " to make this a failure instead")
		return ""
	}

	// Start() rather than Run(): the point is a process that keeps running.
	keepAlive := exec.Command("wsl.exe", "--distribution", name, "--exec", "sleep", keepAliveFor)
	if err := keepAlive.Start(); err != nil {
		t.Fatalf("holding %q open so PROP-6 can be checked against it: %v", name, err)
	}
	t.Cleanup(func() {
		// The keep-alive is unconditionally this test's to kill: it started it.
		if keepAlive.Process != nil {
			_ = keepAlive.Process.Kill()
		}
		_ = keepAlive.Wait()

		// The distribution is not. Terminating one this test found already
		// running would kill whatever the developer had in it — a build, an
		// editor, an unsaved shell — and doing that here would be absurd: this
		// is the test for "avar never stops a distribution it does not own".
		// It is also the failure docs/lessons.md records from the Lima suite, a
		// suite that damaged the machine it ran on.
		if !alreadyRunning {
			_ = exec.Command("wsl.exe", "--terminate", name).Run()
		}
	})

	if !waitForRunning(t, name) {
		t.Fatalf("%q did not come up, so PROP-6 cannot be checked against it", name)
	}
	return name
}

// waitForRunning polls until WSL reports name as running, or gives up.
//
// Starting a distribution is not instantaneous and there is no synchronous form
// of it, so the alternative to polling is a fixed sleep long enough for the
// slowest machine — which is either flaky or slow, and usually both.
func waitForRunning(t *testing.T, name string) bool {
	t.Helper()

	for range 40 {
		if runningDistributions(t)[name] {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// registeredDistributions is everything WSL has, running or not.
func registeredDistributions(t *testing.T) []string {
	t.Helper()

	out, err := exec.Command("wsl.exe", "--list", "--quiet").Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(decodeUTF16(out), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// runningDistributions is what WSL currently has running, read directly rather
// than through avar.
func runningDistributions(t *testing.T) map[string]bool {
	t.Helper()

	out, err := exec.Command("wsl.exe", "--list", "--running", "--quiet").Output()
	if err != nil {
		return map[string]bool{}
	}
	names := map[string]bool{}
	for _, line := range strings.Split(decodeUTF16(out), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names[name] = true
		}
	}
	return names
}

// REQ-10.3, PROP-10, PROP-16: removing the environment reclaims what it holds
// and leaves the host's project files exactly as they were.
//
// It runs last — the z_ prefix orders it after the rest, as it does on the Lima
// side — because it destroys what every test before it was using.
func TestWSL_ZDestroyLeavesHostFilesIntact_REQ_10_3(t *testing.T) {
	requireWSL(t)
	dir := project(t, "destroy")

	if _, stderr, code := avr(t, dir, suiteEnv(t), "true"); code != 0 {
		t.Fatalf("preparing the environment: exit %d\nstderr:\n%s", code, stderr)
	}
	keep := filepath.Join(dir, "keep-me.txt")
	if err := os.WriteFile(keep, []byte("host file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := avr(t, dir, suiteEnv(t), "destroy", "--all", "--yes")
	if code != 0 {
		t.Fatalf("avr destroy --all --yes exited %d\nstderr:\n%s", code, stderr)
	}

	// The distribution is gone from WSL itself, not merely from avar's records.
	for name := range realDistributions(t) {
		if strings.HasPrefix(name, "avr-") {
			t.Errorf("%q is still registered after destroy --all", name)
		}
	}
	// And the project is untouched: a project is shared into the guest, never
	// copied into it, so destroying an environment cannot lose a user's work.
	if body, err := os.ReadFile(keep); err != nil || strings.TrimSpace(string(body)) != "host file" {
		t.Errorf("the host's project file did not survive: %v %q", err, body)
	}
}

// hadScheduledTask records whether the idle-check task existed before the suite
// ran, so cleanup removes one the suite created and leaves alone one the user
// already had. A developer running these tests on their own machine has avar
// installed on it, and deleting their working scheduled task would be the suite
// breaking the thing it is testing.
var hadScheduledTask = scheduledTaskExists()

func scheduledTaskExists() bool {
	cmd := exec.Command("schtasks", "/Query", "/TN", "avar-idle-check")
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Run() == nil
}

// cleanupBackend removes what the suite left behind.
//
// The tests destroy their environment themselves, so the unregister is the
// backstop for a run that was interrupted — a registered distribution nobody
// knows about is several gigabytes that no later run will reclaim. It only ever
// touches names carrying avar's own prefix.
//
// The scheduled task needs removing too, and that is easy to miss: avar
// registers it the first time it creates an environment, pointing at the binary
// that did so — which for this suite is a temporary build that is deleted
// minutes later. Left behind, it is a Task Scheduler entry firing every ten
// minutes at a path that no longer exists.
func cleanupBackend() {
	if out, err := exec.Command("wsl.exe", "--list", "--quiet").Output(); err == nil {
		for _, line := range strings.Split(decodeUTF16(out), "\n") {
			name := strings.TrimSpace(line)
			if strings.HasPrefix(name, "avr-") {
				_ = exec.Command("wsl.exe", "--unregister", name).Run()
			}
		}
	}
	if !hadScheduledTask {
		_ = exec.Command("schtasks", "/Delete", "/TN", "avar-idle-check", "/F").Run()
	}
	if stateDir != "" {
		_ = os.RemoveAll(stateDir)
	}
	if home, err := os.UserHomeDir(); err == nil {
		// The per-test directories remove themselves; this is the empty parent
		// they shared.
		_ = os.Remove(filepath.Join(home, "avr-e2e"))
	}
}

// decodeUTF16 reads what wsl.exe writes to a redirected stream.
//
// The suite decodes it itself rather than calling avar's own decoder, because
// these tests exist to check avar against reality: reading the evidence with the
// code under test would make a decoder bug invisible in exactly the tests meant
// to catch it.
//
// Independent, however, must not mean carrying the defect the production
// decoder was fixed for. Requiring every odd byte to be NUL holds only while the
// text is entirely Latin-1: 名 is U+540D, which encodes to 8D 54, so one
// character outside it breaks the run for the whole buffer. Here that would mean
// a distribution named in a non-Latin script becoming invisible to
// runningDistributions and registeredDistributions — and the PROP-6 check
// skipping on a machine that does have a foreign distribution. So this counts
// NULs by parity, the way looksLikeUTF16LE does, for the same reason.
func decodeUTF16(b []byte) string {
	if len(b) < 2 || len(b)%2 != 0 {
		return string(b)
	}
	var odd, even int
	for i := 0; i+1 < len(b); i += 2 {
		if b[i] == 0 {
			even++
		}
		if b[i+1] == 0 {
			odd++
		}
	}
	// Three in ten is far below what any real wsl.exe listing produces — the
	// CRLF ending every line is itself two Latin-1 code units — and far above
	// the zero that UTF-8 output produces. odd > even separates LE from BE.
	if pairs := len(b) / 2; odd*10 < pairs*3 || odd <= even {
		return string(b)
	}
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		units = append(units, uint16(b[i+1])<<8|uint16(b[i]))
	}
	return string(utf16.Decode(units))
}

// itoa keeps the exit-code table readable without a strconv import at each use.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
