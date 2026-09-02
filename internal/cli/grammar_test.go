package cli

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/olamide226/avar/internal/types"
)

// parseCase is one argv and the whole reading avar must give it. Asserting the
// complete Invocation rather than one field keeps a case from passing because
// the interesting part happened to be zero.
type parseCase struct {
	name string
	argv []string
	want Invocation
}

func runParseCases(t *testing.T, cases []parseCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := Parse(tc.argv)
			if err != nil {
				t.Fatalf("Parse(%q) returned unexpected error: %v", tc.argv, err)
			}
			assertInvocation(t, tc.argv, got, tc.want)
		})
	}
}

func assertInvocation(t *testing.T, argv []string, got, want Invocation) {
	t.Helper()

	if got.Mode != want.Mode {
		t.Errorf("Parse(%q).Mode = %s, want %s", argv, got.Mode, want.Mode)
	}
	if got.Subcommand != want.Subcommand {
		t.Errorf("Parse(%q).Subcommand = %q, want %q", argv, got.Subcommand, want.Subcommand)
	}
	if !slices.Equal(got.SubcommandArgs, want.SubcommandArgs) {
		t.Errorf("Parse(%q).SubcommandArgs = %q, want %q", argv, got.SubcommandArgs, want.SubcommandArgs)
	}
	if !slices.Equal(got.Guest, want.Guest) {
		t.Errorf("Parse(%q).Guest = %q, want %q", argv, got.Guest, want.Guest)
	}
	if got.Selector != want.Selector {
		t.Errorf("Parse(%q).Selector = %+v, want %+v", argv, got.Selector, want.Selector)
	}
	if got.Help != want.Help {
		t.Errorf("Parse(%q).Help = %t, want %t", argv, got.Help, want.Help)
	}
	if got.Version != want.Version {
		t.Errorf("Parse(%q).Version = %t, want %t", argv, got.Version, want.Version)
	}
	if !slices.Equal(got.Env, want.Env) {
		t.Errorf("Parse(%q).Env = %q, want %q", argv, got.Env, want.Env)
	}
	if got.EnvFile != want.EnvFile {
		t.Errorf("Parse(%q).EnvFile = %q, want %q", argv, got.EnvFile, want.EnvFile)
	}
	if got.SSHAgent != want.SSHAgent {
		t.Errorf("Parse(%q).SSHAgent = %t, want %t", argv, got.SSHAgent, want.SSHAgent)
	}
}

// The first non-selector-flag token decides the mode, and everything from it
// onwards belongs to the guest command untouched.
func TestParse_FirstNonFlagTokenStartsTheGuestCommand_REQ_2_5(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{
			name: "no arguments is the interactive shell",
			argv: nil,
			want: Invocation{Mode: ModeShell},
		},
		{
			name: "empty argv slice is the interactive shell",
			argv: []string{},
			want: Invocation{Mode: ModeShell},
		},
		{
			name: "bare dash is a command, not a flag",
			argv: []string{"-"},
			want: Invocation{Mode: ModeGuestCommand, Guest: []string{"-"}},
		},
		{
			name: "command with arguments",
			argv: []string{"npm", "test"},
			want: Invocation{Mode: ModeGuestCommand, Guest: []string{"npm", "test"}},
		},
		{
			name: "a command that looks like a path",
			argv: []string{"./scripts/build.sh", "--release"},
			want: Invocation{Mode: ModeGuestCommand, Guest: []string{"./scripts/build.sh", "--release"}},
		},
		{
			name: "known subcommand wins over a guest command of the same name",
			argv: []string{"status"},
			want: Invocation{Mode: ModeSubcommand, Subcommand: "status"},
		},
		{
			name: "subcommand arguments are left for the subcommand to parse",
			argv: []string{"stop", "--all"},
			want: Invocation{Mode: ModeSubcommand, Subcommand: "stop", SubcommandArgs: []string{"--all"}},
		},
		{
			name: "reset resolves to avar's reset",
			argv: []string{"reset"},
			want: Invocation{Mode: ModeSubcommand, Subcommand: "reset"},
		},
	})
}

