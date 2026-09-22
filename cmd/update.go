package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/olamide226/avar/internal/cli"
	"github.com/olamide226/avar/internal/update"
)

func init() { registerSubcommand("update", runUpdate) }

// updateHost is everything `avr update` learns about this computer and
// everything it does to it: which host this is, where the running binary is,
// what answers an HTTPS request, and the filesystem the binaries live in.
//
// It is one struct so that a flow test can replace the whole of it. The
// alternative — reaching for os.Executable, http.DefaultClient and os.Rename
// directly — would make a test of the successful path a test that downloads a
// release over the network and replaces the developer's own avr, which is the
// mistake docs/lessons.md records twice.
type updateHost struct {
	GOOS, GOARCH string
	// Executable reports the path avar was started from.
	Executable func() (string, error)
	// Resolve follows the links in a path, because both package managers
	// link to a file they keep elsewhere.
	Resolve func(string) string
	// Temporary reports a binary that will not be there later, which is
	// not one to install into (REQ-19.9).
	Temporary func(string) bool
	// HTTP performs the two or three requests an update makes.
	HTTP update.Doer
	// Ops is the filesystem the replacement uses.
	Ops update.FileOps
	// Repo is the repository releases come from.
	Repo string
}

// updateTimeout bounds the whole of an update's network work. A download that
// has stalled should end with a message the user can act on rather than a
// command that never returns.
const updateTimeout = 10 * time.Minute

// realUpdateHost is this computer.
func realUpdateHost() *updateHost {
	return &updateHost{
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		Executable: os.Executable,
		Resolve:    resolveLinks,
		Temporary:  inTemporaryDir,
		HTTP:       &http.Client{Timeout: updateTimeout},
		Ops:        update.OSFileOps{},
		Repo:       update.DefaultRepo,
	}
}

// host returns the computer `avr update` acts on, which is the real one unless
// a test replaced it.
func (a *App) updateComputer() *updateHost {
	if a.updateHost != nil {
		return a.updateHost
	}
	return realUpdateHost()
}

// runUpdate brings avar up to date, by doing it or by saying who can
// (REQ-19.1).
func runUpdate(ctx context.Context, app *App, inv cli.Invocation) error {
	if len(inv.SubcommandArgs) > 0 {
		return Exit(exitUsage, fmt.Errorf("`avr update` takes no arguments, got %q", strings.Join(inv.SubcommandArgs, " ")))
	}

	host := app.updateComputer()
	bin, err := host.Executable()
	if err != nil {
		return fmt.Errorf("find the avr that is running, which is the one an update would replace: %w", err)
	}
	install := update.Detect(host.GOOS, bin, host.Resolve(bin))

	if argv, ok := install.UpgradeCommand(); ok {
		printDelegatedUpdate(app, install, argv)
		return nil
	}
	return selfUpdate(ctx, app, host, install)
}

// printDelegatedUpdate says who owns this installation and what updates it.
//
// avar prints the command rather than running it. On Windows it could not run
// it: `winget upgrade` replaces avr.exe, and Windows cannot replace the image
// of a running program. On both hosts the manager may need elevation and ask
// its own questions, and its own error is the one that names the fix
// (REQ-19.2, REQ-19.3, design §3.12).
func printDelegatedUpdate(app *App, install update.Install, argv []string) {
	fmt.Fprintf(app.Out, "avar %s was installed with %s, which keeps its own record of the version it put here.\n", app.Version, install.Manager())
	fmt.Fprintf(app.Out, "Update it with:\n\n    %s\n\n", strings.Join(argv, " "))
	fmt.Fprintf(app.Out, "avar changed nothing and downloaded nothing: replacing a file %s owns would leave\n", install.Manager())
	fmt.Fprintf(app.Out, "%s describing a version that is not installed.\n", install.Manager())
	if install.Method == update.MethodWinget {
		fmt.Fprintf(app.Out, "winget may ask for administrator approval, and a shell that is already open keeps\n")
		fmt.Fprintf(app.Out, "the old avr.exe until you open a new one.\n")
	}
}

