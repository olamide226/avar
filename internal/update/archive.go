package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// The names of the programs a release ships. The Windows archive holds three,
// and an update that replaced only the first would leave the other two on the
// version before it: avrw.exe is the windowless idle check (REQ-18.16), which
// is the broken check of #100 once it is a release behind, and avar.exe is the
// same command under the product's name (REQ-18.17), which would answer an
// older version to anybody who types it.
const (
	binaryUnix    = "avr"
	binaryWindows = "avr.exe"
	aliasWindows  = "avar.exe"
	helperWindows = "avrw.exe"
)

// Members lists the files inside a release archive that avar installs. The
// running binary's own name comes first.
func Members(goos string) []string {
	if goos == "windows" {
		return []string{binaryWindows, aliasWindows, helperWindows}
	}
	return []string{binaryUnix}
}

// ArchiveName is the release archive that carries this host's binaries, named
// the way .goreleaser.yaml names it: the version without its leading "v", the
// operating system, and either the architecture or `all` for the macOS
// universal binary.
//
// The name is computed and then looked up in the release's own asset list
// rather than turned straight into a URL, so a release that stopped publishing
// this host's archive is an error naming what was looked for instead of a 404
// somewhere further on.
func ArchiveName(goos, goarch string, v Version) (string, error) {
	switch goos {
	case "darwin":
		return fmt.Sprintf("avar_%s_darwin_all.tar.gz", v), nil
	case "windows":
		switch goarch {
		case "amd64", "arm64":
			return fmt.Sprintf("avar_%s_windows_%s.zip", v, goarch), nil
		default:
			return "", fmt.Errorf("avar publishes no Windows build for %s", goarch)
		}
	default:
		return "", fmt.Errorf("avar publishes no build for %s, so there is nothing to update to", goos)
	}
}

// Extract writes the named members of an archive to the paths target gives for
// them, with the executable bit set.
//
// Members are matched on their exact base name and written to paths the caller
// computed, so nothing an archive says about where a file should go is obeyed:
// an entry named ../../etc/something is simply not one of the names avar
// asked for. A member the archive does not carry is an error, and any file
// already written is removed, because half an update is worse than none.
func Extract(archive string, members []string, target func(member string) string) error {
	wanted := make(map[string]string, len(members))
	for _, m := range members {
		wanted[m] = target(m)
	}

	written, err := extractInto(archive, wanted)
	if err == nil && len(written) != len(wanted) {
		err = fmt.Errorf("%s does not contain %s", filepath.Base(archive), strings.Join(missing(wanted, written), " and "))
	}
	if err != nil {
		for _, written := range written {
			_ = os.Remove(written)
		}
		return err
	}
	return nil
}

// extractInto copies each wanted member out of the archive, returning the
// paths it wrote.
func extractInto(archive string, wanted map[string]string) (map[string]string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, fmt.Errorf("open the downloaded archive: %w", err)
	}
	defer f.Close()

	switch {
	case strings.HasSuffix(archive, ".tar.gz"):
		return extractTarGz(f, wanted)
	case strings.HasSuffix(archive, ".zip"):
		info, err := f.Stat()
		if err != nil {
			return nil, fmt.Errorf("read the downloaded archive: %w", err)
		}
		return extractZip(f, info.Size(), wanted)
	default:
		return nil, fmt.Errorf("avar does not know how to unpack %s", filepath.Base(archive))
	}
}

func extractTarGz(f io.Reader, wanted map[string]string) (map[string]string, error) {
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("read the downloaded archive: %w", err)
	}
	defer gz.Close()

	written := make(map[string]string, len(wanted))
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return written, nil
		}
		if err != nil {
			return written, fmt.Errorf("read the downloaded archive: %w", err)
		}
		dest, ok := wanted[path.Base(header.Name)]
		if !ok || header.Typeflag != tar.TypeReg {
			continue
		}
		if _, done := written[path.Base(header.Name)]; done {
			continue
		}
		if err := writeExecutable(dest, tr); err != nil {
			return written, err
		}
		written[path.Base(header.Name)] = dest
	}
}

func extractZip(f io.ReaderAt, size int64, wanted map[string]string) (map[string]string, error) {
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return nil, fmt.Errorf("read the downloaded archive: %w", err)
	}

	written := make(map[string]string, len(wanted))
	for _, entry := range zr.File {
		name := path.Base(entry.Name)
		dest, ok := wanted[name]
		if !ok || entry.FileInfo().IsDir() {
			continue
		}
		if _, done := written[name]; done {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return written, fmt.Errorf("read %s from the downloaded archive: %w", name, err)
		}
		err = writeExecutable(dest, rc)
		rc.Close()
		if err != nil {
			return written, err
		}
		written[name] = dest
	}
	return written, nil
}

// writeExecutable writes one member beside the binary it will replace.
//
// It is written into the installed directory rather than a temporary one for
// two reasons: the rename that follows is only atomic within one filesystem,
// and a directory avar may not write to fails here, before anything has moved
// (design §3.12).
func writeExecutable(dest string, r io.Reader) error {
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("write the new binary to %s: %w", filepath.Dir(dest), err)
	}
	n, err := io.Copy(f, io.LimitReader(r, maxArchiveBytes+1))
	if err == nil && n > maxArchiveBytes {
		err = fmt.Errorf("%s is larger than the %d MiB avar will unpack", filepath.Base(dest), maxArchiveBytes>>20)
	}
	if err == nil {
		// The bytes have to be on disk before the file is renamed into
		// place: a crash between the two would otherwise leave an empty
		// avr where a working one was (REQ-17.5).
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

// missing names the members an archive did not carry.
func missing(wanted, written map[string]string) []string {
	var names []string
	for name := range wanted {
		if _, ok := written[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}
