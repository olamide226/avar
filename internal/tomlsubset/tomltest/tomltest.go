// Package tomltest puts a TOML document in front of a conforming TOML parser,
// for tests.
//
// tomlsubset claims that every file it accepts means the same to any conforming
// TOML parser. A test that compares the reader only with its author's idea of
// TOML cannot show that (docs/lessons.md, "A test that asserts a command's shape
// proves only that you wrote what you wrote"), so the tests of each file that
// uses the reader compare what they accept with Python's standard tomllib, where
// the host has it.
package tomltest

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// Parser returns a function that parses a TOML document with tomllib and
// returns it in the shape encoding/json decodes it to: strings, float64
// numbers, and []any lists. It skips t, saying why, when no Python with tomllib
// (3.11 or later) is on PATH, so a host without one never passes silently.
func Parser(t *testing.T) func(t *testing.T, body string) map[string]any {
	t.Helper()
	python := find()
	if python == "" {
		t.Skip("no Python with tomllib on PATH, so the reader cannot be compared with a conforming TOML parser here")
	}
	return func(t *testing.T, body string) map[string]any {
		t.Helper()
		cmd := exec.Command(python, "-c", "import json, sys, tomllib; print(json.dumps(tomllib.loads(sys.stdin.read())))")
		cmd.Stdin = strings.NewReader(body)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("tomllib refused a file this reader accepts, %q: %v", body, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(out, &doc); err != nil {
			t.Fatalf("reading tomllib's answer %s: %v", out, err)
		}
		return doc
	}
}

// Strings renders a list of strings in the shape tomllib's answer decodes to.
func Strings(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

func find() string {
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if exec.Command(path, "-c", "import tomllib").Run() == nil {
			return path
		}
	}
	return ""
}
