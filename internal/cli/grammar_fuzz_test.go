package cli

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// argvSeparator joins an argv into one fuzzable string. NUL can never appear in
// a real argument (execve terminates arguments with it), so joining and
// splitting on it round-trips every argv a process can actually receive.
const argvSeparator = "\x00"

// fuzzSeeds are the argvs the corpus starts from: the documented grammar plus
// the shapes that have historically broken hand-written argv splitters.
var fuzzSeeds = [][]string{
	{},
	{""},
	{"-"},
	{"--"},
	{"--", "--"},
	{"npm", "test"},
	{"npm", "test", "--watch"},
	{"npm", "test", "--", "--watch"},
	{"status"},
	{"--", "status"},
	{"stop", "--all"},
	{"reset"},
	{"--", "reset"},
	{"--arch", "amd64", "npm", "test"},
	{"--arch=amd64", "npm", "test", "--watch"},
	{"--distro", "fedora", "code"},
	{"--distro", "ubuntu:24.04"},
	{"--distro=debian:12", "--isolate"},
	{"--isolate"},
	{"--shared"},
	{"--isolate", "--shared"},
	{"--arch"},
	{"--arch", "riscv64"},
	{"--help"},
	{"-h"},
	{"--version"},
	{"-v"},
	{"--", "--help"},
	{"--", "npm", "--version"},
	{"--arch", "amd64", "--", "status", "--verbose"},
	{"--arch", "--distro"},
	{"--arch", "--"},
	{"sh", "-c", "echo hi > out.txt"},
	{"--isolate=false", "--shared=true", "ls"},
	{"--", "-"},
	{"--unknown"},
	{"-x"},
	{"--arch=", "ls"},
	{"--distro", "ubuntu:"},
}

// FuzzParse establishes PROP-9: for any argv, the first non-selector-flag
// token is an avar subcommand iff it names one, prefixing "--" always forces
// guest interpretation, and no guest token is ever lost, reordered, or
// rewritten on the way through.
func FuzzParse(f *testing.F) {
	for _, seed := range fuzzSeeds {
		f.Add(strings.Join(seed, argvSeparator))
	}

	f.Fuzz(func(t *testing.T, joined string) {
		argv := strings.Split(joined, argvSeparator)

		// Parsing must never panic, whatever the bytes are.
		inv, err := Parse(argv)

		// Parsing must be a pure function of argv.
		again, errAgain := Parse(argv)
		if !reflect.DeepEqual(inv, again) || (err == nil) != (errAgain == nil) {
			t.Fatalf("Parse(%q) is not deterministic: %+v/%v then %+v/%v", argv, inv, err, again, errAgain)
		}

		if err != nil {
			// A rejected argv yields no partial reading to act on.
			if !reflect.DeepEqual(inv, Invocation{}) {
				t.Fatalf("Parse(%q) failed with %v but still returned %+v", argv, err, inv)
			}
			assertDoubleDashForcesGuest(t, argv)
			return
		}

		if inv.Selector.Isolate && inv.Selector.Shared {
			t.Errorf("Parse(%q) accepted both --isolate and --shared", argv)
		}

		tail := assertModeConsistency(t, argv, inv)
		assertTokensPreserved(t, argv, tail)

		// The mode-deciding token is a subcommand exactly when it names one.
		// A "--" anywhere in argv may have forced guest interpretation, so
		// this half of the invariant is asserted on argvs without one.
		if !slices.Contains(argv, "--") && inv.Mode == ModeGuestCommand && IsSubcommand(inv.Guest[0]) {
			t.Errorf("Parse(%q) read subcommand %q as a guest command without a %q separator", argv, inv.Guest[0], "--")
		}

		assertDoubleDashForcesGuest(t, argv)
	})
}

// assertModeConsistency checks that exactly the fields belonging to the
// reported mode are populated, and returns the tokens avar passed on
// untouched.
func assertModeConsistency(t *testing.T, argv []string, inv Invocation) []string {
	t.Helper()

	switch inv.Mode {
	case ModeShell:
		if inv.Guest != nil || inv.Subcommand != "" || inv.SubcommandArgs != nil {
			t.Fatalf("Parse(%q) reported the shell mode but carried %+v", argv, inv)
		}
		return nil

	case ModeGuestCommand:
		if len(inv.Guest) == 0 {
			t.Fatalf("Parse(%q) reported a guest command with no argv", argv)
		}
		if inv.Subcommand != "" || inv.SubcommandArgs != nil {
			t.Fatalf("Parse(%q) reported a guest command and a subcommand: %+v", argv, inv)
		}
		return inv.Guest

	case ModeSubcommand:
		if !IsSubcommand(inv.Subcommand) {
			t.Fatalf("Parse(%q) reported %q as a subcommand, which is not one", argv, inv.Subcommand)
		}
		if inv.Guest != nil {
			t.Fatalf("Parse(%q) reported a subcommand and a guest command: %+v", argv, inv)
		}
		return append([]string{inv.Subcommand}, inv.SubcommandArgs...)

	default:
		t.Fatalf("Parse(%q) reported unknown mode %s", argv, inv.Mode)
		return nil
	}
}

// assertTokensPreserved checks that the tokens avar passes on are a contiguous
// suffix of argv, byte-identical and in order: nothing dropped, nothing
// reordered, nothing re-quoted.
func assertTokensPreserved(t *testing.T, argv, tail []string) {
	t.Helper()

	if len(tail) == 0 {
		return
	}
	if len(tail) > len(argv) {
		t.Fatalf("Parse(%q) produced more tokens than it was given: %q", argv, tail)
	}

	start := len(argv) - len(tail)
	if !slices.Equal(argv[start:], tail) {
		t.Fatalf("Parse(%q) did not pass tokens through verbatim: got %q, want the suffix %q", argv, tail, argv[start:])
	}

	// Re-joining the passed-through tokens must reproduce the tail of the
	// original command line exactly.
	if want, got := strings.Join(argv[start:], argvSeparator), strings.Join(tail, argvSeparator); want != got {
		t.Fatalf("Parse(%q) changed the joined tokens: got %q, want %q", argv, got, want)
	}

	// A guest command can only start where avar stopped reading flags, so
	// an argv whose first token avar cannot own must be consumed whole.
	if len(argv) > 0 && !strings.HasPrefix(argv[0], "-") && start != 0 {
		t.Fatalf("Parse(%q) consumed %d leading token(s) that are not avar flags", argv, start)
	}
}

// assertDoubleDashForcesGuest checks the REQ-2.6 half of PROP-9 directly:
// prefixing "--" makes any argv a guest command, verbatim, whatever it
// contains.
func assertDoubleDashForcesGuest(t *testing.T, argv []string) {
	t.Helper()

	forced := append([]string{"--"}, argv...)
	inv, err := Parse(forced)
	if err != nil {
		t.Fatalf("Parse(%q) failed after the %q separator: %v", forced, "--", err)
	}
	if len(argv) == 0 {
		if inv.Mode != ModeShell {
			t.Fatalf("Parse(%q) = %s, want the shell mode", forced, inv.Mode)
		}
		return
	}
	if inv.Mode != ModeGuestCommand {
		t.Fatalf("Parse(%q) = %s, want a guest command", forced, inv.Mode)
	}
	if !slices.Equal(inv.Guest, argv) {
		t.Fatalf("Parse(%q).Guest = %q, want %q", forced, inv.Guest, argv)
	}
}
