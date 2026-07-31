package envpolicy

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
)

// hostSecrets are the kind of variables a developer's shell really does have
// exported, named here so that a failure reads as the leak it would be.
var hostSecrets = map[string]string{
	"AWS_SECRET_ACCESS_KEY": "wJalrXUtnFEMI",
	"GITHUB_TOKEN":          "ghp_notarealtoken",
	"NPM_TOKEN":             "npm_notarealtoken",
	"SSH_AUTH_SOCK":         "/private/tmp/com.apple.launchd.abc/Listeners",
	"HOME":                  "/Users/dev",
	"PATH":                  "/opt/homebrew/bin:/usr/bin",
	"PWD":                   "/Users/dev/code/app",
}

func TestCompose_ForwardsOnlyTheTerminalAllowlist_REQ_9_1(t *testing.T) {
	host := map[string]string{
		"TERM":      "xterm-kitty",
		"LANG":      "en_GB.UTF-8",
		"LC_ALL":    "en_GB.UTF-8",
		"LC_TIME":   "en_GB.UTF-8",
		"COLORTERM": "truecolor",
	}
	for name, value := range hostSecrets {
		host[name] = value
	}

	got := Compose(Input{Host: host})

	want := map[string]string{
		"TERM":    "xterm-kitty",
		"LANG":    "en_GB.UTF-8",
		"LC_ALL":  "en_GB.UTF-8",
		"LC_TIME": "en_GB.UTF-8",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Compose() = %v, want %v", got, want)
	}
}

// The boundary is worth stating as its own case: a variable a user has exported
// on the host must be absent from the guest, not merely overwritten (PROP-4).
func TestCompose_HostSecretsAreAbsent_PROP_4(t *testing.T) {
	got := Compose(Input{Host: hostSecrets})

	for name := range hostSecrets {
		if value, present := got[name]; present {
			t.Errorf("host variable %s crossed into the guest with value %q", name, value)
		}
	}
}

func TestCompose_TerminalType_REQ_3_2(t *testing.T) {
	cases := []struct {
		name string
		host map[string]string
		want string
	}{
		{"host value passes through", map[string]string{"TERM": "xterm-256color"}, "xterm-256color"},
		{"an unusual but real terminal passes through", map[string]string{"TERM": "screen.xterm-new"}, "screen.xterm-new"},
		{"unset falls back to a colour-capable default", map[string]string{}, DefaultTERM},
		{"empty falls back", map[string]string{"TERM": ""}, DefaultTERM},
		{"whitespace falls back", map[string]string{"TERM": "  "}, DefaultTERM},
		{"dumb falls back", map[string]string{"TERM": "dumb"}, DefaultTERM},
		{"DUMB falls back", map[string]string{"TERM": "DUMB"}, DefaultTERM},
		{"a nil host still gets a terminal", nil, DefaultTERM},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compose(Input{Host: tc.host})
			if got["TERM"] != tc.want {
				t.Errorf("TERM = %q, want %q", got["TERM"], tc.want)
			}
		})
	}
}

// A name that carries the separator every transport uses to encode an
// environment cannot be carried safely, whatever it is called.
func TestCompose_RefusesNamesCarryingTheAssignmentSeparator(t *testing.T) {
	got := Compose(Input{Host: map[string]string{
		"LC_A=B": "x",
		"":       "y",
	}})

	if len(got) != 1 || got["TERM"] != DefaultTERM {
		t.Errorf("Compose() = %v, want only the default TERM", got)
	}
}

// PROP-4: for any host environment, no variable outside the base allowlist
// appears in the guest, and every variable that does appear carries the host's
// own value — TERM excepted, which has a rule of its own.
func TestProp_EnvironmentIsolationByDefault_PROP_4(t *testing.T) {
	property := func(host environment) bool {
		got := Compose(Input{Host: host})

		for name, value := range got {
			if !Allows(name) {
				t.Errorf("variable %q is outside the allowlist but reached the guest", name)
				return false
			}
			if name == termName {
				continue
			}
			if hostValue, ok := host[name]; !ok || hostValue != value {
				t.Errorf("variable %q reached the guest as %q, but the host had %q (present: %t)", name, value, hostValue, ok)
				return false
			}
		}

		// Nothing the allowlist admits is silently dropped either: a policy
		// that forwarded nothing would satisfy the check above.
		for name, value := range host {
			if !Allows(name) || name == termName {
				continue
			}
			if got[name] != value {
				t.Errorf("allowed variable %q did not reach the guest (got %q, want %q)", name, got[name], value)
				return false
			}
		}

		if got[termName] == "" || strings.EqualFold(got[termName], dumbTERM) {
			t.Errorf("TERM reached the guest as %q, which no full-screen program can use", got[termName])
			return false
		}
		return true
	}

	if err := quick.Check(property, &quick.Config{MaxCount: 2000}); err != nil {
		t.Error(err)
	}
}

// environment is a generated host environment. The generator is deliberate
// rather than random text: an arbitrary string almost never collides with an
// allowlisted name, so a purely random property would prove only that random
// names are refused. This one draws from names that are allowlisted, names
// that differ from an allowlisted one by a character or by case, and names
// that carry secrets.
type environment map[string]string

var generatedNames = []string{
	"TERM", "term", "Term", "TERMINAL", "TERM_PROGRAM", "XTERM",
	"LANG", "LANGUAGE", "lang", "LANGS",
	"LC_ALL", "LC_CTYPE", "LC_TIME", "LC_", "LC", "lc_all", "XLC_ALL", "LC_SECRET",
	"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "HOME", "PATH", "SSH_AUTH_SOCK",
	"", "=", "A=B", "LC_A=B", "TERM=x",
}

var generatedValues = []string{"", " ", "dumb", "DUMB", "xterm-256color", "en_GB.UTF-8", "secret", "a b\tc"}

// Generate implements quick.Generator.
func (environment) Generate(rand *rand.Rand, size int) reflect.Value {
	out := make(environment, size)
	for i := 0; i < size; i++ {
		name := generatedNames[rand.Intn(len(generatedNames))]
		out[name] = generatedValues[rand.Intn(len(generatedValues))]
	}
	return reflect.ValueOf(out)
}

func TestHostEnviron_DropsMalformedEntries(t *testing.T) {
	t.Setenv("AVR_TEST_ENVPOLICY", "value=with=equals")

	got := HostEnviron()

	if got["AVR_TEST_ENVPOLICY"] != "value=with=equals" {
		t.Errorf("HostEnviron()[AVR_TEST_ENVPOLICY] = %q, want %q", got["AVR_TEST_ENVPOLICY"], "value=with=equals")
	}
	if _, ok := got[""]; ok {
		t.Error("HostEnviron() produced a variable with an empty name")
	}
}
