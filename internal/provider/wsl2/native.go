package wsl2

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/olamide226/avar/internal/types"
)

// Linux-native workspace mode on WSL (REQ-14).
//
// WSL reaches a Windows directory through DrvFS, which serves every file
// operation across the boundary into Windows. Editing source that way is
// invisible; `npm install` or a test run that stats a dependency tree is the
// difference between seconds and minutes, and it is the single most common
// complaint about developing in WSL. Native mode answers it by putting a second
// copy of the project on the distribution's own ext4 filesystem and running the
// session there.
//
// Three decisions shape everything below.
//
// **The share is the transport.** The project is already mounted in the guest at
// /mnt/avr/projects/<name>-<hash>, and native mode does not take that away: it
// adds a copy at ~/workspaces/<name>-<hash> and moves the session's working
// directory. Copying is therefore an entirely guest-side operation between two
// guest paths — `cp` from one to the other — and avar needs no file-transfer
// protocol, no second channel, and above all no rsync, which a minimal Fedora
// image does not have and avar has no business installing into a user's
// environment. It also means the security boundary does not move: native mode
// exposes no host path the share did not already expose (PROP-5).
//
// **Content is the only evidence.** Deciding what changed is three manifests of
// SHA-256 hashes compared against each other, never a timestamp. The two copies
// live on different filesystems with different clocks, different granularity and
// different ideas of what a copy does to a modification time; a synchronization
// that trusted them would be wrong in the direction that destroys work.
//
// **A file arrives whole or not at all.** Every copy is written to a temporary
// name beside its destination and moved into place, and the baseline is written
// only after every copy and delete has succeeded. A synchronization killed
// halfway therefore leaves a destination holding some of the new files, none of
// them truncated, and a baseline that still describes the previous agreement —
// so running the command again finishes the job instead of reporting a conflict
// on files nobody touched (REQ-17.5).

// workspacesDirName is the directory under the guest account's home that holds
// native copies. Requirement 14.1 names it, and it is a name a user recognises
// in a prompt: ~/workspaces/<project>.
const workspacesDirName = "workspaces"

// baselineDir is where avar records what the two copies agreed on.
//
// It is inside the guest rather than in avar's state directory on Windows, and
// deliberately: the baseline describes a native copy, the native copy lives and
// dies with the distribution, and a baseline that outlived its subject would
// describe an agreement with a tree that no longer exists. Removing the
// environment removes both together.
const baselineDir = "/var/lib/avar/workspaces"

// syncTempSuffix names the file a copy is written to before it is moved into
// place. It is long and avar-specific because a project may legitimately contain
// almost any name, and this one has to be improbable rather than merely unused.
const syncTempSuffix = ".avr-sync-incoming"

// scan section markers. They are what the parser switches on, and they cannot
// collide with data: a hash line begins with sixty-four hexadecimal characters,
// a metadata line with "x " or "s ", and a baseline line with a hash.
const (
	markerBaseline  = "AVR:baseline"
	markerMount     = "AVR:mount"
	markerWorkspace = "AVR:workspace"
	markerPresent   = "AVR:present"
	markerAbsent    = "AVR:absent"
	markerMeta      = "AVR:meta"
	markerEnd       = "AVR:end"
	markerMissing   = "AVR:missing-tool "
)

