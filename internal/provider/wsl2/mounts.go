package wsl2

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/olamide226/avar/internal/types"
)

// How a Windows directory gets into a WSL guest.
//
// WSL's own answer is automount, which mounts every fixed drive at /mnt/<letter>
// when a distribution starts. avar turns that off at provisioning time, because
// a guest that can read the whole of C: is a guest that can read the user's home
// directory, their credential files and every project avar was not asked to
// share (REQ-9.3, PROP-5). What is left is DrvFS, the same filesystem driver,
// asked for one directory at a time:
//
//	mount -t drvfs 'C:\Users\ola\code\app' /mnt/avr/projects/app-3fa9c2b1d0
//
// That is the whole mechanism. It gives live bidirectional visibility — the same
// bytes, not a copy — which is what REQ-18.5 asks for and what makes editing
// from Windows and building from Linux the same file.
//
// Two things follow that differ from the Lima backend.
//
// Sharing a new project needs no restart. Lima can only change an instance's
// mounts by rewriting its configuration and restarting it, which is why
// Provider.SetMounts is allowed to restart and has to explain the delay. A
// DrvFS mount is a mount: it takes effect immediately, and registering a
// project the user has never opened before costs a few milliseconds rather than
// ten seconds. SetMounts here emits no restart notice because there is no
// restart.
//
// Mounts do not survive a restart by themselves, so avar writes them to
// /etc/fstab as well as mounting them. systemd is enabled in avar's
// distributions, so fstab is processed at boot and a distribution that WSL
// terminated for idleness comes back with its projects where they were.

// fstabPath is the guest file that makes avar's mounts survive a restart.
const fstabPath = "/etc/fstab"

// fstabBegin and fstabEnd delimit the block avar owns in /etc/fstab.
//
// avar rewrites what is between them and never anything else: the file belongs
// to the distribution, and a user who added a line to it must not lose it
// because they registered a project.
const (
	fstabBegin = "# BEGIN avar — managed mounts, do not edit"
	fstabEnd   = "# END avar"
)

// drvfsOptionsExpr is the shell expression that computes the mount options
// every project share gets.
//
// metadata is what makes Linux permissions work on a Windows filesystem at all:
// without it every file is 0777 and owned by the mounting user, so `chmod +x`
// silently does nothing and a build that checks its own script's mode fails in
// a way nobody can explain. uid and gid hand the share to avar's account rather
// than to root, and the masks give files the modes a Linux user expects. They
// are Microsoft's own documented automount defaults, which is the combination
// with the most miles on it.
//
// It is an expression rather than a constant string because the numeric uid is
// only known inside the guest, and it has to be numeric: an fstab line is read
// by mount, not by a shell, so a name would not be resolved.
const drvfsOptionsExpr = `AVR_OPTS="metadata,uid=$(id -u),gid=$(id -g),umask=22,fmask=11,case=off"`

// AppliedMounts reports the project shares the guest currently has.
//
// It reads /proc/mounts rather than avar's own records, because the question is
// what the backend has actually applied: comparing that against what avar
// expects is how a caller decides whether this project still needs registering
// (REQ-6.4). ProjectID is empty in the result — the guest knows which
// directories are mounted, never which project each one belongs to.
func (p *Provider) AppliedMounts(ctx context.Context, machine string) ([]types.MountSpec, error) {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return nil, err
	}

	out, err := p.run(ctx, guestShellArgv(machine, readMountsScript)...)
	if err != nil {
		return nil, fmt.Errorf("reading the shared directories of environment %s: %w", machine, err)
	}
	return parseProcMounts(out), nil
}

// readMountsScript prints the drvfs mounts beneath avar's project root, source
// and target, one per line.
//
// It reads /proc/mounts rather than running `mount` or `findmnt`, because
// /proc/mounts is a kernel-defined format that is the same on all three
// distributions in avar's matrix and is not localized, whereas `mount`'s output
// is a courtesy the util-linux version decides on.
const readMountsScript = `awk '$3 == "drvfs" && $2 ~ /^\/mnt\/avr\/projects\// {print $1 "\t" $2}' /proc/mounts
`

