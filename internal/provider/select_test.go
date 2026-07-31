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
		// Windows routes to WSL2Provider only once Phase 4 builds it. Claiming
		// it earlier would replace a clear "unsupported host" message with a
		// failure much further in.
		{goos: "windows", wantErr: true},
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

// An unsupported host must be told what avar does support, not merely that it
// failed (REQ-17.6, and the house error style in design §6).
func TestProviderIDFor_UnsupportedHostSaysWhatIsSupported_REQ_17_6(t *testing.T) {
	t.Parallel()

	_, err := providerIDFor("windows")
	if err == nil {
		t.Fatal("providerIDFor(\"windows\") = nil error, want an error")
	}
	for _, want := range []string{"Windows", "macOS", "WSL 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestHostProviderID_MatchesTheRunningHost(t *testing.T) {
	t.Parallel()

	got, err := HostProviderID()
	if runtime.GOOS != "darwin" {
		if err == nil {
			t.Fatalf("HostProviderID() = %q on %s, want an error", got, runtime.GOOS)
		}
		return
	}
	if err != nil {
		t.Fatalf("HostProviderID() returned an unexpected error on darwin: %v", err)
	}
	if got != types.ProviderLima {
		t.Errorf("HostProviderID() = %q, want %q", got, types.ProviderLima)
	}
}

func TestSupportedHost(t *testing.T) {
	t.Parallel()

	if !SupportedHost("darwin") {
		t.Error("SupportedHost(\"darwin\") = false, want true")
	}
	if SupportedHost("windows") {
		t.Error("SupportedHost(\"windows\") = true, want false until Phase 4 builds the WSL2Provider")
	}
}

// The provider a host routes to must be one types.ValidateProviderID accepts,
// or the resolver would reject a target avar itself selected.
func TestProviderForHost_ReturnsValidProviderIDs(t *testing.T) {
	t.Parallel()

	for _, goos := range []string{"darwin"} {
		id, ok := providerForHost(goos)
		if !ok {
			t.Fatalf("providerForHost(%q) reported no provider", goos)
		}
		if err := types.ValidateProviderID(id); err != nil {
			t.Errorf("provider %q for host %q is not a valid ProviderID: %v", id, goos, err)
		}
	}
}