// MapNativeWorkspace plans where this project's native copy lives and which
// directory a native-mode session starts in.
//
// The copy is named exactly as the share is — the same readable label and the
// same truncated Project_Identity — so that `~/workspaces/app-3fa9c2b1d0` and
// `/mnt/avr/projects/app-3fa9c2b1d0` are visibly two views of one project. The
// hash half is not decoration here any more than it is there: two projects
// called `api` must not become one workspace (PROP-14).
//
// It is a pure function: deterministic, no filesystem access, no subprocess.
func (p *Provider) MapNativeWorkspace(projectID, hostRoot, hostCwd string) (types.NativeWorkspace, string, error) {
	mount, mountCwd, err := p.MapProjectPath(projectID, hostRoot, hostCwd)
	if err != nil {
		return types.NativeWorkspace{}, "", err
	}

	root := p.workspaceRoot(projectID, mount.HostPath)
	ws := types.NativeWorkspace{
		ProjectID: projectID,
		HostPath:  mount.HostPath,
		MountPath: mount.GuestPath,
		Path:      root,
	}
	if err := ws.Validate(); err != nil {
		return types.NativeWorkspace{}, "", fmt.Errorf("planning a Linux-native workspace for %s: %w", mount.HostPath, err)
	}

	// The session's directory inside the copy keeps the suffix the user was
	// standing in, exactly as it does inside the share: `avr --native-fs` run
	// from a subdirectory lands in the same subdirectory (PROP-1's rule, read
	// against the copy the user asked to work in).
	guestCwd := root
	if rel := strings.TrimPrefix(mountCwd, mount.GuestPath); rel != "" {
		guestCwd = path.Join(root, strings.TrimPrefix(rel, "/"))
	}
	return ws, guestCwd, nil
}

// workspaceRoot is the guest path of one project's native copy.
func (p *Provider) workspaceRoot(projectID, hostRoot string) string {
	return path.Join("/home", p.guestUser, workspacesDirName, guestDirName(projectID, hostRoot))
}

// baselinePath is where the agreement between the two copies is recorded.
func baselinePath(ws types.NativeWorkspace) string {
	return path.Join(baselineDir, path.Base(ws.Path)+".manifest")
}

// ScanNativeWorkspace reads both copies and the recorded baseline in one guest
// invocation.
//
// One invocation rather than three because each one is a wsl.exe process and the
// warm path has a budget (REQ-17.1), and because three would give three
// different moments: a file written between two of them would appear in one
// manifest and not another, and the plan built from that would describe a state
// that never existed.
func (p *Provider) ScanNativeWorkspace(ctx context.Context, machine string, ws types.NativeWorkspace) (types.WorkspaceScan, error) {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return types.WorkspaceScan{}, err
	}
	if err := ws.Validate(); err != nil {
		return types.WorkspaceScan{}, fmt.Errorf("scanning the Linux-native workspace for %s: %w", ws.HostPath, err)
	}

	out, err := p.run(ctx, guestShellArgv(machine, nativeScanScript(ws))...)
	if err != nil {
		return types.WorkspaceScan{}, fmt.Errorf("reading the Linux-native workspace for %s in environment %s: %w", ws.HostPath, machine, err)
	}
	scan, err := parseNativeScan(out)
	if err != nil {
		return types.WorkspaceScan{}, fmt.Errorf("reading the Linux-native workspace for %s in environment %s: %w", ws.HostPath, machine, err)
	}
	return scan, nil
}

// nativeScanScript renders the guest-side scan.
//
// It reports and does not judge, and it does not use `set -e`: a tree avar
// cannot read is a fact the caller has to be told about, and a script that
// exited early would leave it unable to say which of the two copies was the
// problem.
//
// The tools it needs are named first and checked before anything is walked.
// `sha256sum`, `base64`, `cat`, `dirname` and `install` are coreutils, which
// every distribution in avar's matrix has — provisioning already depends on it.
// `find` is findutils and is a genuine additional dependency, so the script says
// so plainly rather than producing an empty manifest, which would look exactly
// like an empty project and would let avar delete a user's files on the strength
// of it.
func nativeScanScript(ws types.NativeWorkspace) string {
	b := &strings.Builder{}
	b.WriteString("for avr_tool in find sha256sum base64; do\n")
	fmt.Fprintf(b, "  command -v \"$avr_tool\" >/dev/null 2>&1 || { echo '%s'\"$avr_tool\"; exit 0; }\n", markerMissing)
	b.WriteString("done\n")

	b.WriteString("avr_scan() {\n")
	fmt.Fprintf(b, "  if [ ! -d \"$1\" ]; then echo '%s'; return 0; fi\n", markerAbsent)
	fmt.Fprintf(b, "  echo '%s'\n", markerPresent)
	fmt.Fprintf(b, "  ( cd \"$1\" && find . %s \\( -type f -exec sha256sum {} + \\) ) 2>/dev/null || true\n", prunePredicate())
	fmt.Fprintf(b, "  echo '%s'\n", markerMeta)
	fmt.Fprintf(b, "  ( cd \"$1\" && find . %s \\( -type f -perm -u+x -printf 'x %%p\\n' \\) -o \\( ! -type d ! -type f -printf 's %%p\\n' \\) ) 2>/dev/null || true\n", prunePredicate())
	b.WriteString("}\n")

	fmt.Fprintf(b, "echo '%s'\n", markerBaseline)
	fmt.Fprintf(b, "cat %s 2>/dev/null || true\n", shellQuote(baselinePath(ws)))
	fmt.Fprintf(b, "echo '%s'\n", markerMount)
	fmt.Fprintf(b, "avr_scan %s\n", shellQuote(ws.MountPath))
	fmt.Fprintf(b, "echo '%s'\n", markerWorkspace)
	fmt.Fprintf(b, "avr_scan %s\n", shellQuote(ws.Path))
	fmt.Fprintf(b, "echo '%s'\n", markerEnd)
	return b.String()
}

