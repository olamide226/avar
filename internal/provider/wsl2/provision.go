package wsl2

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/olamide226/avar/internal/types"
)

// Provisioning a WSL distribution is four steps, and each of them exists
// because of something the default would otherwise do.
//
//  1. Install under avar's own name and in avar's own directory, without
//     launching it. Launching runs the distribution's first-run setup, which
//     asks the user for a username and password at a prompt they did not ask
//     for; --no-launch means avar creates the account itself and the user never
//     sees a wizard (REQ-1.2). --name and --location are what make the result
//     avar's rather than a registration the user now has to think about
//     (REQ-18.7).
//  2. Configure the guest: a non-root account matching the host user with
//     passwordless sudo (REQ-1.4), and a /etc/wsl.conf that closes the three
//     doors WSL opens by default (see wslConf).
//  3. Terminate it, so that /etc/wsl.conf takes effect. WSL reads it when a
//     distribution starts, and the distribution is already running by the time
//     avar has written it.
//  4. Verify, rather than assume: the distribution is WSL 2, it is the release
//     the selector named, the default account is the non-root one, sudo needs
//     no password, and nothing is mounted. A machine that fails any of those is
//     removed rather than recorded (REQ-18.12, PROP-7).
//
// Steps 2 and 4 need a guest shell, because writing four files and reading back
// five facts is not something an argv expresses. The script travels
// base64-encoded and is decoded inside the guest. That is not obfuscation: it
// makes the entire script one token containing no space, quote, backslash or
// newline, so nothing between Go's argv and the guest's shell — and Windows
// command-line quoting in particular — can reinterpret any part of it.

// markerPath is where avar records, inside the guest, that it made this
// distribution.
//
// The Windows-side record says avar owns a name; this says the filesystem
// behind that name is the one avar built. Reconciliation needs both before it
// will adopt an unrecorded distribution, because a name is cheap to reuse and a
// user could have registered "avr-ubuntu-24.04-amd64" themselves (REQ-18.7,
// design §5).
const markerPath = "/etc/avar/managed.json"

// marker is what avar writes at markerPath.
type marker struct {
	Provider types.ProviderID          `json:"provider"`
	Machine  string                    `json:"machine"`
	Selector types.EnvironmentSelector `json:"selector"`
	// Distro is the registry entry the distribution was installed from, which
	// is what a later avar needs in order to reproduce it.
	Distro string `json:"registry_distro"`
	// User is the non-root account avar created.
	User string `json:"user"`
}

// wslConf is the guest configuration avar writes, and every line of it closes
// something WSL leaves open by default.
//
// automount.enabled=false is the mount policy. Left on, WSL mounts every fixed
// drive at /mnt/<letter>, so the guest can read the user's whole filesystem —
// their home directory, their credential files, every project avar did not
// register. avar shares registered project directories and nothing else, and
// that promise cannot survive a default that shares everything (REQ-6.3,
// REQ-9.3, REQ-9.4, PROP-5).
//
// interop.appendWindowsPath=false is the environment policy. Left on, the whole
// of the Windows PATH is appended to the guest's, so `python` in Linux can
// resolve to python.exe on Windows. That is a surprising enough result on its
// own; it is also host state crossing into the guest that the user never
// granted (REQ-9.1, PROP-4).
//
// boot.systemd=true is what makes the guest a normal Linux machine: services
// start, timers run, and `systemctl` works, which is what a developer expects
// of the environment their project's tooling assumes.
//
// user.default is the account a session gets. It is set here as well as through
// `wsl --manage --set-default-user` because this file is the one that survives
// an export and import of the distribution, which is how a snapshot is restored
// (REQ-1.4).
const wslConfTemplate = `[boot]
systemd=true

[automount]
enabled=false

[interop]
appendWindowsPath=false

[user]
default=%s
`