// SetMounts makes mounts the complete set of project shares the guest has.
//
// It is replace-not-append, which is what makes confinement checkable: after it
// returns, the guest can reach exactly the project roots in mounts, at exactly
// the paths avar planned, and nothing else — never the home directory, never a
// whole drive, never an unregistered sibling (REQ-6.3, REQ-9.3, PROP-5).
//
// It is idempotent. When the applied set already equals the desired one nothing
// is mounted, nothing is unmounted, /etc/fstab is left alone and no progress is
// emitted, because returning to a project the user already had open has to stay
// instant (REQ-17.1).
func (p *Provider) SetMounts(ctx context.Context, machine string, mounts []types.MountSpec, progress types.ProgressSink) error {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return err
	}
	if progress == nil {
		progress = types.DiscardProgress
	}

	desired, err := types.NormalizeMounts(mounts)
	if err != nil {
		return fmt.Errorf("sharing directories with environment %s: %w", machine, err)
	}
	for _, m := range desired {
		if !strings.HasPrefix(m.GuestPath, GuestProjectRoot+"/") {
			// A guest target outside avar's own root is not a mount avar
			// planned, and applying it would put a Windows directory somewhere
			// nothing checks (PROP-5, PROP-14).
			return fmt.Errorf("sharing directories with environment %s: %s would appear at %s, which is outside %s",
				machine, m.HostPath, m.GuestPath, GuestProjectRoot)
		}
	}

	applied, err := p.AppliedMounts(ctx, machine)
	if err != nil {
		return err
	}
	if types.EqualMappings(applied, desired) {
		return nil
	}

	progress.Progress(types.ProgressEvent{
		Kind:    types.ProgressMounting,
		Machine: machine,
		Message: fmt.Sprintf("sharing %s", strings.Join(types.MountHostPaths(desired), ", ")),
	})

	script := mountScript(applied, desired)
	if _, err := p.run(ctx, guestShellArgv(machine, script)...); err != nil {
		return fmt.Errorf("sharing directories with environment %s: %w", machine, err)
	}

	// Verify rather than assume. Dropping the user into a shell at a path that
	// looks like their project and is empty is worse than failing outright, and
	// a DrvFS mount can fail for reasons avar cannot see from Windows — a path
	// that has been renamed, a drive that is not ready (REQ-6.5).
	nowApplied, err := p.AppliedMounts(ctx, machine)
	if err != nil {
		return err
	}
	if !types.EqualMappings(nowApplied, desired) {
		return fmt.Errorf("sharing directories with environment %s: it now has %s, not %s",
			machine, describeMounts(nowApplied), describeMounts(desired))
	}
	return nil
}

// mountScript builds the guest-side change from the applied set to the desired
// one.
//
// Only the difference is acted on: a project already mounted at the path avar
// planned is left exactly as it is, so registering a second project does not
// disturb a build running in the first.
//
// Unmounting comes before mounting. A stale share at a guest path avar is about
// to reuse would otherwise be shadowed by the new one rather than replaced, and
// the guest would hold two mounts where avar's records describe one.
func mountScript(applied, desired []types.MountSpec) string {
	appliedByGuest := byGuestPath(applied)
	desiredByGuest := byGuestPath(desired)

	b := &strings.Builder{}
	b.WriteString("set -e\n")
	b.WriteString(drvfsOptionsExpr + "\n")

	for _, guestPath := range sortedKeys(appliedByGuest) {
		want, keep := desiredByGuest[guestPath]
		if keep && want.HostPath == appliedByGuest[guestPath].HostPath {
			continue
		}
		fmt.Fprintf(b, "sudo umount %s\n", shellQuote(guestPath))
		fmt.Fprintf(b, "sudo rmdir %s 2>/dev/null || true\n", shellQuote(guestPath))
	}

	for _, guestPath := range sortedKeys(desiredByGuest) {
		m := desiredByGuest[guestPath]
		if have, ok := appliedByGuest[guestPath]; ok && have.HostPath == m.HostPath {
			continue
		}
		fmt.Fprintf(b, "sudo install -d -m 0755 %s\n", shellQuote(guestPath))
		fmt.Fprintf(b, "sudo mount -t drvfs %s %s -o \"$AVR_OPTS\"\n", shellQuote(m.HostPath), shellQuote(guestPath))
	}

	b.WriteString(fstabScript(desired))
	return b.String()
}

