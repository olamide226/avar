package provider

import (
	"runtime"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/types"
)

func TestProviderIDFor_RoutesByHost_REQ_18_1(t *testing.T) {
	t.Parallel()

	tests := []struct {
		goos    string
		want    types.ProviderID
		wantErr bool
	}{
		{goos: "darwin", want: types.ProviderLima},
		{goos: "windows", want: types.ProviderWSL2},
		// avar has no Linux-host backend: a Linux user already has Linux, and
		// running one inside another is not what avar is for (REQ-17.6).
		{goos: "linux", wantErr: true},
		{goos: "plan9", wantErr: true},
		{goos: "", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.goos, func(t *testing.T) {
			t.Parallel()

			got, err := providerIDFor(tc.goos)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("providerIDFor(%q) = %q, want an error", tc.goos, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("providerIDFor(%q) returned an unexpected error: %v", tc.goos, err)
			}
			if got != tc.want {
				t.Errorf("providerIDFor(%q) = %q, want %q", tc.goos, got, tc.want)
			}
		})
	}
}

// The two supported hosts route to two different backends. Asserting it directly
// is what keeps a refactor from quietly collapsing them: a Windows user sent to
// the Lima backend would be told to install Homebrew (PROP-13).
func TestProviderIDFor_EachHostGetsItsOwnBackend_REQ_18_1(t *testing.T) {
	t.Parallel()

	mac, err := providerIDFor("darwin")
	if err != nil {
		t.Fatalf("providerIDFor(darwin): %v", err)
	}
	windows, err := providerIDFor("windows")
	if err != nil {
		t.Fatalf("providerIDFor(windows): %v", err)
	}
	if mac == windows {
		t.Errorf("both hosts route to %q; each has its own runtime and its own dependency", mac)
	}
}

// An unsupported host must be told what avar does support, not merely that it
// failed (REQ-17.6, and the house error style in design §6).
func TestProviderIDFor_UnsupportedHostSaysWhatIsSupported_REQ_17_6(t *testing.T) {
	t.Parallel()

	_, err := providerIDFor("linux")
	if err == nil {
		t.Fatal("providerIDFor(\"linux\") = nil error, want an error")
	}
	for _, want := range []string{"Linux", "macOS", "Windows"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestHostProviderID_MatchesTheRunningHost(t *testing.T) {
	t.Parallel()

	want := map[string]types.ProviderID{
		"darwin":  types.ProviderLima,
		"windows": types.ProviderWSL2,
	}[runtime.GOOS]

	got, err := HostProviderID()
	if want == "" {
		if err == nil {
			t.Fatalf("HostProviderID() = %q on %s, want an error", got, runtime.GOOS)
		}
		return
	}
	if err != nil {
		t.Fatalf("HostProviderID() returned an unexpected error on %s: %v", runtime.GOOS, err)
	}
	if got != want {
		t.Errorf("HostProviderID() = %q, want %q", got, want)
	}
}

func TestSupportedHost(t *testing.T) {
	t.Parallel()

	if !SupportedHost("darwin") {
		t.Error("SupportedHost(\"darwin\") = false, want true")
	}
	if !SupportedHost("windows") {
		t.Error("SupportedHost(\"windows\") = false, want true now that the WSL2Provider exists")
	}
	if SupportedHost("linux") {
		t.Error("SupportedHost(\"linux\") = true, want false: avar has no Linux-host backend")
	}
}

// The provider a host routes to must be one types.ValidateProviderID accepts,
// or the resolver would reject a target avar itself selected.
func TestProviderForHost_ReturnsValidProviderIDs(t *testing.T) {
	t.Parallel()

	for _, goos := range []string{"darwin", "windows"} {
		id, ok := providerForHost(goos)
		if !ok {
			t.Fatalf("providerForHost(%q) reported no provider", goos)
		}
		if err := types.ValidateProviderID(id); err != nil {
			t.Errorf("provider %q for host %q is not a valid ProviderID: %v", id, goos, err)
		}
	}
}
