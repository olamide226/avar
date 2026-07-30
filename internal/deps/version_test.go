package deps

import (
	"strings"
	"testing"
)

// contains keeps the message assertions in this package readable.
func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func TestParseVersion_ToleratesRealLimactlOutputForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    Version
		wantStr string
		wantErr bool
	}{
		{
			name:    "limactl output as it is printed today",
			input:   "limactl version 1.0.4\n",
			want:    Version{Major: 1, Minor: 0, Patch: 4},
			wantStr: "1.0.4",
		},
		{
			name:    "bare version",
			input:   "1.0.0",
			want:    Version{Major: 1, Minor: 0, Patch: 0},
			wantStr: "1.0.0",
		},
		{
			name:    "v prefix",
			input:   "limactl version v1.2.3",
			want:    Version{Major: 1, Minor: 2, Patch: 3},
			wantStr: "1.2.3",
		},
		{
			name:    "pre-release suffix",
			input:   "limactl version 1.1.0-beta.1",
			want:    Version{Major: 1, Minor: 1, Patch: 0, Pre: "beta.1"},
			wantStr: "1.1.0-beta.1",
		},
		{
			name:    "build metadata suffix",
			input:   "limactl version 1.2.3+dirty",
			want:    Version{Major: 1, Minor: 2, Patch: 3, Build: "dirty"},
			wantStr: "1.2.3+dirty",
		},
		{
			name:    "pre-release and build metadata",
			input:   "limactl version v1.2.3-rc.2+g0abcdef",
			want:    Version{Major: 1, Minor: 2, Patch: 3, Pre: "rc.2", Build: "g0abcdef"},
			wantStr: "1.2.3-rc.2+g0abcdef",
		},
		{
			name:    "two-component version implies patch zero",
			input:   "limactl version 1.2",
			want:    Version{Major: 1, Minor: 2},
			wantStr: "1.2.0",
		},
		{
			name:    "double-digit minor is not confused for a fraction",
			input:   "limactl version 1.10.0",
			want:    Version{Major: 1, Minor: 10, Patch: 0},
			wantStr: "1.10.0",
		},
		{
			name:    "surrounding whitespace",
			input:   "\n   limactl version 1.0.4   \n\n",
			want:    Version{Major: 1, Minor: 0, Patch: 4},
			wantStr: "1.0.4",
		},
		{
			name:    "trailing punctuation and extra words",
			input:   "limactl version 1.0.4, built with go1.23.1 (darwin/arm64)",
			want:    Version{Major: 1, Minor: 0, Patch: 4},
			wantStr: "1.0.4",
		},
		{
			name:    "extra lines after the version",
			input:   "limactl version 1.3.0\nbuildinfo: go1.23.1 darwin/arm64\n",
			want:    Version{Major: 1, Minor: 3, Patch: 0},
			wantStr: "1.3.0",
		},
		{name: "empty output", input: "", wantErr: true},
		{name: "whitespace only", input: "  \n\t ", wantErr: true},
		{name: "no version anywhere", input: "limactl: command not found", wantErr: true},
		{name: "word where the version should be", input: "limactl version banana", wantErr: true},
		{name: "single number is not a version", input: "limactl version 1", wantErr: true},
		{name: "go version embedded in a name is not picked up", input: "built with go1.23.1", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseVersion(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseVersion(%q) = %v, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVersion(%q) returned unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ParseVersion(%q) = %+v, want %+v", tc.input, got, tc.want)
			}
			if got.String() != tc.wantStr {
				t.Errorf("ParseVersion(%q).String() = %q, want %q", tc.input, got.String(), tc.wantStr)
			}
		})
	}
}

func TestParseVersion_ErrorNamesTheOutputItCouldNotRead(t *testing.T) {
	t.Parallel()

	_, err := ParseVersion("limactl: no such tool")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !contains(err.Error(), "limactl: no such tool") {
		t.Errorf("error %q should quote the output it failed to parse", err)
	}

	// Unexpected output must not flood the terminal.
	flood := strings.Repeat("nonsense ", 400)
	_, err = ParseVersion(flood)
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(err.Error()) > 200 {
		t.Errorf("error message is %d bytes; unexpected output should be truncated", len(err.Error()))
	}
}