// fstabScript rewrites avar's own block in /etc/fstab so the shares come back
// after a restart.
//
// It rewrites only what is between the markers, and appends a fresh block to a
// file that has none. The distribution's fstab belongs to the distribution: a
// user who added a line to it must not lose it because they opened a new
// project.
//
// The whole file is rebuilt in a temporary file and installed over the original,
// so an interrupted rewrite cannot leave a distribution with half an fstab and
// no way to boot its mounts (REQ-17.5, PROP-7).
func fstabScript(desired []types.MountSpec) string {
	b := &strings.Builder{}
	b.WriteString("AVR_FSTAB=$(mktemp)\n")
	fmt.Fprintf(b, "sudo awk 'BEGIN{skip=0} $0 == %s {skip=1; next} $0 == %s {skip=0; next} skip==0 {print}' %s > \"$AVR_FSTAB\" 2>/dev/null || true\n",
		shellQuote(fstabBegin), shellQuote(fstabEnd), fstabPath)
	fmt.Fprintf(b, "printf '%%s\\n' %s >> \"$AVR_FSTAB\"\n", shellQuote(fstabBegin))
	for _, m := range desired {
		// fstab is whitespace-separated, so a path containing a space carries
		// it as \040 — the same escaping /proc/mounts uses coming the other
		// way. The options come from the variable because an fstab line is read
		// by mount rather than by a shell and cannot expand anything itself.
		fmt.Fprintf(b, "printf '%%s %%s drvfs %%s 0 0\\n' %s %s \"$AVR_OPTS\" >> \"$AVR_FSTAB\"\n",
			shellQuote(escapeFstabField(m.HostPath)),
			shellQuote(escapeFstabField(m.GuestPath)))
	}
	fmt.Fprintf(b, "printf '%%s\\n' %s >> \"$AVR_FSTAB\"\n", shellQuote(fstabEnd))
	fmt.Fprintf(b, "sudo install -m 0644 -o root -g root \"$AVR_FSTAB\" %s\n", fstabPath)
	b.WriteString("rm -f \"$AVR_FSTAB\"\n")
	return b.String()
}

// byGuestPath indexes a mount set by the path it appears at in the guest, which
// is the half that must be unique for the set to describe anything at all.
func byGuestPath(mounts []types.MountSpec) map[string]types.MountSpec {
	out := make(map[string]types.MountSpec, len(mounts))
	for _, m := range mounts {
		out[m.GuestPath] = m
	}
	return out
}

// parseProcMounts reads the source and target of each drvfs share the guest
// reported.
//
// /proc/mounts escapes the four characters that would otherwise break its own
// whitespace-separated format, and a Windows path contains one of them in every
// component: the backslash, written \134. Failing to unescape would make every
// applied mount compare unequal to the one avar planned, so avar would tear down
// and rebuild every share on every invocation.
func parseProcMounts(out string) []types.MountSpec {
	var mounts []types.MountSpec
	for _, line := range strings.Split(out, "\n") {
		source, target, ok := strings.Cut(strings.TrimSpace(strings.TrimSuffix(line, "\r")), "\t")
		if !ok {
			continue
		}
		source, target = unescapeProcMounts(source), unescapeProcMounts(target)
		if source == "" || target == "" {
			continue
		}
		mounts = append(mounts, types.MountSpec{HostPath: source, GuestPath: target, Writable: true})
	}
	normalized, err := types.NormalizeMounts(mounts)
	if err != nil {
		// A share avar cannot describe is one it will replace, and reporting
		// nothing applied makes SetMounts rebuild the set rather than trust a
		// line it could not read.
		return nil
	}
	return normalized
}

// procMountsUnescaper reverses the octal escaping the kernel applies to the four
// characters that are special in /proc/mounts.
var procMountsUnescaper = strings.NewReplacer(
	`\134`, `\`,
	`\040`, " ",
	`\011`, "\t",
	`\012`, "\n",
)

func unescapeProcMounts(s string) string { return procMountsUnescaper.Replace(s) }

// fstabEscaper applies the same escaping in the other direction, for the fstab
// lines avar writes. The backslash is escaped first, so an escape avar produces
// is not escaped again.
var fstabEscaper = strings.NewReplacer(
	`\`, `\134`,
	" ", `\040`,
	"\t", `\011`,
)

func escapeFstabField(s string) string { return fstabEscaper.Replace(s) }

// shellQuote wraps a value so a POSIX shell sees it as one literal argument.
//
// The generated script is the one place in this backend where a value becomes
// part of a command line a shell parses, because mounting is a sequence of
// privileged commands rather than a single argv avar can hand to wsl.exe. Single
// quotes make every character literal except the single quote itself, which is
// closed, escaped and reopened — so a directory named `it's mine` is a
// directory name and not syntax.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sortedKeys orders a mount map so one desired set produces one script, which is
// what makes the script assertable in a test.
func sortedKeys(m map[string]types.MountSpec) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// describeMounts renders a mount set for an error the user reads.
func describeMounts(mounts []types.MountSpec) string {
	if len(mounts) == 0 {
		return "nothing shared"
	}
	return strings.Join(types.MountHostPaths(mounts), ", ")
}
