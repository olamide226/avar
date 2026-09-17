package state

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olamide226/avar/internal/tomlsubset"
	"github.com/olamide226/avar/internal/tomlsubset/tomltest"
)

const testConfigPath = "/Users/dev/.avr/config.toml"

func disabled() Config {
	return Config{Path: testConfigPath, IdleTimeout: 0, IdleTimeoutSet: true}
}

// Files that the lenient readers read with the meaning their author intended
// must mean the same to the strict one. These are the shapes such files take:
// the README's own examples, and the forms the old readers' tests accepted.
func TestParseConfig_ReadsFilesWrittenForEarlierVersions_REQ_17_7(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want Config
	}{
		{"empty file", "", Config{Path: testConfigPath}},
		{"comments only", "# avar settings\n\n  # nothing yet\n", Config{Path: testConfigPath}},
		{"a timeout", "idle_timeout = \"2h\"\n", Config{Path: testConfigPath, IdleTimeout: 2 * time.Hour, IdleTimeoutSet: true}},
		{"a timeout in minutes", "idle_timeout = '90m'\n", Config{Path: testConfigPath, IdleTimeout: 90 * time.Minute, IdleTimeoutSet: true}},
		{"auto-stop off", "idle_timeout = \"0\"\n", disabled()},
		{"auto-stop off as a bare zero", "idle_timeout = 0\n", disabled()},
		{"auto-stop off as a negative duration", "idle_timeout = \"-1h\"\n", disabled()},
		{"forward_env on one line", `forward_env = ["AWS_PROFILE", "GITHUB_TOKEN"]`, Config{Path: testConfigPath, ForwardEnv: []string{"AWS_PROFILE", "GITHUB_TOKEN"}}},
		{"forward_env without spaces", `forward_env=["A","B"]`, Config{Path: testConfigPath, ForwardEnv: []string{"A", "B"}}},
		{"forward_env in single quotes", `forward_env = ['A', 'B']`, Config{Path: testConfigPath, ForwardEnv: []string{"A", "B"}}},
		{"an empty forward_env", `forward_env = []`, Config{Path: testConfigPath}},
		{"a name listed twice is forwarded once", `forward_env = ["A", "B", "A"]`, Config{Path: testConfigPath, ForwardEnv: []string{"A", "B"}}},
		{
			name: "both keys, forward_env over several lines with comments and a trailing comma",
			body: "# avar global settings\n" +
				"idle_timeout = \"4h\"\n" +
				"\n" +
				"forward_env = [\n" +
				"  \"AWS_PROFILE\",   # work account\n" +
				"  # \"NPM_TOKEN\",   # not today\n" +
				"  \"GITHUB_TOKEN\",\n" +
				"]\n",
			want: Config{Path: testConfigPath, IdleTimeout: 4 * time.Hour, IdleTimeoutSet: true, ForwardEnv: []string{"AWS_PROFILE", "GITHUB_TOKEN"}},
		},
		{"CRLF line endings", "idle_timeout = \"1h\"\r\nforward_env = [\"A\"]\r\n", Config{Path: testConfigPath, IdleTimeout: time.Hour, IdleTimeoutSet: true, ForwardEnv: []string{"A"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseConfig(testConfigPath, []byte(tc.body))
			if err != nil {
				t.Fatalf("ParseConfig(%q): %v", tc.body, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseConfig(%q) = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}

// Files the lenient readers misread, and silently: each now reads as its
// author meant it.
func TestParseConfig_ReadsWhatTheLenientReadersMisread_REQ_17_7(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want Config
	}{
		// The old reader matched only the literal prefix `idle_timeout = `,
		// so this was the two-hour default.
		{"no spaces around =", "idle_timeout=\"0\"\n", disabled()},
		{"a tab before =", "idle_timeout\t= \"0\"\n", disabled()},
		// The old reader kept the comment as part of the value, failed to parse
		// it as a duration, and used the default.
		{"a trailing comment", "idle_timeout = \"30m\" # half an hour\n", Config{Path: testConfigPath, IdleTimeout: 30 * time.Minute, IdleTimeoutSet: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseConfig(testConfigPath, []byte(tc.body))
			if err != nil {
				t.Fatalf("ParseConfig(%q): %v", tc.body, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseConfig(%q) = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}

// Everything the reader cannot read exactly is refused, naming the file, the
// line and the key, saying what is wrong and what to write instead. Every
// construct here was accepted by the lenient readers, and each was ignored,
// guessed at, or read as something its author did not write.
func TestParseConfig_RefusesWhatItCannotReadExactly_REQ_17_7(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		line  int
		wants []string
	}{
		{"a misspelt key", "idle_timout = \"0\"", 1, []string{`unknown key "idle_timout"`, "did you mean idle_timeout?"}},
		{"a hyphen for an underscore", `forward-env = ["A"]`, 1, []string{"did you mean forward_env?"}},
		{"a key avar has never had", `color = "auto"`, 1, []string{`unknown key "color"`, "understands idle_timeout, forward_env"}},
		{"distro, which config.toml does not support yet", `distro = "fedora"`, 1, []string{"distro is not supported in config.toml yet", `distro = "fedora"`, "avr --distro fedora", ".avr.toml"}},
		{"arch, which config.toml does not support yet", `arch = "amd64"`, 1, []string{"arch is not supported in config.toml yet", "avr --arch amd64"}},
		{"cpus, a project setting", "cpus = 4", 1, []string{"cpus is not supported in config.toml yet", ".avr.toml"}},
		{"memory, a project setting", `memory = "8GiB"`, 1, []string{"memory is not supported in config.toml yet"}},
		{"packages, a project setting", `packages = ["jq"]`, 1, []string{"packages is not supported in config.toml"}},
		{"a table, whose keys the old reader read as top-level", "[defaults]\nidle_timeout = \"0\"", 1, []string{"tables are not supported", "top-level key (idle_timeout, forward_env)"}},
		{"a dotted key", `defaults.idle_timeout = "0"`, 1, []string{"dotted keys are not supported"}},
		{"a key set twice, where the old readers took the first", "idle_timeout = \"1h\"\nidle_timeout = \"0\"", 2, []string{`"idle_timeout" is already set on line 1`}},
		{"an unquoted duration", "idle_timeout = 4h", 1, []string{"idle_timeout: 4h needs quotes", `idle_timeout = "4h"`}},
		{"an unquoted duration with a comment", "idle_timeout = 30m # short", 1, []string{"30m needs quotes", `idle_timeout = "30m"`}},
		{"a number with no unit", "idle_timeout = 4", 1, []string{"idle_timeout: 4 has no unit", `idle_timeout = "4h"`}},
		{"a quoted number with no unit", `idle_timeout = "4"`, 1, []string{`"4" is not a duration`, `idle_timeout = "30m"`, `idle_timeout = "0"`}},
		{"a duration avar cannot read", `idle_timeout = "two hours"`, 1, []string{`"two hours" is not a duration`}},
		{"an empty duration", `idle_timeout = ""`, 1, []string{`"" is not a duration`}},
		{"a list for idle_timeout", `idle_timeout = ["2h"]`, 1, []string{"idle_timeout: takes a quoted duration"}},
		{"a boolean for idle_timeout", `idle_timeout = false`, 1, []string{"a boolean"}},
		{"a single name for forward_env", `forward_env = "AWS_PROFILE"`, 1, []string{"forward_env: takes a list of quoted variable names", `forward_env = ["AWS_PROFILE", "GITHUB_TOKEN"]`}},
		{"unquoted names in forward_env", `forward_env = [AWS_PROFILE]`, 1, []string{"only quoted strings"}},
		{"a comma inside one quoted name", `forward_env = ["AWS_PROFILE,GITHUB_TOKEN"]`, 1, []string{`"AWS_PROFILE,GITHUB_TOKEN" is not a variable name`, "each variable as its own quoted name"}},
		{"a name that is not a variable name", `forward_env = ["MY-TOKEN"]`, 1, []string{`"MY-TOKEN" is not a variable name`}},
		{"an assignment in forward_env", `forward_env = ["A=b"]`, 1, []string{"not a variable name"}},
		{"an empty name in forward_env", `forward_env = ["A", ""]`, 1, []string{"not a variable name"}},
		{"a list that never closes", "forward_env = [\n  \"A\",\n", 3, []string{"not closed"}},
		{"an error inside a multi-line list names its line", "forward_env = [\n  \"A\",\n  B,\n]", 3, []string{"only quoted strings"}},
		{"an escape sequence", "idle_timeout = \"2\\x68\"", 1, []string{"escape sequences are not supported"}},
		{"no value", "idle_timeout =", 1, []string{"no value"}},
		{"no equals sign", "idle_timeout", 1, []string{"expected key = value"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConfig(testConfigPath, []byte(tc.body))
			if err == nil {
				t.Fatalf("ParseConfig(%q) accepted a file it must refuse", tc.body)
			}
			msg := err.Error()
			if want := testConfigPath + " line " + strconv.Itoa(tc.line) + ": "; !strings.HasPrefix(msg, want) {
				t.Errorf("error does not start with %q: %v", want, err)
			}
			for _, want := range tc.wants {
				if !strings.Contains(msg, want) {
					t.Errorf("error = %v, want it to mention %q", err, want)
				}
			}
		})
	}
}

func TestParseConfig_RefusesInvalidUTF8_REQ_17_7(t *testing.T) {
	if _, err := ParseConfig(testConfigPath, []byte("idle_timeout = \"\xff\"")); err == nil {
		t.Fatal("ParseConfig accepted a file that is not UTF-8")
	}
}

func TestStore_ConfigWithNoFileIsTheZeroConfig_REQ_17_7(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Config()
	if err != nil {
		t.Fatalf("a missing config.toml is not an error: %v", err)
	}
	if !reflect.DeepEqual(got, Config{}) {
		t.Errorf("Config() = %+v with no config.toml, want the zero Config", got)
	}
}

func TestStore_ConfigReadsTheFile_REQ_12_4(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(s.ConfigPath(), []byte("forward_env = [\"AWS_PROFILE\"]\n")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Config()
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Path: filepath.Join(s.Root(), "config.toml"), ForwardEnv: []string{"AWS_PROFILE"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Config() = %+v, want %+v", got, want)
	}
}

func TestStore_ConfigRefusesWhatIsNotAConfigurationFile_REQ_17_7(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, path string)
		want  string
	}{
		{"a directory", func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}, "is a directory"},
		{"an oversized file", func(t *testing.T, path string) {
			body := "# " + strings.Repeat("x", tomlsubset.MaxFileSize) + "\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "larger than 64 KiB, which no config.toml needs to be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			tc.setup(t, s.ConfigPath())
			_, err = s.Config()
			if err == nil || !strings.Contains(err.Error(), s.ConfigPath()) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Config() = %v, want an error naming %s and %q", err, s.ConfigPath(), tc.want)
			}
		})
	}
}

// Every config.toml the reader accepts means the same to a conforming TOML
// parser, as .avr.toml's does (see projconfig's test of the same name).
func TestParseConfig_AgreesWithAConformingTOMLParser_REQ_17_7(t *testing.T) {
	tomllib := tomltest.Parser(t)

	for _, body := range []string{
		"",
		"idle_timeout = \"2h\"\n",
		"idle_timeout = '0'\n",
		"idle_timeout = 0\n",
		"idle_timeout=\"45m\" # tight\r\n",
		"forward_env = [\"AWS_PROFILE\", 'GITHUB_TOKEN',]\n",
		"# avar global settings\nidle_timeout = \"4h\"\n\nforward_env = [\n  \"AWS_PROFILE\",   # work account\n  # \"NPM_TOKEN\",\n  \"GITHUB_TOKEN\",\n]\n",
	} {
		t.Run(body, func(t *testing.T) {
			cfg, err := ParseConfig(testConfigPath, []byte(body))
			if err != nil {
				t.Fatalf("ParseConfig(%q): %v", body, err)
			}
			theirs := tomllib(t, body)

			if cfg.IdleTimeoutSet != (theirs["idle_timeout"] != nil) {
				t.Errorf("for %q this reader set idle_timeout %t, and tomllib read %v", body, cfg.IdleTimeoutSet, theirs["idle_timeout"])
			}
			switch v := theirs["idle_timeout"].(type) {
			case string:
				if d, err := time.ParseDuration(v); err != nil || max(d, 0) != cfg.IdleTimeout {
					t.Errorf("for %q this reader understood idle_timeout %v and tomllib %q", body, cfg.IdleTimeout, v)
				}
			case float64:
				if v != 0 || cfg.IdleTimeout != 0 {
					t.Errorf("for %q this reader understood idle_timeout %v and tomllib %v", body, cfg.IdleTimeout, v)
				}
			}

			var ours any
			if cfg.ForwardEnv != nil {
				ours = tomltest.Strings(cfg.ForwardEnv)
			}
			if !reflect.DeepEqual(ours, theirs["forward_env"]) {
				t.Errorf("for %q this reader understood forward_env %v and tomllib %v", body, ours, theirs["forward_env"])
			}
		})
	}
}