// prunePredicate renders the find expression that stops the walk entering a
// build output directory.
//
// It is generated from types.WorkspaceExcludedDirs rather than written out here,
// so that the list the user is told about and the list the walk actually applies
// cannot differ.
func prunePredicate() string {
	names := make([]string, 0, len(types.WorkspaceExcludedDirs))
	for _, name := range types.WorkspaceExcludedDirs {
		names = append(names, "-name "+shellQuote(name))
	}
	return `\( -type d \( ` + strings.Join(names, " -o ") + ` \) -prune \) -o`
}

// parseNativeScan turns the scan output into the three manifests.
func parseNativeScan(out string) (types.WorkspaceScan, error) {
	scan := types.WorkspaceScan{}
	var (
		section  string
		hashes   = map[string]map[string]string{}
		execs    = map[string]map[string]bool{}
		skipped  = map[string]struct{}{}
		inMeta   bool
		haveEnd  bool
		mountSet bool
	)
	hashes[markerMount], hashes[markerWorkspace] = map[string]string{}, map[string]string{}
	execs[markerMount], execs[markerWorkspace] = map[string]bool{}, map[string]bool{}

	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		switch {
		case strings.HasPrefix(line, markerMissing):
			tool := strings.TrimSpace(strings.TrimPrefix(line, markerMissing))
			return types.WorkspaceScan{}, fmt.Errorf("this environment has no %s, which avar needs in order to compare the two copies of your project; install it in the environment (for example `avr sudo apt-get install -y findutils`) and try again", tool)
		case line == markerBaseline:
			section, inMeta = markerBaseline, false
			continue
		case line == markerMount:
			section, inMeta = markerMount, false
			continue
		case line == markerWorkspace:
			section, inMeta = markerWorkspace, false
			continue
		case line == markerPresent:
			if section == markerWorkspace {
				scan.Exists = true
			}
			mountSet = mountSet || section == markerMount
			inMeta = false
			continue
		case line == markerAbsent:
			inMeta = false
			continue
		case line == markerMeta:
			inMeta = true
			continue
		case line == markerEnd:
			haveEnd = true
			continue
		}
		if line == "" {
			continue
		}

		switch section {
		case markerBaseline:
			rel, entry, ok := parseBaselineLine(line)
			if !ok {
				continue
			}
			if scan.Baseline == nil {
				scan.Baseline = types.WorkspaceManifest{}
			}
			scan.Baseline[rel] = entry
		case markerMount, markerWorkspace:
			if inMeta {
				if kind, rel, ok := parseMetaLine(line); ok {
					if kind == 'x' {
						execs[section][rel] = true
					} else {
						skipped[rel] = struct{}{}
					}
				}
				continue
			}
			rel, hash, ok := parseHashLine(line)
			if !ok {
				// A line sha256sum escaped is a name avar cannot carry
				// unambiguously — one holding a backslash or a newline. It is
				// reported as skipped rather than guessed at.
				if rel != "" {
					skipped[rel] = struct{}{}
				}
				continue
			}
			hashes[section][rel] = hash
		}
	}

	if !haveEnd {
		return types.WorkspaceScan{}, fmt.Errorf("the environment's report ended early; avar will not compare two copies of a project on a partial listing")
	}
	if !mountSet {
		return types.WorkspaceScan{}, fmt.Errorf("the shared copy of the project is not present in the environment; avar shares a project before it copies one")
	}

	scan.Mount = buildManifest(hashes[markerMount], execs[markerMount])
	scan.Guest = buildManifest(hashes[markerWorkspace], execs[markerWorkspace])
	for rel := range skipped {
		scan.Skipped = append(scan.Skipped, rel)
	}
	sort.Strings(scan.Skipped)
	return scan, nil
}