// provisionScript is the guest-side setup, run once as root.
//
// It is idempotent throughout, because it is also what repairs a distribution
// whose provisioning was interrupted: creating the account only if it is
// missing, and rewriting the three files unconditionally, means running it twice
// is indistinguishable from running it once.
//
// `set -e` is what makes a partial provision impossible to mistake for a
// finished one: the first failing command ends the script non-zero, and the
// caller deletes the distribution rather than recording it (PROP-7).
const provisionScript = `set -e
if ! id -u '%[1]s' >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash '%[1]s'
fi
install -d -m 0755 /etc/sudoers.d
printf '%%s ALL=(ALL) NOPASSWD:ALL\n' '%[1]s' > /etc/sudoers.d/avar
chmod 0440 /etc/sudoers.d/avar
install -d -m 0755 /etc/avar
cat > %[2]s <<'AVR_MARKER_EOF'
%[3]s
AVR_MARKER_EOF
chmod 0644 %[2]s
cat > /etc/wsl.conf <<'AVR_WSLCONF_EOF'
%[4]s
AVR_WSLCONF_EOF
chmod 0644 /etc/wsl.conf
`

// verifyScript reads back the facts a usable environment has to have, one per
// line, for verifyOutput to check.
//
// It reports rather than judges, and it does not use `set -e`: a fact avar
// cannot read is a fact avar has to report as missing, and a script that exited
// early would leave the caller unable to say which check failed.
//
// The mount check asks about DrvFS specifically, not about everything under
// /mnt. WSL keeps its own machinery there — /mnt/wsl and /mnt/wslg, which carry
// the shared utility-VM state and the GUI plumbing — and those are tmpfs mounts
// belonging to WSL rather than doors into the user's filesystem. The first real
// provisioning run of this backend refused a perfectly good environment because
// of them. What automount does, and what avar turns off, is mount the Windows
// *drives*, so a Windows drive is what the check looks for (REQ-9.3, PROP-5).
//
// Recognising one is drvfsPredicate's business; getting it wrong here is how a
// guest with the whole of C: mounted passes a check written to prevent exactly
// that.
var verifyScript = `. /etc/os-release 2>/dev/null || true
echo "os-id=${ID}"
echo "os-version=${VERSION_ID}"
echo "marker=$(cat %[1]s 2>/dev/null | tr -d '\n' | head -c 4000)"
echo "user=$(id -un '%[2]s' 2>/dev/null)"
if sudo -n -u root true >/dev/null 2>&1; then echo "sudo=yes"; else echo "sudo=no"; fi
echo "mounts=$(awk '` + drvfsPredicate + ` && index($2, "%[3]s/") != 1 {print $2}' /proc/mounts | tr '\n' ',')"
`

// guestShellArgv is how avar runs a script inside a distribution as root.
//
// The base64 hop is the point: `sh -c` receives one argument with no character
// in it that any layer between here and the guest treats as special, so a script
// containing quotes, newlines and here-documents survives Windows command-line
// quoting untouched. base64 is in coreutils, so it is present on every
// distribution in avar's matrix.
func guestShellArgv(machine, script string) []string {
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	return []string{
		"--distribution", machine,
		"--user", "root",
		"--exec", "/bin/sh", "-c",
		"echo " + encoded + " | base64 -d | /bin/sh",
	}
}

// buildProvisionScript renders the guest-side setup for one machine.
func (p *Provider) buildProvisionScript(machine string, sel types.EnvironmentSelector, entry registryEntry) (string, error) {
	body, err := json.MarshalIndent(marker{
		Provider: types.ProviderWSL2,
		Machine:  machine,
		Selector: sel,
		Distro:   entry.Distro,
		User:     p.guestUser,
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("recording avar's marker for environment %s: %w", machine, err)
	}
	return fmt.Sprintf(provisionScript,
		p.guestUser,
		markerPath,
		string(body),
		fmt.Sprintf(wslConfTemplate, p.guestUser),
	), nil
}

// guestFacts is what verifyScript reported.
type guestFacts struct {
	OSID      string
	OSVersion string
	Marker    marker
	HasMarker bool
	User      string
	Sudo      bool
	Mounts    []string
}

// parseGuestFacts reads verifyScript's output.
//
// An unrecognised line is ignored rather than refused: the script's shape is
// avar's own, but the shells that run it are three different distributions'
// and a warning on stdout from one of them must not fail a verification that is
// otherwise fine.
func parseGuestFacts(out string) guestFacts {
	var facts guestFacts
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(strings.TrimSuffix(line, "\r")), "=")
		if !ok {
			continue
		}
		switch key {
		case "os-id":
			facts.OSID = strings.Trim(value, `"`)
		case "os-version":
			facts.OSVersion = strings.Trim(value, `"`)
		case "marker":
			if value != "" && json.Unmarshal([]byte(value), &facts.Marker) == nil {
				facts.HasMarker = true
			}
		case "user":
			facts.User = value
		case "sudo":
			facts.Sudo = value == "yes"
		case "mounts":
			for _, mount := range strings.Split(value, ",") {
				if mount != "" {
					facts.Mounts = append(facts.Mounts, mount)
				}
			}
		}
	}
	return facts
}

