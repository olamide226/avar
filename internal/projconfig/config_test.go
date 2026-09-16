package projconfig

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/types"
)

const testPath = "/work/app/.avr.toml"

func TestParse_Schema_REQ_15_1(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want Config
	}{
		{
			name: "empty file is valid and asks for nothing",
			body: "",
			want: Config{Path: testPath},
		},
		{
			name: "comments and blank lines only",
			body: "# nothing here\n\n   # indented comment\n",
			want: Config{Path: testPath},
		},
		{
			name: "distro without a version",
			body: `distro = "fedora"`,
			want: Config{Path: testPath, Distro: types.DistroFedora},
		},
		{
			name: "distro with a version, as --distro takes it",
			body: `distro = "debian:13"`,
			want: Config{Path: testPath, Distro: types.DistroDebian, Version: "13"},
		},
		{
			name: "arch",
			body: `arch = "amd64"`,
			want: Config{Path: testPath, Arch: types.ArchAMD64},
		},
		{
			name: "both, literal strings, trailing comments, CRLF line endings",
			body: "distro = 'ubuntu:24.04' # the LTS\r\narch='arm64'#native\r\n",
			want: Config{Path: testPath, Distro: types.DistroUbuntu, Version: "24.04", Arch: types.ArchARM64},
		},
		{
			name: "a # inside a string is part of the string, not a comment",
			body: `distro = "ubuntu#x"`,
			want: Config{Path: testPath, Distro: types.Distro("ubuntu#x")},
		},
		{
			name: "cpus and memory in GiB",
			body: "cpus = 8\nmemory = \"16GiB\"",
			want: Config{Path: testPath, CPUs: 8, MemoryMiB: 16 * 1024},
		},
		{
			name: "memory in MiB",
			body: `memory = "1536MiB"`,
			want: Config{Path: testPath, MemoryMiB: 1536},
		},
		{
			name: "packages on one line, with the distro they belong to",
			body: "distro = \"ubuntu\"\npackages = [\"ripgrep\", 'jq', \"g++\", \"python3.12-venv\"]",
			want: Config{Path: testPath, Distro: types.DistroUbuntu, Packages: []string{"ripgrep", "jq", "g++", "python3.12-venv"}},
		},
		{
			name: "a list over several lines with comments and a trailing comma",
			body: "distro = \"fedora\"\npackages = [\n  \"gcc-c++\",  # compilers\n\n  \"python3-PyYAML\", # capitals are real Fedora names\n]\ncpus = 2",
			want: Config{Path: testPath, Distro: types.DistroFedora, Packages: []string{"gcc-c++", "python3-PyYAML"}, CPUs: 2},
		},
		{
			name: "an empty list asks for nothing, and needs no distro",
			body: "packages = []\nforward_env = [ ]",
			want: Config{Path: testPath},
		},
		{
			name: "forward_env",
			body: `forward_env = ["GITHUB_TOKEN", "_private", "AWS_PROFILE"]`,
			want: Config{Path: testPath, ForwardEnv: []string{"GITHUB_TOKEN", "_private", "AWS_PROFILE"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(testPath, []byte(tc.body))
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.body, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse(%q) = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}

// Everything the reader cannot read exactly is refused, and the refusal names
// the file, the line, and enough of the problem to fix it. Each TOML construct
// outside the subset appears here, because a construct that was accepted and
// misread would apply an environment the file's author did not write.
func TestParse_RefusesWhatItCannotReadExactly_REQ_15_1(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		line int
		want string
	}{
		{"unknown key", `packges = ["jq"]`, 1, `unknown key "packges"`},
		{"unknown key names the keys it knows", `cpu = 4`, 1, "understands distro, arch, cpus, memory, packages, forward_env"},
		{"duplicate key", "distro = \"ubuntu\"\n\ndistro = \"fedora\"", 3, "already set on line 1"},
		{"table", "[tools]\nnode = \"20\"", 1, "tables are not supported"},
		{"array of tables", "[[x]]", 1, "tables are not supported"},
		{"dotted key", `tool.distro = "ubuntu"`, 1, "dotted keys are not supported"},
		{"quoted key", `"distro" = "ubuntu"`, 1, "quoted keys are not supported"},
		{"no equals sign", "distro", 1, "expected key = value"},
		{"no key", `= "ubuntu"`, 1, "needs a key"},
		{"no value", "distro =", 1, "no value"},
		{"bare word", "distro = ubuntu", 1, "write a quoted string"},
		{"integer where a string is needed", "arch = 64", 1, "takes a quoted architecture"},
		{"string where an integer is needed", `cpus = "4"`, 1, "takes a whole number"},
		{"list where a string is needed", `distro = ["ubuntu"]`, 1, "takes a quoted name"},
		{"string where a list is needed", `packages = "jq"`, 1, "takes a list"},
		{"zero cpus", "cpus = 0", 1, "at least 1"},
		{"signed integer", "cpus = +4", 1, "a signed number"},
		{"negative integer", "cpus = -4", 1, "a signed number"},
		{"float", "cpus = 4.5", 1, "digits only"},
		{"underscore in an integer", "cpus = 1_0", 1, "digits only"},
		{"leading zero", "cpus = 04", 1, "digits only"},
		{"hexadecimal", "cpus = 0x10", 1, "digits only"},
		{"date", "cpus = 1979-05-27", 1, "digits only"},
		{"integer too large", "cpus = 99999999999999999999999", 1, "too large"},
		{"ambiguous memory unit", `memory = "8GB"`, 1, "GiB or MiB"},
		{"memory without a unit", `memory = "8"`, 1, "GiB or MiB"},
		{"fractional memory", `memory = "1.5GiB"`, 1, "GiB or MiB"},
		{"memory as a number", "memory = 8", 1, "takes a quoted size"},
		{"packages without a distro", `packages = ["jq"]`, 1, "packages needs distro"},
		{"packages without a distro, reported on the packages line", "cpus = 2\npackages = [\"jq\"]", 2, "packages needs distro"},
		{"a package that is an option", "distro = \"ubuntu\"\npackages = [\"--allow-unauthenticated\"]", 2, "not a package name"},
		{"a package that is a file", "distro = \"ubuntu\"\npackages = [\"./evil.deb\"]", 2, "not a package name"},
		{"a package that is a URL", "distro = \"ubuntu\"\npackages = [\"https://example.com/x.rpm\"]", 2, "not a package name"},
		{"a package with a version pin", "distro = \"ubuntu\"\npackages = [\"jq=1.6\"]", 2, "not a package name"},
		{"a package with an architecture", "distro = \"ubuntu\"\npackages = [\"libc6:amd64\"]", 2, "not a package name"},
		{"a package pattern", "distro = \"ubuntu\"\npackages = [\"python3-*\"]", 2, "not a package name"},
		{"a package with a space", "distro = \"ubuntu\"\npackages = [\"jq curl\"]", 2, "not a package name"},
		{"a package listed twice", "distro = \"ubuntu\"\npackages = [\"jq\", \"jq\"]", 2, "listed twice"},
		{"a variable name with =", `forward_env = ["A=b"]`, 1, "not a variable name"},
		{"a variable name starting with a digit", `forward_env = ["1PASSWORD"]`, 1, "not a variable name"},
		{"an empty variable name", `forward_env = [""]`, 1, "not a variable name"},
		{"a list holding a number", "forward_env = [\"A\", 3]", 1, "only quoted strings"},
		{"a nested list", `forward_env = [["A"]]`, 1, "a nested list"},
		{"a list missing a comma", `forward_env = ["A" "B"]`, 1, "expected , or ]"},
		{"a list with a leading comma", `forward_env = [, "A"]`, 1, "only quoted strings"},
		{"a list that never closes, reported where it ran out", "forward_env = [\n  \"A\",\n", 3, "not closed"},
		{"an error inside a multi-line list names its line", "forward_env = [\n  \"A\",\n  true,\n]", 3, "a boolean"},
		{"trailing garbage after a list", `forward_env = ["A"] x`, 1, "unexpected"},
		{"boolean", "distro = true", 1, "a boolean"},
		{"inline table", `distro = { name = "ubuntu" }`, 1, "an inline table"},
		{"unclosed string", `distro = "ubuntu`, 1, "not closed"},
		{"multi-line basic string", `distro = """ubuntu"""`, 1, "multi-line strings"},
		{"multi-line literal string", `distro = '''ubuntu'''`, 1, "multi-line strings"},
		{"escape sequence", "distro = \"ubu\\tntu\"", 1, "escape sequences are not supported"},
		{"trailing garbage", `distro = "ubuntu" "fedora"`, 1, "unexpected"},
		{"empty distro", `distro = ""`, 1, "names no distribution"},
		{"distro with an empty version", `distro = "ubuntu:"`, 1, "names no version"},
		{"empty arch", `arch = ""`, 1, "names no architecture"},
		{"control character", "distro = \"ubu\x01ntu\"", 1, "control characters"},
		{"line number counts earlier lines", "# comment\n\narch = \"arm64\"\nbogus = \"x\"", 4, `unknown key "bogus"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(testPath, []byte(tc.body))
			if err == nil {
				t.Fatalf("Parse(%q) accepted a file it must refuse", tc.body)
			}
			msg := err.Error()
			if !strings.Contains(msg, testPath) {
				t.Errorf("error does not name the file: %v", err)
			}
			if wantLine := "line " + strconv.Itoa(tc.line); !strings.Contains(msg, wantLine) {
				t.Errorf("error does not name %s: %v", wantLine, err)
			}
			if !strings.Contains(msg, tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestParse_RefusesInvalidUTF8(t *testing.T) {
	if _, err := Parse(testPath, []byte("distro = \"\xff\"")); err == nil {
		t.Fatal("Parse accepted a file that is not UTF-8")
	}
}

// A project with no file is the ordinary case, and must yield exactly the zero
// Config: anything else would mean the absence of a file changes behaviour.
func TestLoad_AbsentFileIsTheZeroConfig_REQ_15_4(t *testing.T) {
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load of a directory with no %s: %v", FileName, err)
	}
	if !reflect.DeepEqual(got, Config{}) {
		t.Errorf("Load = %+v, want the zero Config", got)
	}
	if got.Present() {
		t.Error("an absent file reports itself present")
	}
}

func TestLoad_ReadsTheProjectFile_REQ_15_1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("distro = \"fedora\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{Path: path, Distro: types.DistroFedora}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
	if !got.Present() {
		t.Error("a file that was read reports itself absent")
	}
}

func TestLoad_RefusesADirectoryInPlaceOfTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, FileName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("Load = %v, want a refusal naming the directory", err)
	}
}

func TestLoad_RefusesAnOversizedFile(t *testing.T) {
	dir := t.TempDir()
	body := "# " + strings.Repeat("x", maxFileSize) + "\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("Load = %v, want a refusal of the file's size", err)
	}
}

func TestLoad_ReportsParseErrorsWithThePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("distro = fedora\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), path+" line 1") {
		t.Fatalf("Load = %v, want an error naming %s line 1", err, path)
	}
}

// The reader claims that every file it accepts means the same thing to a
// conforming TOML parser. A test that only compares this reader with its own
// author's idea of TOML cannot show that (docs/lessons.md, "A test that asserts
// a command's shape proves only that you wrote what you wrote"), so the accepted
// fixtures are put in front of a real one: Python's standard tomllib, where the
// host has it. Where it does not, the test says so and skips rather than
// passing.
func TestParse_AgreesWithAConformingTOMLParser_REQ_15_1(t *testing.T) {
	python := conformingTOMLParser(t)

	for _, body := range acceptedFixtures {
		t.Run(body, func(t *testing.T) {
			cfg, err := Parse(testPath, []byte(body))
			if err != nil {
				t.Fatalf("Parse(%q): %v", body, err)
			}

			cmd := exec.Command(python, "-c", "import json, sys, tomllib; print(json.dumps(tomllib.loads(sys.stdin.read())))")
			cmd.Stdin = strings.NewReader(body)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("tomllib refused a file this reader accepts, %q: %v", body, err)
			}
			var theirs map[string]any
			if err := json.Unmarshal(out, &theirs); err != nil {
				t.Fatalf("reading tomllib's answer %s: %v", out, err)
			}
			if ours := document(cfg); !reflect.DeepEqual(ours, theirs) {
				t.Errorf("for %q this reader understood %v and tomllib %v", body, ours, theirs)
			}
		})
	}
}

// acceptedFixtures are files the reader accepts, chosen to exercise every
// construct it accepts: both string forms, comments in each position, spacing,
// and line endings.
var acceptedFixtures = []string{
	"",
	"# only a comment\n",
	`distro = "fedora"`,
	"distro = 'debian:13'\narch = \"amd64\"\n",
	"  distro=\"ubuntu\"   # trailing comment\r\n\r\narch='arm64'#tight\n",
	`distro = "ubuntu#not-a-comment"`,
	`distro = 'C:\literal'`,
	"cpus = 4\nmemory = \"8GiB\"\n",
	"memory = '1536MiB' # comment\n",
	"distro = \"ubuntu\"\npackages = [\"ripgrep\", 'jq', \"g++\"]\n",
	"distro = \"fedora\"\npackages = [\n  \"gcc-c++\",  # compilers\n\n  \"python3-PyYAML\", # a comment\n]\n",
	"forward_env = [ \"GITHUB_TOKEN\" , '_x' ,]",
	"forward_env = [\"A\",#comment\n\"C\"]",
}

// document renders a Config as the TOML document it was read from, in the
// shape tomllib reports.
func document(c Config) map[string]any {
	doc := map[string]any{}
	if c.Distro != "" {
		distro := string(c.Distro)
		if c.Version != "" {
			distro += ":" + c.Version
		}
		doc["distro"] = distro
	}
	if c.Arch != "" {
		doc["arch"] = string(c.Arch)
	}
	if c.CPUs != 0 {
		doc["cpus"] = float64(c.CPUs) // JSON numbers decode as float64
	}
	if c.MemoryMiB != 0 {
		doc["memory"] = formatMemory(c.MemoryMiB)
	}
	if c.Packages != nil {
		doc["packages"] = strings2any(c.Packages)
	}
	if c.ForwardEnv != nil {
		doc["forward_env"] = strings2any(c.ForwardEnv)
	}
	return doc
}

func strings2any(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// conformingTOMLParser finds a Python with tomllib (3.11 or later), or skips.
func conformingTOMLParser(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if exec.Command(path, "-c", "import tomllib").Run() == nil {
			return path
		}
	}
	t.Skip("no Python with tomllib on PATH, so the reader cannot be compared with a conforming TOML parser here")
	return ""
}
