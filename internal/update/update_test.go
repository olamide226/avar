package update

import (
	"strings"
	"testing"
)

// Detection is what keeps avar from overwriting a file a package manager owns,
// so the paths here are the real ones: a Homebrew cask stages into
// <prefix>/Caskroom/<cask>/<version>/ and links from <prefix>/bin, and winget
// unpacks a portable package into
// %LOCALAPPDATA%\Microsoft\WinGet\Packages\<id>_<source>\ and links from
// ...\WinGet\Links.
func TestDetect_NamesTheOwnerOfTheBinary_REQ_19_1(t *testing.T) {
	tests := []struct {
		name       string
		goos       string
		invoked    string
		resolved   string
		wantMethod Method
		wantName   string
	}{
		{
			name:       "homebrew cask reached through its link",
			goos:       "darwin",
			invoked:    "/opt/homebrew/bin/avr",
			resolved:   "/opt/homebrew/Caskroom/avar/0.12.12/avr",
			wantMethod: MethodHomebrewCask,
			wantName:   "avar",
		},
		{
			name:       "homebrew cask on an Intel prefix",
			goos:       "darwin",
			invoked:    "/usr/local/Caskroom/avar/0.12.12/avr",
			resolved:   "/usr/local/Caskroom/avar/0.12.12/avr",
			wantMethod: MethodHomebrewCask,
			wantName:   "avar",
		},
		{
			name:       "a cask is still a cask when the link cannot be resolved",
			goos:       "darwin",
			invoked:    "/opt/homebrew/Caskroom/avar/0.12.12/avr",
			resolved:   "",
			wantMethod: MethodHomebrewCask,
			wantName:   "avar",
		},
		{
			name:       "archive install on macOS",
			goos:       "darwin",
			invoked:    "/usr/local/bin/avr",
			resolved:   "/usr/local/bin/avr",
			wantMethod: MethodArchive,
		},
		{
			name:       "Caskroom means nothing on Windows",
			goos:       "windows",
			invoked:    `C:\Caskroom\avar\avr.exe`,
			resolved:   `C:\Caskroom\avar\avr.exe`,
			wantMethod: MethodArchive,
		},
		{
			name:       "winget package directory",
			goos:       "windows",
			invoked:    `C:\Users\dev\AppData\Local\Microsoft\WinGet\Links\avr.exe`,
			resolved:   `C:\Users\dev\AppData\Local\Microsoft\WinGet\Packages\olamide226.avar_Microsoft.Winget.Source_8wekyb3d8bbwe\avr.exe`,
			wantMethod: MethodWinget,
			wantName:   "olamide226.avar",
		},
		{
			name:       "winget link whose target could not be resolved",
			goos:       "windows",
			invoked:    `C:\Users\dev\AppData\Local\Microsoft\WinGet\Links\avr.exe`,
			resolved:   `C:\Users\dev\AppData\Local\Microsoft\WinGet\Links\avr.exe`,
			wantMethod: MethodWinget,
			wantName:   "olamide226.avar",
		},
		{
			name:       "an unzipped archive on Windows is avar's to replace",
			goos:       "windows",
			invoked:    `C:\Tools\avar\avr.exe`,
			resolved:   `C:\Tools\avar\avr.exe`,
			wantMethod: MethodArchive,
		},
		{
			name:       "winget is not a package manager avar looks for on macOS",
			goos:       "darwin",
			invoked:    "/home/dev/WinGet/Packages/olamide226.avar_x/avr",
			resolved:   "/home/dev/WinGet/Packages/olamide226.avar_x/avr",
			wantMethod: MethodArchive,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Detect(tt.goos, tt.invoked, tt.resolved)
			if got.Method != tt.wantMethod {
				t.Errorf("Detect(%s, %q, %q).Method = %s, want %s", tt.goos, tt.invoked, tt.resolved, got.Method, tt.wantMethod)
			}
			if got.Name != tt.wantName {
				t.Errorf("Detect(...).Name = %q, want %q", got.Name, tt.wantName)
			}
			want := tt.resolved
			if want == "" {
				want = tt.invoked
			}
			if got.Path != want {
				t.Errorf("Detect(...).Path = %q, want the resolved path %q", got.Path, want)
			}
		})
	}
}

