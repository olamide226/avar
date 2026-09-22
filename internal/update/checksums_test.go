package update

import (
	"errors"
	"strings"
	"testing"
)

// The real checksums.txt of release v0.12.12, which is where the format comes
// from: a lower-case SHA-256, two spaces, the file's name.
const realChecksums = `f5e8b9f794e437a8bad41a42034685ff1415ae81291b9676fbdbd148f54a3076  avar_0.12.12_darwin_all.tar.gz
0d7450979cc6f1a03374e61c9ed5efa6d1e6c2f5e2a1d7c5bd1a5cf1d9f4f4e1  avar_0.12.12_windows_amd64.zip
`

func TestParseChecksums_ReadsTheReleaseFormat_REQ_19_5(t *testing.T) {
	sums, err := ParseChecksums([]byte(realChecksums))
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}
	if len(sums) != 2 {
		t.Fatalf("read %d checksums, want 2: %v", len(sums), sums)
	}
	if got := sums["avar_0.12.12_darwin_all.tar.gz"]; got != "f5e8b9f794e437a8bad41a42034685ff1415ae81291b9676fbdbd148f54a3076" {
		t.Errorf("macOS checksum = %q, want the one in the file", got)
	}
}

// A line the reader cannot make sense of is refused rather than skipped. A
// skipped line becomes a file with no checksum, which is indistinguishable
// from a release that published none — and avar's answer to "no checksum" has
// to be a refusal (docs/lessons.md, "A lenient reader…").
func TestParseChecksums_RefusesWhatItCannotRead_REQ_19_5(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty", ""},
		{"only whitespace", "\n  \n"},
		{"no file name", "f5e8b9f794e437a8bad41a42034685ff1415ae81291b9676fbdbd148f54a3076\n"},
		{"one space instead of two", "f5e8b9f794e437a8bad41a42034685ff1415ae81291b9676fbdbd148f54a3076 avar.tar.gz\n"},
		{"checksum is not hexadecimal", "zzzzb9f794e437a8bad41a42034685ff1415ae81291b9676fbdbd148f54a3076  avar.tar.gz\n"},
		{"checksum is too short", "f5e8b9f7  avar.tar.gz\n"},
		{"an html error page", "<html><head><title>404</title></head></html>\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if sums, err := ParseChecksums([]byte(tt.body)); err == nil {
				t.Errorf("ParseChecksums(%q) = %v, want a refusal", tt.body, sums)
			}
		})
	}
}

func TestParseChecksums_AcceptsWindowsLineEndings(t *testing.T) {
	sums, err := ParseChecksums([]byte(strings.ReplaceAll(realChecksums, "\n", "\r\n")))
	if err != nil {
		t.Fatalf("ParseChecksums with CRLF: %v", err)
	}
	if _, ok := sums["avar_0.12.12_windows_amd64.zip"]; !ok {
		t.Errorf("read %v, want the Windows archive's name without its carriage return", sums)
	}
}

// A file the checksums do not mention cannot be verified, and an unverifiable
// download is refused exactly as a bad one is.
func TestChecksumFor_RefusesAFileTheReleaseDidNotRecord_REQ_19_5(t *testing.T) {
	sums, err := ParseChecksums([]byte(realChecksums))
	if err != nil {
		t.Fatalf("ParseChecksums: %v", err)
	}
	if _, err := ChecksumFor(sums, "avar_0.12.12_windows_arm64.zip"); !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("ChecksumFor on an unlisted file = %v, want ErrChecksumMismatch", err)
	}
	if _, err := ChecksumFor(sums, "avar_0.12.12_windows_amd64.zip"); err != nil {
		t.Errorf("ChecksumFor on a listed file = %v, want the recorded checksum", err)
	}
}