// check reports why the guest is not a usable avar environment, or nil.
//
// Every condition here is one the provisioning script was supposed to establish,
// so a failure means provisioning did not do what it appeared to. That is worth
// the round trip: the alternative is recording a machine that drops the user
// into a root shell, or into a guest that mounts their whole C: drive, and
// discovering it when they notice (REQ-18.12, PROP-7).
func (f guestFacts) check(machine string, sel types.EnvironmentSelector, entry registryEntry, wantUser string) error {
	if err := f.checkOwnedAndConfined(machine, wantUser); err != nil {
		return err
	}
	return f.checkRelease(machine, sel, entry)
}

// checkOwnedAndConfined is the half of the verification that asks only whether
// this is an avar environment and whether it is confined — no selector, no
// registry entry, nothing about which release it holds.
//
// It is separate because Restore needs exactly this half and cannot use the
// other. A snapshot holds whatever release it held when it was captured, and
// checking that against today's selector would refuse a restore for doing
// precisely what the user asked; but that a restored disk carries avar's marker
// for this machine, has the account, and does not have the Windows drives
// mounted are all still true of any environment avar will hand a user.
//
// The confinement clause is the one that matters most here. /etc/wsl.conf
// travels inside the VHD, so the policy is expected to come back with the disk —
// but that is an assumption about a file inside an artifact exported some time
// ago, and asserting it costs one guest command.
func (f guestFacts) checkOwnedAndConfined(machine, wantUser string) error {
	switch {
	case !f.HasMarker:
		return fmt.Errorf("environment %s does not carry avar's marker at %s, so avar cannot confirm it built it", machine, markerPath)
	case f.Marker.Machine != machine:
		return fmt.Errorf("environment %s carries a marker for %s", machine, f.Marker.Machine)
	case f.User != wantUser:
		return fmt.Errorf("environment %s has no account named %s; avar gives you a non-root account matching your Windows one", machine, wantUser)
	case !f.Sudo:
		return fmt.Errorf("environment %s does not grant %s passwordless sudo", machine, wantUser)
	case len(f.Mounts) > 0:
		// A guest that still has a Windows drive mounted has automount on, which
		// means /etc/wsl.conf did not take effect and the guest can reach every
		// file on it (REQ-9.3, PROP-5).
		return fmt.Errorf("environment %s still has a Windows drive mounted at %s; avar shares registered project directories only",
			machine, strings.Join(f.Mounts, ", "))
	}
	return nil
}

// checkRelease reports a guest that is not the release the selector named.
//
// This is where the registry's inexactness is caught. `wsl --install Debian`
// installs Debian stable, which is 13 today and will be 14 when Debian says so,
// with no change to the name avar asked for. Reading the guest's own
// /etc/os-release turns that from an environment quietly not being what the user
// asked for into a refusal that names both versions and can be fixed by one line
// in the registry table.
func (f guestFacts) checkRelease(machine string, sel types.EnvironmentSelector, entry registryEntry) error {
	if f.OSID != entry.OSReleaseID {
		return fmt.Errorf("environment %s reports itself as %q, not %s; WSL's %q entry is no longer %s",
			machine, f.OSID, entry.OSReleaseID, entry.Distro, sel.Distro)
	}
	// A point release of the release avar asked for is the release avar asked
	// for: 24.04.1 satisfies 24.04, and 13.2 satisfies 13.
	if f.OSVersion != entry.OSReleaseVersion && !strings.HasPrefix(f.OSVersion, entry.OSReleaseVersion+".") {
		return fmt.Errorf("environment %s is %s %s, but you asked for %s %s; WSL's %q entry now installs %s",
			machine, sel.Distro, f.OSVersion, sel.Distro, sel.Version, entry.Distro, f.OSVersion)
	}
	return nil
}
