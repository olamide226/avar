package projconfig

import (
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/types"
)

// Each manifest type REQ-15.2 names, one table: what the file says, and what
// avr init reads from it.
func TestDetect_EachManifest_REQ_15_2(t *testing.T) {
	for _, tc := range []struct {
		manifest string
		body     string
		want     []Finding
	}{
		{"package.json", `{"name": "app"}`,
			[]Finding{{Stack: "Node.js", Runtime: RuntimeNode}}},
		{"package.json", `{"engines": {"node": ">=20"}}`,
			[]Finding{{Stack: "Node.js", Runtime: RuntimeNode, Detail: "engines.node >=20", Pinned: true}}},
		{"package.json", `{not json`,
			[]Finding{{Stack: "Node.js", Runtime: RuntimeNode, Detail: "not valid JSON, so no version was read"}}},
		{"pyproject.toml", "[project]\nname = \"app\"\nrequires-python = \">=3.11\" # floor\n",
			[]Finding{{Stack: "Python", Runtime: RuntimePython, Detail: "requires-python >=3.11", Pinned: true}}},
		{"pyproject.toml", "[tool.poetry]\nname = \"app\"\n",
			[]Finding{{Stack: "Python", Runtime: RuntimePython}}},
		{"go.mod", "module example.com/app\n\ngo 1.22.3\n\nrequire example.com/x v1.0.0\n",
			[]Finding{{Stack: "Go", Runtime: RuntimeGo, Detail: "go 1.22.3", Pinned: true}}},
		{"Cargo.toml", "[package]\nname = \"app\"\nrust-version = \"1.75\"\n",
			[]Finding{{Stack: "Rust", Runtime: RuntimeRust, Detail: "rust-version 1.75", Pinned: true}}},
		{".tool-versions", "# pinned\nnodejs 20.11.0\npython 3.12.1 3.11.8\njava openjdk-21\n",
			[]Finding{
				{Stack: "Node.js", Runtime: RuntimeNode, Detail: "nodejs 20.11.0", Pinned: true},
				{Stack: "Python", Runtime: RuntimePython, Detail: "python 3.12.1 3.11.8", Pinned: true},
				{Stack: "java", Detail: "java openjdk-21"},
			}},
		{"mise.toml", "[env]\nNODE_ENV = \"development\"\n\n[tools]\nnode = \"22\"\ngo = [\"1.23\", \"1.22\"]\n\"npm:prettier\" = \"latest\"\n\n[tasks.build]\nrun = \"make\"\n",
			[]Finding{
				{Stack: "Node.js", Runtime: RuntimeNode, Detail: "node 22", Pinned: true},
				{Stack: "Go", Runtime: RuntimeGo, Detail: "go 1.23 1.22", Pinned: true},
				{Stack: "npm:prettier", Detail: "npm:prettier latest"},
			}},
		{"Dockerfile", "FROM ubuntu:24.04\nRUN apt-get update\n",
			[]Finding{{Stack: "Dockerfile", Detail: "FROM ubuntu:24.04", Distro: types.DistroUbuntu}}},
		{"Dockerfile", "FROM --platform=linux/amd64 docker.io/library/fedora:43\n",
			[]Finding{{Stack: "Dockerfile", Detail: "FROM docker.io/library/fedora:43", Distro: types.DistroFedora, Arch: types.ArchAMD64}}},
		{"Dockerfile", "FROM golang:1.23 AS build\nRUN go build\nFROM python:3.12-slim\nCOPY --from=build /app /app\n",
			[]Finding{{Stack: "Python", Runtime: RuntimePython, Detail: "FROM python:3.12-slim (Debian-based)", Distro: types.DistroDebian}}},
		{"Dockerfile", "FROM node:22-alpine AS base\nFROM base\n",
			[]Finding{{Stack: "Node.js", Runtime: RuntimeNode, Detail: "FROM node:22-alpine"}}},
		{"Dockerfile", "ARG BASE=ubuntu\nFROM ${BASE}\n",
			[]Finding{{Stack: "Dockerfile", Detail: "FROM ${BASE} (a build argument, so no base could be read)"}}},
		{"Dockerfile", "# no FROM at all\n", nil},
		{"docker-compose.yml", "services:\n  db:\n    image: postgres\n",
			[]Finding{{Stack: "Docker Compose"}}},
		{"docker-compose.yaml", "services: {}\n",
			[]Finding{{Stack: "Docker Compose"}}},
	} {
		t.Run(tc.manifest+" "+firstLine(tc.body), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.manifest), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := Detect(dir)
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			for i := range tc.want {
				tc.want[i].Manifest = tc.manifest
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Detect found\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// Detection reads the named files in the project directory and nothing else:
// not a subdirectory, and not a file that merely resembles a manifest.
func TestDetect_ReadsOnlyTheProjectDirectory_REQ_15_2(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		"web/package.json": "{}",
		"go.sum":           "x",
		"requirements.txt": "flask",
		"README.md":        "FROM ubuntu",
	} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "Dockerfile"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Detect found %+v in a directory with no manifest of its own", got)
	}
}

func TestPropose_PackagesPerDistribution_REQ_15_2(t *testing.T) {
	findings := []Finding{
		{Manifest: "package.json", Stack: "Node.js", Runtime: RuntimeNode},
		{Manifest: "go.mod", Stack: "Go", Runtime: RuntimeGo, Detail: "go 1.22", Pinned: true},
	}
	for _, tc := range []struct {
		choice Choice
		want   Config
	}{
		{Choice{}, Config{Distro: types.DistroUbuntu, Packages: []string{"nodejs", "npm", "golang-go"}}},
		{Choice{Distro: types.DistroDebian, Version: "13"}, Config{Distro: types.DistroDebian, Version: "13", Packages: []string{"nodejs", "npm", "golang-go"}}},
		{Choice{Distro: types.DistroFedora, Arch: types.ArchAMD64}, Config{Distro: types.DistroFedora, Arch: types.ArchAMD64, Packages: []string{"nodejs", "nodejs-npm", "golang"}}},
	} {
		got := Propose(findings, tc.choice, types.DistroUbuntu)
		if !reflect.DeepEqual(got.Config, tc.want) {
			t.Errorf("Propose(%+v).Config = %+v, want %+v", tc.choice, got.Config, tc.want)
		}
		if !slices.ContainsFunc(got.Notes, func(n string) bool { return strings.Contains(n, "go 1.22 (go.mod)") }) {
			t.Errorf("Propose did not say the pinned Go version is not what gets installed: %q", got.Notes)
		}
	}
}

// A Dockerfile's base chooses the distribution unless the user did; an explicit
// choice always wins.
func TestPropose_DistributionPrecedence_REQ_15_2(t *testing.T) {
	findings := []Finding{
		{Manifest: "Dockerfile", Stack: "Python", Runtime: RuntimePython, Detail: "FROM python:3.12-slim (Debian-based)", Distro: types.DistroDebian, Arch: types.ArchAMD64},
	}
	if got := Propose(findings, Choice{}, types.DistroUbuntu).Config; got.Distro != types.DistroDebian || got.Arch != types.ArchAMD64 {
		t.Errorf("the Dockerfile's base did not choose: %+v", got)
	}
	if got := Propose(findings, Choice{Distro: types.DistroFedora}, types.DistroUbuntu).Config; got.Distro != types.DistroFedora {
		t.Errorf("the user's choice did not win: %+v", got)
	}

	alpine := []Finding{{Manifest: "Dockerfile", Stack: "Node.js", Runtime: RuntimeNode, Detail: "FROM node:22-alpine"}}
	p := Propose(alpine, Choice{}, types.DistroUbuntu)
	if p.Config.Distro != types.DistroUbuntu || !slices.Equal(p.Config.Packages, []string{"nodejs", "npm"}) {
		t.Errorf("an Alpine base did not fall back to the default with Node's packages: %+v", p.Config)
	}
	if len(p.Notes) == 0 || !strings.Contains(p.Notes[0], "node:22-alpine") {
		t.Errorf("Propose did not say why the Dockerfile's base was not used: %q", p.Notes)
	}
}

// REQ-15.3: detection never proposes a size or a credential grant, whatever
// the manifests say.
func TestPropose_NeverProposesGrantsOrSizes_REQ_15_3(t *testing.T) {
	findings := []Finding{
		{Manifest: "package.json", Stack: "Node.js", Runtime: RuntimeNode},
		{Manifest: ".tool-versions", Stack: "java", Detail: "java 21"},
		{Manifest: "docker-compose.yml", Stack: "Docker Compose"},
	}
	p := Propose(findings, Choice{}, types.DistroUbuntu)
	if p.Config.ForwardEnv != nil || p.Config.CPUs != 0 || p.Config.MemoryMiB != 0 {
		t.Errorf("Propose proposed a grant or a size: %+v", p.Config)
	}
	notes := strings.Join(p.Notes, "\n")
	if !strings.Contains(notes, "container engine") || !strings.Contains(notes, "java 21 (.tool-versions)") {
		t.Errorf("Propose did not explain what it leaves out:\n%s", notes)
	}
}

func TestPropose_NothingFoundProposesNothing_REQ_15_4(t *testing.T) {
	if p := Propose(nil, Choice{Distro: types.DistroFedora}, types.DistroUbuntu); !reflect.DeepEqual(p, Proposal{}) {
		t.Errorf("Propose(nothing) = %+v, want an empty proposal", p)
	}
}

// Every package avr init can propose is one the reader accepts and the
// installer will run.
func TestRuntimePackages_AreValidNames(t *testing.T) {
	for runtime, pkgs := range runtimePackages {
		for _, name := range append(slices.Clone(pkgs.apt), pkgs.dnf...) {
			if !ValidPackageName(name) {
				t.Errorf("%s: %q is not a valid package name", runtime, name)
			}
		}
	}
}

// Render and Parse agree for every Config the reader can return: what avr init
// writes, avr reads back exactly.
func TestRender_RoundTrips(t *testing.T) {
	rng := rand.New(rand.NewSource(15))
	pick := func(options ...string) string { return options[rng.Intn(len(options))] }
	names := func(pool ...string) []string {
		var out []string
		for _, name := range pool {
			if rng.Intn(2) == 0 {
				out = append(out, name)
			}
		}
		return out
	}

	for i := 0; i < 500; i++ {
		c := Config{
			Path:       testPath,
			Distro:     types.Distro(pick("", "ubuntu", "debian", "fedora")),
			Arch:       types.Arch(pick("", "arm64", "amd64")),
			CPUs:       rng.Intn(3) * rng.Intn(64),
			MemoryMiB:  rng.Intn(2) * (1 + rng.Intn(64*1024)),
			ForwardEnv: names("GITHUB_TOKEN", "AWS_PROFILE", "_X"),
		}
		if c.Distro != "" {
			c.Version = pick("", "24.04", "13")
			c.Packages = names("jq", "g++", "python3.12-venv", "gcc-c++")
		}

		body, err := Render(c)
		if err != nil {
			t.Fatalf("Render(%+v): %v", c, err)
		}
		back, err := Parse(testPath, body)
		if err != nil {
			t.Fatalf("Parse(Render(%+v)): %v\n%s", c, err, body)
		}
		if !reflect.DeepEqual(back, c) {
			t.Fatalf("Parse(Render(c)) = %+v, want %+v\n%s", back, c, body)
		}
	}
}

func TestRender_RefusesWhatWouldNotReadBack(t *testing.T) {
	for _, c := range []Config{
		{Distro: `ubu"ntu`},
		{Arch: `arm\64`},
		{Packages: []string{"jq"}}, // packages without distro
		{Distro: "ubuntu", Packages: []string{"-y"}},
	} {
		if _, err := Render(c); err == nil {
			t.Errorf("Render(%+v) wrote a file that would not read back", c)
		}
	}
}