// selfUpdate replaces a binary avar owns with the current release (REQ-19.4 to
// REQ-19.7).
func selfUpdate(ctx context.Context, app *App, host *updateHost, install update.Install) error {
	dir := filepath.Dir(install.Path)

	// Whatever an earlier update left goes first, before this one stages
	// anything of its own. A file that is still mapped refuses to be
	// removed, which is fine: it is inert, and the next run gets it.
	for _, left := range update.Sweep(host.Ops, dir) {
		fmt.Fprintf(app.Err, "avr: removed %s, left by an earlier update.\n", filepath.Base(left))
	}

	if host.Temporary(install.Path) {
		return fmt.Errorf("update the avr in %s: it is a temporary folder, so whatever avar installed there would be deleted with it. "+
			"Install a release somewhere permanent and update that", dir)
	}
	current, err := update.ParseVersion(app.Version)
	if err != nil {
		return fmt.Errorf("update this avr: %w. This is a build from source rather than a release, "+
			"and replacing it with a release archive would swap it for a different program. "+
			"Install a release from https://github.com/%s/releases if you want one avar updates", err, host.Repo)
	}

	release, err := update.LatestRelease(ctx, host.HTTP, host.Repo, "avr/"+app.Version)
	if err != nil {
		return err
	}
	latest, err := release.Version()
	if err != nil {
		return err
	}
	if !current.Before(latest) {
		fmt.Fprintf(app.Out, "avar %s is the latest release. Nothing was downloaded.\n", current)
		return nil
	}

	archiveName, err := update.ArchiveName(host.GOOS, host.GOARCH, latest)
	if err != nil {
		return fmt.Errorf("find the release archive for this computer: %w", err)
	}
	archive, ok := release.Asset(archiveName)
	if !ok {
		return fmt.Errorf("release %s does not publish %s, which is the archive for this computer; it publishes %s",
			release.Tag, archiveName, strings.Join(release.AssetNames(), ", "))
	}
	sumsAsset, ok := release.Asset(update.ChecksumsAsset)
	if !ok {
		return fmt.Errorf("release %s publishes no %s, so avar cannot check that a download of %s is the file this release published",
			release.Tag, update.ChecksumsAsset, archiveName)
	}

	sums, err := update.FetchChecksums(ctx, host.HTTP, sumsAsset, "avr/"+app.Version)
	if err != nil {
		return err
	}
	want, err := update.ChecksumFor(sums, archiveName)
	if err != nil {
		return err
	}

	fmt.Fprintf(app.Out, "Updating avar %s to %s.\n", current, latest)
	downloaded, cleanup, err := downloadArchive(ctx, app, host, archive, want)
	if err != nil {
		return err
	}
	defer cleanup()

	members := update.Members(host.GOOS)
	targets := installTargets(install.Path, dir, members)
	if err := update.Extract(downloaded, members, func(member string) string {
		return targets[member] + update.NewSuffix
	}); err != nil {
		update.Discard(host.Ops, targetPaths(members, targets))
		// The new binary is written beside the installed one before
		// anything moves, so a directory avar may not write to fails
		// here, with everything still as it was. Elevation is the
		// usual reason, and REQ-19.8 wants it said rather than met
		// halfway through.
		if errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("%w. avar %s is still installed and was not changed; %s belongs to somebody else, so %s",
				err, current, dir, elevationAdvice(host.GOOS))
		}
		return fmt.Errorf("%w. avar %s is still installed and was not changed", err, current)
	}

	result, err := update.Replace(host.Ops, update.StyleFor(host.GOOS), targetPaths(members, targets))
	if err != nil {
		update.Discard(host.Ops, targetPaths(members, targets))
		if errors.Is(err, update.ErrPartiallyReplaced) {
			return fmt.Errorf("%w. Put that file back under its original name before running avr again", err)
		}
		return fmt.Errorf("%w. avar %s is still installed and was not changed", err, current)
	}

	printUpdated(app, host, current, latest, dir, result)
	return nil
}

// downloadArchive writes the release archive to a temporary file and returns
// its path, having checked it against the release's own checksum.
//
// The archive is downloaded somewhere avar will never run anything from, and
// is unpacked only after this returns without error (REQ-19.5, PROP-26).
func downloadArchive(ctx context.Context, app *App, host *updateHost, archive update.Asset, want string) (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "avr-update-")
	if err != nil {
		return "", nil, fmt.Errorf("make somewhere to download the release to: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	path = filepath.Join(dir, archive.Name)
	f, err := os.Create(path)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("download %s: %w", archive.Name, err)
	}

	fmt.Fprintf(app.Out, "Downloading %s.\n", archive.Name)
	err = update.DownloadVerified(ctx, host.HTTP, archive, "avr/"+app.Version, want, f)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		if errors.Is(err, update.ErrChecksumMismatch) {
			return "", nil, fmt.Errorf("%w. The download was discarded and avar was not changed", err)
		}
		return "", nil, err
	}
	return path, cleanup, nil
}