// buildManifest joins the hash pass and the metadata pass.
func buildManifest(hashes map[string]string, execs map[string]bool) types.WorkspaceManifest {
	out := make(types.WorkspaceManifest, len(hashes))
	for rel, hash := range hashes {
		out[rel] = types.WorkspaceEntry{Hash: hash, Exec: execs[rel]}
	}
	return out
}

// parseHashLine reads one `sha256sum` line.
//
// GNU coreutils prefixes the whole line with a backslash when the name contains
// a backslash or a newline, and escapes them inside. avar does not attempt to
// unescape: such a name cannot come from the Windows side at all — Win32 forbids
// both characters — so it can only exist in the guest copy, where refusing to
// carry it is the honest answer. The name is returned so the caller can report
// it as skipped.
func parseHashLine(line string) (rel, hash string, ok bool) {
	if strings.HasPrefix(line, `\`) {
		_, name, _ := strings.Cut(line, "  ")
		return normalizeRel(name), "", false
	}
	hash, name, found := strings.Cut(line, "  ")
	if !found || len(hash) != 64 || !isHex(hash) {
		return "", "", false
	}
	rel = normalizeRel(name)
	if rel == "" {
		return "", "", false
	}
	return rel, strings.ToLower(hash), true
}

// parseMetaLine reads one line of the metadata pass: 'x' for an executable
// regular file, 's' for an entry that is neither a regular file nor a
// directory.
func parseMetaLine(line string) (kind byte, rel string, ok bool) {
	if len(line) < 3 || (line[0] != 'x' && line[0] != 's') || line[1] != ' ' {
		return 0, "", false
	}
	rel = normalizeRel(line[2:])
	if rel == "" {
		return 0, "", false
	}
	return line[0], rel, true
}

// parseBaselineLine reads one stored baseline entry: hash, executable bit, and
// the path base64-encoded.
//
// The path is encoded because the baseline is written by a shell here-document
// and read back by this parser, and a name containing a space or a tab would
// otherwise be ambiguous in one of the two.
func parseBaselineLine(line string) (string, types.WorkspaceEntry, bool) {
	fields := strings.Fields(line)
	if len(fields) != 3 || len(fields[0]) != 64 || !isHex(fields[0]) {
		return "", types.WorkspaceEntry{}, false
	}
	exec, err := strconv.ParseBool(fields[1])
	if err != nil {
		return "", types.WorkspaceEntry{}, false
	}
	decoded, err := base64.StdEncoding.DecodeString(fields[2])
	if err != nil || len(decoded) == 0 {
		return "", types.WorkspaceEntry{}, false
	}
	return string(decoded), types.WorkspaceEntry{Hash: strings.ToLower(fields[0]), Exec: exec}, true
}

// normalizeRel turns a path `find` reported into the manifest's vocabulary: no
// leading "./", no leading or trailing slash, and no way out of the tree.
//
// The traversal guard is defence in depth rather than a fix for a reachable
// bug. Neither source can produce a `..` component today: the manifests come
// from `find .` piped through sha256sum, and a path component cannot be `..`
// because a filename cannot contain a slash; a `..` entry crafted into the
// baseline reaches classify as absent on both sides, converges, and is dropped
// before it can enter a copy plan.
//
// That makes it safe by argument rather than by check, and the argument rests
// on find's output shape and on the plan builder never promoting a
// baseline-only path — either of which could change without anyone revisiting
// this function. What is on the other end is the reason to spend four lines
// here anyway: for --to-host the destination is the DrvFS mount, so a path that
// escaped the project root would write into the user's real Windows filesystem
// outside the directory they registered (PROP-5).
//
// An empty result is already what both callers treat as unparseable.
func normalizeRel(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "./")
	name = strings.Trim(name, "/")
	if name == "." || name == "" {
		return ""
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == ".." {
			return ""
		}
	}
	return name
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// ApplyNativeWorkspace carries out one direction's synchronization and records
// the baseline it leaves behind.
func (p *Provider) ApplyNativeWorkspace(ctx context.Context, machine string, ws types.NativeWorkspace, sync types.WorkspaceSync, progress types.ProgressSink) error {
	if err := p.gate(ctx, machine, ownershipRecord); err != nil {
		return err
	}
	if err := ws.Validate(); err != nil {
		return fmt.Errorf("synchronizing the Linux-native workspace for %s: %w", ws.HostPath, err)
	}
	if progress == nil {
		progress = types.DiscardProgress
	}
	if sync.Direction != types.ToGuest && sync.Direction != types.ToHost {
		return fmt.Errorf("synchronizing the Linux-native workspace for %s: %q is not a direction avar knows", ws.HostPath, sync.Direction)
	}

	if !sync.Empty() {
		progress.Progress(types.ProgressEvent{
			Kind:    types.ProgressSyncing,
			Machine: machine,
			Message: describeSync(sync),
		})
	}

	if _, err := p.run(ctx, guestShellArgv(machine, p.nativeApplyScript(ws, sync))...); err != nil {
		return fmt.Errorf("synchronizing the Linux-native workspace for %s in environment %s: %w", ws.HostPath, machine, err)
	}
	return nil
}

// describeSync says what is about to happen, in one line, for the progress sink.
func describeSync(sync types.WorkspaceSync) string {
	where := "into the Linux filesystem"
	if sync.Direction == types.ToHost {
		where = "back to the host"
	}
	parts := make([]string, 0, 2)
	if n := len(sync.Copy); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "file", "files")))
	}
	if n := len(sync.Delete); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "deletion", "deletions")))
	}
	return "applying " + strings.Join(parts, " and ") + " " + where
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// nativeApplyScript renders the guest-side synchronization.
//
// `set -e` is what makes a partial synchronization impossible to mistake for a
// finished one: the first failing copy ends the script non-zero and the baseline
// at the end is never reached, so the recorded agreement still describes the
// state before this attempt.
//
// Every path travels base64-encoded and is decoded inside the guest, for the
// same reason the script itself does: a project file may be named almost
// anything, and a name that became shell syntax would act on a different file
// than the one the user reviewed. That costs one process per changed file, which
// is paid only for files that actually changed — never for the whole tree.
func (p *Provider) nativeApplyScript(ws types.NativeWorkspace, sync types.WorkspaceSync) string {
	src, dst := ws.MountPath, ws.Path
	if sync.Direction == types.ToHost {
		src, dst = ws.Path, ws.MountPath
	}

	// A file copied into the guest must belong to the user's account, not to
	// root. Every script this package sends runs as root (guestShellArgv), so
	// without this the whole workspace would be root-owned and the user could
	// not write to their own project — the same defect the DrvFS mount options
	// exist to avoid. On the host side there is nothing to set: a DrvFS file's
	// ownership comes from the mount's own uid/gid options.
	owner := ""
	if sync.Direction == types.ToGuest {
		owner = fmt.Sprintf("-o %[1]s -g %[1]s ", shellQuote(p.guestUser))
	}

	b := &strings.Builder{}
	b.WriteString("set -e\n")
	fmt.Fprintf(b, "AVR_SRC=%s\n", shellQuote(src))
	fmt.Fprintf(b, "AVR_DST=%s\n", shellQuote(dst))
	fmt.Fprintf(b, "[ -d \"$AVR_DST\" ] || install -d %s-m 0755 -- \"$AVR_DST\"\n", owner)

	// cp preserves the mode and the timestamps and deliberately not the
	// ownership. Preserving ownership is what `cp -p` also does, and as root it
	// would either hand a guest file to whatever uid the share reports or fail
	// outright on a filesystem that refuses the change — and a failure there
	// would abort a synchronization over something avar does not want anyway.
	// The executable bit is the part that matters and mode carries it.
	b.WriteString("avr_cp() {\n")
	b.WriteString("  avr_rel=$(printf '%s' \"$1\" | base64 -d)\n")
	b.WriteString("  avr_dir=$(dirname -- \"$avr_rel\")\n")
	fmt.Fprintf(b, "  [ -d \"$AVR_DST/$avr_dir\" ] || install -d %s-m 0755 -- \"$AVR_DST/$avr_dir\"\n", owner)
	b.WriteString("  cp --preserve=mode,timestamps -- \"$AVR_SRC/$avr_rel\" \"$AVR_DST/$avr_rel" + syncTempSuffix + "\"\n")
	if owner != "" {
		fmt.Fprintf(b, "  chown %[1]s:%[1]s -- \"$AVR_DST/$avr_rel%s\"\n", shellQuote(p.guestUser), syncTempSuffix)
	}
	b.WriteString("  mv -f -- \"$AVR_DST/$avr_rel" + syncTempSuffix + "\" \"$AVR_DST/$avr_rel\"\n")
	b.WriteString("}\n")

	b.WriteString("avr_rm() {\n")
	b.WriteString("  avr_rel=$(printf '%s' \"$1\" | base64 -d)\n")
	b.WriteString("  rm -f -- \"$AVR_DST/$avr_rel\"\n")
	b.WriteString("}\n")

	for _, rel := range sync.Copy {
		fmt.Fprintf(b, "avr_cp %s\n", shellQuote(base64.StdEncoding.EncodeToString([]byte(rel))))
	}
	for _, rel := range sync.Delete {
		fmt.Fprintf(b, "avr_rm %s\n", shellQuote(base64.StdEncoding.EncodeToString([]byte(rel))))
	}

	b.WriteString(baselineWriteScript(ws, sync.Baseline))
	return b.String()
}

// baselineWriteScript records the agreement, atomically and last.
func baselineWriteScript(ws types.NativeWorkspace, baseline types.WorkspaceManifest) string {
	target := baselinePath(ws)
	b := &strings.Builder{}
	fmt.Fprintf(b, "install -d -m 0700 -- %s\n", shellQuote(baselineDir))
	fmt.Fprintf(b, "AVR_BASE=%s\n", shellQuote(target+".incoming"))
	b.WriteString("cat > \"$AVR_BASE\" <<'AVR_BASELINE_EOF'\n")
	b.WriteString(renderBaseline(baseline))
	b.WriteString("AVR_BASELINE_EOF\n")
	b.WriteString("chmod 0600 \"$AVR_BASE\"\n")
	fmt.Fprintf(b, "mv -f -- \"$AVR_BASE\" %s\n", shellQuote(target))
	return b.String()
}

// renderBaseline writes the manifest in the form parseBaselineLine reads: hash,
// executable bit, and the path base64-encoded so that no name can be mistaken
// for a field separator or close the here-document it travels in.
func renderBaseline(m types.WorkspaceManifest) string {
	b := &strings.Builder{}
	for _, rel := range m.Paths() {
		entry := m[rel]
		fmt.Fprintf(b, "%s %t %s\n", entry.Hash, entry.Exec, base64.StdEncoding.EncodeToString([]byte(rel)))
	}
	return b.String()
}