// Guest flags are the guest's business: avar must not consume, reorder, or
// validate anything after the mode-deciding token.
func TestParse_GuestFlagsAreNeverParsedByAvar_REQ_4_5(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{
			name: "guest flag after the command",
			argv: []string{"npm", "test", "--watch"},
			want: Invocation{Mode: ModeGuestCommand, Guest: []string{"npm", "test", "--watch"}},
		},
		{
			name: "selector flag applies to avar, guest flag to the guest",
			argv: []string{"--arch", "amd64", "npm", "test", "--watch"},
			want: Invocation{
				Mode:     ModeGuestCommand,
				Selector: Selector{Arch: types.ArchAMD64},
				Guest:    []string{"npm", "test", "--watch"},
			},
		},
		{
			name: "equals form of a selector flag",
			argv: []string{"--arch=amd64", "npm", "test", "--watch"},
			want: Invocation{
				Mode:     ModeGuestCommand,
				Selector: Selector{Arch: types.ArchAMD64},
				Guest:    []string{"npm", "test", "--watch"},
			},
		},
		{
			name: "a selector flag name reappearing as a guest flag stays with the guest",
			argv: []string{"--arch", "amd64", "cargo", "build", "--arch", "wasm32"},
			want: Invocation{
				Mode:     ModeGuestCommand,
				Selector: Selector{Arch: types.ArchAMD64},
				Guest:    []string{"cargo", "build", "--arch", "wasm32"},
			},
		},
		{
			name: "a second double dash belongs to the guest command verbatim",
			argv: []string{"npm", "test", "--", "--watch"},
			want: Invocation{Mode: ModeGuestCommand, Guest: []string{"npm", "test", "--", "--watch"}},
		},
		{
			name: "guest help flag is the guest's",
			argv: []string{"npm", "--version"},
			want: Invocation{Mode: ModeGuestCommand, Guest: []string{"npm", "--version"}},
		},
		{
			name: "subcommand help flag is the subcommand's",
			argv: []string{"status", "--help"},
			want: Invocation{Mode: ModeSubcommand, Subcommand: "status", SubcommandArgs: []string{"--help"}},
		},
		{
			name: "selector flags apply to a subcommand too",
			argv: []string{"--distro", "fedora", "code"},
			want: Invocation{
				Mode:       ModeSubcommand,
				Subcommand: "code",
				Selector:   Selector{Distro: types.DistroFedora},
			},
		},
		{
			name: "selector flags combine",
			argv: []string{"--distro", "debian:12", "--arch", "amd64", "--isolate", "make", "-j4"},
			want: Invocation{
				Mode: ModeGuestCommand,
				Selector: Selector{
					Distro:        types.DistroDebian,
					DistroVersion: "12",
					Arch:          types.ArchAMD64,
					Isolate:       true,
				},
				Guest: []string{"make", "-j4"},
			},
		},
	})
}

// `--` always forces guest interpretation of what follows, so a project script
// can shadow any avar subcommand name.
func TestParse_DoubleDashForcesGuestCommand_REQ_2_6(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{
			name: "subcommand name after the separator is a guest command",
			argv: []string{"--", "status"},
			want: Invocation{Mode: ModeGuestCommand, Guest: []string{"status"}},
		},
		{
			name: "reset after the separator is the guest's reset",
			argv: []string{"--", "reset"},
			want: Invocation{Mode: ModeGuestCommand, Guest: []string{"reset"}},
		},
		{
			name: "avar's own help flag after the separator is a guest command",
			argv: []string{"--", "--help"},
			want: Invocation{Mode: ModeGuestCommand, Guest: []string{"--help"}},
		},
		{
			name: "guest command with its own version flag",
			argv: []string{"--", "npm", "--version"},
			want: Invocation{Mode: ModeGuestCommand, Guest: []string{"npm", "--version"}},
		},
		{
			name: "selector flags before the separator are still avar's",
			argv: []string{"--arch", "amd64", "--", "status", "--verbose"},
			want: Invocation{
				Mode:     ModeGuestCommand,
				Selector: Selector{Arch: types.ArchAMD64},
				Guest:    []string{"status", "--verbose"},
			},
		},
		{
			name: "separator with nothing after it is the interactive shell",
			argv: []string{"--"},
			want: Invocation{Mode: ModeShell},
		},
		{
			name: "separator after selector flags with nothing following",
			argv: []string{"--isolate", "--"},
			want: Invocation{Mode: ModeShell, Selector: Selector{Isolate: true}},
		},
	})
}