// The command avar prints is the one the manager that owns the binary
// understands, and an archive install has none because avar does that job
// itself.
func TestUpgradeCommand_IsTheOwningManagersOwn_REQ_19_2_REQ_19_3(t *testing.T) {
	cask := Detect("darwin", "/opt/homebrew/bin/avr", "/opt/homebrew/Caskroom/avar/0.12.12/avr")
	if argv, ok := cask.UpgradeCommand(); !ok || strings.Join(argv, " ") != "brew upgrade --cask avar" {
		t.Errorf("cask upgrade command = %q (ok=%t), want `brew upgrade --cask avar`", argv, ok)
	}
	if cask.Manager() != "Homebrew" {
		t.Errorf("cask manager = %q, want Homebrew", cask.Manager())
	}

	winget := Detect("windows", `C:\WinGet\Links\avr.exe`, `C:\Users\d\AppData\Local\Microsoft\WinGet\Packages\olamide226.avar_x\avr.exe`)
	if argv, ok := winget.UpgradeCommand(); !ok || strings.Join(argv, " ") != "winget upgrade olamide226.avar" {
		t.Errorf("winget upgrade command = %q (ok=%t), want `winget upgrade olamide226.avar`", argv, ok)
	}

	archive := Detect("darwin", "/usr/local/bin/avr", "/usr/local/bin/avr")
	if argv, ok := archive.UpgradeCommand(); ok {
		t.Errorf("an archive install reported the upgrade command %q; it is avar's own job", argv)
	}
	if archive.Manager() != "" {
		t.Errorf("an archive install named %q as its manager, and nothing owns it", archive.Manager())
	}
}

// A version that is not exactly a release version is how avar knows this
// binary is not a release it may replace (REQ-19.9).
func TestParseVersion_AcceptsReleaseVersionsOnly_REQ_19_9(t *testing.T) {
	for _, in := range []string{"1.2.3", "v1.2.3", "0.12.12", " v0.12.12 "} {
		if _, err := ParseVersion(in); err != nil {
			t.Errorf("ParseVersion(%q) = %v, want it accepted", in, err)
		}
	}
	for _, in := range []string{"dev", "", "v0.12.12-3-gabc1234", "0.12.12-dirty", "1.2", "1.2.3.4", "v1.2.x", "latest"} {
		if v, err := ParseVersion(in); err == nil {
			t.Errorf("ParseVersion(%q) = %v, want a refusal: a build with that version is not a release avar may replace", in, v)
		}
	}
}

func TestVersion_Before(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"1.2.3", "1.2.4", true},
		{"1.2.3", "1.3.0", true},
		{"1.2.3", "2.0.0", true},
		{"1.2.3", "1.2.3", false},
		{"1.3.0", "1.2.9", false},
		{"2.0.0", "10.0.0", true},
		{"0.9.0", "0.10.0", true},
	}
	for _, tt := range tests {
		a, err := ParseVersion(tt.a)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", tt.a, err)
		}
		b, err := ParseVersion(tt.b)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", tt.b, err)
		}
		if got := a.Before(b); got != tt.want {
			t.Errorf("%s.Before(%s) = %t, want %t", tt.a, tt.b, got, tt.want)
		}
	}
}

// The archive names are the ones .goreleaser.yaml produces, checked against
// the assets of a real release (v0.12.12): the version is written without its
// leading v, macOS ships one universal archive, and Windows ships one per
// architecture.
func TestArchiveName_MatchesWhatTheReleasePublishes_REQ_19_5(t *testing.T) {
	v, err := ParseVersion("v0.12.12")
	if err != nil {
		t.Fatalf("ParseVersion: %v", err)
	}
	tests := []struct {
		goos, goarch, want string
	}{
		{"darwin", "arm64", "avar_0.12.12_darwin_all.tar.gz"},
		{"darwin", "amd64", "avar_0.12.12_darwin_all.tar.gz"},
		{"windows", "amd64", "avar_0.12.12_windows_amd64.zip"},
		{"windows", "arm64", "avar_0.12.12_windows_arm64.zip"},
	}
	for _, tt := range tests {
		got, err := ArchiveName(tt.goos, tt.goarch, v)
		if err != nil {
			t.Errorf("ArchiveName(%s, %s): %v", tt.goos, tt.goarch, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ArchiveName(%s, %s) = %q, want %q", tt.goos, tt.goarch, got, tt.want)
		}
	}
	for _, tt := range []struct{ goos, goarch string }{{"linux", "amd64"}, {"windows", "386"}} {
		if name, err := ArchiveName(tt.goos, tt.goarch, v); err == nil {
			t.Errorf("ArchiveName(%s, %s) = %q, want a refusal: avar publishes no such archive", tt.goos, tt.goarch, name)
		}
	}
}

// Every program the Windows archive ships moves or none does. A stale
// avrw.exe is the broken idle check of #100, and a stale avar.exe answers an
// older version to anybody who types the product's own name (REQ-18.17).
func TestMembers_WindowsCarriesEveryProgramItShips_REQ_19_7(t *testing.T) {
	if got := Members("windows"); strings.Join(got, " ") != "avr.exe avar.exe avrw.exe" {
		t.Errorf("Members(windows) = %q, want avr.exe, avar.exe and avrw.exe", got)
	}
	if got := Members("darwin"); strings.Join(got, " ") != "avr" {
		t.Errorf("Members(darwin) = %q, want avr alone", got)
	}
}