func TestVersion_CompareOrdersNumericallyNotLexically(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b string
		want int
	}{
		{name: "equal", a: "1.0.0", b: "1.0.0", want: 0},
		{name: "major", a: "2.0.0", b: "1.9.9", want: 1},
		{name: "minor 1.10.0 is newer than 1.9.0", a: "1.10.0", b: "1.9.0", want: 1},
		{name: "minor 1.9.0 is older than 1.10.0", a: "1.9.0", b: "1.10.0", want: -1},
		{name: "patch 1.0.10 is newer than 1.0.9", a: "1.0.10", b: "1.0.9", want: 1},
		{name: "double-digit major", a: "10.0.0", b: "9.0.0", want: 1},
		{name: "build metadata is ignored", a: "1.0.4+dirty", b: "1.0.4", want: 0},
		{name: "pre-release is older than its release", a: "1.0.0-beta.1", b: "1.0.0", want: -1},
		{name: "release is newer than its pre-release", a: "1.0.0", b: "1.0.0-rc.1", want: 1},
		{name: "pre-release of a newer minor beats an older release", a: "1.1.0-beta.1", b: "1.0.0", want: 1},
		{name: "numeric pre-release identifiers compare numerically", a: "1.0.0-beta.10", b: "1.0.0-beta.9", want: 1},
		{name: "alphanumeric pre-release identifiers compare in order", a: "1.0.0-rc.1", b: "1.0.0-beta.1", want: 1},
		{name: "fewer pre-release identifiers is older", a: "1.0.0-beta", b: "1.0.0-beta.1", want: -1},
		{name: "numeric pre-release ranks below alphanumeric", a: "1.0.0-1", b: "1.0.0-alpha", want: -1},
		{name: "identical pre-releases are equal", a: "1.0.0-beta.1", b: "1.0.0-beta.1", want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			a, b := mustParseVersion(tc.a), mustParseVersion(tc.b)
			if got := a.Compare(b); got != tc.want {
				t.Errorf("%s.Compare(%s) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
			// Comparison must be antisymmetric, or the version gate depends
			// on argument order.
			if got := b.Compare(a); got != -tc.want {
				t.Errorf("%s.Compare(%s) = %d, want %d", tc.b, tc.a, got, -tc.want)
			}
		})
	}
}

// The minimum itself must be accepted: a user on exactly MinLimaVersion is
// supported and must never be told to upgrade (REQ-8.1 vs REQ-8.4).
func TestVersion_AtLeastAcceptsTheMinimumItself_REQ_8_1(t *testing.T) {
	t.Parallel()

	// Cases are derived from the floor rather than spelling it out, so that
	// moving MinLimaVersion cannot leave this test asserting against a version
	// nobody supports any more.
	min := MinLima()
	bump := func(f func(*Version)) string {
		v := min
		f(&v)
		return v.String()
	}

	tests := []struct {
		version string
		want    bool
	}{
		{version: MinLimaVersion, want: true},
		{version: bump(func(v *Version) { v.Patch++ }), want: true},
		{version: bump(func(v *Version) { v.Minor += 9 }), want: true},
		{version: bump(func(v *Version) { v.Minor += 10 }), want: true},
		{version: bump(func(v *Version) { v.Major++ }), want: true},
		// Build metadata takes no part in precedence.
		{version: bump(func(v *Version) { v.Build = "dirty" }), want: true},
		// A pre-release precedes its own release, so it is below the floor.
		{version: bump(func(v *Version) { v.Pre = "beta.1" }), want: false},
		{version: bump(func(v *Version) { v.Major--; v.Minor = 23; v.Patch = 2 }), want: false},
		{version: bump(func(v *Version) { v.Major--; v.Minor = 9; v.Patch = 9 }), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.version, func(t *testing.T) {
			t.Parallel()

			if got := mustParseVersion(tc.version).AtLeast(MinLima()); got != tc.want {
				t.Errorf("%s.AtLeast(%s) = %t, want %t", tc.version, MinLimaVersion, got, tc.want)
			}
		})
	}
}

// MinLimaVersion is the single point of change for the version gate; a typo in
// it would panic at init, so pin it down here.
func TestMinLimaVersion_Parses(t *testing.T) {
	t.Parallel()

	if got := MinLima().String(); got != MinLimaVersion {
		t.Errorf("MinLima() = %q, want %q", got, MinLimaVersion)
	}
}