// Isolation selection: --isolate and --shared are the two halves of one choice.
func TestParse_IsolationSelectorFlags(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{
			name: "isolate alone still means the interactive shell",
			argv: []string{"--isolate"},
			want: Invocation{Mode: ModeShell, Selector: Selector{Isolate: true}},
		},
		{
			name: "shared alone",
			argv: []string{"--shared"},
			want: Invocation{Mode: ModeShell, Selector: Selector{Shared: true}},
		},
		{
			name: "explicit false turns a boolean flag off",
			argv: []string{"--isolate=false"},
			want: Invocation{Mode: ModeShell},
		},
		{
			name: "isolate applies to the isolate subcommand as well",
			argv: []string{"--isolate", "isolate", "off"},
			want: Invocation{
				Mode:           ModeSubcommand,
				Subcommand:     "isolate",
				SubcommandArgs: []string{"off"},
				Selector:       Selector{Isolate: true},
			},
		},
	})
}

// --help/-h and --version/-v are avar's only in avar position.
func TestParse_HelpAndVersionIntents(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{
			name: "long help",
			argv: []string{"--help"},
			want: Invocation{Mode: ModeShell, Help: true},
		},
		{
			name: "short help",
			argv: []string{"-h"},
			want: Invocation{Mode: ModeShell, Help: true},
		},
		{
			name: "long version",
			argv: []string{"--version"},
			want: Invocation{Mode: ModeShell, Version: true},
		},
		{
			name: "short version",
			argv: []string{"-v"},
			want: Invocation{Mode: ModeShell, Version: true},
		},
		{
			name: "help subcommand is reported as a subcommand",
			argv: []string{"help"},
			want: Invocation{Mode: ModeSubcommand, Subcommand: "help"},
		},
		{
			name: "version subcommand is reported as a subcommand",
			argv: []string{"version"},
			want: Invocation{Mode: ModeSubcommand, Subcommand: "version"},
		},
		{
			name: "help after selector flags is still avar's",
			argv: []string{"--arch", "amd64", "--help"},
			want: Invocation{Mode: ModeShell, Selector: Selector{Arch: types.ArchAMD64}, Help: true},
		},
	})
}