// elevationAdvice names the way this host runs a command with the rights to
// write into a directory somebody else owns (REQ-19.8).
func elevationAdvice(goos string) string {
	if goos == "windows" {
		return "run `avr update` from a terminal opened as administrator"
	}
	return "run `avr update` with the account that owns it, or install avar somewhere you own"
}

// installTargets maps each program in the archive to the file it replaces.
//
// Each is installed beside the running binary under its own name, which is
// where avar looks for avrw.exe and where a user's PATH finds avar.exe
// (REQ-19.7). The one exception is a binary saved under a name the archive
// does not have: then the first member replaces the file that is running,
// because that is the one the user invokes.
func installTargets(running, dir string, members []string) map[string]string {
	running = filepath.Clean(running)
	targets := make(map[string]string, len(members))
	named := false
	for _, member := range members {
		if strings.EqualFold(member, filepath.Base(running)) {
			targets[member], named = running, true
			continue
		}
		targets[member] = filepath.Join(dir, member)
	}
	if !named {
		targets[members[0]] = running
	}
	return targets
}

// targetPaths is the installed paths in the archive's own order, which is the
// order a replacement moves them in.
func targetPaths(members []string, targets map[string]string) []string {
	paths := make([]string, 0, len(members))
	for _, member := range members {
		paths = append(paths, targets[member])
	}
	return paths
}

// printUpdated says what changed, and the two things that are true afterwards
// and would otherwise puzzle somebody: a file left aside on Windows, and what
// Gatekeeper and SmartScreen do with a binary avar downloaded itself.
func printUpdated(app *App, host *updateHost, from, to update.Version, dir string, result update.Result) {
	fmt.Fprintf(app.Out, "\navar %s is installed in %s.\n", to, dir)
	fmt.Fprintf(app.Out, "It was checked against the release's own %s before anything was replaced.\n", update.ChecksumsAsset)
	if len(result.Replaced) > 1 {
		fmt.Fprintf(app.Out, "%s were replaced, all from that one archive.\n", andList(baseNames(result.Replaced)))
	}
	if len(result.Aside) > 0 {
		fmt.Fprintf(app.Out, "The avar %s programs are kept as %s and removed by a later avr, because Windows\n", from, andList(baseNames(result.Aside)))
		fmt.Fprintf(app.Out, "cannot delete the file of a program that is running.\n")
	}
	fmt.Fprintf(app.Out, "This avr is still the old one; the next one you run is %s.\n", to)

	// The binaries are unsigned, and what that means depends on how the
	// file arrived rather than on what it is. A file a browser downloads
	// is quarantined or marked; one avar downloaded itself is not.
	switch host.GOOS {
	case "darwin":
		fmt.Fprintf(app.Out, "\nReleased binaries are unsigned. avar downloaded this one itself, so it carries no\n")
		fmt.Fprintf(app.Out, "quarantine attribute and Gatekeeper does not stop it the way it stops an archive\n")
		fmt.Fprintf(app.Out, "downloaded in a browser.\n")
	case "windows":
		fmt.Fprintf(app.Out, "\nReleased binaries are unsigned. avar downloaded this one itself, so it carries no\n")
		fmt.Fprintf(app.Out, "mark of the web and SmartScreen does not warn the way it does for a file saved\n")
		fmt.Fprintf(app.Out, "from a browser.\n")
	}
}

// baseNames is the file names without their directories, for a sentence about
// what was replaced.
func baseNames(paths []string) []string {
	names := make([]string, 0, len(paths))
	for _, path := range paths {
		names = append(names, filepath.Base(path))
	}
	return names
}

// andList renders names as a sentence would: "a and b", "a, b and c".
func andList(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

// sweepUpdateLeftovers removes what an earlier update left beside the running
// binary.
//
// It is called from the scheduled idle check, which is avar's one regular
// background run and is never on the warm path REQ-17.1 budgets. Windows
// cannot delete the image of a running program, so the file `avr update`
// renames aside has to be deleted by some later run; this is that run
// (REQ-19.7).
//
// A binary in a temporary directory is skipped, which is also what keeps every
// test out of this: a `go test` binary runs from a temporary directory, and a
// sweep is the one part of an update that would otherwise reach a real
// directory from a test.
func sweepUpdateLeftovers(app *App) {
	host := app.updateComputer()
	bin, err := host.Executable()
	if err != nil {
		return
	}
	resolved := host.Resolve(bin)
	if host.Temporary(resolved) {
		return
	}
	update.Sweep(host.Ops, filepath.Dir(resolved))
}
