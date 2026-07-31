package types

import "testing"

func TestValidateProviderID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      ProviderID
		wantErr bool
	}{
		{name: "the MVP backend", id: ProviderLima},
		{name: "a backend avar does not ship yet", id: ProviderID("wsl2")},
		{name: "the test double", id: ProviderID("fake")},
		{name: "empty", id: ProviderID(""), wantErr: true},
		{name: "blank", id: ProviderID("  "), wantErr: true},
		{name: "upper case", id: ProviderID("Lima"), wantErr: true},
		{name: "a single character is too short to read", id: ProviderID("l"), wantErr: true},
		{name: "separators are not part of an id", id: ProviderID("lima-vm"), wantErr: true},
		{name: "path traversal", id: ProviderID("../lima"), wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateProviderID(tc.id)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateProviderID(%q) = nil, want error", tc.id)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateProviderID(%q) = %v, want nil", tc.id, err)
			}
		})
	}
}
