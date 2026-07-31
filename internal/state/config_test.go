package state

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseConfigList(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want []string
	}{
		{"single line", `forward_env = ["A", "B"]`, []string{"A", "B"}},
		{"no spaces", `forward_env=["A","B"]`, []string{"A", "B"}},
		{"single quotes", `forward_env = ['A', 'B']`, []string{"A", "B"}},
		{"empty list", `forward_env = []`, nil},
		{"trailing comma", `forward_env = ["A",]`, []string{"A"}},
		{"multi-line", "forward_env = [\n  \"A\",\n  \"B\",\n]", []string{"A", "B"}},
		{"with comment", `forward_env = ["A"] # keep this one`, []string{"A"}},
		{"comment above", "# forward_env = [\"NO\"]\nforward_env = [\"A\"]", []string{"A"}},
		{"other keys present", "idle_timeout = \"2h\"\nforward_env = [\"A\"]", []string{"A"}},
		{"key absent", `idle_timeout = "2h"`, nil},
		{"scalar under a list key", `forward_env = "A"`, nil},
		{"empty file", "", nil},
		// A key whose name merely contains ours must not match, or
		// "no_forward_env" would silently grant what it names.
		{"similar key name", `no_forward_env = ["A"]`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseConfigList(tc.body, "forward_env")
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseConfigList(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

func TestStore_ConfigListWithNoConfigFile(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ConfigList("forward_env")
	if err != nil {
		t.Fatalf("a missing config.toml is not an error: %v", err)
	}
	if got != nil {
		t.Errorf("got %v from a state directory with no config.toml", got)
	}
}

func TestStore_ConfigListReadsTheFile(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(filepath.Join(dir, "config.toml"), []byte("forward_env = [\"AWS_PROFILE\"]\n")); err != nil {
		t.Fatal(err)
	}
	got, err := s.ConfigList("forward_env")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"AWS_PROFILE"}) {
		t.Errorf("ConfigList = %v, want [AWS_PROFILE]", got)
	}
}