// Unsupported selector values fail before anything else happens, naming the
// values that would have worked.
func TestParse_UnsupportedSelectorValuesListSupportedOnes_REQ_4_4(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		argv         []string
		wantContains []string
	}{
		{
			name:         "unsupported arch",
			argv:         []string{"--arch", "riscv64"},
			wantContains: []string{"--arch", "riscv64", "arm64", "amd64"},
		},
		{
			name:         "unsupported arch in equals form with a guest command",
			argv:         []string{"--arch=x86_64", "npm", "test"},
			wantContains: []string{"--arch", "x86_64", "arm64", "amd64"},
		},
		{
			name:         "unsupported distro",
			argv:         []string{"--distro", "arch"},
			wantContains: []string{"--distro", "arch", "ubuntu", "debian", "fedora"},
		},
		{
			name:         "distro with an empty version",
			argv:         []string{"--distro", "ubuntu:"},
			wantContains: []string{"--distro", "ubuntu:"},
		},
		{
			name:         "arch with no value",
			argv:         []string{"--arch"},
			wantContains: []string{"--arch", "needs a value"},
		},
		{
			name:         "distro with no value at the end of a longer argv",
			argv:         []string{"--isolate", "--distro"},
			wantContains: []string{"--distro", "needs a value"},
		},
		{
			name:         "isolate and shared conflict",
			argv:         []string{"--isolate", "--shared"},
			wantContains: []string{"--isolate", "--shared", "mutually exclusive"},
		},
		{
			name:         "isolate and shared conflict with a guest command",
			argv:         []string{"--shared", "--isolate", "npm", "test"},
			wantContains: []string{"mutually exclusive"},
		},
		{
			name:         "boolean flag given a non-boolean value",
			argv:         []string{"--isolate=maybe"},
			wantContains: []string{"--isolate", "maybe"},
		},
		{
			name:         "unknown avar flag names the escape hatch",
			argv:         []string{"--verbose"},
			wantContains: []string{"unknown flag", "--verbose", "avr -- --verbose"},
		},
		{
			name:         "unknown short flag",
			argv:         []string{"-x", "ls"},
			wantContains: []string{"unknown flag", "-x"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := Parse(tc.argv)
			if err == nil {
				t.Fatalf("Parse(%q) = %+v, want error", tc.argv, got)
			}
			if !reflect.DeepEqual(got, Invocation{}) {
				t.Errorf("Parse(%q) returned %+v alongside its error, want the zero Invocation", tc.argv, got)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Parse(%q) error %q does not mention %q", tc.argv, err, want)
				}
			}
		})
	}
}

func TestParse_DoesNotMutateOrAliasArgv(t *testing.T) {
	t.Parallel()

	argv := []string{"--arch", "amd64", "npm", "test", "--watch"}
	original := slices.Clone(argv)

	inv, err := Parse(argv)
	if err != nil {
		t.Fatalf("Parse(%q) returned unexpected error: %v", argv, err)
	}
	if !slices.Equal(argv, original) {
		t.Errorf("Parse mutated argv: got %q, want %q", argv, original)
	}

	// A caller reusing its argv buffer must not be able to change an
	// invocation that was already handed out.
	inv.Guest[0] = "mutated"
	if argv[2] != "npm" {
		t.Errorf("Invocation.Guest aliases argv: argv is now %q", argv)
	}
}

func TestSubcommands_AreSortedAndCopied(t *testing.T) {
	t.Parallel()

	got := Subcommands()
	if !slices.IsSorted(got) {
		t.Errorf("Subcommands() = %q, want sorted order", got)
	}

	// Every name the grammar claims must be recognised by IsSubcommand, and
	// the returned slice must not be the package's own.
	for _, name := range got {
		if !IsSubcommand(name) {
			t.Errorf("IsSubcommand(%q) = false, want true", name)
		}
	}
	got[0] = "mutated"
	if IsSubcommand("mutated") {
		t.Error("Subcommands() returned the package's own slice")
	}
}

func TestIsSubcommand_RejectsNonSubcommands(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"", "npm", "Status", "status ", "--status", "ls", "avr"} {
		if IsSubcommand(name) {
			t.Errorf("IsSubcommand(%q) = true, want false", name)
		}
	}
}

