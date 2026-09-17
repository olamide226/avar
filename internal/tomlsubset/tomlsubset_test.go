package tomlsubset

import (
	"path/filepath"
	"strings"
	"testing"
)

// The constructs the reader accepts and refuses are tested through the two
// schemas that use it, in internal/projconfig and internal/state, together with
// a comparison against a conforming TOML parser. What is tested here is what
// neither schema owns: how an unknown key is explained, and reading the file.

var testSchema = Schema{
	File: "test.toml",
	Keys: []Key{
		{Name: "idle_timeout", Kind: String},
		{Name: "forward_env", Kind: StringList},
		{Name: "cpus", Kind: Integer},
		{Name: "arch", Kind: String},
	},
	Unsupported: map[string]string{"distro": "distro is not supported here yet"},
}

func parse(body string) error {
	return Parse("/x/test.toml", []byte(body), testSchema, func(Setting) error { return nil })
}

func TestParse_SuggestsTheKeyATypoWasMeantToBe_REQ_17_7(t *testing.T) {
	for typo, want := range map[string]string{
		"idle_timout":  "idle_timeout",
		"idle-timeout": "idle_timeout",
		"idletimeout":  "idle_timeout",
		"Idle_Timeout": "idle_timeout",
		"forward_envs": "forward_env",
		"cpu":          "cpus",
		"acrh":         "",
		"arc":          "arch",
	} {
		t.Run(typo, func(t *testing.T) {
			err := parse(typo + " = 1")
			if err == nil {
				t.Fatalf("an unknown key %q was accepted", typo)
			}
			suggests := strings.Contains(err.Error(), "did you mean")
			switch {
			case want == "" && suggests:
				t.Errorf("suggested a key for %q, which is not a near miss: %v", typo, err)
			case want != "" && !strings.Contains(err.Error(), "did you mean "+want+"?"):
				t.Errorf("error = %v, want it to suggest %s", err, want)
			}
			if !strings.Contains(err.Error(), "understands idle_timeout, forward_env, cpus, arch") {
				t.Errorf("error does not list the keys the file understands: %v", err)
			}
		})
	}
}

func TestParse_DoesNotSuggestAnUnrelatedKey_REQ_17_7(t *testing.T) {
	for _, key := range []string{"color", "timeout", "env", "memory"} {
		err := parse(key + " = 1")
		if err == nil || strings.Contains(err.Error(), "did you mean") {
			t.Errorf("%s: error = %v, want an unknown key with no suggestion", key, err)
		}
		if err != nil && !strings.Contains(err.Error(), "a key from a newer avar needs a newer avr") {
			t.Errorf("%s: an unknown key with no near miss does not suggest upgrading: %v", key, err)
		}
	}
}

func TestParse_ExplainsAnUnsupportedKeyInsteadOfCallingItUnknown(t *testing.T) {
	err := parse(`distro = "fedora"`)
	if err == nil || err.Error() != "/x/test.toml line 1: distro is not supported here yet" {
		t.Errorf("error = %v, want the schema's own explanation", err)
	}
}

// An unquoted word where a string belongs is shown quoted, because that is
// almost always what was meant; the same word for a key that takes something
// else, or a construct TOML names, is described as what it is.
func TestParse_ShowsAnUnquotedStringQuoted(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"idle_timeout = 30m", `idle_timeout: 30m needs quotes: write a quoted string, as in idle_timeout = "30m"`},
		{"idle_timeout = -1h # comment", `idle_timeout: -1h needs quotes: write a quoted string, as in idle_timeout = "-1h"`},
		{"arch = amd64", `arch: amd64 needs quotes: write a quoted string, as in arch = "amd64"`},
		{"arch = true", "arch: a boolean is not a value this file accepts"},
		{"arch = {a = 1}", "arch: an inline table is not a value this file accepts"},
		{"cpus = 4x", `cpus: "4x" is not a number this file accepts`},
	} {
		err := parse(tc.body)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want it to contain %q", tc.body, err, tc.want)
		}
	}
}

func TestReadFile_AbsentFileIsNotFoundAndNoError(t *testing.T) {
	body, found, err := ReadFile(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil || found || body != nil {
		t.Errorf("ReadFile of a missing file = %q, %t, %v; want nothing, not found, no error", body, found, err)
	}
}
