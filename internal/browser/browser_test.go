package browser

import (
	"context"
	"strings"
	"testing"
)

// Both host mechanisms open whatever they are handed with whatever application
// is registered for it, so anything that is not a web address is refused before
// it reaches them — and refused without starting anything, which is why these
// cases can run on a developer's machine.
func TestOpen_RefusesAnythingButAWebAddress_REQ_16_2(t *testing.T) {
	t.Parallel()

	for _, address := range []string{
		"",
		"localhost:3000",
		"/Applications/Calculator.app",
		`C:\Windows\System32\calc.exe`,
		"file:///etc/passwd",
		"javascript:alert(1)",
		"http://",
	} {
		err := System().Open(context.Background(), address)
		if err == nil {
			t.Errorf("Open(%q) was handed to the host", address)
			continue
		}
		if !strings.Contains(err.Error(), "in your browser") {
			t.Errorf("Open(%q) error %q does not say what was attempted", address, err)
		}
	}
}

func TestValidate_AcceptsTheAddressAvarBuilds_REQ_16_2(t *testing.T) {
	t.Parallel()

	for _, address := range []string{"http://localhost:3000", "https://localhost:8443/path?a=1&b=2"} {
		if err := validate(address); err != nil {
			t.Errorf("validate(%q) = %v, want nil", address, err)
		}
	}
}