// Forwarding flags: --env (repeatable), --env-file, --ssh-agent.
// They are consumed in avar position, before the mode-deciding token,
// and are passed through to the invocation unchanged (REQ-12.1, REQ-12.2, REQ-12.3).
func TestParse_EnvForwardingFlags_REQ_12_1_12_2_12_3(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{
			name: "a single --env with NAME",
			argv: []string{"--env", "SECRET"},
			want: Invocation{Mode: ModeShell, Env: []string{"SECRET"}},
		},
		{
			name: "a single --env with NAME=value",
			argv: []string{"--env", "NODE_ENV=production"},
			want: Invocation{Mode: ModeShell, Env: []string{"NODE_ENV=production"}},
		},
		{
			name: "repeatable --env accumulates values",
			argv: []string{"--env", "FOO", "--env", "BAR=baz"},
			want: Invocation{Mode: ModeShell, Env: []string{"FOO", "BAR=baz"}},
		},
		{
			name: "--env with equals form",
			argv: []string{"--env=NODE_ENV=production"},
			want: Invocation{Mode: ModeShell, Env: []string{"NODE_ENV=production"}},
		},
		{
			name: "--env-file forwards a path",
			argv: []string{"--env-file", ".env.prod"},
			want: Invocation{Mode: ModeShell, EnvFile: ".env.prod"},
		},
		{
			name: "--env-file with equals form",
			argv: []string{"--env-file=.env"},
			want: Invocation{Mode: ModeShell, EnvFile: ".env"},
		},
		{
			name: "--ssh-agent sets the boolean",
			argv: []string{"--ssh-agent"},
			want: Invocation{Mode: ModeShell, SSHAgent: true},
		},
		{
			name: "--ssh-agent=false is accepted",
			argv: []string{"--ssh-agent=false"},
			want: Invocation{Mode: ModeShell, SSHAgent: false},
		},
		{
			name: "forwarding flags with a guest command",
			argv: []string{"--env", "NODE_ENV=staging", "--ssh-agent", "npm", "test"},
			want: Invocation{
				Mode:     ModeGuestCommand,
				Guest:    []string{"npm", "test"},
				Env:      []string{"NODE_ENV=staging"},
				SSHAgent: true,
			},
		},
		{
			name: "forwarding flags with selector flags",
			argv: []string{"--arch", "amd64", "--env", "CC=gcc", "--env-file", ".env"},
			want: Invocation{
				Mode:     ModeShell,
				Selector: Selector{Arch: types.ArchAMD64},
				Env:      []string{"CC=gcc"},
				EnvFile:  ".env",
			},
		},
		{
			name: "forwarding flags with a subcommand",
			argv: []string{"--ssh-agent", "status"},
			want: Invocation{
				Mode:       ModeSubcommand,
				Subcommand: "status",
				SSHAgent:   true,
			},
		},
	})
}

// --native-fs is an avar-position boolean like the forwarding flags, and `sync`
// is an avar subcommand rather than a guest command (REQ-14.1, REQ-14.2).
//
// It is deliberately not part of Selector: it changes where a session runs, not
// which environment it runs in, so two invocations differing only in this flag
// still resolve to one machine (PROP-2).
func TestParse_NativeWorkspaceFlagAndSubcommand_REQ_14_1(t *testing.T) {
	t.Parallel()

	runParseCases(t, []parseCase{
		{
			name: "--native-fs sets the boolean",
			argv: []string{"--native-fs"},
			want: Invocation{Mode: ModeShell, NativeFS: true},
		},
		{
			name: "--native-fs with a guest command",
			argv: []string{"--native-fs", "npm", "test"},
			want: Invocation{Mode: ModeGuestCommand, Guest: []string{"npm", "test"}, NativeFS: true},
		},
		{
			name: "--native-fs does not select an environment",
			argv: []string{"--native-fs", "--distro", "fedora"},
			want: Invocation{Mode: ModeShell, NativeFS: true, Selector: Selector{Distro: types.DistroFedora}},
		},
		{
			name: "sync is an avar subcommand",
			argv: []string{"sync", "--to-host"},
			want: Invocation{Mode: ModeSubcommand, Subcommand: "sync", SubcommandArgs: []string{"--to-host"}},
		},
		{
			// The escape hatch a project script named `sync` needs (REQ-2.6).
			name: "-- forces a guest command named sync",
			argv: []string{"--", "sync"},
			want: Invocation{Mode: ModeGuestCommand, Guest: []string{"sync"}},
		},
	})
}

func TestMode_String(t *testing.T) {
	t.Parallel()

	for mode, want := range map[Mode]string{
		ModeShell:        "shell",
		ModeGuestCommand: "guest-command",
		ModeSubcommand:   "subcommand",
		Mode(99):         "mode(99)",
	} {
		if got := mode.String(); got != want {
			t.Errorf("Mode(%d).String() = %q, want %q", int(mode), got, want)
		}
	}
}
